package core

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	gomail "github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/mailsync"
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
	From             string
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
// server keeps in several folders (Gmail's INBOX, labels and All Mail) is one
// message, in all of its FolderIDs. One in the account's Sent folder is your
// own mail and reads as read (\Seen), as ListThreads counts it, whatever the
// server's flags. ReplyTarget is the message a reply to the whole thread
// answers: the newest that is not the user's own forward, whose recipient the
// reply is not for; 0 when all are, and the newest then is the one.
type Thread struct {
	ID          int64
	Subject     string
	Messages    []MessageSummary
	ReplyTarget int64
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
	ms, err := s.db.Query(ctx, `SELECT thread_id, from_addr, snippet, flags, has_attachments, auth_results, security_status, security_reason, security_scanned_at,
		(SELECT email FROM accounts WHERE id = m.account_id),
		(SELECT COALESCE(NULLIF(role_override, ''), role, '') FROM folders WHERE id = `+shownFolder+`), `+shownFolders+` FROM messages m
		WHERE thread_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`) AND `+notPurgedCond+` ORDER BY date DESC, id DESC`, ids...)
	if err != nil {
		return ThreadPage{}, err
	}
	sentIDs, err := s.roleFolderIDs(ctx, "sent")
	if err != nil {
		return ThreadPage{}, err
	}
	seen := map[string]bool{}
	for _, r := range ms.Values {
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
		if gomail.SecurityRank(sec.Status) > gomail.SecurityRank(t.Security.Status) { // a warning on any message is the thread's
			t.Security = sec
		}
		if t.Count == 0 {
			t.Snippet = gomail.Snippet(store.Str(r[2])) // newest first; clean of what an older sync kept
		}
		if role := store.Str(r[10]); t.From == "" && role != "sent" && role != "drafts" && gomail.AddrKey(fromAddr) != gomail.AddrKey(store.Str(r[9])) {
			t.From = fromAddr
		}
		t.Count++
		// Read once it is in Sent: your own mail, which another client or an
		// import can leave unflagged.
		flags := store.Str(r[3])
		inSent := slices.ContainsFunc(scanFolderIDs(r[11]), func(f int64) bool { return slices.Contains(sentIDs, any(f)) })
		if !strings.Contains(flags, `\Seen`) && !inSent {
			t.Unread++
		}
		if strings.Contains(flags, `\Flagged`) {
			t.Flagged++
		}
		t.HasAttachments = t.HasAttachments || store.Int(r[4]) != 0
		name := displayName(fromAddr)
		if k := fmt.Sprint(t.ID, "\x00", name); !seen[k] {
			seen[k] = true
			t.Participants = append(t.Participants, name)
		}
	}
	return page, nil
}

// GetThread returns the thread's messages by date ascending.
func (s *Service) GetThread(ctx context.Context, id int64) (Thread, error) {
	rows, err := s.db.Query(ctx, "SELECT t.subject, a.email, a.id FROM threads t JOIN accounts a ON a.id = t.account_id WHERE t.id = ?", id)
	if err != nil {
		return Thread{}, err
	}
	if len(rows.Values) == 0 {
		return Thread{}, fmt.Errorf("thread %d not found", id)
	}
	t, self := Thread{ID: id, Subject: store.Str(rows.Values[0][0])}, store.Str(rows.Values[0][1])
	sentID, _, err := mailsync.RoleFolder(ctx, s.db, store.Int(rows.Values[0][2]), "sent")
	if err != nil {
		return Thread{}, err
	}
	ms, err := s.db.Query(ctx, "SELECT "+msgCols+", m.in_reply_to, m.refs, m.reply_to FROM messages m WHERE m.thread_id = ? AND "+notPurgedCond+" ORDER BY m.date, m.id", id)
	if err != nil {
		return Thread{}, err
	}
	for _, r := range ms.Values {
		m := scanSummary(r)
		if sentID != 0 && slices.Contains(m.FolderIDs, sentID) && !slices.Contains(m.Flags, `\Seen`) {
			m.Flags = append(m.Flags, `\Seen`)
		}
		t.Messages = append(t.Messages, m)
		inReplyTo, refs, replyTo := store.Str(r[msgColCount]), store.Str(r[msgColCount+1]), store.Str(r[msgColCount+2])
		// A forward as enqueue sends it. With its subject edited it counts as mail
		// to answer: the compose window still shows who the reply goes to.
		forward := inReplyTo == "" && refs != "" && gomail.IsForwardSubject(m.Subject) && gomail.FromSelf(m.From, replyTo, self)
		if !forward {
			t.ReplyTarget = m.ID
		}
	}
	return t, nil
}
