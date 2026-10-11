package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/events"
	gomail "github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/mailsync"
	"github.com/godwit-email/godwit/core/store"
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

// Message is a fully loaded message. HTML is always sanitized. Truncated means
// HTML and Text hold only the start of the body; GetRawMessage has all of it.
type Message struct {
	MessageSummary
	ReplyTo             string
	HTML, Text          string
	Truncated           bool
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

// SearchQuery is a search: free text ANDed with the filters set. Zero
// fields filter nothing; AccountID 0 is every account.
type SearchQuery struct {
	AccountID      int64
	Text           string
	From           string    // a substring of the From header, any case
	Since, Until   time.Time // the date from Since, before Until
	HasAttachments bool
}

// Filtered reports whether a filter beyond the account is set: such a query
// needs no text.
func (q SearchQuery) Filtered() bool {
	return strings.TrimSpace(q.From) != "" || !q.Since.IsZero() || !q.Until.IsZero() || q.HasAttachments
}

// Search finds synced messages; accountID 0 searches every account.
func (s *Service) Search(ctx context.Context, accountID int64, query string, limit int) ([]MessageSummary, error) {
	return s.SearchWith(ctx, SearchQuery{AccountID: accountID, Text: query}, limit)
}

// likeArg is a LIKE pattern matching s anywhere, its wildcards literal.
func likeArg(s string) string {
	return "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(s) + "%"
}

// SearchWith finds synced messages matching q, newest first. A query with
// neither text nor a filter finds nothing.
func (s *Service) SearchWith(ctx context.Context, q SearchQuery, limit int) ([]MessageSummary, error) {
	query := strings.TrimSpace(q.Text)
	if query == "" && !q.Filtered() {
		return nil, nil
	}
	cond, args := "1", []any(nil)
	switch {
	case query == "":
	case utf8.RuneCountInString(query) < 3:
		// ponytail: full scan; use a bigram index when mailboxes get large.
		like := likeArg(query)
		cond = `(m.subject LIKE ? ESCAPE '\' OR m.from_addr LIKE ? ESCAPE '\' OR m.body_text LIKE ? ESCAPE '\')`
		args = []any{like, like, like}
	default:
		cond = "m.id IN (SELECT rowid FROM messages_fts WHERE messages_fts MATCH ?)"
		args = []any{`"` + strings.ReplaceAll(query, `"`, `""`) + `"`}
	}
	if q.AccountID != 0 {
		cond += " AND m.account_id = ?"
		args = append(args, q.AccountID)
	}
	// shortcut: filters without text scan every message (no (account_id, date) index); add one if 100k-mail stores get slow.
	if from := strings.TrimSpace(q.From); from != "" {
		cond += ` AND m.from_addr LIKE ? ESCAPE '\'`
		args = append(args, likeArg(from))
	}
	if !q.Since.IsZero() {
		cond += " AND m.date >= ?"
		args = append(args, q.Since.Unix())
	}
	if !q.Until.IsZero() {
		cond += " AND m.date < ?"
		args = append(args, q.Until.Unix())
	}
	if q.HasAttachments {
		cond += " AND m.has_attachments = 1" // as the list's paperclip: inline parts count
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

// RebuildSearchIndex recreates the full-text index from the messages table in
// one transaction, repairing an index that drifted or was damaged.
func (s *Service) RebuildSearchIndex(ctx context.Context) error {
	_, err := s.db.Transaction(ctx, []sqlite.Statement{
		{SQL: "DELETE FROM messages_fts"},
		{SQL: "INSERT INTO messages_fts(rowid, subject, from_addr, body) SELECT id, subject, from_addr, body_text FROM messages"},
	})
	return err
}

// GetMessage returns the message with sanitized HTML. Inline images are
// downloaded first if needed and embedded.
func (s *Service) GetMessage(ctx context.Context, id int64, loadRemoteImages bool) (Message, error) {
	rows, err := s.db.Query(ctx, "SELECT "+msgCols+", m.reply_to, m.body_text, m.body_html, m.body_truncated FROM messages m WHERE m.id = ?", id)
	if err != nil {
		return Message{}, err
	}
	if len(rows.Values) == 0 {
		return Message{}, fmt.Errorf("message %d not found", id)
	}
	r := rows.Values[0]
	msg := Message{MessageSummary: scanSummary(r), ReplyTo: store.Str(r[11]), Text: store.Str(r[12]), Truncated: store.Int(r[14]) != 0}
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

// Op is a queued local change that has not reached the server yet, or could not.
type Op struct {
	ID          int64
	Kind, State string // Kind: flags or move. State: pending, awaiting_claim, conflict or failed
	Attempts    int
	LastError   string
}

// PendingOps lists the account's ops that are not done, oldest first.
func (s *Service) PendingOps(ctx context.Context, accountID int64) ([]Op, error) {
	rows, err := s.db.Query(ctx, "SELECT id, kind, state, attempts, last_error FROM ops WHERE account_id = ? AND state != 'done' ORDER BY id", accountID)
	if err != nil {
		return nil, err
	}
	out := make([]Op, 0, len(rows.Values))
	for _, r := range rows.Values {
		out = append(out, Op{ID: store.Int(r[0]), Kind: store.Str(r[1]), State: store.Str(r[2]),
			Attempts: int(store.Int(r[3])), LastError: store.Str(r[4])})
	}
	return out, nil
}

// latestOp is the message's newest unfinished op, which a new op must wait for.
const latestOp = "(SELECT max(id) FROM ops WHERE message_id = ? AND state IN ('pending', 'awaiting_claim'))"

type target struct {
	id, thread, folder int64
	flags              string
}

// targets loads the messages, which must all exist and belong to one account.
func (s *Service) targets(ctx context.Context, ids []int64) (int64, []target, error) {
	ids = slices.Compact(slices.Sorted(slices.Values(ids)))
	if len(ids) == 0 {
		return 0, nil, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.Query(ctx, "SELECT id, account_id, thread_id, folder_id, flags FROM messages WHERE id IN ("+
		strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+")", args...)
	if err != nil {
		return 0, nil, err
	}
	if len(rows.Values) != len(ids) {
		return 0, nil, errors.New("some of the messages do not exist")
	}
	acc := store.Int(rows.Values[0][1])
	out := make([]target, 0, len(ids))
	for _, r := range rows.Values {
		if store.Int(r[1]) != acc {
			return 0, nil, errors.New("the messages belong to different accounts")
		}
		out = append(out, target{id: store.Int(r[0]), thread: store.Int(r[2]), folder: store.Int(r[3]), flags: store.Str(r[4])})
	}
	return acc, out, nil
}

// apply commits a local change of ts and announces it.
func (s *Service) apply(ctx context.Context, accountID int64, ts []target, stmts []sqlite.Statement) error {
	if len(stmts) == 0 {
		return nil
	}
	if _, err := s.db.Transaction(ctx, stmts); err != nil {
		return err
	}
	var ids, tids []int64
	for _, m := range ts {
		ids = append(ids, m.id)
		if m.thread != 0 {
			tids = append(tids, m.thread)
		}
	}
	s.bus.Publish(events.Event{Type: events.MessagesChanged, AccountID: accountID, MessageIDs: ids,
		ThreadIDs: slices.Compact(slices.Sorted(slices.Values(tids)))})
	s.wakeWorker(accountID)
	return nil
}

// checkFlag accepts IMAP flag atoms the user may set. \Deleted is refused: deleting
// is a move to Trash, never an expunge.
func checkFlag(f string) error {
	atom := strings.TrimPrefix(f, `\`)
	if atom == "" || strings.EqualFold(f, `\Deleted`) || strings.EqualFold(f, `\Recent`) ||
		strings.ContainsFunc(atom, func(r rune) bool { return r <= ' ' || r >= 0x7f || strings.ContainsRune(`(){%*"\]`, r) }) {
		return fmt.Errorf("invalid flag %q", f)
	}
	return nil
}

// systemFlags maps lower-cased IMAP system flags to their canonical spelling.
var systemFlags = map[string]string{`\seen`: `\Seen`, `\answered`: `\Answered`, `\flagged`: `\Flagged`,
	`\deleted`: `\Deleted`, `\draft`: `\Draft`}

// flagSet canonicalises system flags and drops case-insensitive duplicates
// (IMAP flags are case-insensitive), keeping the first spelling.
func flagSet(fl []string) []string {
	var out []string
	for _, f := range fl {
		if c, ok := systemFlags[strings.ToLower(f)]; ok {
			f = c
		}
		if !slices.ContainsFunc(out, func(g string) bool { return strings.EqualFold(f, g) }) {
			out = append(out, f)
		}
	}
	return out
}

// SetFlags adds then removes flags on the messages locally and queues the change
// for the server.
func (s *Service) SetFlags(ctx context.Context, ids []int64, add, remove []string) error {
	for _, f := range slices.Concat(add, remove) {
		if err := checkFlag(f); err != nil {
			return err
		}
	}
	add, remove = flagSet(add), flagSet(remove)
	if len(add)+len(remove) == 0 {
		return nil
	}
	payload, err := json.Marshal(mailsync.FlagsPayload{Add: add, Remove: remove})
	if err != nil {
		return err
	}
	s.editMu.Lock()
	defer s.editMu.Unlock()
	acc, ts, err := s.targets(ctx, ids)
	if err != nil {
		return err
	}
	var stmts []sqlite.Statement
	for _, m := range ts {
		set := slices.DeleteFunc(flagSet(append(strings.Fields(m.flags), add...)), func(f string) bool {
			return slices.ContainsFunc(remove, func(r string) bool { return strings.EqualFold(f, r) })
		})
		slices.Sort(set)
		stmts = append(stmts,
			sqlite.Statement{SQL: "UPDATE messages SET flags = ? WHERE id = ?", Args: []any{strings.Join(set, " "), m.id}},
			sqlite.Statement{SQL: `INSERT INTO ops(account_id, message_id, kind, payload, depends_on, created_at)
				VALUES (?, ?, 'flags', ?, ` + latestOp + `, unixepoch())`, Args: []any{acc, m.id, string(payload), m.id}})
	}
	return s.apply(ctx, acc, ts, stmts)
}

// Move moves the messages to folderID, which must belong to their account. The
// local message IDs stay the same.
func (s *Service) Move(ctx context.Context, ids []int64, folderID int64) error {
	s.editMu.Lock()
	defer s.editMu.Unlock()
	rows, err := s.db.Query(ctx, "SELECT account_id FROM folders WHERE id = ?", folderID)
	if err != nil {
		return err
	}
	if len(rows.Values) == 0 {
		return fmt.Errorf("folder %d not found", folderID)
	}
	acc, ts, err := s.targets(ctx, ids)
	if err != nil {
		return err
	}
	if len(ts) > 0 && acc != store.Int(rows.Values[0][0]) {
		return errors.New("the folder belongs to another account")
	}
	return s.move(ctx, acc, ts, folderID)
}

// Delete moves the messages to their account's Trash; ones already there stay.
func (s *Service) Delete(ctx context.Context, ids []int64) error {
	s.editMu.Lock()
	defer s.editMu.Unlock()
	acc, ts, err := s.targets(ctx, ids)
	if err != nil || len(ts) == 0 {
		return err
	}
	rows, err := s.db.Query(ctx, "SELECT id FROM folders WHERE account_id = ? AND role = 'trash' ORDER BY id LIMIT 1", acc)
	if err != nil {
		return err
	}
	if len(rows.Values) == 0 {
		return errors.New("the account has no Trash folder")
	}
	return s.move(ctx, acc, ts, store.Int(rows.Values[0][0]))
}

// move sets the desired folder of the messages not already in folderID and queues
// the moves; their server location changes only when the move is pushed.
func (s *Service) move(ctx context.Context, accountID int64, ts []target, folderID int64) error {
	var moved []target
	var stmts []sqlite.Statement
	for _, m := range ts {
		if m.folder == folderID {
			continue
		}
		moved = append(moved, m)
		stmts = append(stmts,
			sqlite.Statement{SQL: "UPDATE messages SET folder_id = ? WHERE id = ?", Args: []any{folderID, m.id}},
			sqlite.Statement{SQL: `INSERT INTO ops(account_id, message_id, kind, dst_folder_id, depends_on, created_at)
				VALUES (?, ?, 'move', ?, ` + latestOp + `, unixepoch())`, Args: []any{accountID, m.id, folderID, m.id}})
	}
	return s.apply(ctx, accountID, moved, stmts)
}
