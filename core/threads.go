package core

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	gomail "github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/store"
)

// View selects what a list shows: one folder, a unified view ("inbox",
// "starred") or one label's mail across accounts. Exactly one is set.
type View struct {
	FolderID  int64
	Unified   string
	LabelID   int64
	AccountID int64
}

// ThreadSummary is a conversation as listed. Count, Unread and Flagged cover the whole thread.
type ThreadSummary struct {
	ID               int64
	Subject, Snippet string
	Participants     []string
	Count, Unread    int
	Flagged          int // messages starred
	LastDate         time.Time
	HasAttachments   bool
	Security         gomail.SecurityReport
}

// ThreadPage is one page of threads.
type ThreadPage struct {
	Items      []ThreadSummary
	NextCursor string
}

// Thread is a conversation with its messages, oldest first. A message the
// server keeps in several folders (Gmail's INBOX, labels and All Mail) is in
// Messages once; Copies holds its other copies, which an action on the whole
// thread still covers, and CopyOf, for each, the ID of the one in Messages.
type Thread struct {
	ID       int64
	Subject  string
	Messages []MessageSummary
	Copies   []MessageSummary
	CopyOf   []int64
}

// All is every message of the thread, Messages then Copies.
func (t Thread) All() []MessageSummary { return append(slices.Clip(t.Messages), t.Copies...) }

// CopiesOf are the other copies of the message id.
func (t Thread) CopiesOf(id int64) []MessageSummary {
	var out []MessageSummary
	for i, c := range t.Copies {
		if t.CopyOf[i] == id {
			out = append(out, c)
		}
	}
	return out
}

// Merged is Messages, each with its copies' state as ListThreads counts it:
// unread while a copy is, starred while one is, and the gravest security.
func (t Thread) Merged() []MessageSummary {
	out := slices.Clone(t.Messages)
	for i := range out {
		m := &out[i]
		for _, c := range t.CopiesOf(m.ID) {
			if !slices.Contains(c.Flags, `\Seen`) && slices.Contains(m.Flags, `\Seen`) {
				m.Flags = slices.DeleteFunc(slices.Clone(m.Flags), func(f string) bool { return f == `\Seen` })
			}
			if slices.Contains(c.Flags, `\Flagged`) && !slices.Contains(m.Flags, `\Flagged`) {
				m.Flags = append(slices.Clone(m.Flags), `\Flagged`)
			}
			if gomail.SecurityRank(c.Security.Status) > gomail.SecurityRank(m.Security.Status) {
				m.Security = c.Security
			}
		}
	}
	return out
}

// copyCols are the columns, on messages alias m, that copyOf reads: the
// thread, the Message-ID and how much the copy's folder is preferred. The
// inbox copy shows, so acting on what is shown acts in the inbox; All Mail's
// last, as it holds a copy of every message.
const copyCols = `m.thread_id, m.message_id, CASE (SELECT f.role FROM folders f WHERE f.id = m.folder_id) WHEN 'inbox' THEN 0 WHEN 'all' THEN 2 ELSE 1 END`

// copyOf gives, for rows each holding copyCols at col, the index of the row
// that shows each row's message, its own index for that row: of a thread's
// rows sharing a Message-ID, the one of the preferred folder, then lowest id
// (idCol). Copies are only
// ever within a thread, so the same mail in two accounts' threads stays in
// both; a row without a Message-ID is always its own message.
func copyOf(rows [][]any, idCol, col int) []int {
	key := func(r []any) string {
		if mid := store.Str(r[col+1]); mid != "" {
			return fmt.Sprint(store.Int(r[col]), "\x00", mid)
		}
		return ""
	}
	best := map[string]int{}
	for i, r := range rows {
		k := key(r)
		if k == "" {
			continue
		}
		if j, ok := best[k]; !ok || copyLess(r, rows[j], idCol, col) {
			best[k] = i
		}
	}
	of := make([]int, len(rows))
	for i, r := range rows {
		if j, ok := best[key(r)]; ok {
			of[i] = j
		} else {
			of[i] = i
		}
	}
	return of
}

func copyLess(a, b []any, idCol, col int) bool {
	if ra, rb := store.Int(a[col+2]), store.Int(b[col+2]); ra != rb {
		return ra < rb
	}
	return store.Int(a[idCol]) < store.Int(b[idCol])
}

// ListThreads lists threads with at least one message in the view, newest first.
func (s *Service) ListThreads(ctx context.Context, v View, cursor string, limit int) (ThreadPage, error) {
	cond, args, err := viewCond(v)
	if err != nil {
		return ThreadPage{}, err
	}
	where := "EXISTS (SELECT 1 FROM messages m WHERE m.thread_id = t.id AND " + cond + ")"
	if cursor != "" {
		d, id, err := decodeCursor(cursor)
		if err != nil {
			return ThreadPage{}, err
		}
		where += " AND (t.last_date < ? OR (t.last_date = ? AND t.id < ?))"
		args = append(args, d, d, id)
	}
	limit = clampLimit(limit)
	rows, err := s.db.Query(ctx, "SELECT t.id, t.subject, t.last_date FROM threads t WHERE "+where+
		" ORDER BY t.last_date DESC, t.id DESC LIMIT ?", append(args, limit+1)...)
	if err != nil {
		return ThreadPage{}, err
	}
	var page ThreadPage
	idx := map[int64]int{}
	var ids []any
	for _, r := range rows.Values {
		if len(page.Items) == limit {
			last := page.Items[limit-1]
			page.NextCursor = encodeCursor(last.LastDate.Unix(), last.ID)
			break
		}
		t := ThreadSummary{ID: store.Int(r[0]), Subject: store.Str(r[1]), LastDate: time.Unix(store.Int(r[2]), 0)}
		idx[t.ID] = len(page.Items)
		ids = append(ids, t.ID)
		page.Items = append(page.Items, t)
	}
	if len(ids) == 0 {
		return page, nil
	}
	ms, err := s.db.Query(ctx, `SELECT thread_id, from_addr, snippet, flags, has_attachments, auth_results, security_status, security_reason, security_scanned_at, m.id, `+copyCols+` FROM messages m
		WHERE thread_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`) AND `+notPurgedCond+` ORDER BY date DESC, id DESC`, ids...)
	if err != nil {
		return ThreadPage{}, err
	}
	seen := map[string]bool{}
	of := copyOf(ms.Values, 9, 10)
	unread, flagged := map[int]bool{}, map[int]bool{} // a message is unread or starred if any copy is
	for i, r := range ms.Values {
		unread[of[i]] = unread[of[i]] || !strings.Contains(store.Str(r[3]), `\Seen`)
		flagged[of[i]] = flagged[of[i]] || strings.Contains(store.Str(r[3]), `\Flagged`)
	}
	for _, r := range ms.Values { // a warning on any copy is the thread's
		t := &page.Items[idx[store.Int(r[0])]]
		fromAddr := store.Str(r[1])
		secStatus := store.Str(r[6])
		secReason := store.Str(r[7])
		secScanned := store.Int(r[8])
		var sec gomail.SecurityReport
		if secStatus != "" {
			sec = gomail.DBToSecurityReport(fromAddr, store.Str(r[5]), secStatus, secReason)
		} else if secScanned > 0 {
			sec = gomail.EvaluateSecurity(fromAddr, store.Str(r[5]))
		}
		if gomail.SecurityRank(sec.Status) > gomail.SecurityRank(t.Security.Status) {
			t.Security = sec
		}
	}
	for i, r := range ms.Values {
		if of[i] != i {
			continue
		}
		t := &page.Items[idx[store.Int(r[0])]]
		if t.Count == 0 {
			t.Snippet = gomail.Snippet(store.Str(r[2])) // newest first; clean of what an older sync kept
		}
		t.Count++
		if unread[i] {
			t.Unread++
		}
		if flagged[i] {
			t.Flagged++
		}
		t.HasAttachments = t.HasAttachments || store.Int(r[4]) != 0
		name := displayName(store.Str(r[1]))
		if k := fmt.Sprint(t.ID, "\x00", name); !seen[k] {
			seen[k] = true
			t.Participants = append(t.Participants, name)
		}
	}
	return page, nil
}

// GetThread returns the thread's messages by date ascending, each once.
func (s *Service) GetThread(ctx context.Context, id int64) (Thread, error) {
	rows, err := s.db.Query(ctx, "SELECT subject FROM threads WHERE id = ?", id)
	if err != nil {
		return Thread{}, err
	}
	if len(rows.Values) == 0 {
		return Thread{}, fmt.Errorf("thread %d not found", id)
	}
	t := Thread{ID: id, Subject: store.Str(rows.Values[0][0])}
	ms, err := s.db.Query(ctx, "SELECT "+msgCols+", "+copyCols+" FROM messages m WHERE m.thread_id = ? AND "+notPurgedCond+" ORDER BY m.date, m.id", id)
	if err != nil {
		return Thread{}, err
	}
	of := copyOf(ms.Values, 0, msgColCount)
	for i, r := range ms.Values {
		if of[i] == i {
			t.Messages = append(t.Messages, scanSummary(r))
		} else {
			t.Copies = append(t.Copies, scanSummary(r))
			t.CopyOf = append(t.CopyOf, store.Int(ms.Values[of[i]][0]))
		}
	}
	return t, nil
}
