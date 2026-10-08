package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	gomail "github.com/godwitmail/godwit/core/mail"
	"github.com/godwitmail/godwit/core/store"
)

// MessageSummary is a message as listed.
type MessageSummary struct {
	ID, ThreadID, FolderID int64
	Subject, From, Snippet string
	To, Cc                 []string
	Date                   time.Time
	Flags                  []string
	HasAttachments         bool
}

// MessagePage is one page of a flat message list.
type MessagePage struct {
	Items      []MessageSummary
	NextCursor string
}

// Message is a fully loaded message. HTML is always sanitized.
type Message struct {
	MessageSummary
	ReplyTo             string
	HTML, Text          string
	RemoteImagesBlocked int
	Attachments         []AttachmentInfo
}

// AttachmentInfo describes one attachment; its bytes are fetched lazily.
type AttachmentInfo struct {
	ID                    int64
	Filename, ContentType string
	Size                  int
	Inline                bool
}

const msgCols = `m.id, m.thread_id, m.folder_id, m.subject, m.from_addr, m.snippet, m.to_addrs, m.cc_addrs, m.date, m.flags, m.has_attachments`

func scanSummary(r []any) MessageSummary {
	return MessageSummary{ID: store.Int(r[0]), ThreadID: store.Int(r[1]), FolderID: store.Int(r[2]),
		Subject: store.Str(r[3]), From: store.Str(r[4]), Snippet: store.Str(r[5]),
		To: jsonList(store.Str(r[6])), Cc: jsonList(store.Str(r[7])), Date: time.Unix(store.Int(r[8]), 0),
		Flags: strings.Fields(store.Str(r[9])), HasAttachments: store.Int(r[10]) != 0}
}

func jsonList(s string) []string {
	var out []string
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

func clampLimit(n int) int {
	if n <= 0 {
		return 50
	}
	return min(n, 200)
}

func encodeCursor(date, id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d:%d", date, id)))
}

func decodeCursor(c string) (date, id int64, err error) {
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err == nil {
		d, i, ok := strings.Cut(string(b), ":")
		if ok {
			if date, err = strconv.ParseInt(d, 10, 64); err == nil {
				id, err = strconv.ParseInt(i, 10, 64)
			}
		} else {
			err = errors.New("no separator")
		}
	}
	if err != nil {
		return 0, 0, fmt.Errorf("invalid cursor: %w", err)
	}
	return date, id, nil
}

// viewCond returns a SQL condition on messages alias m selecting the view.
func viewCond(v View) (string, []any, error) {
	switch {
	case v.Unified == "inbox" && v.FolderID == 0:
		return "m.folder_id IN (SELECT id FROM folders WHERE role = 'inbox')", nil, nil
	case v.Unified == "starred" && v.FolderID == 0:
		return `instr(m.flags, '\Flagged') > 0`, nil, nil
	case v.Unified == "" && v.FolderID != 0:
		return "m.folder_id = ?", []any{v.FolderID}, nil
	}
	return "", nil, errors.New("view needs exactly one of FolderID or Unified (inbox, starred)")
}

// ListMessages lists the view's messages as flat rows, newest first.
func (s *Service) ListMessages(ctx context.Context, v View, cursor string, limit int) (MessagePage, error) {
	cond, args, err := viewCond(v)
	if err != nil {
		return MessagePage{}, err
	}
	if cursor != "" {
		d, id, err := decodeCursor(cursor)
		if err != nil {
			return MessagePage{}, err
		}
		cond += " AND (m.date < ? OR (m.date = ? AND m.id < ?))"
		args = append(args, d, d, id)
	}
	limit = clampLimit(limit)
	rows, err := s.db.Query(ctx, "SELECT "+msgCols+" FROM messages m WHERE "+cond+" ORDER BY m.date DESC, m.id DESC LIMIT ?", append(args, limit+1)...)
	if err != nil {
		return MessagePage{}, err
	}
	var page MessagePage
	for _, r := range rows.Values {
		page.Items = append(page.Items, scanSummary(r))
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[limit-1]
		page.NextCursor = encodeCursor(last.Date.Unix(), last.ID)
	}
	return page, nil
}

// Search finds synced messages; accountID 0 searches every account.
func (s *Service) Search(ctx context.Context, accountID int64, query string, limit int) ([]MessageSummary, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	var cond string
	var args []any
	if utf8.RuneCountInString(query) < 3 {
		// ponytail: full scan; use a bigram index when mailboxes get large.
		like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(query) + "%"
		cond = `(m.subject LIKE ? ESCAPE '\' OR m.from_addr LIKE ? ESCAPE '\' OR m.body_text LIKE ? ESCAPE '\')`
		args = []any{like, like, like}
	} else {
		cond = "m.id IN (SELECT rowid FROM messages_fts WHERE messages_fts MATCH ?)"
		args = []any{`"` + strings.ReplaceAll(query, `"`, `""`) + `"`}
	}
	if accountID != 0 {
		cond += " AND m.account_id = ?"
		args = append(args, accountID)
	}
	rows, err := s.db.Query(ctx, "SELECT "+msgCols+" FROM messages m WHERE "+cond+" ORDER BY m.date DESC, m.id DESC LIMIT ?",
		append(args, clampLimit(limit))...)
	if err != nil {
		return nil, err
	}
	out := make([]MessageSummary, 0, len(rows.Values))
	for _, r := range rows.Values {
		out = append(out, scanSummary(r))
	}
	return out, nil
}

// GetMessage returns the message with sanitized HTML. Inline images are
// downloaded first if needed and embedded.
func (s *Service) GetMessage(ctx context.Context, id int64, loadRemoteImages bool) (Message, error) {
	rows, err := s.db.Query(ctx, "SELECT "+msgCols+", m.reply_to, m.body_text, m.body_html FROM messages m WHERE m.id = ?", id)
	if err != nil {
		return Message{}, err
	}
	if len(rows.Values) == 0 {
		return Message{}, fmt.Errorf("message %d not found", id)
	}
	r := rows.Values[0]
	msg := Message{MessageSummary: scanSummary(r), ReplyTo: store.Str(r[11]), Text: store.Str(r[12])}
	raw := store.Str(r[13])
	ar, err := s.db.Query(ctx, "SELECT id, filename, content_type, size, inline, content_id FROM attachments WHERE message_id = ? ORDER BY id", id)
	if err != nil {
		return Message{}, err
	}
	inline := map[string]gomail.Attachment{}
	for _, a := range ar.Values {
		info := AttachmentInfo{ID: store.Int(a[0]), Filename: store.Str(a[1]), ContentType: store.Str(a[2]),
			Size: int(store.Int(a[3])), Inline: store.Int(a[4]) != 0}
		msg.Attachments = append(msg.Attachments, info)
		cid := store.Str(a[5])
		if raw == "" || cid == "" || !strings.HasPrefix(info.ContentType, "image/") || !strings.Contains(raw, cid) {
			continue
		}
		path, _, err := s.GetAttachment(ctx, info.ID)
		if err != nil {
			if ctx.Err() != nil {
				return Message{}, ctx.Err()
			}
			continue // image stays blocked; the message is still readable
		}
		if data, err := readFile(path); err == nil {
			inline[cid] = gomail.Attachment{Data: data, ContentType: info.ContentType}
		}
	}
	if raw != "" {
		msg.HTML, msg.RemoteImagesBlocked = gomail.Sanitize(raw, inline, loadRemoteImages)
	}
	return msg, nil
}

// displayName is the From's name, or its address when it has none.
func displayName(from string) string {
	a, err := mail.ParseAddress(from)
	switch {
	case err != nil:
		return from
	case a.Name != "":
		return a.Name
	}
	return a.Address
}
