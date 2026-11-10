package testmail_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/backendtest"
	"github.com/godwit-email/godwit/backend/conn"
	"github.com/godwit-email/godwit/backend/pop3"
	"github.com/godwit-email/godwit/core/internal/testmail"
)

func pop3Receiver(t *testing.T, s *testmail.POP3, sec conn.Security) *pop3.Receiver {
	r := pop3.New(pop3.Config{Host: "127.0.0.1", Port: s.Port(), Security: sec, Credentials: func(context.Context) (backend.Credentials, error) {
		return backend.Credentials{Username: "alice", Password: "secret"}, nil
	}})
	t.Cleanup(func() { r.Close() })
	return r
}

type pop3Server struct{ *testmail.POP3 }

func (s pop3Server) Receiver(t *testing.T) backend.Receiver {
	return pop3Receiver(t, s.POP3, conn.None)
}
func (s pop3Server) Deliver(t *testing.T, folder string, raw []byte) {
	if folder != "INBOX" {
		t.Skip("POP3 has only an inbox")
	}
	s.POP3.Deliver(raw)
}
func (s pop3Server) Reset(t *testing.T, _ string) { t.Skip("POP3 never resets its inbox") }
func (s pop3Server) RejectAuth(*testing.T)        { s.POP3.RejectAuth() }

func TestPOP3BackendContract(t *testing.T) {
	backendtest.Run(t, func(t *testing.T) backendtest.Server { return pop3Server{testmail.StartPOP3(t)} })
}

// pop3Local is a backend.Local holding refs in the inbox.
type pop3Local map[backend.MessageRef]bool

func (l pop3Local) KnownIDs(context.Context, []string) (map[string]bool, error) { return nil, nil }
func (l pop3Local) HeldRefs(_ context.Context, _ backend.FolderRef, refs []backend.MessageRef) (map[backend.MessageRef]bool, error) {
	out := map[backend.MessageRef]bool{}
	for _, r := range refs {
		out[r] = l[r]
	}
	return out, nil
}
func (l pop3Local) Held(context.Context, backend.FolderRef) (map[backend.MessageRef]string, error) {
	out := map[backend.MessageRef]string{}
	for r, ok := range l {
		if ok {
			out[r] = ""
		}
	}
	return out, nil
}
func (l pop3Local) Lost(context.Context) ([]string, error) { return nil, nil }

// pop3Store is a minimal local store for one POP3 inbox.
type pop3Store struct {
	held   pop3Local
	states map[backend.FolderRef]backend.FolderState
}

func newPOP3Store() *pop3Store {
	return &pop3Store{held: pop3Local{}, states: map[backend.FolderRef]backend.FolderState{}}
}

// sync runs one round with req's Window, Older and Pages, applies it and
// returns its changes and more.
func (st *pop3Store) sync(t *testing.T, r backend.Receiver, req backend.SyncRequest) ([]backend.Change, bool) {
	t.Helper()
	req.Folders, req.Local, req.HeaderFields = st.states, st.held, []string{"Subject"}
	var cs []backend.Change
	more, err := r.Sync(tctx(t), req, func(p backend.Page) error {
		for _, c := range p.Changes {
			switch c := c.(type) {
			case backend.MessageAppeared:
				st.held[c.Ref] = true
			case backend.MessageGone:
				delete(st.held, c.Ref)
			}
		}
		for f, s := range p.Folders {
			st.states[f] = s
		}
		cs = append(cs, p.Changes...)
		return nil
	})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	return cs, more
}

// arrivals is the refs of the MessageAppeared in cs, with "+" before a new arrival's.
func arrivals(cs []backend.Change) []string {
	var out []string
	for _, c := range cs {
		if m, ok := c.(backend.MessageAppeared); ok {
			if m.Backfill {
				out = append(out, string(m.Ref))
			} else {
				out = append(out, "+"+string(m.Ref))
			}
		}
	}
	return out
}

func dated(id string, d time.Time) []byte {
	return []byte("From: a@example.com\r\nSubject: " + id + "\r\nMessage-ID: <" + id + "@example.com>\r\nDate: " +
		d.Format(time.RFC1123Z) + "\r\n\r\nbody\r\n")
}

func TestPOP3CheckWithoutUIDL(t *testing.T) {
	s := testmail.StartPOP3(t)
	s.DisableUIDL()
	if _, err := pop3Receiver(t, s, conn.None).Check(tctx(t)); !errors.Is(err, pop3.ErrNoUIDL) {
		t.Errorf("Check without UIDL = %v, want ErrNoUIDL", err)
	}
}

func TestPOP3STLS(t *testing.T) {
	s := testmail.StartPOP3(t)
	conn.TLSRoots = s.Roots
	t.Cleanup(func() { conn.TLSRoots = nil })
	uid := s.Deliver(backendtest.Message("a@example.com", "a"))
	cs, _ := newPOP3Store().sync(t, pop3Receiver(t, s, conn.StartTLS), backend.SyncRequest{})
	if got := arrivals(cs); !slices.Equal(got, []string{uid}) || s.STLS() == 0 {
		t.Errorf("over STLS got %v after %d upgrades, want %s over TLS", got, s.STLS(), uid)
	}
}

// A first sync with a window since a date fetches the mail dated after it only.
func TestPOP3WindowSince(t *testing.T) {
	s := testmail.StartPOP3(t)
	now := time.Now()
	s.Deliver(dated("old", now.AddDate(0, -3, 0)))
	mid := s.Deliver(dated("mid", now.AddDate(0, 0, -10)))
	recent := s.Deliver(dated("new", now.AddDate(0, 0, -1)))
	cs, _ := newPOP3Store().sync(t, pop3Receiver(t, s, conn.None), backend.SyncRequest{Window: backend.Window{Since: now.AddDate(0, -1, 0)}})
	if got := arrivals(cs); !slices.Equal(got, []string{recent, mid}) {
		t.Errorf("first sync since a month ago = %v, want %v", got, []string{recent, mid})
	}
}

// 加载更早 fetches the mail the window left out, newest first, page by page.
func TestPOP3Older(t *testing.T) {
	s := testmail.StartPOP3(t)
	a := s.Deliver(backendtest.Message("a@example.com", "a"))
	b := s.Deliver(backendtest.Message("b@example.com", "b"))
	c := s.Deliver(backendtest.Message("c@example.com", "c"))
	r := pop3Receiver(t, s, conn.None)
	st := newPOP3Store()
	if _, err := r.Sync(tctx(t), backend.SyncRequest{Only: []backend.FolderRef{pop3.Inbox}, Older: 1}, func(backend.Page) error { return nil }); !errors.Is(err, backend.ErrNotSynced) {
		t.Errorf("older before a first sync = %v, want ErrNotSynced", err)
	}
	if cs, _ := st.sync(t, r, backend.SyncRequest{Window: backend.Window{Newest: 1}}); !slices.Equal(arrivals(cs), []string{c}) {
		t.Fatalf("first sync of the newest one = %v, want %s", arrivals(cs), c)
	}
	older := backend.SyncRequest{Only: []backend.FolderRef{pop3.Inbox}, Older: 1}
	if cs, more := st.sync(t, r, older); !slices.Equal(arrivals(cs), []string{b}) || !more {
		t.Errorf("older = %v (more %v), want %s and more", arrivals(cs), more, b)
	}
	if cs, more := st.sync(t, r, older); !slices.Equal(arrivals(cs), []string{a}) || more {
		t.Errorf("older = %v (more %v), want %s and no more", arrivals(cs), more, a)
	}
}

// With the cursor's message gone, mail after the newest one held is still new.
func TestPOP3CursorGone(t *testing.T) {
	s := testmail.StartPOP3(t)
	a := s.Deliver(backendtest.Message("a@example.com", "a"))
	b := s.Deliver(backendtest.Message("b@example.com", "b"))
	r := pop3Receiver(t, s, conn.None)
	st := newPOP3Store()
	st.sync(t, r, backend.SyncRequest{})
	s.Remove(b)
	c := s.Deliver(backendtest.Message("c@example.com", "c"))
	cs, _ := st.sync(t, r, backend.SyncRequest{})
	if got := arrivals(cs); !slices.Equal(got, []string{"+" + c}) {
		t.Errorf("after the cursor's message went got %v, want %s new", got, c)
	}
	if !slices.Contains(cs, backend.Change(backend.MessageGone{Location: backend.Location{Folder: pop3.Inbox, Ref: backend.MessageRef(b)}})) {
		t.Errorf("changes %+v lack %s gone", cs, b)
	}
	if !st.held[backend.MessageRef(a)] {
		t.Errorf("%s no longer held", a)
	}
}

// A round that dies on a later page leaves no mail behind: the next one gets
// the rest as new arrivals.
func TestPOP3InterruptedRound(t *testing.T) {
	s := testmail.StartPOP3(t)
	s.Deliver(backendtest.Message("a@example.com", "a"))
	r := pop3Receiver(t, s, conn.None)
	st := newPOP3Store()
	st.sync(t, r, backend.SyncRequest{})
	var want []string
	for _, id := range []string{"b", "c", "d", "e", "f"} {
		want = append(want, "+"+s.Deliver(backendtest.Message(id+"@example.com", id)))
	}
	s.FailRETR(2)
	var got []string
	_, err := r.Sync(tctx(t), backend.SyncRequest{Folders: st.states, Local: st.held, PageSize: 2}, func(p backend.Page) error {
		for _, c := range p.Changes {
			if m, ok := c.(backend.MessageAppeared); ok {
				st.held[m.Ref] = true
			}
		}
		maps.Copy(st.states, p.Folders)
		got = append(got, arrivals(p.Changes)...)
		return nil
	})
	if err == nil {
		t.Fatal("Sync with RETR failing on page 2 succeeded")
	}
	s.FailRETR(-1)
	cs, _ := st.sync(t, r, backend.SyncRequest{})
	got = append(got, arrivals(cs)...)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("across the interrupted round and the next got %v, want %v", got, want)
	}
}

// With the cursor's message and every held one gone, the mail left is taken
// as a first sync would take it: backfill, within the window.
func TestPOP3AllHeldGone(t *testing.T) {
	s := testmail.StartPOP3(t)
	a := s.Deliver(backendtest.Message("a@example.com", "a"))
	b := s.Deliver(backendtest.Message("b@example.com", "b"))
	r := pop3Receiver(t, s, conn.None)
	st := newPOP3Store()
	st.sync(t, r, backend.SyncRequest{})
	s.Remove(a)
	s.Remove(b)
	s.Deliver(backendtest.Message("c@example.com", "c"))
	d := s.Deliver(backendtest.Message("d@example.com", "d"))
	e := s.Deliver(backendtest.Message("e@example.com", "e"))
	cs, _ := st.sync(t, r, backend.SyncRequest{Window: backend.Window{Newest: 2}})
	if got := arrivals(cs); !slices.Equal(got, []string{e, d}) {
		t.Errorf("with every held message gone got %v, want %v as backfill", got, []string{e, d})
	}
}

// Purge marks the message with DELE; it leaves the server at QUIT. A message
// already gone is purged too.
func TestPOP3Purge(t *testing.T) {
	s := testmail.StartPOP3(t)
	a := s.Deliver(backendtest.Message("a@example.com", "a"))
	b := s.Deliver(backendtest.Message("b@example.com", "b"))
	r := pop3Receiver(t, s, conn.None)
	at := backend.Location{Folder: pop3.Inbox, Ref: backend.MessageRef(a)}
	if res, err := r.Apply(tctx(t), backend.Purge{At: at, Confirm: func() bool { return true }}); err != nil || res.Outcome != backend.Done {
		t.Fatalf("Purge = %+v, %v", res, err)
	}
	if got := s.UIDs(); !slices.Equal(got, []string{b}) || s.Deletes() != 1 {
		t.Errorf("after Purge the server holds %v (%d deletes), want %s", got, s.Deletes(), b)
	}
	if res, err := r.Apply(tctx(t), backend.Purge{At: at}); err != nil || res.Outcome != backend.Done {
		t.Errorf("Purge of a message already gone = %+v, %v; want done", res, err)
	}
	at.Ref = backend.MessageRef(b)
	if res, err := r.Apply(tctx(t), backend.Purge{At: at, Confirm: func() bool { return false }}); err != nil || res.Reason != backend.Cancelled {
		t.Errorf("cancelled Purge = %+v, %v", res, err)
	}
	if got := s.UIDs(); !slices.Equal(got, []string{b}) {
		t.Errorf("after a cancelled Purge the server holds %v, want %s", got, b)
	}
}

// Dates are the sender's, not the server's: an old one delivered late does
// not hide the window's mail delivered before it.
func TestPOP3WindowSinceOutOfOrder(t *testing.T) {
	s := testmail.StartPOP3(t)
	now := time.Now()
	in := s.Deliver(dated("in", now.AddDate(0, 0, -5)))
	s.Deliver(dated("late", now.AddDate(-1, 0, 0)))
	cs, _ := newPOP3Store().sync(t, pop3Receiver(t, s, conn.None), backend.SyncRequest{Window: backend.Window{Since: now.AddDate(0, -1, 0)}})
	if got := arrivals(cs); !slices.Equal(got, []string{in}) {
		t.Errorf("first sync since a month ago = %v, want %v", got, []string{in})
	}
}

// A window widened after the first sync brings the mail it now takes in, as
// existing mail.
func TestPOP3WindowWidens(t *testing.T) {
	s := testmail.StartPOP3(t)
	now := time.Now()
	a := s.Deliver(dated("a", now.AddDate(0, -6, 0)))
	b := s.Deliver(dated("b", now.AddDate(0, -2, 0)))
	s.Deliver(dated("c", now.AddDate(0, 0, -1)))
	r := pop3Receiver(t, s, conn.None)
	st := newPOP3Store()
	st.sync(t, r, backend.SyncRequest{Window: backend.Window{Newest: 1}})
	if cs, _ := st.sync(t, r, backend.SyncRequest{Window: backend.Window{Newest: 1}}); len(arrivals(cs)) != 0 {
		t.Errorf("the same window again = %v, want nothing", arrivals(cs))
	}
	if cs, _ := st.sync(t, r, backend.SyncRequest{Window: backend.Window{Since: now.AddDate(0, -3, 0)}}); !slices.Equal(arrivals(cs), []string{b}) {
		t.Errorf("widened to three months = %v, want %v as backfill", arrivals(cs), []string{b})
	}
	if cs, _ := st.sync(t, r, backend.SyncRequest{Window: backend.Window{Since: now.AddDate(0, -3, 0)}}); len(arrivals(cs)) != 0 {
		t.Errorf("the same window again = %v, want nothing", arrivals(cs))
	}
	d := s.Deliver(dated("d", now))
	if cs, _ := st.sync(t, r, backend.SyncRequest{}); !slices.Equal(arrivals(cs), []string{"+" + d, a}) {
		t.Errorf("widened to all, with new mail = %v, want %v", arrivals(cs), []string{"+" + d, a})
	}
}
