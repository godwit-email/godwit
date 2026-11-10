package pop3

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/mail"
	"slices"
	"strings"
	"time"

	"github.com/godwit-email/godwit/backend"
)

// cursor is the inbox's cursor: the UIDL of the newest message the last sync saw.
type cursor struct {
	Newest string `json:"n"`
}

func (c cursor) encode() backend.Cursor {
	b, _ := json.Marshal(c)
	return backend.Cursor(b)
}

const pageSize = 50

// Sync implements backend.Receiver. Gone locations are reported one by one,
// not as a snapshot, which would reset the local-only flags. The first
// round takes the window's mail as backfill, page by page; later rounds take
// what came after the cursor's message as new; Older takes mail before the
// oldest one held.
func (r *Receiver) Sync(ctx context.Context, req backend.SyncRequest, commit func(backend.Page) error) (more bool, err error) {
	st := req.Folders[Inbox]
	if req.Older > 0 && !st.Synced {
		return false, backend.ErrNotSynced
	}
	var cur cursor
	if st.Cursor != "" {
		if err := json.Unmarshal([]byte(st.Cursor), &cur); err != nil {
			return false, fmt.Errorf("pop3: cursor %q: %w", st.Cursor, err)
		}
	}
	err = r.do(ctx, func(s *session) error {
		held := map[backend.MessageRef]string{}
		if req.Local != nil {
			var err error
			if held, err = req.Local.Held(ctx, Inbox); err != nil {
				return err
			}
		}
		isHeld := func(e entry) bool { _, ok := held[backend.MessageRef(e.uid)]; return ok }
		changes := []backend.Change{backend.FolderUpdated{Folder: backend.Folder{Ref: Inbox, Name: "INBOX", Role: backend.RoleInbox}}}
		for _, ref := range slices.Sorted(maps.Keys(held)) {
			if _, ok := s.find(ref); !ok {
				changes = append(changes, backend.MessageGone{Location: backend.Location{Folder: Inbox, Ref: ref}})
			}
		}
		next := st
		var pick []entry // newest first
		backfill := true
		switch {
		case req.Older > 0:
			oldest := slices.IndexFunc(s.list, isHeld)
			if oldest < 0 {
				oldest = len(s.list)
			}
			pick = unheld(s.list[:oldest], isHeld)
			more = len(pick) > req.Older
			pick = pick[:min(len(pick), req.Older)]
		case !st.Synced:
			var err error
			if pick, err = window(s, req.Window, isHeld); err != nil {
				return err
			}
		default:
			backfill = false
			bound := slices.IndexFunc(s.list, func(e entry) bool { return e.uid == cur.Newest })
			if bound < 0 { // the cursor's message is gone: the newest one held bounds what is new
				// shortcut: with every held message gone too, all mail left counts as
				// new; keep more UIDLs in the cursor if old mail then shows up as new.
				for bound = len(s.list) - 1; bound >= 0 && !isHeld(s.list[bound]); bound-- {
				}
			}
			pick = unheld(s.list[bound+1:], isHeld)
		}
		if req.Older == 0 {
			next.Cursor = ""
			if len(s.list) > 0 {
				next.Cursor = cursor{s.list[len(s.list)-1].uid}.encode()
			}
		}
		size := req.PageSize
		if size <= 0 {
			size = pageSize
		}
		for pages := 1; ; pages++ {
			n := min(size, len(pick))
			for _, e := range pick[:n] {
				c, err := content(s, e, req.HeaderFields)
				if err != nil {
					return err
				}
				changes = append(changes, backend.MessageAppeared{Location: backend.Location{Folder: Inbox, Ref: backend.MessageRef(e.uid)}, Backfill: backfill, Content: c})
			}
			pick = pick[n:]
			stop := len(pick) == 0 || !st.Synced && req.Pages > 0 && pages >= req.Pages
			if stop && !st.Synced {
				next.Synced = len(pick) == 0
				more = !next.Synced
			}
			if err := commit(backend.Page{Changes: changes, Folders: map[backend.FolderRef]backend.FolderState{Inbox: next}}); err != nil {
				return err
			}
			if stop {
				return nil
			}
			changes = nil
		}
	})
	return more, err
}

// unheld is the entries of list held does not hold, newest first.
func unheld(list []entry, held func(entry) bool) []entry {
	var out []entry
	for _, e := range slices.Backward(list) {
		if !held(e) {
			out = append(out, e)
		}
	}
	return out
}

// window is the mail of w that held does not hold, newest first. A window
// since a date reads each Date header with TOP, newest first, until one is older.
func window(s *session, w backend.Window, held func(entry) bool) ([]entry, error) {
	list := s.list
	switch {
	case w.Newest > 0:
		return unheld(list[max(0, len(list)-w.Newest):], held), nil
	case w.Since.IsZero():
		return unheld(list, held), nil
	}
	var out []entry
	for _, e := range slices.Backward(list) {
		if held(e) {
			continue // in the window when it was taken
		}
		h, err := s.top(e)
		if err != nil {
			return nil, err
		}
		if d, ok := date(h); ok && d.Before(w.Since) {
			break
		}
		out = append(out, e)
	}
	return out, nil
}

// top is the header of the message e, read with TOP n 0.
func (s *session) top(e entry) ([]byte, error) {
	b, err := s.c.Cmd("TOP", true, e.n, 0)
	if err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func (s *session) retr(e entry) ([]byte, error) {
	b, err := s.c.RetrRaw(e.n)
	if err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// date is the Date header field of the message raw begins.
func date(raw []byte) (time.Time, bool) {
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return time.Time{}, false
	}
	d, err := m.Header.Date()
	return d, err == nil
}

// content is what a sync stores of e: the whole message, as POP3 gives no structure.
func content(s *session, e entry, fields []string) (*backend.Content, error) {
	raw, err := s.retr(e)
	if err != nil {
		return nil, err
	}
	d, ok := date(raw)
	if !ok {
		d = time.Now()
	}
	return &backend.Content{Size: e.size, Date: d, Header: headerFields(raw, fields), Raw: raw}, nil
}

// headerFields is the fields of raw's header named in names, as they are,
// followed by the blank line that ends a header.
func headerFields(raw []byte, names []string) []byte {
	var out bytes.Buffer
	keep := false
	for line := range bytes.Lines(raw) {
		if len(bytes.TrimRight(line, "\r\n")) == 0 {
			break
		}
		if line[0] != ' ' && line[0] != '\t' {
			name, _, _ := bytes.Cut(line, []byte(":"))
			keep = slices.ContainsFunc(names, func(n string) bool { return strings.EqualFold(n, string(bytes.TrimSpace(name))) })
		}
		if keep {
			out.Write(line)
		}
	}
	out.WriteString("\r\n")
	return out.Bytes()
}
