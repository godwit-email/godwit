package core

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/godwit-email/godwit/core/store"
)

// View selects what a list shows: one folder or a unified view ("inbox", "starred").
type View struct {
	FolderID int64
	Unified  string
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
}

// ThreadPage is one page of threads.
type ThreadPage struct {
	Items      []ThreadSummary
	NextCursor string
}

// Thread is a conversation with its messages, oldest first.
type Thread struct {
	ID       int64
	Subject  string
	Messages []MessageSummary
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
	ms, err := s.db.Query(ctx, `SELECT thread_id, from_addr, snippet, flags, has_attachments FROM messages
		WHERE thread_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`) ORDER BY date DESC, id DESC`, ids...)
	if err != nil {
		return ThreadPage{}, err
	}
	seen := map[string]bool{}
	for _, r := range ms.Values {
		t := &page.Items[idx[store.Int(r[0])]]
		if t.Count == 0 {
			t.Snippet = store.Str(r[2]) // newest first
		}
		t.Count++
		if !strings.Contains(store.Str(r[3]), `\Seen`) {
			t.Unread++
		}
		if strings.Contains(store.Str(r[3]), `\Flagged`) {
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

// GetThread returns the thread's messages by date ascending.
func (s *Service) GetThread(ctx context.Context, id int64) (Thread, error) {
	rows, err := s.db.Query(ctx, "SELECT subject FROM threads WHERE id = ?", id)
	if err != nil {
		return Thread{}, err
	}
	if len(rows.Values) == 0 {
		return Thread{}, fmt.Errorf("thread %d not found", id)
	}
	t := Thread{ID: id, Subject: store.Str(rows.Values[0][0])}
	ms, err := s.db.Query(ctx, "SELECT "+msgCols+" FROM messages m WHERE m.thread_id = ? ORDER BY m.date, m.id", id)
	if err != nil {
		return Thread{}, err
	}
	for _, r := range ms.Values {
		t.Messages = append(t.Messages, scanSummary(r))
	}
	return t, nil
}
