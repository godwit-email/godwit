package testmail_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/backendtest"
	"github.com/godwit-email/godwit/backend/ews"
	"github.com/godwit-email/godwit/core/internal/testmail"
)

func ewsReceiver(t *testing.T, s *testmail.EWS) *ews.Receiver { return ewsReceiverAs(t, s, "alice") }

// ewsReceiverAs signs in with login, the login name as the user typed it.
func ewsReceiverAs(t *testing.T, s *testmail.EWS, login string) *ews.Receiver {
	r := ews.New(ews.Config{URL: s.URL(), Address: "alice@example.com", Credentials: func(context.Context) (backend.Credentials, error) {
		return backend.Credentials{Username: login, Password: s.Password()}, nil
	}})
	t.Cleanup(func() { r.Close() })
	return r
}

type ewsServer struct{ *testmail.EWS }

func (s ewsServer) Receiver(t *testing.T) backend.Receiver          { return ewsReceiver(t, s.EWS) }
func (s ewsServer) Deliver(_ *testing.T, folder string, raw []byte) { s.EWS.Deliver(folder, raw) }
func (s ewsServer) Reset(_ *testing.T, folder string)               { s.EWS.Reset(folder) }
func (s ewsServer) RejectAuth(*testing.T)                           { s.EWS.RejectAuth() }

// shortcut: only the parts EWS implements so far; Run the whole contract once
// incremental sync, write-back and push are in.
func TestEWSBackendContract(t *testing.T) {
	backendtest.RunOnly(t, func(t *testing.T) backendtest.Server { return ewsServer{testmail.StartEWS(t)} },
		"Describe", "Check", "FirstSync", "CursorResumes", "FolderReset", "Fetch", "Headers", "Find", "SetFlags", "Move", "Copy", "Append", "Failed", "Unsupported", "Watch", "AuthFailed")
}

// syncAll runs one round from nothing and returns its changes.
func syncAll(t *testing.T, r backend.Receiver) []backend.Change {
	t.Helper()
	var cs []backend.Change
	_, err := r.Sync(tctx(t), backend.SyncRequest{HeaderFields: []string{"Subject"}}, func(p backend.Page) error {
		cs = append(cs, p.Changes...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

// Folders take their roles from the server's distinguished folders; a
// subfolder is named by its path; calendars and the like are not mail.
func TestEWSFolders(t *testing.T) {
	s := testmail.StartEWS(t)
	s.Deliver("Inbox/Projects", backendtest.Message("a@example.com", "a"))
	got := map[string]backend.Folder{}
	for _, c := range syncAll(t, ewsReceiver(t, s)) {
		if f, ok := c.(backend.FolderUpdated); ok {
			got[f.Name] = f.Folder
		}
	}
	for name, role := range map[string]backend.Role{"Inbox": backend.RoleInbox, "Sent Items": backend.RoleSent, "Drafts": backend.RoleDrafts,
		"Deleted Items": backend.RoleTrash, "Junk Email": backend.RoleJunk, "Archive": backend.RoleArchive, "Inbox/Projects": ""} {
		if f, ok := got[name]; !ok || f.Role != role {
			t.Errorf("folder %q = %+v (found %v), want role %q", name, f, ok, role)
		}
	}
	if p := got["Inbox/Projects"]; p.Parent != got["Inbox"].Ref || p.Delim != "/" {
		t.Errorf("subfolder %+v, want the inbox as its parent", p)
	}
	if _, ok := got["Calendar"]; ok {
		t.Error("the calendar is listed as a mail folder")
	}
}

// A server older than Exchange 2016 fails the add-account check with its reason.
func TestEWSServerTooOld(t *testing.T) {
	s := testmail.StartEWS(t)
	s.SetVersion(15, 0) // Exchange 2013
	_, err := ewsReceiver(t, s).Check(tctx(t))
	if !errors.Is(err, ews.ErrServerTooOld) || err.Error() != ews.ReasonServerTooOld {
		t.Errorf("Check on Exchange 2013 = %v, want ErrServerTooOld", err)
	}
	s.SetVersion(15, 1) // Exchange 2016
	if _, err := ewsReceiver(t, s).Check(tctx(t)); err != nil {
		t.Errorf("Check on Exchange 2016 = %v", err)
	}
}

// local is what local storage keeps between rounds: the cursors, the
// folders' names and the messages held.
type local struct {
	account backend.Cursor
	states  map[backend.FolderRef]backend.FolderState
	names   map[backend.FolderRef]string
	held    pop3Local
}

// sync runs one round on a new receiver from l's cursors and returns its pages.
func (l *local) sync(t *testing.T, s *testmail.EWS) []backend.Page {
	t.Helper()
	pages, _ := l.run(t, s, backend.SyncRequest{})
	return pages
}

// run runs one round of req (its Window, Only and Older) on a new receiver
// from l's cursors and returns its pages and more.
func (l *local) run(t *testing.T, s *testmail.EWS, req backend.SyncRequest) ([]backend.Page, bool) {
	t.Helper()
	if l.states == nil {
		l.states, l.names = map[backend.FolderRef]backend.FolderState{}, map[backend.FolderRef]string{}
	}
	if l.held == nil {
		l.held = pop3Local{}
	}
	var pages []backend.Page
	req.Account, req.Folders, req.Local = l.account, maps.Clone(l.states), l.held
	more, err := ewsReceiver(t, s).Sync(tctx(t), req, func(p backend.Page) error {
		pages = append(pages, p)
		for _, c := range p.Changes {
			switch c := c.(type) {
			case backend.MessageAppeared:
				l.held[c.Ref] = true
			case backend.MessageGone:
				delete(l.held, c.Ref)
			case backend.FolderUpdated:
				l.names[c.Ref] = c.Name
				if _, ok := l.states[c.Ref]; !ok {
					l.states[c.Ref] = backend.FolderState{}
				}
			case backend.FolderGone:
				delete(l.names, c.Ref)
				delete(l.states, c.Ref)
			}
		}
		if p.Account != "" {
			l.account = p.Account
		}
		maps.Copy(l.states, p.Folders)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return pages, more
}

func (l *local) ref(t *testing.T, name string) backend.FolderRef {
	t.Helper()
	for ref, n := range l.names {
		if n == name {
			return ref
		}
	}
	t.Fatalf("no folder %q among %v", name, l.names)
	return ""
}

func changes(pages []backend.Page) []backend.Change {
	var out []backend.Change
	for _, p := range pages {
		out = append(out, p.Changes...)
	}
	return out
}

// Changes made elsewhere come in the next round from the cursors: new mail
// as a new arrival, read, flagged and answered as flags, deleted mail as
// gone, and folders renamed (keeping their ref) or deleted.
func TestEWSIncremental(t *testing.T) {
	s := testmail.StartEWS(t)
	a := s.Deliver("INBOX", backendtest.Message("a@example.com", "a"))
	b := s.Deliver("INBOX", backendtest.Message("b@example.com", "b"))
	c := s.Deliver("INBOX", backendtest.Message("c@example.com", "c"))
	s.Deliver("Projects", backendtest.Message("p@example.com", "p"))
	s.Deliver("Old", backendtest.Message("o@example.com", "o"))
	var l local
	l.sync(t, s)
	inbox, projects, old := l.ref(t, "Inbox"), l.ref(t, "Projects"), l.ref(t, "Old")

	s.MarkRead(a, true)
	s.MarkFlagged(b, true)
	s.MarkAnswered(b)
	s.Remove(c)
	d := s.Deliver("INBOX", backendtest.Message("d@example.com", "d"))
	s.RenameFolder("Projects", "Plans")
	s.RemoveFolder("Old")
	got := map[string]backend.Change{}
	for _, ch := range changes(l.sync(t, s)) {
		switch ch := ch.(type) {
		case backend.FlagsChanged:
			got["flags "+string(ch.Ref)] = ch
		case backend.MessageGone:
			got["gone "+string(ch.Ref)] = ch
		case backend.MessageAppeared:
			ch.Content = nil
			got["appeared "+string(ch.Ref)] = ch
		case backend.FolderUpdated:
			got["folder "+string(ch.Ref)] = ch
		case backend.FolderGone:
			got["folder gone "+string(ch.Ref)] = ch
		}
	}
	at := func(ref string) backend.Location {
		return backend.Location{Folder: inbox, Ref: backend.MessageRef(ref)}
	}
	for key, want := range map[string]backend.Change{
		"flags " + a:                 backend.FlagsChanged{Location: at(a), Flags: backend.Flags{Seen: true}},
		"flags " + b:                 backend.FlagsChanged{Location: at(b), Flags: backend.Flags{Flagged: true, Answered: true}},
		"gone " + c:                  backend.MessageGone{Location: at(c)},
		"appeared " + d:              backend.MessageAppeared{Location: at(d)},
		"folder gone " + string(old): backend.FolderGone{Ref: old},
	} {
		if !reflect.DeepEqual(got[key], want) {
			t.Errorf("%s = %+v, want %+v", key, got[key], want)
		}
	}
	if f, ok := got["folder "+string(projects)].(backend.FolderUpdated); !ok || f.Name != "Plans" {
		t.Errorf("the renamed folder = %+v, want Plans at the same ref", got["folder "+string(projects)])
	}
	if cs := changes(l.sync(t, s)); len(cs) != 0 {
		t.Errorf("a round with nothing new reported %+v", cs)
	}
}

// A folder's changes come at most 512 a page, each page with its cursor.
func TestEWSPages(t *testing.T) {
	s := testmail.StartEWS(t)
	for i := range 513 {
		s.Deliver("INBOX", backendtest.Message(fmt.Sprintf("%d@example.com", i), "m"))
	}
	var l local
	n, withCursor := 0, 0
	for _, p := range l.sync(t, s) {
		m := 0
		for _, c := range p.Changes {
			if _, ok := c.(backend.MessageAppeared); ok {
				m++
			}
		}
		if m > 512 {
			t.Errorf("a page of %d messages", m)
		}
		if m > 0 && p.Folders[l.ref(t, "Inbox")].Cursor != "" {
			withCursor++
		}
		n += m
	}
	if n != 513 || withCursor < 2 {
		t.Errorf("%d messages in %d pages with the inbox's cursor, want 513 in 2 or more", n, withCursor)
	}
}

// A window since a date takes the mail received after it, as backfill on the
// first round; later, mail arriving is new and mail received before the
// date (moved in from elsewhere) is left out.
func TestEWSWindowSince(t *testing.T) {
	s := testmail.StartEWS(t)
	now := time.Now()
	s.DeliverAt("INBOX", backendtest.Message("old@example.com", "old"), now.AddDate(0, -3, 0))
	mid := s.DeliverAt("INBOX", backendtest.Message("mid@example.com", "mid"), now.AddDate(0, 0, -10))
	recent := s.DeliverAt("INBOX", backendtest.Message("new@example.com", "new"), now.AddDate(0, 0, -1))
	var l local
	w := backend.SyncRequest{Window: backend.Window{Since: now.AddDate(0, -1, 0)}}
	pages, _ := l.run(t, s, w)
	if got := arrivals(changes(pages)); !sameSet(got, []string{mid, recent}) {
		t.Errorf("first sync since a month ago = %v, want %s and %s as backfill", got, mid, recent)
	}
	if !l.states[l.ref(t, "Inbox")].Synced {
		t.Error("the inbox is not synced after its first round")
	}
	d := s.Deliver("INBOX", backendtest.Message("d@example.com", "d"))
	s.DeliverAt("INBOX", backendtest.Message("moved@example.com", "moved"), now.AddDate(-1, 0, 0))
	pages, _ = l.run(t, s, w)
	if got := arrivals(changes(pages)); !slices.Equal(got, []string{"+" + d}) {
		t.Errorf("next round = %v, want only %s, new", got, d)
	}
}

// A window of the newest messages takes those, by the time they were
// received; later, mail received before them (moved in) is left out.
func TestEWSWindowNewest(t *testing.T) {
	s := testmail.StartEWS(t)
	now := time.Now()
	b := s.DeliverAt("INBOX", backendtest.Message("b@example.com", "b"), now.AddDate(0, 0, -2))
	s.DeliverAt("INBOX", backendtest.Message("a@example.com", "a"), now.AddDate(0, 0, -3)) // delivered later, received earlier
	c := s.DeliverAt("INBOX", backendtest.Message("c@example.com", "c"), now.AddDate(0, 0, -1))
	var l local
	w := backend.SyncRequest{Window: backend.Window{Newest: 2}}
	pages, _ := l.run(t, s, w)
	if got := arrivals(changes(pages)); !sameSet(got, []string{b, c}) {
		t.Errorf("first sync of the newest 2 = %v, want %s and %s", got, b, c)
	}
	d := s.Deliver("INBOX", backendtest.Message("d@example.com", "d"))
	s.DeliverAt("INBOX", backendtest.Message("moved@example.com", "moved"), now.AddDate(-1, 0, 0))
	pages, _ = l.run(t, s, w)
	if got := arrivals(changes(pages)); !slices.Equal(got, []string{"+" + d}) {
		t.Errorf("next round = %v, want only %s, new", got, d)
	}
}

// 加载更早 takes the mail the window left out, newest received first, across
// the server's FindItem pages; before the folder's first round completes it
// is ErrNotSynced.
func TestEWSOlder(t *testing.T) {
	s := testmail.StartEWS(t)
	s.LimitFindItem(2)
	now := time.Now()
	var ids []string // oldest received first
	for i := range 5 {
		ids = append(ids, s.DeliverAt("INBOX", backendtest.Message(fmt.Sprintf("%d@example.com", i), "m"), now.AddDate(0, 0, i-5)))
	}
	var l local
	l.run(t, s, backend.SyncRequest{Window: backend.Window{Since: now}}) // learns the folders, takes no mail
	inbox := l.ref(t, "Inbox")
	older := func(n int) backend.SyncRequest {
		return backend.SyncRequest{Only: []backend.FolderRef{inbox}, Older: n}
	}
	_, err := ewsReceiver(t, s).Sync(tctx(t), backend.SyncRequest{Folders: map[backend.FolderRef]backend.FolderState{inbox: {}}, Only: []backend.FolderRef{inbox}, Older: 1},
		func(backend.Page) error { return nil })
	if !errors.Is(err, backend.ErrNotSynced) {
		t.Errorf("older before the first round = %v, want ErrNotSynced", err)
	}
	l.held[backend.MessageRef(ids[4])] = true // the window's
	pages, more := l.run(t, s, older(2))
	if got := arrivals(changes(pages)); !slices.Equal(got, []string{ids[3], ids[2]}) || !more {
		t.Errorf("older 2 = %v (more %v), want %s, %s and more", got, more, ids[3], ids[2])
	}
	pages, more = l.run(t, s, older(2))
	if got := arrivals(changes(pages)); !slices.Equal(got, []string{ids[1], ids[0]}) || more {
		t.Errorf("older 2 = %v (more %v), want %s, %s and no more", got, more, ids[1], ids[0])
	}
	if pages, more = l.run(t, s, older(2)); len(changes(pages)) != 0 || more {
		t.Errorf("older with nothing left = %+v (more %v)", changes(pages), more)
	}
}

// sameSet reports whether a and b hold the same strings.
func sameSet(a, b []string) bool {
	return slices.Equal(slices.Sorted(slices.Values(a)), slices.Sorted(slices.Values(b)))
}

// Fetch cuts a part, by its IMAP part path, from the whole message; Find
// matches a size against the message's; a deleted item is ErrNotFound.
func TestEWSContent(t *testing.T) {
	s := testmail.StartEWS(t)
	raw := []byte("Message-ID: <m@example.com>\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=b\r\n\r\n" +
		"--b\r\nContent-Type: text/plain\r\n\r\nfirst part\r\n--b\r\nContent-Type: text/plain\r\n\r\nsecond part\r\n--b--\r\n")
	id := s.Deliver("INBOX", raw)
	var l local
	l.sync(t, s)
	r, inbox := ewsReceiver(t, s), l.ref(t, "Inbox")
	at := backend.Location{Folder: inbox, Ref: backend.MessageRef(id)}
	rc, err := r.Fetch(tctx(t), backend.ContentRequest{Location: at, Part: "2"})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(rc); string(b) != "second part" {
		t.Errorf("part 2 = %q", b)
	}
	rc.Close()
	for size, want := range map[int64]int{int64(len(raw)): 1, int64(len(raw)) + 1: 0} {
		if got, err := r.Find(tctx(t), inbox, "m@example.com", size); err != nil || len(got) != want {
			t.Errorf("Find of size %d = %v, %v; want %d", size, got, err, want)
		}
	}
	s.Remove(id)
	if _, err := r.Fetch(tctx(t), backend.ContentRequest{Location: at}); !errors.Is(err, backend.ErrNotFound) {
		t.Errorf("Fetch of a deleted item = %v, want ErrNotFound", err)
	}
}

// Refused credentials are ErrAuthFailed.
func TestEWSAuthFailed(t *testing.T) {
	s := testmail.StartEWS(t)
	s.RejectAuth()
	if _, err := ewsReceiver(t, s).Describe(tctx(t)); !errors.Is(err, backend.ErrAuthFailed) {
		t.Errorf("Describe with a wrong password = %v, want ErrAuthFailed", err)
	}
}

// ewsNTLMServer is the fake over NTLM only, as Exchange's EWS directory is
// by default, signed in to with a domain login name.
type ewsNTLMServer struct{ ewsServer }

func (s ewsNTLMServer) Receiver(t *testing.T) backend.Receiver {
	return ewsReceiverAs(t, s.EWS, `EXAMPLE\alice`)
}

func (s ewsNTLMServer) Sender(*testing.T) backend.Sender {
	return ews.NewSender(ews.Config{URL: s.URL(), Credentials: func(context.Context) (backend.Credentials, error) {
		return backend.Credentials{Username: `EXAMPLE\alice`, Password: s.Password()}, nil
	}})
}

func TestEWSSenderContractNTLM(t *testing.T) {
	backendtest.RunSender(t, func(t *testing.T) backendtest.SendServer {
		s := testmail.StartEWS(t)
		s.UseNTLM()
		return ewsNTLMServer{ewsServer{s}}
	})
}

// The NTLM handshake writes the whole request before it is signed in: the
// connection failing then sent nothing, so it is a plain error, tried
// again, not ErrUncertain.
func TestEWSSendDroppedHandshakeIsNotUncertain(t *testing.T) {
	s := testmail.StartEWS(t)
	s.UseNTLM()
	s.DropNextChallenge()
	err := ewsNTLMServer{ewsServer{s}}.Sender(t).Send(tctx(t), "a@example.com", []string{"b@example.com"}, backendtest.Message("b@example.com", "hello"))
	if err == nil || errors.Is(err, backend.ErrUncertain) || errors.Is(err, backend.ErrAuthFailed) {
		t.Errorf("Send with the handshake dropped = %v, want a plain error", err)
	}
	if n := len(s.Sent()); n != 0 {
		t.Errorf("sent %d messages, want 0", n)
	}
}

func TestEWSBackendContractNTLM(t *testing.T) {
	backendtest.RunOnly(t, func(t *testing.T) backendtest.Server {
		s := testmail.StartEWS(t)
		s.UseNTLM()
		return ewsNTLMServer{ewsServer{s}}
	}, "Check", "FirstSync", "AuthFailed")
}

// NTLM takes the login name as DOMAIN\user or as a UPN.
func TestEWSNTLMLoginNames(t *testing.T) {
	for _, login := range []string{`EXAMPLE\alice`, `example\Alice`, "alice@example.com"} {
		s := testmail.StartEWS(t)
		s.UseNTLM()
		if _, err := ewsReceiverAs(t, s, login).Check(tctx(t)); err != nil {
			t.Errorf("Check as %s = %v", login, err)
		}
	}
	s := testmail.StartEWS(t)
	s.UseNTLM()
	if _, err := ewsReceiverAs(t, s, `OTHER\alice`).Check(tctx(t)); !errors.Is(err, backend.ErrAuthFailed) {
		t.Errorf(`Check as OTHER\alice = %v, want ErrAuthFailed`, err)
	}
}

// A server that refuses NTLM without channel binding, yet takes the same
// password over Basic, requires Extended Protection: the check says so.
func TestEWSExtendedProtection(t *testing.T) {
	s := testmail.StartEWS(t)
	s.UseNTLM()
	s.RequireExtendedProtection()
	_, err := ewsReceiverAs(t, s, `EXAMPLE\alice`).Check(tctx(t))
	if !errors.Is(err, ews.ErrExtendedProtection) || err.Error() != ews.ReasonExtendedProtection {
		t.Errorf("Check under Extended Protection = %v, want ErrExtendedProtection", err)
	}
	s.RejectAuth() // a wrong password is still a wrong password
	if _, err := ewsReceiverAs(t, s, `EXAMPLE\alice`).Check(tctx(t)); !errors.Is(err, backend.ErrAuthFailed) {
		t.Errorf("Check with a wrong password under Extended Protection = %v, want ErrAuthFailed", err)
	}
}

func (s ewsServer) Sender(*testing.T) backend.Sender {
	return ews.NewSender(ews.Config{URL: s.URL(), Credentials: func(context.Context) (backend.Credentials, error) {
		return backend.Credentials{Username: "alice", Password: s.Password()}, nil
	}})
}

func (s ewsServer) Accepted(*testing.T) []backendtest.Delivery {
	var out []backendtest.Delivery
	for _, m := range s.EWS.Sent() {
		out = append(out, backendtest.Delivery{Rcpts: m.Rcpts, Raw: m.Raw})
	}
	return out
}

func (s ewsServer) Refuse(_ *testing.T, rcpt string) { s.EWS.Refuse(rcpt) }
func (s ewsServer) LoseVerdict(*testing.T)           { s.EWS.LoseVerdict() }

func TestEWSSenderContract(t *testing.T) {
	backendtest.RunSender(t, func(t *testing.T) backendtest.SendServer { return ewsServer{testmail.StartEWS(t)} })
}

// A Bcc recipient gets the message but is not named in it, and the sent
// copy is filed in Sent Items once.
func TestEWSSendBccAndSentCopy(t *testing.T) {
	s := testmail.StartEWS(t)
	raw := backendtest.Message("bcc@example.com", "hello")
	snd := ewsServer{s}.Sender(t)
	if !snd.SavesSent() {
		t.Error("SavesSent = false")
	}
	if err := snd.Send(tctx(t), "a@example.com", []string{"b@example.com", "hidden@example.com"}, raw); err != nil {
		t.Fatal(err)
	}
	sent := s.Sent()
	if len(sent) != 1 || !slices.Contains(sent[0].Rcpts, "hidden@example.com") {
		t.Fatalf("sent %+v, want one message to hidden@ too", sent)
	}
	if bytes.Contains(sent[0].Raw, []byte("hidden@")) {
		t.Errorf("the message names its Bcc recipient: %s", sent[0].Raw)
	}
	if n := s.Count("Sent Items"); n != 1 {
		t.Errorf("Sent Items has %d messages, want 1", n)
	}
}

// busyReceiver is a receiver that records the back-offs it waits out
// instead of waiting.
func busyReceiver(t *testing.T, s *testmail.EWS) (*ews.Receiver, *[]time.Duration) {
	var waits []time.Duration
	r := ews.New(ews.Config{URL: s.URL(), Credentials: func(context.Context) (backend.Credentials, error) {
		return backend.Credentials{Username: "alice", Password: s.Password()}, nil
	}, Wait: func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}})
	t.Cleanup(func() { r.Close() })
	return r, &waits
}

// ErrorServerBusy, as a SOAP fault or a response message, is waited out for
// BackOffMilliseconds and the request sent again, up to three times.
func TestEWSServerBusyRetries(t *testing.T) {
	for _, fault := range []bool{true, false} {
		s := testmail.StartEWS(t)
		s.Busy(3, 2*time.Second, fault)
		r, waits := busyReceiver(t, s)
		if _, err := r.Describe(tctx(t)); err != nil {
			t.Fatalf("Describe after 3 busy answers (fault %v) = %v", fault, err)
		}
		if want := []time.Duration{2 * time.Second, 2 * time.Second, 2 * time.Second}; !slices.Equal(*waits, want) {
			t.Errorf("waited %v (fault %v), want %v", *waits, fault, want)
		}
	}
}

// A server still busy after three retries fails the request with a plain
// error, not a sign-in failure; a back-off over a minute waits a minute.
func TestEWSServerBusyGivesUp(t *testing.T) {
	s := testmail.StartEWS(t)
	s.Busy(4, 10*time.Minute, false)
	r, waits := busyReceiver(t, s)
	_, err := r.Describe(tctx(t))
	if err == nil || errors.Is(err, backend.ErrAuthFailed) {
		t.Errorf("Describe after 4 busy answers = %v, want a plain error", err)
	}
	if n := s.Requests(); n != 4 {
		t.Errorf("sent %d requests, want 4", n)
	}
	if want := []time.Duration{time.Minute, time.Minute, time.Minute}; !slices.Equal(*waits, want) {
		t.Errorf("waited %v, want %v", *waits, want)
	}
}

// An account's requests go one at a time, even from several backends (as
// the receiver and the sender are).
func TestEWSRequestsOneAtATime(t *testing.T) {
	s := testmail.StartEWS(t)
	s.SetLatency(20 * time.Millisecond)
	errs := make(chan error)
	for range 3 {
		go func() {
			_, err := ewsReceiver(t, s).Check(tctx(t))
			errs <- err
		}()
	}
	for range 3 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if n := s.MaxConcurrent(); n != 1 {
		t.Errorf("%d requests at once, want 1", n)
	}
}

// Waiting out a back-off ends with ctx.
func TestEWSServerBusyWaitCancels(t *testing.T) {
	s := testmail.StartEWS(t)
	s.Busy(1, time.Minute, true)
	ctx, cancel := context.WithTimeout(tctx(t), 50*time.Millisecond)
	defer cancel()
	if _, err := ewsReceiver(t, s).Describe(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Describe cancelled while backing off = %v, want DeadlineExceeded", err)
	}
}

// flagsOf is the flags a fresh sync sees on each message.
func flagsOf(t *testing.T, s *testmail.EWS) map[string]backend.Flags {
	t.Helper()
	out := map[string]backend.Flags{}
	for _, c := range syncAll(t, ewsReceiver(t, s)) {
		if m, ok := c.(backend.MessageAppeared); ok {
			out[string(m.Ref)] = m.Flags
		}
	}
	return out
}

// Read, flagged and answered are written back, and taken off again; the
// read-only draft flag and keywords are left alone without failing. A
// message gone from the server is ErrNotFound.
func TestEWSSetFlags(t *testing.T) {
	s := testmail.StartEWS(t)
	a := s.Deliver("INBOX", backendtest.Message("a@example.com", "a"))
	var l local
	l.sync(t, s)
	at := backend.Location{Folder: l.ref(t, "Inbox"), Ref: backend.MessageRef(a)}
	r := ewsReceiver(t, s)
	apply := func(op backend.SetFlags) {
		t.Helper()
		if res, err := r.Apply(tctx(t), op); err != nil || res.Outcome != backend.Done {
			t.Fatalf("Apply(%+v) = %+v, %v", op, res, err)
		}
	}
	apply(backend.SetFlags{At: at, Add: backend.Flags{Seen: true, Flagged: true, Answered: true, Draft: true, Keywords: []string{"x"}}})
	if got, want := flagsOf(t, s)[a], (backend.Flags{Seen: true, Flagged: true, Answered: true}); !reflect.DeepEqual(got, want) {
		t.Errorf("after adding, flags = %+v, want %+v", got, want)
	}
	apply(backend.SetFlags{At: at, Remove: backend.Flags{Seen: true, Flagged: true, Answered: true}})
	if got := flagsOf(t, s)[a]; !reflect.DeepEqual(got, backend.Flags{}) {
		t.Errorf("after removing, flags = %+v, want none", got)
	}
	if cs := changes(l.sync(t, s)); len(cs) != 1 {
		t.Errorf("the next round from the cursors = %+v, want the flags changed once", cs)
	}
	apply(backend.SetFlags{At: at, Add: backend.Flags{Draft: true}}) // nothing to write

	s.Remove(a)
	if _, err := r.Apply(tctx(t), backend.SetFlags{At: at, Add: backend.Flags{Seen: true}}); !errors.Is(err, backend.ErrNotFound) {
		t.Errorf("SetFlags on a removed message = %v, want ErrNotFound", err)
	}
}

// Describe declares the spec's table: draft is read-only, and what one
// folder per message rules out is unsupported, each with an ews.* reason.
func TestEWSDescribe(t *testing.T) {
	d, err := ewsReceiver(t, testmail.StartEWS(t)).Describe(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	levels := map[backend.Level][]backend.Feature{
		backend.LevelSupported: {backend.FeatureMove, backend.FeatureCopy, backend.FeaturePurge, backend.FeatureAppend,
			backend.FeatureSeen, backend.FeatureFlagged, backend.FeatureAnswered, backend.FeatureFolderCreate,
			backend.FeatureFolderRename, backend.FeatureFolderDelete, backend.FeatureSavesSent, backend.FeaturePush},
		backend.LevelNoted: {backend.FeatureDraft},
		backend.LevelUnsupported: {backend.FeatureAddToFolder, backend.FeatureRemoveFromFolder, backend.FeatureKeywords,
			backend.FeatureLabelDelete, backend.FeatureAllMail},
	}
	for l, fs := range levels {
		for _, f := range fs {
			s := d.Features[f]
			if s.Level != l || (l == backend.LevelSupported) != (s.Reason == "") || l != backend.LevelSupported && !strings.HasPrefix(s.Reason, "ews.") {
				t.Errorf("%s = %+v, want %s", f, s, l)
			}
		}
	}
	if d.Identity != backend.IdentityLocation || d.PollInterval != time.Minute {
		t.Errorf("Describe = %+v", d)
	}
}
