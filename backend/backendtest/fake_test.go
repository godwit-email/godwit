package backendtest_test

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
	"github.com/godwit-email/godwit/backend/backendtest"
)

// The contract runs from outside its package against a minimal in-memory
// backend: one folder per name, locations "epoch/n", a folder cursor "epoch/last".
func TestContract(t *testing.T) {
	backendtest.Run(t, func(*testing.T) backendtest.Server { return newServer() })
}

type message struct {
	raw   []byte
	flags backend.Flags
}

type folder struct {
	epoch, next int
	msgs        map[int]*message
}

type server struct {
	mu       sync.Mutex
	folders  map[string]*folder
	rejected bool
	changed  chan struct{} // closed and replaced on each delivery
}

func newServer() *server {
	return &server{folders: map[string]*folder{}, changed: make(chan struct{})}
}

func (s *server) Receiver(t *testing.T) backend.Receiver {
	r := &receiver{s}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func (s *server) folder(name string) *folder {
	if s.folders[name] == nil {
		s.folders[name] = &folder{epoch: 1, next: 1, msgs: map[int]*message{}}
	}
	return s.folders[name]
}

func (s *server) put(name string, m *message) backend.MessageRef {
	f := s.folder(name)
	n := f.next
	f.next++
	f.msgs[n] = m
	close(s.changed)
	s.changed = make(chan struct{})
	return ref(f.epoch, n)
}

func (s *server) Deliver(_ *testing.T, name string, raw []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.put(name, &message{raw: raw})
}

func (s *server) Reset(_ *testing.T, name string) {
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

func (s *server) RejectAuth(*testing.T) {
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

type receiver struct{ s *server }

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

func (r *receiver) Sync(_ context.Context, req backend.SyncRequest, commit func(backend.Page) error) (bool, error) {
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
		for _, n := range slices.Sorted(maps.Keys(f.msgs)) {
			m := f.msgs[n]
			snap.Messages[ref(f.epoch, n)] = m.flags
			if n <= last {
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
	unlock, err := r.lock()
	if err != nil {
		return backend.Result{}, err
	}
	defer unlock()
	switch op := op.(type) {
	case backend.SetFlags:
		m := r.at(op.At)
		if m == nil {
			return backend.Result{Outcome: backend.Failed, Reason: "fake.gone"}, nil
		}
		m.flags.Seen = (m.flags.Seen || op.Add.Seen) && !op.Remove.Seen
		m.flags.Flagged = (m.flags.Flagged || op.Add.Flagged) && !op.Remove.Flagged
		m.flags.Answered = (m.flags.Answered || op.Add.Answered) && !op.Remove.Answered
		m.flags.Draft = (m.flags.Draft || op.Add.Draft) && !op.Remove.Draft
		return backend.Result{Outcome: backend.Done}, nil
	case backend.Move:
		m := r.at(op.At)
		if m == nil || r.s.folders[string(op.To)] == nil {
			return backend.Result{Outcome: backend.Failed, Reason: "fake.gone"}, nil
		}
		_, n := parse(string(op.At.Ref))
		delete(r.s.folders[string(op.At.Folder)].msgs, n)
		return backend.Result{Outcome: backend.Done, Ref: r.s.put(string(op.To), m)}, nil
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
