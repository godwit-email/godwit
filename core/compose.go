package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"mime"
	netmail "net/mail"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwitmail/godwit/core/events"
	"github.com/godwitmail/godwit/core/mail"
	"github.com/godwitmail/godwit/core/store"
)

// Draft is an unsent message. Mode is new, reply, reply_all or forward.
type Draft struct {
	ID, AccountID          int64
	Mode                   string
	SourceMessageID        int64
	To, Cc, Bcc            []string
	ReplyTo, Subject, HTML string
	Attachments            []DraftAttachment
	UpdatedAt              time.Time
}

// DraftAttachment is a file copied into the draft's directory.
type DraftAttachment struct {
	ID                    int64
	Filename, ContentType string
	Size                  int
}

var draftModes = map[string]bool{"new": true, "reply": true, "reply_all": true, "forward": true}

func encodeList(l []string) string {
	b, _ := json.Marshal(l)
	return string(b)
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// NewDraft starts and saves a draft. reply and reply_all address the source's
// sender (and everyone, for reply_all) and quote it; forward quotes it and copies
// its non-inline attachments, downloading them if needed. The account signature
// ends the body.
func (s *Service) NewDraft(ctx context.Context, accountID int64, mode string, sourceMessageID int64) (Draft, error) {
	if !draftModes[mode] {
		return Draft{}, fmt.Errorf("unknown draft mode %q", mode)
	}
	acc, err := s.account(ctx, accountID)
	if err != nil {
		return Draft{}, err
	}
	d := Draft{AccountID: accountID, Mode: mode}
	if mode != "new" {
		orig, err := s.sourceMessage(ctx, accountID, sourceMessageID)
		if err != nil {
			return Draft{}, err
		}
		d.SourceMessageID = sourceMessageID
		if mode == "forward" {
			d.Subject = mail.ForwardSubject(orig.Subject)
		} else {
			d.To, d.Cc = mail.ReplyRecipients(orig, acc.Email, mode == "reply_all")
			d.Subject = mail.ReplySubject(orig.Subject)
		}
		d.HTML = "<p></p>" + mail.QuoteHTML(orig)
	}
	if acc.Signature != "" {
		d.HTML += "<p>-- <br>" + strings.ReplaceAll(html.EscapeString(acc.Signature), "\n", "<br>") + "</p>"
	}
	if d, err = s.SaveDraft(ctx, d); err != nil {
		return Draft{}, err
	}
	if mode == "forward" {
		if err := s.copyAttachments(ctx, d.ID, sourceMessageID); err != nil {
			return Draft{}, errors.Join(err, s.deleteDraft(context.WithoutCancel(ctx), d.ID))
		}
	}
	return s.loadDraft(ctx, d.ID)
}

// sourceMessage rebuilds what replying and forwarding need from the stored message.
func (s *Service) sourceMessage(ctx context.Context, accountID, id int64) (*mail.Parsed, error) {
	rows, err := s.db.Query(ctx, `SELECT message_id, subject, from_addr, reply_to, to_addrs, cc_addrs, date, body_text, body_html
		FROM messages WHERE id = ? AND account_id = ?`, id, accountID)
	if err != nil {
		return nil, err
	}
	if len(rows.Values) == 0 {
		return nil, fmt.Errorf("message %d not found in account %d", id, accountID)
	}
	r := rows.Values[0]
	return &mail.Parsed{MessageID: store.Str(r[0]), Subject: store.Str(r[1]), From: store.Str(r[2]), ReplyTo: store.Str(r[3]),
		To: jsonList(store.Str(r[4])), Cc: jsonList(store.Str(r[5])), Date: time.Unix(store.Int(r[6]), 0),
		Text: store.Str(r[7]), HTML: store.Str(r[8])}, nil
}

func (s *Service) copyAttachments(ctx context.Context, draftID, messageID int64) error {
	rows, err := s.db.Query(ctx, "SELECT id FROM attachments WHERE message_id = ? AND inline = 0 ORDER BY id", messageID)
	if err != nil {
		return err
	}
	for _, r := range rows.Values {
		path, info, err := s.GetAttachment(ctx, store.Int(r[0]))
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if _, err := s.addAttachment(ctx, draftID, info.Filename, info.ContentType, data); err != nil {
			return err
		}
	}
	return nil
}

// SaveDraft inserts d (ID 0) or updates its addresses, subject and body.
// Attachments are changed with AddAttachment and RemoveAttachment.
func (s *Service) SaveDraft(ctx context.Context, d Draft) (Draft, error) {
	now := time.Now().Unix()
	if d.ID == 0 {
		if !draftModes[d.Mode] {
			return Draft{}, fmt.Errorf("unknown draft mode %q", d.Mode)
		}
		r, err := s.db.Execute(ctx, `INSERT INTO drafts(account_id, mode, source_message_id, to_addrs, cc_addrs, bcc_addrs,
			reply_to, subject, body_html, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			d.AccountID, d.Mode, nullID(d.SourceMessageID), encodeList(d.To), encodeList(d.Cc), encodeList(d.Bcc),
			d.ReplyTo, d.Subject, d.HTML, now)
		if err != nil {
			return Draft{}, err
		}
		return s.loadDraft(ctx, r.LastInsertID)
	}
	r, err := s.db.Execute(ctx, `UPDATE drafts SET to_addrs = ?, cc_addrs = ?, bcc_addrs = ?, reply_to = ?, subject = ?,
		body_html = ?, updated_at = ? WHERE id = ?`,
		encodeList(d.To), encodeList(d.Cc), encodeList(d.Bcc), d.ReplyTo, d.Subject, d.HTML, now, d.ID)
	if err != nil {
		return Draft{}, err
	}
	if r.Changes == 0 {
		return Draft{}, fmt.Errorf("draft %d not found", d.ID)
	}
	return s.loadDraft(ctx, d.ID)
}

func (s *Service) loadDraft(ctx context.Context, id int64) (Draft, error) {
	rows, err := s.db.Query(ctx, `SELECT account_id, mode, source_message_id, to_addrs, cc_addrs, bcc_addrs, reply_to,
		subject, body_html, updated_at FROM drafts WHERE id = ?`, id)
	if err != nil {
		return Draft{}, err
	}
	if len(rows.Values) == 0 {
		return Draft{}, fmt.Errorf("draft %d not found", id)
	}
	r := rows.Values[0]
	d := Draft{ID: id, AccountID: store.Int(r[0]), Mode: store.Str(r[1]), SourceMessageID: store.Int(r[2]),
		To: jsonList(store.Str(r[3])), Cc: jsonList(store.Str(r[4])), Bcc: jsonList(store.Str(r[5])),
		ReplyTo: store.Str(r[6]), Subject: store.Str(r[7]), HTML: store.Str(r[8]), UpdatedAt: time.Unix(store.Int(r[9]), 0)}
	ar, err := s.db.Query(ctx, "SELECT id, filename, content_type, size FROM draft_attachments WHERE draft_id = ? ORDER BY id", id)
	if err != nil {
		return Draft{}, err
	}
	for _, a := range ar.Values {
		d.Attachments = append(d.Attachments, DraftAttachment{ID: store.Int(a[0]), Filename: store.Str(a[1]),
			ContentType: store.Str(a[2]), Size: int(store.Int(a[3]))})
	}
	return d, nil
}

func (s *Service) draftDir(id int64) string {
	return filepath.Join(s.dataDir, "drafts", strconv.FormatInt(id, 10))
}

// AddAttachment copies the file at srcPath into the draft.
func (s *Service) AddAttachment(ctx context.Context, draftID int64, srcPath string) (DraftAttachment, error) {
	st, err := os.Stat(srcPath)
	if err != nil {
		return DraftAttachment{}, err
	}
	if st.Size() > attachLimit {
		return DraftAttachment{}, ErrTooLarge
	}
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return DraftAttachment{}, err
	}
	ct := mime.TypeByExtension(filepath.Ext(srcPath))
	if ct == "" {
		ct = "application/octet-stream"
	}
	return s.addAttachment(ctx, draftID, filepath.Base(srcPath), ct, data)
}

func (s *Service) addAttachment(ctx context.Context, draftID int64, filename, contentType string, data []byte) (DraftAttachment, error) {
	if int64(len(data)) > attachLimit {
		return DraftAttachment{}, ErrTooLarge
	}
	r, err := s.db.Execute(ctx, "INSERT INTO draft_attachments(draft_id, filename, content_type, size) VALUES (?, ?, ?, ?)",
		draftID, filename, contentType, len(data))
	if err != nil {
		return DraftAttachment{}, err
	}
	id := r.LastInsertID
	dir := s.draftDir(draftID)
	path := filepath.Join(dir, strconv.FormatInt(id, 10))
	err = os.MkdirAll(dir, 0o700)
	if err == nil {
		err = store.WriteFileAtomic(path, data)
	}
	if err == nil {
		_, err = s.db.Execute(ctx, "UPDATE draft_attachments SET path = ? WHERE id = ?", path, id)
	}
	if err != nil {
		os.Remove(path)
		_, derr := s.db.Execute(context.WithoutCancel(ctx), "DELETE FROM draft_attachments WHERE id = ?", id)
		return DraftAttachment{}, errors.Join(err, derr)
	}
	return DraftAttachment{ID: id, Filename: filename, ContentType: contentType, Size: len(data)}, nil
}

// RemoveAttachment deletes the attachment from the draft and its file.
func (s *Service) RemoveAttachment(ctx context.Context, draftID, attachmentID int64) error {
	rows, err := s.db.Query(ctx, "SELECT path FROM draft_attachments WHERE id = ? AND draft_id = ?", attachmentID, draftID)
	if err != nil {
		return err
	}
	if len(rows.Values) == 0 {
		return fmt.Errorf("attachment %d not found in draft %d", attachmentID, draftID)
	}
	if _, err := s.db.Execute(ctx, "DELETE FROM draft_attachments WHERE id = ?", attachmentID); err != nil {
		return err
	}
	if err := os.Remove(store.Str(rows.Values[0][0])); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s *Service) deleteDraft(ctx context.Context, id int64) error {
	if _, err := s.db.Execute(ctx, "DELETE FROM drafts WHERE id = ?", id); err != nil { // attachments cascade
		return err
	}
	return os.RemoveAll(s.draftDir(id))
}

// recipients validates the draft's addresses and returns the bare envelope
// recipients, To, Cc and Bcc together without duplicates.
func recipients(d Draft) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, l := range [][]string{d.To, d.Cc, d.Bcc} {
		for _, a := range l {
			p, err := netmail.ParseAddress(a)
			if err != nil {
				return nil, fmt.Errorf("invalid address %q", a)
			}
			if k := strings.ToLower(p.Address); !seen[k] {
				seen[k] = true
				out = append(out, p.Address)
			}
		}
	}
	if len(out) == 0 {
		return nil, errors.New("the message has no recipients")
	}
	return out, nil
}

// Send moves the draft into the outbox; the next sync submits it. Nothing is
// sent and the draft stays when validation or building fails.
func (s *Service) Send(ctx context.Context, draftID int64) error {
	s.editMu.Lock() // a draft must enter the outbox once, even if Send is called twice
	defer s.editMu.Unlock()
	d, err := s.loadDraft(ctx, draftID)
	if err != nil {
		return err
	}
	rcpts, err := recipients(d)
	if err != nil {
		return err
	}
	total := int64(len(d.HTML))
	for _, a := range d.Attachments {
		total += int64(a.Size)
	}
	if total > attachLimit {
		return ErrTooLarge
	}
	acc, err := s.account(ctx, d.AccountID)
	if err != nil {
		return err
	}
	o := mail.Outgoing{From: (&netmail.Address{Name: acc.Name, Address: acc.Email}).String(), ReplyTo: d.ReplyTo,
		To: d.To, Cc: d.Cc, Bcc: d.Bcc, Subject: d.Subject, HTML: d.HTML}
	if d.SourceMessageID != 0 && (d.Mode == "reply" || d.Mode == "reply_all") {
		rows, err := s.db.Query(ctx, "SELECT message_id, refs FROM messages WHERE id = ?", d.SourceMessageID)
		if err != nil {
			return err
		}
		if len(rows.Values) > 0 && store.Str(rows.Values[0][0]) != "" {
			o.InReplyTo = store.Str(rows.Values[0][0])
			o.References = append(strings.Fields(store.Str(rows.Values[0][1])), o.InReplyTo)
		}
	}
	atts, err := s.db.Query(ctx, "SELECT filename, content_type, path FROM draft_attachments WHERE draft_id = ? ORDER BY id", draftID)
	if err != nil {
		return err
	}
	for _, a := range atts.Values {
		data, err := os.ReadFile(store.Str(a[2]))
		if err != nil {
			return err
		}
		o.Attachments = append(o.Attachments, mail.Attachment{Filename: store.Str(a[0]), ContentType: store.Str(a[1]), Data: data})
	}
	raw, messageID, err := mail.Build(o, time.Now())
	if err != nil {
		return err
	}
	dir := filepath.Join(s.dataDir, "outbox")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	rawPath := filepath.Join(dir, strconv.FormatInt(draftID, 10)+".eml") // draft ids are never reused
	if err := store.WriteFileAtomic(rawPath, raw); err != nil {
		return err
	}
	if _, err := s.db.Transaction(ctx, []sqlite.Statement{
		{SQL: `INSERT INTO outbox(account_id, message_id_hdr, raw_path, rcpts, state, subject, created_at)
			VALUES (?, ?, ?, ?, 'queued', ?, ?)`,
			Args: []any{d.AccountID, messageID, rawPath, encodeList(rcpts), d.Subject, time.Now().Unix()}},
		{SQL: "DELETE FROM draft_attachments WHERE draft_id = ?", Args: []any{draftID}},
		{SQL: "DELETE FROM drafts WHERE id = ?", Args: []any{draftID}},
	}); err != nil {
		os.Remove(rawPath)
		return err
	}
	s.bus.Publish(events.Event{Type: events.OutboxChanged, AccountID: d.AccountID})
	// ponytail: a failed removal leaves an orphan directory; sweep drafts/ on startup if it matters.
	os.RemoveAll(s.draftDir(draftID))
	return nil
}
