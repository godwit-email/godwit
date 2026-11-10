// Package backendtest is the protocol backend contract (ADR 0037): one suite
// every backend.Receiver runs, against a fixture that starts the backend and
// puts mail on, or injects faults into, its server.
package backendtest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/godwit-email/godwit/backend"
)

// Server is one fresh account on a fixture's server.
type Server interface {
	// Receiver starts a new backend on the account, closed when t ends.
	Receiver(t *testing.T) backend.Receiver
	// Deliver puts raw into the folder named folder, creating it if need be,
	// as mail arriving. "INBOX" is the inbox.
	Deliver(t *testing.T, folder string, raw []byte)
	// Reset makes the server void every location in the folder named folder:
	// IMAP's UIDVALIDITY changes, JMAP cannot calculate changes. A fixture
	// that cannot calls t.Skip.
	Reset(t *testing.T, folder string)
	// RejectAuth makes the server refuse the account's credentials from now on.
	RejectAuth(t *testing.T)
}

// Fixture starts a Server for one test.
type Fixture func(t *testing.T) Server

type contractTest struct {
	name string
	test func(*testing.T, Fixture)
}

// allTests is the whole contract.
var allTests = []contractTest{
	{"Describe", testDescribe},
	{"FirstSync", testFirstSync},
	{"CursorResumes", testCursorResumes},
	{"FolderReset", testFolderReset},
	{"Check", testCheck},
	{"Fetch", testFetch},
	{"Headers", testHeaders},
	{"Find", testFind},
	{"SetFlags", testSetFlags},
	{"Move", testMove},
	{"Copy", testCopy},
	{"Append", testAppend},
	{"Failed", testFailed},
	{"Unsupported", testUnsupported},
	{"Watch", testWatch},
	{"AuthFailed", testAuthFailed},
}

// Run runs the contract against the backend start gives.
func Run(t *testing.T, start Fixture) { run(t, start, allTests) }

// RunOnly runs only the named parts of the contract, for a backend whose
// other verbs are still to come.
func RunOnly(t *testing.T, start Fixture, names ...string) {
	var tests []contractTest
	for _, n := range names {
		i := slices.IndexFunc(allTests, func(c contractTest) bool { return c.name == n })
		if i < 0 {
			t.Fatalf("no contract test %q", n)
		}
		tests = append(tests, allTests[i])
	}
	run(t, start, tests)
}

func run(t *testing.T, start Fixture, tests []contractTest) {
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { c.test(t, start) })
	}
}

// Message is a small message with subject and Message-ID <id>.
func Message(id, subject string) []byte {
	return []byte("From: a@example.com\r\nTo: b@example.com\r\nSubject: " + subject +
		"\r\nMessage-ID: <" + id + ">\r\nDate: Mon, 9 Nov 2026 10:00:00 +0000\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\nbody of " + subject + "\r\n")
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	return c
}

// view is a minimal local store: what the changes so far say the server has.
type view struct {
	account  backend.Cursor
	states   map[backend.FolderRef]backend.FolderState
	folders  map[backend.FolderRef]backend.Folder
	messages map[backend.FolderRef]map[backend.MessageRef]backend.Flags
	ids      map[string]bool             // ServerIDs stored
	sids     map[backend.Location]string // the ServerID of each location, keyed without it
}

func newView() *view {
	return &view{states: map[backend.FolderRef]backend.FolderState{}, folders: map[backend.FolderRef]backend.Folder{},
		messages: map[backend.FolderRef]map[backend.MessageRef]backend.Flags{}, ids: map[string]bool{}, sids: map[backend.Location]string{}}
}

func (v *view) KnownIDs(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = v.ids[id]
	}
	return out, nil
}

func (v *view) HeldRefs(_ context.Context, f backend.FolderRef, refs []backend.MessageRef) (map[backend.MessageRef]bool, error) {
	out := map[backend.MessageRef]bool{}
	for _, r := range refs {
		_, out[r] = v.messages[f][r]
	}
	return out, nil
}

func (v *view) Held(_ context.Context, f backend.FolderRef) (map[backend.MessageRef]string, error) {
	out := map[backend.MessageRef]string{}
	for r := range v.messages[f] {
		out[r] = v.sids[backend.Location{Folder: f, Ref: r}]
	}
	return out, nil
}

// Lost is empty: the view deletes nothing on its own.
func (v *view) Lost(context.Context) ([]string, error) { return nil, nil }

// sync runs one round on r from v's cursors, applies it to v and returns its changes.
func (v *view) sync(t *testing.T, r backend.Receiver) []backend.Change {
	t.Helper()
	var all []backend.Change
	_, err := r.Sync(ctx(t), backend.SyncRequest{
		Account: v.account, Folders: maps.Clone(v.states), Local: v,
		HeaderFields: []string{"Message-ID", "Subject"}, TextBytes: 1 << 10,
		TextParts: func(p *backend.Part) []backend.PartRef { return textParts(p) },
	}, func(p backend.Page) error {
		for _, c := range p.Changes {
			v.apply(t, c)
		}
		if p.Account != "" {
			v.account = p.Account
		}
		for f, s := range p.Folders {
			v.states[f] = s
		}
		all = append(all, p.Changes...)
		return nil
	})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	return all
}

func textParts(p *backend.Part) []backend.PartRef {
	if len(p.Children) == 0 {
		if strings.HasPrefix(p.ContentType, "text/") && p.Disposition != "attachment" {
			return []backend.PartRef{p.Ref}
		}
		return nil
	}
	var out []backend.PartRef
	for i := range p.Children {
		out = append(out, textParts(&p.Children[i])...)
	}
	return out
}

func (v *view) apply(t *testing.T, c backend.Change) {
	t.Helper()
	switch c := c.(type) {
	case backend.FolderUpdated:
		v.folders[c.Ref] = c.Folder
	case backend.FolderGone:
		delete(v.folders, c.Ref)
		delete(v.messages, c.Ref)
		delete(v.states, c.Ref)
	case backend.FolderReset:
		delete(v.messages, c.Ref)
		delete(v.states, c.Ref)
	case backend.MessageAppeared:
		if _, ok := v.folders[c.Folder]; !ok {
			t.Errorf("MessageAppeared in folder %q before its FolderUpdated", c.Folder)
		}
		if c.Content == nil && !v.ids[c.ServerID] {
			t.Errorf("MessageAppeared without content for unknown ServerID %q", c.ServerID)
		}
		if v.messages[c.Folder] == nil {
			v.messages[c.Folder] = map[backend.MessageRef]backend.Flags{}
		}
		v.messages[c.Folder][c.Ref] = c.Flags
		if c.ServerID != "" {
			v.ids[c.ServerID] = true
			v.sids[backend.Location{Folder: c.Folder, Ref: c.Ref}] = c.ServerID
		}
	case backend.MessageGone:
		delete(v.messages[c.Folder], c.Ref)
	case backend.FlagsChanged:
		if _, ok := v.messages[c.Folder][c.Ref]; ok {
			v.messages[c.Folder][c.Ref] = c.Flags
		}
	case backend.FolderSnapshot:
		for ref := range v.messages[c.Folder] {
			if f, ok := c.Messages[ref]; ok {
				v.messages[c.Folder][ref] = f
			} else {
				delete(v.messages[c.Folder], ref)
			}
		}
	default:
		t.Errorf("unknown change %T", c)
	}
}

// folder is the ref of the folder named name; it fails t if there is none.
func (v *view) folder(t *testing.T, name string) backend.FolderRef {
	t.Helper()
	for ref, f := range v.folders {
		if f.Name == name || name == "INBOX" && f.Role == backend.RoleInbox {
			return ref
		}
	}
	t.Fatalf("no folder %q among %v", name, v.folders)
	return ""
}

// appeared is the MessageAppeared changes, in order.
func appeared(cs []backend.Change) []backend.MessageAppeared {
	var out []backend.MessageAppeared
	for _, c := range cs {
		if m, ok := c.(backend.MessageAppeared); ok {
			out = append(out, m)
		}
	}
	return out
}

// only is the single MessageAppeared in cs; it fails t otherwise.
func only(t *testing.T, cs []backend.Change) backend.MessageAppeared {
	t.Helper()
	ms := appeared(cs)
	if len(ms) != 1 {
		t.Fatalf("got %d MessageAppeared, want 1: %+v", len(ms), cs)
	}
	return ms[0]
}

func subject(m backend.MessageAppeared) string {
	if m.Content == nil {
		return ""
	}
	for _, l := range strings.Split(string(m.Content.Header), "\n") {
		if k, v, ok := strings.Cut(l, ":"); ok && strings.EqualFold(k, "Subject") {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// started is a Server with one message, "first", in INBOX, synced once.
func started(t *testing.T, start Fixture) (Server, backend.Receiver, *view, backend.MessageAppeared) {
	s := start(t)
	s.Deliver(t, "INBOX", Message("first@example.com", "first"))
	r := s.Receiver(t)
	v := newView()
	return s, r, v, only(t, v.sync(t, r))
}

func describe(t *testing.T, r backend.Receiver) backend.Description {
	t.Helper()
	d, err := r.Describe(ctx(t))
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	return d
}

func testDescribe(t *testing.T, start Fixture) {
	d := describe(t, start(t).Receiver(t))
	for _, f := range backend.AllFeatures {
		s, ok := d.Features[f]
		switch {
		case !ok:
			t.Errorf("feature %s not declared", f)
		case !slices.Contains([]backend.Level{backend.LevelSupported, backend.LevelNoted, backend.LevelUnsupported}, s.Level):
			t.Errorf("feature %s: level %q", f, s.Level)
		case s.Level != backend.LevelSupported && s.Reason == "":
			t.Errorf("feature %s is %s without a reason", f, s.Level)
		}
	}
	if !slices.Contains([]backend.Identity{backend.IdentityStable, backend.IdentityLocation, backend.IdentityInboxOnly}, d.Identity) {
		t.Errorf("identity %q", d.Identity)
	}
	if d.PollInterval <= 0 {
		t.Errorf("poll interval %v", d.PollInterval)
	}
}

func testCheck(t *testing.T, start Fixture) {
	s := start(t)
	s.Deliver(t, "INBOX", Message("first@example.com", "first"))
	c, err := s.Receiver(t).Check(ctx(t))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if c.Host == "" || c.Folders < 1 || c.Identity == "" {
		t.Errorf("Check = %+v, want a host, folders and a description", c)
	}
}

// The first sync reports the inbox and its mail as backfill, with content.
func testFirstSync(t *testing.T, start Fixture) {
	_, r, v, m := started(t, start)
	inbox := v.folder(t, "INBOX")
	if v.folders[inbox].Role != backend.RoleInbox {
		t.Errorf("inbox role %q", v.folders[inbox].Role)
	}
	if m.Folder != inbox || m.Ref == "" || !m.Backfill || m.Content == nil {
		t.Fatalf("MessageAppeared = %+v, want backfill with content in the inbox", m)
	}
	if subject(m) != "first" {
		t.Errorf("header %q, want the Subject", m.Content.Header)
	}
	if m.Content.Size == 0 || (m.Content.Structure == nil) == (m.Content.Raw == nil) {
		t.Errorf("content: size %d, a structure or the raw message but not both", m.Content.Size)
	}
	if m.Content.Structure != nil && !anyContains(m.Content.Text, "body of first") {
		t.Errorf("text parts %q, want the body", m.Content.Text)
	}
	if d := describe(t, r); d.Identity == backend.IdentityStable && m.ServerID == "" {
		t.Error("a stable identity without a ServerID")
	}
}

func anyContains(m map[backend.PartRef][]byte, s string) bool {
	for _, b := range m {
		if bytes.Contains(b, []byte(s)) {
			return true
		}
	}
	return false
}

// A new backend given the cursors reports only what is new, as a new arrival.
func testCursorResumes(t *testing.T, start Fixture) {
	s, _, v, _ := started(t, start)
	s.Deliver(t, "INBOX", Message("second@example.com", "second"))
	m := only(t, v.sync(t, s.Receiver(t)))
	if subject(m) != "second" || m.Backfill {
		t.Errorf("got %q (backfill %v), want second as a new arrival", subject(m), m.Backfill)
	}
	if ms := appeared(v.sync(t, s.Receiver(t))); len(ms) != 0 {
		t.Errorf("a sync with nothing new reported %d messages", len(ms))
	}
}

// After a reset the folder's locations are voided before its mail reappears.
func testFolderReset(t *testing.T, start Fixture) {
	s, r, v, _ := started(t, start)
	inbox := v.folder(t, "INBOX")
	s.Reset(t, "INBOX")
	cs := v.sync(t, r)
	reset := slices.IndexFunc(cs, func(c backend.Change) bool { return c == backend.FolderReset{Ref: inbox} })
	again := slices.IndexFunc(cs, func(c backend.Change) bool {
		m, ok := c.(backend.MessageAppeared)
		return ok && m.Folder == inbox
	})
	if reset < 0 || again < reset {
		t.Fatalf("got %+v, want a FolderReset of the inbox and then its message", cs)
	}
	if len(v.messages[inbox]) != 1 {
		t.Errorf("inbox holds %d messages after the reset, want 1", len(v.messages[inbox]))
	}
}

func read(t *testing.T, r backend.Receiver, req backend.ContentRequest) string {
	t.Helper()
	rc, err := r.Fetch(ctx(t), req)
	if err != nil {
		t.Fatalf("Fetch(%+v): %v", req, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func testFetch(t *testing.T, start Fixture) {
	_, r, _, m := started(t, start)
	if raw := read(t, r, backend.ContentRequest{Location: m.Location}); !strings.Contains(raw, "Subject: first") || !strings.Contains(raw, "body of first") {
		t.Errorf("whole message %q", raw)
	}
	h := read(t, r, backend.ContentRequest{Location: m.Location, HeaderFields: []string{"Subject"}})
	if !strings.Contains(h, "Subject: first") || strings.Contains(h, "Message-ID") || strings.Contains(h, "body of") {
		t.Errorf("Subject header field %q", h)
	}
	if m.Content.Structure != nil {
		ps := textParts(m.Content.Structure)
		if len(ps) == 0 || !strings.Contains(read(t, r, backend.ContentRequest{Location: m.Location, Part: ps[0]}), "body of first") {
			t.Errorf("text part %v", ps)
		}
	}
}

// Headers reads the asked header fields of several messages of a folder.
func testHeaders(t *testing.T, start Fixture) {
	s := start(t)
	s.Deliver(t, "INBOX", Message("first@example.com", "first"))
	s.Deliver(t, "INBOX", Message("second@example.com", "second"))
	r := s.Receiver(t)
	v := newView()
	v.sync(t, r)
	inbox := v.folder(t, "INBOX")
	refs := slices.Collect(maps.Keys(v.messages[inbox]))
	got, err := r.Headers(ctx(t), inbox, refs, []string{"Subject"})
	if err != nil {
		t.Fatalf("Headers: %v", err)
	}
	var subjects []string
	for _, ref := range refs {
		h := string(got[ref])
		if strings.Contains(h, "Message-ID") || strings.Contains(h, "body of") {
			t.Errorf("Headers of %q gave more than Subject: %q", ref, h)
		}
		subjects = append(subjects, strings.TrimSpace(h))
	}
	slices.Sort(subjects)
	if want := []string{"Subject: first", "Subject: second"}; !slices.Equal(subjects, want) {
		t.Errorf("Headers = %q, want %q", subjects, want)
	}
}

func testFind(t *testing.T, start Fixture) {
	_, r, v, m := started(t, start)
	got, err := r.Find(ctx(t), v.folder(t, "INBOX"), "first@example.com", 0)
	if err != nil || !slices.Equal(got, []backend.MessageRef{m.Ref}) {
		t.Errorf("Find = %v, %v; want %v", got, err, m.Ref)
	}
	if got, err := r.Find(ctx(t), v.folder(t, "INBOX"), "first@example", 0); err != nil || len(got) != 0 {
		t.Errorf("Find of a partial Message-ID = %v, %v; want none", got, err)
	}
}

func apply(t *testing.T, r backend.Receiver, op backend.Op) backend.Result {
	t.Helper()
	res, err := r.Apply(ctx(t), op)
	if err != nil {
		t.Fatalf("Apply(%T): %v", op, err)
	}
	return res
}

// SetFlags takes effect locally first, as local storage does it, and the next
// sync does not undo it; a backend that fully supports seen stores it on the
// server, as a fresh local view shows.
func testSetFlags(t *testing.T, start Fixture) {
	_, r, v, m := started(t, start)
	if res := apply(t, r, backend.SetFlags{At: m.Location, Add: backend.Flags{Seen: true}}); res.Outcome != backend.Done {
		t.Fatalf("SetFlags = %+v", res)
	}
	f := v.messages[m.Folder][m.Ref]
	f.Seen = true
	v.messages[m.Folder][m.Ref] = f
	v.sync(t, r)
	if !v.messages[m.Folder][m.Ref].Seen {
		t.Error("the next sync undid the seen flag")
	}
	if describe(t, r).Features[backend.FeatureSeen].Level != backend.LevelSupported {
		return
	}
	fresh := newView()
	fresh.sync(t, r)
	if f, ok := fresh.messages[m.Folder][m.Ref]; !ok || !f.Seen {
		t.Error("a fresh sync does not show the message seen")
	}
}

func testMove(t *testing.T, start Fixture) {
	s := start(t)
	s.Deliver(t, "Archive", Message("old@example.com", "old"))
	s.Deliver(t, "INBOX", Message("first@example.com", "first"))
	r := s.Receiver(t)
	v := newView()
	v.sync(t, r)
	inbox, archive := v.folder(t, "INBOX"), v.folder(t, "Archive")
	var m backend.MessageAppeared
	for ref := range v.messages[inbox] {
		m.Location = backend.Location{Folder: inbox, Ref: ref}
	}
	res := apply(t, r, backend.Move{At: m.Location, To: archive})
	switch {
	case res.Outcome == backend.Unsupported:
		t.Skip("Move unsupported:", res.Reason)
	case res.Outcome == backend.Done && res.Ref == "":
		t.Error("Move done without its new location")
	case res.Outcome != backend.Done && res.Outcome != backend.Unlocated:
		t.Fatalf("Move = %+v", res)
	}
	v.sync(t, r)
	if _, ok := v.messages[inbox][m.Ref]; ok {
		t.Error("the moved message is still in the inbox")
	}
	if len(v.messages[archive]) != 2 {
		t.Errorf("archive holds %d messages, want 2", len(v.messages[archive]))
	}
	if _, ok := v.messages[archive][res.Ref]; res.Outcome == backend.Done && !ok {
		t.Errorf("archive lacks the reported location %q", res.Ref)
	}
}

// Copy leaves the message where it is and puts an independent copy in the
// target: a backend that declares it does it.
func testCopy(t *testing.T, start Fixture) {
	s := start(t)
	s.Deliver(t, "Archive", Message("old@example.com", "old"))
	s.Deliver(t, "INBOX", Message("first@example.com", "first"))
	r := s.Receiver(t)
	if sup := describe(t, r).Features[backend.FeatureCopy]; sup.Level == backend.LevelUnsupported {
		t.Skip("Copy unsupported:", sup.Reason)
	}
	v := newView()
	v.sync(t, r)
	inbox, archive := v.folder(t, "INBOX"), v.folder(t, "Archive")
	var at backend.Location
	for ref := range v.messages[inbox] {
		at = backend.Location{Folder: inbox, Ref: ref}
	}
	res := apply(t, r, backend.Copy{At: at, To: archive})
	switch {
	case res.Outcome == backend.Done && res.Ref == "":
		t.Error("Copy done without the copy's location")
	case res.Outcome != backend.Done && res.Outcome != backend.Unlocated:
		t.Fatalf("Copy = %+v", res)
	}
	v.sync(t, r)
	if _, ok := v.messages[inbox][at.Ref]; !ok {
		t.Error("the copied message left the inbox")
	}
	if len(v.messages[archive]) != 2 {
		t.Errorf("archive holds %d messages, want 2", len(v.messages[archive]))
	}
	if _, ok := v.messages[archive][res.Ref]; res.Outcome == backend.Done && !ok {
		t.Errorf("archive lacks the reported location %q", res.Ref)
	}
}

// Append stores a message once, with its flags: appending it again where it
// already is stores nothing more.
func testAppend(t *testing.T, start Fixture) {
	_, r, v, _ := started(t, start)
	if s := describe(t, r).Features[backend.FeatureAppend]; s.Level == backend.LevelUnsupported {
		t.Skip("Append unsupported:", s.Reason)
	}
	inbox := v.folder(t, "INBOX")
	raw := Message("appended@example.com", "appended")
	op := backend.Append{To: inbox, Raw: raw, Flags: backend.Flags{Seen: true}, MessageID: "appended@example.com"}
	for range 2 {
		if res := apply(t, r, op); res.Outcome != backend.Done && res.Outcome != backend.Unlocated {
			t.Fatalf("Append = %+v, want done", res)
		}
	}
	got, err := r.Find(ctx(t), inbox, "appended@example.com", int64(len(raw)))
	if err != nil || len(got) != 1 {
		t.Fatalf("Find after appending twice = %v, %v; want one", got, err)
	}
	m := only(t, v.sync(t, r))
	if subject(m) != "appended" || !m.Flags.Seen {
		t.Errorf("the next sync shows %q seen=%v, want the appended message seen", subject(m), m.Flags.Seen)
	}
}

// An op the server refuses, a move to a folder there is not, fails.
func testFailed(t *testing.T, start Fixture) {
	_, r, _, m := started(t, start)
	if s := describe(t, r).Features[backend.FeatureMove]; s.Level == backend.LevelUnsupported {
		t.Skip("Move unsupported:", s.Reason)
	}
	if res := apply(t, r, backend.Move{At: m.Location, To: "no such folder"}); res.Outcome != backend.Failed || res.Reason == "" {
		t.Errorf("Move to a missing folder = %+v, want failed with a reason", res)
	}
}

// Each op whose feature is declared unsupported is answered unsupported, with
// the feature's reason.
func testUnsupported(t *testing.T, start Fixture) {
	_, r, v, m := started(t, start)
	d := describe(t, r)
	inbox := v.folder(t, "INBOX")
	ops := map[backend.Feature]backend.Op{
		backend.FeatureMove:             backend.Move{At: m.Location, To: inbox},
		backend.FeatureCopy:             backend.Copy{At: m.Location, To: inbox},
		backend.FeatureAddToFolder:      backend.AddToFolder{At: m.Location, To: inbox},
		backend.FeatureRemoveFromFolder: backend.RemoveFromFolder{At: m.Location},
		backend.FeaturePurge:            backend.Purge{At: m.Location},
		backend.FeatureAppend:           backend.Append{To: inbox, Raw: Message("x@example.com", "x"), MessageID: "x@example.com"},
	}
	for f, op := range ops {
		if s := d.Features[f]; s.Level == backend.LevelUnsupported {
			if res := apply(t, r, op); res.Outcome != backend.Unsupported || res.Reason != s.Reason {
				t.Errorf("%s = %+v, want unsupported: %s", f, res, s.Reason)
			}
		}
	}
}

func testWatch(t *testing.T, start Fixture) {
	s, r, v, _ := started(t, start)
	c, cancel := context.WithCancel(ctx(t))
	defer cancel()
	wake := make(chan []backend.FolderRef, 1)
	done := make(chan error, 1)
	go func() {
		done <- r.Watch(c, func(fs []backend.FolderRef) {
			select {
			case wake <- fs:
			default:
			}
		})
	}()
	if describe(t, r).Features[backend.FeaturePush].Level == backend.LevelUnsupported {
		if err := <-done; !errors.Is(err, backend.ErrUnsupported) {
			t.Errorf("Watch without push = %v, want ErrUnsupported", err)
		}
		return
	}
	defer func() { cancel(); <-done }()
	inbox := v.folder(t, "INBOX")
	select { // it wakes once as it starts watching
	case <-wake:
	case err := <-done:
		t.Fatalf("Watch = %v before it woke", err)
	case <-time.After(10 * time.Second):
		t.Fatal("Watch did not wake as it started watching")
	}
	s.Deliver(t, "INBOX", Message("watched@example.com", "watched"))
	select {
	case fs := <-wake:
		if fs != nil && !slices.Contains(fs, inbox) {
			t.Errorf("woken for %v, want the inbox", fs)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Watch did not wake for new mail")
	}
}

func testAuthFailed(t *testing.T, start Fixture) {
	s := start(t)
	s.RejectAuth(t)
	_, err := s.Receiver(t).Sync(ctx(t), backend.SyncRequest{}, func(backend.Page) error { return nil })
	if !errors.Is(err, backend.ErrAuthFailed) {
		t.Errorf("Sync with refused credentials = %v, want ErrAuthFailed", err)
	}
}
