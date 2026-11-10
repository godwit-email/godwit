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
		var pick []item // backfill newest first; new arrivals first, oldest first
		since := false  // pick holds a whole scan of a window since a date
		switch {
		case req.Older > 0:
			oldest := slices.IndexFunc(s.list, isHeld)
			if oldest < 0 {
				oldest = len(s.list)
			}
			es := unheld(s.list[:oldest], isHeld)
			more = len(es) > req.Older
			pick = items(es[:min(len(es), req.Older)], true)
		case !st.Synced:
			es, err := window(s, req.Window, isHeld, len(s.list))
			if err != nil {
				return err
			}
			pick, since = items(es, true), !req.Window.Since.IsZero()
		default:
			bound := -1 // no cursor: the inbox was empty, so all of it is new
			lost := false
			if cur.Newest != "" {
				bound = slices.IndexFunc(s.list, func(e entry) bool { return e.uid == cur.Newest })
				if bound < 0 { // the cursor's message is gone: the newest one held bounds what is new
					for bound = len(s.list) - 1; bound >= 0 && !isHeld(s.list[bound]); bound-- {
					}
				}
				if bound < 0 {
					// Every held message is gone too. POP3 lists in arrival order, so the
					// mail left is mostly old: take it as a first sync would.
					// shortcut: a message that arrived at that moment comes as backfill,
					// with no notification; keep more UIDLs in the cursor once a user
					// reports such a missed notification.
					bound, lost = len(s.list)-1, true
				}
			}
			es := unheld(s.list[bound+1:], isHeld)
			slices.Reverse(es) // oldest first, so a page's newest message bounds all fetched so far
			pick = items(es, false)
			// The window may have grown since it was filled. One since a date costs
			// a TOP of each message outside it, so it is scanned again only once
			// it reaches further back than the scan before (Filled).
			if w := req.Window; lost || w.Since.IsZero() || st.Filled.IsZero() || w.Since.Before(st.Filled) {
				old, err := window(s, w, isHeld, bound+1)
				if err != nil {
					return err
				}
				pick, since = append(pick, items(old, true)...), !w.Since.IsZero()
			}
		}
		size := req.PageSize
		if size <= 0 {
			size = pageSize
		}
		budget := r.cfg.PageBytes
		if budget <= 0 {
			budget = 32 << 20
		}
		for pages := 1; ; pages++ {
			// Whole messages: a page also ends once it holds budget bytes.
			n, got := 0, int64(0)
			for n < min(size, len(pick)) && (n == 0 || got < budget) {
				it := pick[n]
				c, err := content(s, it.entry, req.HeaderFields)
				if err != nil {
					return err
				}
				changes = append(changes, backend.MessageAppeared{Location: backend.Location{Folder: Inbox, Ref: backend.MessageRef(it.uid)}, Backfill: it.backfill, Content: c})
				n, got = n+1, got+int64(len(c.Raw))
			}
			pick = pick[n:]
			// The cursor moves only past new mail this round has committed, so one
			// that dies on a later page leaves the rest new for the next.
			arriving := slices.ContainsFunc(pick, func(it item) bool { return !it.backfill })
			switch {
			case req.Older > 0:
			case arriving:
				// The entry before the first one left: committed, or held already.
				if i := slices.IndexFunc(s.list, func(e entry) bool { return e.uid == pick[0].uid }); i > 0 {
					next.Cursor = cursor{s.list[i-1].uid}.encode()
				}
			case len(s.list) > 0:
				next.Cursor = cursor{s.list[len(s.list)-1].uid}.encode()
			default:
				next.Cursor = ""
			}
			// A backfill yields after Pages; new mail always comes down in full.
			stop := len(pick) == 0 || req.Older == 0 && !arriving && req.Pages > 0 && pages >= req.Pages
			if stop && req.Older == 0 {
				if len(pick) == 0 {
					next.Synced = true
					if since {
						next.Filled = req.Window.Since
					}
				} else {
					more = true
				}
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

// item is an entry to fetch, as backfill or as a new arrival.
type item struct {
	entry
	backfill bool
}

func items(es []entry, backfill bool) []item {
	out := make([]item, len(es))
	for i, e := range es {
		out[i] = item{e, backfill}
	}
	return out
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

// window is the mail of w among the first upto entries of the listing that
// held does not hold, newest first. A window since a date reads each Date
// header with TOP: the sender's dates, so the whole listing is read.
func window(s *session, w backend.Window, held func(entry) bool, upto int) ([]entry, error) {
	switch {
	case w.Newest > 0:
		from := max(0, len(s.list)-w.Newest)
		if from >= upto {
			return nil, nil
		}
		return unheld(s.list[from:upto], held), nil
	case w.Since.IsZero():
		return unheld(s.list[:upto], held), nil
	}
	var out []entry
	for _, e := range slices.Backward(s.list[:upto]) {
		if held(e) {
			continue
		}
		h, err := s.top(e)
		if err != nil {
			return nil, err
		}
		if d, ok := date(h); ok && d.Before(w.Since) {
			continue // a late delivery's old date says nothing of the mail before it
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
