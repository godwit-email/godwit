package pop3

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/mail"
	"strings"

	"github.com/godwit-email/godwit/backend"
)

// Fetch implements backend.Receiver: the whole message with RETR, header
// fields with TOP n 0. POP3 gives no parts.
func (r *Receiver) Fetch(ctx context.Context, req backend.ContentRequest) (io.ReadCloser, error) {
	if req.Part != "" {
		return nil, fmt.Errorf("pop3: part %q: %w", req.Part, backend.ErrNotFound)
	}
	var out []byte
	err := r.do(ctx, func(s *session) error {
		e, ok := s.find(req.Ref)
		if !ok {
			return backend.ErrNotFound
		}
		var err error
		if req.HeaderFields == nil {
			out, err = s.retr(e)
		} else if out, err = s.top(e); err == nil {
			out = headerFields(out, req.HeaderFields)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(out)), nil
}

// Headers implements backend.Receiver, with TOP n 0 for each.
func (r *Receiver) Headers(ctx context.Context, _ backend.FolderRef, refs []backend.MessageRef, fields []string) (map[backend.MessageRef][]byte, error) {
	out := map[backend.MessageRef][]byte{}
	err := r.do(ctx, func(s *session) error {
		for _, ref := range refs {
			if e, ok := s.find(ref); ok {
				h, err := s.top(e)
				if err != nil {
					return err
				}
				out[ref] = headerFields(h, fields)
			}
		}
		return nil
	})
	return out, err
}

// Find implements backend.Receiver: it reads the header of each message in
// the inbox, of those of the size when one is given.
func (r *Receiver) Find(ctx context.Context, _ backend.FolderRef, messageID string, size int64) ([]backend.MessageRef, error) {
	var out []backend.MessageRef
	err := r.do(ctx, func(s *session) error {
		for _, e := range s.list {
			if size > 0 && e.size != size {
				continue
			}
			h, err := s.top(e)
			if err != nil {
				return err
			}
			if m, err := mail.ReadMessage(bytes.NewReader(h)); err == nil &&
				strings.Trim(strings.TrimSpace(m.Header.Get("Message-ID")), "<>") == messageID {
				out = append(out, backend.MessageRef(e.uid))
			}
		}
		return nil
	})
	return out, err
}

// Apply implements backend.Receiver. Flags are kept locally and the server
// has only the inbox, so only Purge reaches the server.
func (r *Receiver) Apply(ctx context.Context, op backend.Op) (backend.Result, error) {
	switch op := op.(type) {
	case backend.SetFlags:
		return backend.Result{Outcome: backend.Done}, nil
	case backend.Move:
		if op.To == Inbox {
			return backend.Result{Outcome: backend.Done, Ref: op.At.Ref}, nil
		}
		return backend.Result{Outcome: backend.Failed, Reason: ReasonInboxOnly}, nil
	case backend.Purge:
		return r.purge(ctx, op)
	}
	return backend.Result{Outcome: backend.Unsupported, Reason: ReasonInboxOnly}, nil
}

// purge marks the message with DELE, which the server carries out at QUIT.
// A message already gone is done.
func (r *Receiver) purge(ctx context.Context, op backend.Purge) (backend.Result, error) {
	confirm := func() bool { return op.Confirm == nil || op.Confirm() }
	cancelled := backend.Result{Outcome: backend.Failed, Reason: backend.Cancelled}
	res := backend.Result{Outcome: backend.Done}
	err := r.do(ctx, func(s *session) error {
		e, ok := s.find(op.At.Ref)
		switch {
		case !ok:
			return nil
		case !confirm():
			res = cancelled
			return nil
		}
		if err := s.c.Dele(e.n); err != nil {
			if !serverErr(err) {
				return err
			}
			res = backend.Result{Outcome: backend.Failed, Reason: err.Error()}
			return nil
		}
		if !confirm() { // just before the QUIT that makes it final
			res = cancelled
			return s.c.Rset()
		}
		return nil
	})
	if err != nil {
		return backend.Result{}, err
	}
	return res, nil
}
