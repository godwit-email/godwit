package backendtest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"net/mail"
	"net/textproto"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godwit-email/godwit/backend"
)

type message struct {
	raw   []byte
	flags backend.Flags
}

type folder struct {
	epoch, next int
	msgs        map[int]*message
}

// Fake is a minimal in-memory Server that passes the contract: one folder per
// name, locations "epoch/n", a folder cursor "epoch/last", IdentityLocation.
// Tests of local storage script it through OnSync and OnApply.
type Fake struct {
	mu       sync.Mutex
	folders  map[string]*folder
	rejected bool
	changed  chan struct{} // closed and replaced on each delivery

	// OnSync, when set, runs each Sync in place of the fake's own: it hands
	// commit the pages a test wants stored. It runs without the fake's lock.
	OnSync func(req backend.SyncRequest, commit func(backend.Page) error) (more bool, err error)
	// OnApply, when set, answers each Apply: apply is the fake's own Apply
	// (the server doing op), which it may call or not, and whose answer it may
	// replace (an Unlocated move, a refusal, a lost reply). It runs without the
	// fake's lock.
	OnApply func(op backend.Op, apply func() (backend.Result, error)) (backend.Result, error)
}

// NewFake returns an empty Fake.
func NewFake() *Fake {
	return &Fake{folders: map[string]*folder{}, changed: make(chan struct{})}
}

func (s *Fake) Receiver(t *testing.T) backend.Receiver {
	r := &receiver{s}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// Messages is the folder named name as the server has it: each location's flags.
func (s *Fake) Messages(name string) map[backend.MessageRef]backend.Flags {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[backend.MessageRef]backend.Flags{}
	if f := s.folders[name]; f != nil {
		for n, m := range f.msgs {
			out[ref(f.epoch, n)] = m.flags
		}
	}
	return out
}

// Remove deletes the message at r in the folder named name, as another client would.
func (s *Fake) Remove(name string, r backend.MessageRef) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f := s.folders[name]; f != nil {
		_, n := parse(string(r))
		delete(f.msgs, n)
	}
}

// SetFlags sets the flags of the message at r in the folder named name, as another client would.
func (s *Fake) SetFlags(name string, r backend.MessageRef, fl backend.Flags) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := (&receiver{s}).at(backend.Location{Folder: backend.FolderRef(name), Ref: r}); m != nil {
		m.flags = fl
	}
}

func (s *Fake) folder(name string) *folder {
	if s.folders[name] == nil {
		s.folders[name] = &folder{epoch: 1, next: 1, msgs: map[int]*message{}}
	}
	return s.folders[name]
}

func (s *Fake) put(name string, m *message) backend.MessageRef {
	f := s.folder(name)
	n := f.next
	f.next++
	f.msgs[n] = m
	close(s.changed)
	s.changed = make(chan struct{})
	return ref(f.epoch, n)
}

func (s *Fake) Deliver(_ *testing.T, name string, raw []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.put(name, &message{raw: raw})
}

func (s *Fake) Reset(_ *testing.T, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.folder(name)
	f.epoch++
	old := f.msgs
	f.msgs, f.next = map[int]*message{}, 1
	for _, n := range slices.Sorted(maps.Keys(old)) {
		f.msgs[f.next] = old[n]
		f.next++
	}
}

func (s *Fake) RejectAuth(*testing.T) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rejected = true
}

func ref(epoch, n int) backend.MessageRef { return backend.MessageRef(fmt.Sprintf("%d/%d", epoch, n)) }

func parse(r string) (epoch, n int) {
	a, b, _ := strings.Cut(r, "/")
	epoch, _ = strconv.Atoi(a)
	n, _ = strconv.Atoi(b)
	return
}

type receiver struct{ s *Fake }

func (r *receiver) Close() error { return nil }

func (r *receiver) lock() (func(), error) {
	r.s.mu.Lock()
	if r.s.rejected {
		r.s.mu.Unlock()
		return nil, fmt.Errorf("fake: %w", backend.ErrAuthFailed)
	}
	return r.s.mu.Unlock, nil
}

func role(name string) backend.Role {
	if name == "INBOX" {
		return backend.RoleInbox
	}
	return backend.Role(strings.ToLower(name))
}

// at is the message at l, nil if there is none.
func (r *receiver) at(l backend.Location) *message {
	f := r.s.folders[string(l.Folder)]
	if f == nil {
		return nil
	}
	epoch, n := parse(string(l.Ref))
	if epoch != f.epoch {
		return nil
	}
	return f.msgs[n]
}

// gone is Apply's error for l, which names no message: ErrFolderReset when
// the folder was reset since, else ErrNotFound.
func (r *receiver) gone(l backend.Location) error {
	if f := r.s.folders[string(l.Folder)]; f != nil {
		if epoch, _ := parse(string(l.Ref)); epoch != f.epoch {
			return backend.ErrFolderReset
		}
	}
	return backend.ErrNotFound
}

func (r *receiver) Sync(ctx context.Context, req backend.SyncRequest, commit func(backend.Page) error) (bool, error) {
	r.s.mu.Lock()
	hook := r.s.OnSync
	r.s.mu.Unlock()
	if hook != nil {
		return hook(req, commit)
	}
	unlock, err := r.lock()
	if err != nil {
		return false, err
	}
	defer unlock()
	for _, name := range slices.Sorted(maps.Keys(r.s.folders)) {
		f, fr := r.s.folders[name], backend.FolderRef(name)
		st, known := req.Folders[fr]
		epoch, last := parse(string(st.Cursor))
		var p backend.Page
		if !known {
			p.Changes = append(p.Changes, backend.FolderUpdated{Folder: backend.Folder{Ref: fr, Name: name, Role: role(name)}})
		} else if epoch != f.epoch {
			p.Changes = append(p.Changes, backend.FolderReset{Ref: fr})
			last, st.Synced = 0, false // reclaimed mail is not new
		}
		snap := backend.FolderSnapshot{Folder: fr, Messages: map[backend.MessageRef]backend.Flags{}}
		var refs []backend.MessageRef
		for n := range f.msgs {
			refs = append(refs, ref(f.epoch, n))
		}
		held := map[backend.MessageRef]bool{} // stored already, such as a move's new location: not reported again
		if req.Local != nil {
			if held, err = req.Local.HeldRefs(ctx, fr, refs); err != nil {
				return false, err
			}
		}
		for _, n := range slices.Sorted(maps.Keys(f.msgs)) {
			m := f.msgs[n]
			snap.Messages[ref(f.epoch, n)] = m.flags
			if n <= last || held[ref(f.epoch, n)] {
				continue
			}
			p.Changes = append(p.Changes, backend.MessageAppeared{
				Location: backend.Location{Folder: fr, Ref: ref(f.epoch, n)},
				Flags:    m.flags, Backfill: !st.Synced, Content: content(m.raw, req.HeaderFields),
			})
		}
		p.Changes = append(p.Changes, snap)
		p.Folders = map[backend.FolderRef]backend.FolderState{fr: {Cursor: backend.Cursor(fmt.Sprintf("%d/%d", f.epoch, f.next-1)), Synced: true}}
		if err := commit(p); err != nil {
			return false, err
		}
	}
	return false, nil
}

// content is what a sync stores of raw: no structure, so the raw message.
func content(raw []byte, fields []string) *backend.Content {
	return &backend.Content{Size: int64(len(raw)), Date: time.Now(), Header: header(raw, fields), Raw: raw}
}

// header is raw's header fields named in fields.
func header(raw []byte, fields []string) []byte {
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	var b bytes.Buffer
	for _, f := range fields {
		for _, v := range m.Header[textproto.CanonicalMIMEHeaderKey(f)] {
			fmt.Fprintf(&b, "%s: %s\r\n", f, v)
		}
	}
	return b.Bytes()
}

func (r *receiver) Fetch(_ context.Context, req backend.ContentRequest) (io.ReadCloser, error) {
	unlock, err := r.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	m := r.at(req.Location)
	if m == nil {
		return nil, backend.ErrNotFound
	}
	b := m.raw
	if req.HeaderFields != nil {
		b = header(m.raw, req.HeaderFields)
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (r *receiver) Headers(ctx context.Context, fr backend.FolderRef, refs []backend.MessageRef, fields []string) (map[backend.MessageRef][]byte, error) {
	out := map[backend.MessageRef][]byte{}
	for _, ref := range refs {
		rc, err := r.Fetch(ctx, backend.ContentRequest{Location: backend.Location{Folder: fr, Ref: ref}, HeaderFields: fields})
		if err == backend.ErrNotFound {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[ref], _ = io.ReadAll(rc)
	}
	return out, nil
}

func (r *receiver) Find(_ context.Context, fr backend.FolderRef, messageID string, size int64) ([]backend.MessageRef, error) {
	unlock, err := r.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	var out []backend.MessageRef
	if f := r.s.folders[string(fr)]; f != nil {
		for _, n := range slices.Sorted(maps.Keys(f.msgs)) {
			m := f.msgs[n]
			if msg, err := mail.ReadMessage(bytes.NewReader(m.raw)); err == nil &&
				msg.Header.Get("Message-ID") == "<"+messageID+">" && (size <= 0 || int64(len(m.raw)) == size) {
				out = append(out, ref(f.epoch, n))
			}
		}
	}
	return out, nil
}

func (r *receiver) Apply(_ context.Context, op backend.Op) (backend.Result, error) {
	r.s.mu.Lock()
	hook := r.s.OnApply
	r.s.mu.Unlock()
	if hook != nil {
		return hook(op, func() (backend.Result, error) { return r.apply(op) })
	}
	return r.apply(op)
}

func (r *receiver) apply(op backend.Op) (backend.Result, error) {
	unlock, err := r.lock()
	if err != nil {
		return backend.Result{}, err
	}
	defer unlock()
	switch op := op.(type) {
	case backend.SetFlags:
		m := r.at(op.At)
		if m == nil {
			return backend.Result{}, r.gone(op.At)
		}
		m.flags.Seen = (m.flags.Seen || op.Add.Seen) && !op.Remove.Seen
		m.flags.Flagged = (m.flags.Flagged || op.Add.Flagged) && !op.Remove.Flagged
		m.flags.Answered = (m.flags.Answered || op.Add.Answered) && !op.Remove.Answered
		m.flags.Draft = (m.flags.Draft || op.Add.Draft) && !op.Remove.Draft
		return backend.Result{Outcome: backend.Done}, nil
	case backend.Move:
		m := r.at(op.At)
		if m == nil {
			return backend.Result{}, r.gone(op.At)
		}
		if r.s.folders[string(op.To)] == nil {
			return backend.Result{Outcome: backend.Failed, Reason: "fake.no_folder"}, nil
		}
		_, n := parse(string(op.At.Ref))
		delete(r.s.folders[string(op.At.Folder)].msgs, n)
		dst := r.s.put(string(op.To), m)
		if want, _ := parse(string(op.Cursor)); op.Cursor != "" && want != r.s.folders[string(op.To)].epoch {
			return backend.Result{Outcome: backend.Unlocated}, nil
		}
		return backend.Result{Outcome: backend.Done, Ref: dst}, nil
	}
	return backend.Result{Outcome: backend.Unsupported, Reason: "fake.unsupported"}, nil
}

func (r *receiver) Watch(ctx context.Context, wake func([]backend.FolderRef)) error {
	for first := true; ; first = false {
		unlock, err := r.lock()
		if err != nil {
			return err
		}
		ch := r.s.changed
		unlock()
		if first {
			wake(nil)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ch:
			wake(nil)
		}
	}
}

func (r *receiver) Check(ctx context.Context) (backend.CheckResult, error) {
	d, err := r.Describe(ctx)
	if err != nil {
		return backend.CheckResult{}, err
	}
	unlock, _ := r.lock()
	defer unlock()
	return backend.CheckResult{Host: "fake.invalid", Folders: len(r.s.folders), Description: d}, nil
}

func (r *receiver) Describe(context.Context) (backend.Description, error) {
	unlock, err := r.lock()
	if err != nil {
		return backend.Description{}, err
	}
	defer unlock()
	d := backend.Description{Features: map[backend.Feature]backend.Support{}, Identity: backend.IdentityLocation, PollInterval: 5 * time.Minute}
	for _, f := range backend.AllFeatures {
		d.Features[f] = backend.Support{Level: backend.LevelUnsupported, Reason: "fake.unsupported"}
	}
	for _, f := range []backend.Feature{backend.FeatureMove, backend.FeatureSeen, backend.FeatureFlagged, backend.FeatureAnswered, backend.FeatureDraft, backend.FeaturePush} {
		d.Features[f] = backend.Support{Level: backend.LevelSupported}
	}
	return d, nil
}
