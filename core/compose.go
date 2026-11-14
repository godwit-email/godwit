package core

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"mime"
	netmail "net/mail"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/gateway"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/store"
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
	// Revision grows by one on every update, an attachment added or removed
	// included (refetch it after one). SaveDraft with a non-zero Revision
	// fails with ErrStaleDraft unless it is still the stored one.
	Revision int64
	// FromProgram: a program (the Local API, godwit mcp, a plugin or Wit)
	// created or changed it, so the interface labels it and offers
	// ApproveDraft. Local only; the user's own edits keep it (ADR 0042).
	FromProgram bool
}

// DraftAttachment is a file copied into the draft's directory.
type DraftAttachment struct {
	ID                    int64
	Filename, ContentType string
	Size                  int
}

// afterDraftLoad, when set, runs in Send right after the draft is read. Tests use
// it to edit the draft while Send is building the message.
var afterDraftLoad func()

// ErrStaleDraft means the draft changed since the revision the caller saved
// against, or it is in the outbox: read-only until it is taken back
// (CancelOutbox), or gone once the message may have gone out.
var ErrStaleDraft = errors.New("draft changed since it was loaded")

// checkWritable is ErrStaleDraft while an outbox item holds the draft. The
// caller holds editMu, so no Send queues the draft until it unlocks.
func (s *Service) checkWritable(ctx context.Context, draftID int64) error {
	rows, err := s.db.Query(ctx, "SELECT 1 FROM outbox WHERE draft_id = ? LIMIT 1", draftID)
	if err != nil {
		return err
	}
	if len(rows.Values) > 0 {
		return ErrStaleDraft
	}
	return nil
}

// checkWitDraft is ErrStaleDraft for Wit on a draft in the outbox: a run or
// conversation there would outlive the draft once the message goes. A draftID
// of 0 is no draft. A caller that starts a run holds the run ledger, which Send holds
// from stopping the draft's runs until it is queued.
func (s *Service) checkWitDraft(ctx context.Context, draftID int64) error {
	if draftID == 0 {
		return nil
	}
	s.editMu.Lock() // the run ledger, if held, before editMu, as enqueue
	defer s.editMu.Unlock()
	return s.checkWritable(ctx, draftID)
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
	if err := s.auditMCPDraft(ctx, "NewDraft", sourceMessageID); err != nil {
		return Draft{}, err
	}
	// From reading the signature to storing the draft, no signature change
	// can clean up the images the draft is about to show.
	sourceMessageID, err := s.resolveID(ctx, sourceMessageID)
	if err != nil {
		return Draft{}, err
	}
	s.sigMu.Lock()
	d, err := s.buildDraft(ctx, accountID, mode, sourceMessageID)
	if err == nil {
		d, err = s.saveDraft(ctx, d)
	}
	s.sigMu.Unlock()
	if err != nil {
		return Draft{}, err
	}
	if mode == "forward" {
		if err := s.copyAttachments(ctx, d.ID, sourceMessageID); err != nil {
			return Draft{}, errors.Join(err, s.deleteDraft(context.WithoutCancel(ctx), d.ID))
		}
	}
	return s.GetDraft(ctx, d.ID)
}

// buildDraft is NewDraft's draft before it is saved: addresses, subject,
// quote and signature, without attachments.
func (s *Service) buildDraft(ctx context.Context, accountID int64, mode string, sourceMessageID int64) (Draft, error) {
	if !draftModes[mode] {
		return Draft{}, fmt.Errorf("unknown draft mode %q", mode)
	}
	acc, err := s.account(ctx, accountID)
	if err != nil {
		return Draft{}, err
	}
	if acc.Kind == KindLocalArchive {
		if mode == "new" {
			return Draft{}, ErrLocalArchive
		}
		// Its mail is answered from the default account that sends (ADR 0036).
		if acc, err = s.draftAccount(ctx, 0); err != nil {
			return Draft{}, ErrLocalArchive
		}
	}
	self := acc.Email    // whose mail is answered: its own sent mail and address are the user's
	if acc.ReceiveOnly { // so is a receive-only account's, and its new mail
		if acc, err = s.draftAccount(ctx, 0); err != nil {
			return Draft{}, ErrReceiveOnly
		}
	}
	d := Draft{AccountID: acc.ID, Mode: mode}
	st, err := s.loadSettings(ctx)
	if err != nil {
		return Draft{}, err
	}
	if mode != "new" {
		orig, err := s.sourceMessage(ctx, accountID, sourceMessageID)
		if err != nil {
			return Draft{}, err
		}
		d.SourceMessageID = sourceMessageID
		if mode == "forward" {
			d.Subject = mail.ForwardSubject(orig.Subject)
		} else {
			d.To, d.Cc = mail.ReplyRecipients(orig, self, mode == "reply_all")
			d.Subject = mail.ReplySubject(orig.Subject)
		}
		d.Subject = defuseRefs(d.Subject) // a reference planted in the mail would block sending the reply
		// The marker blockquote only marks the quote (attribution line included)
		// as read-only for Wit; it is styled away so the quote is not indented twice.
		lang := cmp.Or(st.ReadingLanguage, st.UILanguage, s.opts.SystemLanguage)
		d.HTML = `<p></p><blockquote data-godwit="quote" style="margin:0;padding:0;border:0">` + defuseRefs(mail.QuoteHTMLIn(orig, lang)) + "</blockquote>"
	}
	cids, err := s.signatureCIDs(ctx)
	if err != nil {
		return Draft{}, err
	}
	d.HTML += signatureHTML(acc.Signature, cids, st.SignatureDelimiter)
	return d, nil
}

// SignatureHTMLPrefix marks a stored signature as the settings editor's HTML;
// any other signature (the CLI's, the Local API's, an older one) is plain text.
const SignatureHTMLPrefix = "<!--godwit:html-->"

// SignatureEditorHTML is sig as safe compose HTML, without the draft marker
// or the "-- " delimiter: a marked one sanitized (user markers stripped, the
// images in cids kept), a plain one escaped into a paragraph; "" for none.
func SignatureEditorHTML(sig string, cids map[string]bool) string {
	if rest, ok := strings.CutPrefix(sig, SignatureHTMLPrefix); ok {
		return mail.SanitizeDraft(mail.StripMarkers(rest), cids)
	}
	if sig == "" {
		return ""
	}
	return "<p>" + escapeLines(sig) + "</p>"
}

func escapeLines(s string) string { return strings.ReplaceAll(html.EscapeString(s), "\n", "<br>") }

// signatureHTML is the marked signature block that ends a draft body, "" for
// none; cids are the stored signature images. delim puts the "-- " line
// before it (Settings.SignatureDelimiter); the text part follows the HTML.
func signatureHTML(sig string, cids map[string]bool, delim bool) string {
	if !strings.HasPrefix(sig, SignatureHTMLPrefix) {
		if sig == "" {
			return ""
		}
		if delim {
			return `<div data-godwit="signature"><p>-- <br>` + escapeLines(sig) + "</p></div>"
		}
		return `<div data-godwit="signature"><p>` + escapeLines(sig) + "</p></div>"
	}
	body := SignatureEditorHTML(sig, cids)
	switch {
	case body == "":
		return ""
	case delim:
		return `<div data-godwit="signature"><p>-- </p>` + body + "</div>" // the delimiter a plain one has; the body is blocks
	}
	return `<div data-godwit="signature">` + body + "</div>"
}

// sourceMessage rebuilds what replying and forwarding need from the stored message.
func (s *Service) sourceMessage(ctx context.Context, accountID, id int64) (*mail.Parsed, error) {
	rows, err := s.db.Query(ctx, `SELECT message_id, subject, from_addr, reply_to, to_addrs, cc_addrs, date, body_text, body_html
		FROM messages WHERE id = ? AND (account_id = ? OR account_id IN (SELECT id FROM accounts WHERE kind = ?))`, id, accountID, KindLocalArchive) // a local archive's mail is answered from another account (ADR 0036)
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
	fetcher := s.newAttachmentFetcher(ctx)
	defer fetcher.close()
	for _, r := range rows.Values {
		path, info, err := fetcher.get(store.Int(r[0]))
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if _, _, err := s.addAttachment(ctx, draftID, -1, info.Filename, info.ContentType, data); err != nil {
			return err
		}
	}
	return nil
}

// SaveDraft inserts d (ID 0) or updates its addresses, subject and body. The
// HTML is sanitized for composing (no scripts, handlers or remote resources).
// Attachments are changed with AddAttachment and RemoveAttachment.
func (s *Service) SaveDraft(ctx context.Context, d Draft) (Draft, error) {
	if err := s.auditMCPDraft(ctx, "SaveDraft", d.ID); err != nil {
		return Draft{}, err
	}
	return s.saveDraft(ctx, d)
}

// auditMCPDraft audits godwit mcp's draft calls (the mcp role) before they
// run; drafts by the other roles and the engine are not audited.
func (s *Service) auditMCPDraft(ctx context.Context, action string, target int64) error {
	if e, ok := apiAudit(ctx, action, target); ok && e.Source == AuditMCP {
		return s.gateway().Record(ctx, e)
	}
	return nil
}

type programKey struct{}

// withProgram marks ctx as a program's draft write that comes in through no
// Local API role: a plugin's or a Wit rule's (ADR 0042).
func withProgram(ctx context.Context) context.Context {
	return context.WithValue(ctx, programKey{}, true)
}

// fromProgram is 1 when ctx writes a draft for a program: the Local API's
// full or mcp role, or withProgram; 0 for the user's interface and the engine.
func fromProgram(ctx context.Context) int {
	if src, _ := ctx.Value(externalSourceKey{}).(string); (src != "" && uiSessionFromContext(ctx) == nil) || ctx.Value(programKey{}) != nil {
		return 1
	}
	return 0
}

// programChanged tells the interface a program's draft write succeeded
// (err nil), so an open thread shows the draft as it is now (GetThread).
func (s *Service) programChanged(ctx context.Context, err error) {
	if err == nil && fromProgram(ctx) == 1 {
		s.bus.Publish(events.Event{Type: events.DraftsChanged})
	}
}

// saveDraft is SaveDraft, unaudited.
func (s *Service) saveDraft(ctx context.Context, d Draft) (out Draft, err error) {
	defer func() { s.programChanged(ctx, err) }()
	// Draft changes and Send never interleave.
	s.editMu.Lock()
	defer s.editMu.Unlock()
	html, err := s.sanitizeDraft(ctx, d.HTML) // stored safe, so every read of a draft is safe
	if err != nil {
		return Draft{}, err
	}
	d.HTML = html
	now := time.Now().Unix()
	if d.ID == 0 {
		if !draftModes[d.Mode] {
			return Draft{}, fmt.Errorf("unknown draft mode %q", d.Mode)
		}
		if local, err := s.localArchive(ctx, d.AccountID); local || err != nil {
			return Draft{}, cmp.Or(err, ErrLocalArchive)
		}
		r, err := s.db.Execute(ctx, `INSERT INTO drafts(account_id, mode, source_message_id, to_addrs, cc_addrs, bcc_addrs,
			reply_to, subject, body_html, updated_at, from_program) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			d.AccountID, d.Mode, nullID(d.SourceMessageID), encodeList(d.To), encodeList(d.Cc), encodeList(d.Bcc),
			d.ReplyTo, d.Subject, d.HTML, now, fromProgram(ctx))
		if err != nil {
			return Draft{}, err
		}
		return s.GetDraft(ctx, r.LastInsertID)
	}
	if err := s.checkWritable(ctx, d.ID); err != nil {
		return Draft{}, err
	}
	q, args := `UPDATE drafts SET to_addrs = ?, cc_addrs = ?, bcc_addrs = ?, reply_to = ?, subject = ?,
		body_html = ?, updated_at = ?, revision = revision + 1, from_program = MAX(from_program, ?) WHERE id = ?`,
		[]any{encodeList(d.To), encodeList(d.Cc), encodeList(d.Bcc), d.ReplyTo, d.Subject, d.HTML, now, fromProgram(ctx), d.ID}
	if d.Revision != 0 {
		q, args = q+" AND revision = ?", append(args, d.Revision)
	}
	r, err := s.db.Execute(ctx, q, args...)
	if err != nil {
		return Draft{}, err
	}
	if r.Changes == 0 && d.Revision != 0 {
		rows, err := s.db.Query(ctx, "SELECT 1 FROM drafts WHERE id = ?", d.ID)
		if err != nil {
			return Draft{}, err
		}
		if len(rows.Values) > 0 {
			return Draft{}, ErrStaleDraft
		}
	}
	if r.Changes == 0 {
		return Draft{}, fmt.Errorf("draft %d not found", d.ID)
	}
	return s.GetDraft(ctx, d.ID)
}

// ListDrafts returns the account's drafts, most recently saved first. Drafts
// in the outbox are not listed.
func (s *Service) ListDrafts(ctx context.Context, accountID int64) ([]Draft, error) {
	rows, err := s.db.Query(ctx, `SELECT id FROM drafts WHERE account_id = ?
		AND id NOT IN (SELECT draft_id FROM outbox WHERE draft_id IS NOT NULL) ORDER BY updated_at DESC, id DESC`, accountID)
	if err != nil {
		return nil, err
	}
	out := make([]Draft, 0, len(rows.Values))
	for _, r := range rows.Values {
		d, err := s.GetDraft(ctx, store.Int(r[0]))
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

// GetDraft returns the draft with its attachments. HTML is sanitized again on
// ErrDraftGone: an approval made no draft, or it was sent or deleted since.
var ErrDraftGone = errors.New("the draft is gone")

// errDraftNotFound: GetDraft found no draft of the ID ("draft N not found").
var errDraftNotFound = errors.New("not found")

// DraftForApproval is the draft approval approvalID made (a create_draft the
// user allowed), to open from where it was asked for.
func (s *Service) DraftForApproval(ctx context.Context, approvalID int64) (int64, error) {
	rows, err := s.db.Query(ctx, "SELECT id FROM drafts WHERE ai_approval_id = ? ORDER BY id DESC LIMIT 1", approvalID)
	if err != nil {
		return 0, err
	}
	if len(rows.Values) == 0 {
		return 0, fmt.Errorf("approval %d: %w", approvalID, ErrDraftGone)
	}
	return store.Int(rows.Values[0][0]), nil
}

// read, covering rows stored before SaveDraft sanitized.
func (s *Service) GetDraft(ctx context.Context, id int64) (Draft, error) {
	rows, err := s.db.Query(ctx, `SELECT account_id, mode, source_message_id, to_addrs, cc_addrs, bcc_addrs, reply_to,
		subject, body_html, updated_at, revision, from_program FROM drafts WHERE id = ?`, id)
	if err != nil {
		return Draft{}, err
	}
	if len(rows.Values) == 0 {
		return Draft{}, fmt.Errorf("draft %d %w", id, errDraftNotFound)
	}
	r := rows.Values[0]
	d := Draft{ID: id, AccountID: store.Int(r[0]), Mode: store.Str(r[1]), SourceMessageID: store.Int(r[2]),
		To: jsonList(store.Str(r[3])), Cc: jsonList(store.Str(r[4])), Bcc: jsonList(store.Str(r[5])),
		ReplyTo: store.Str(r[6]), Subject: store.Str(r[7]), UpdatedAt: time.Unix(store.Int(r[9]), 0), Revision: store.Int(r[10]),
		FromProgram: store.Int(r[11]) != 0}
	if d.HTML, err = s.sanitizeDraft(ctx, store.Str(r[8])); err != nil {
		return Draft{}, err
	}
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
	a, _, err := s.addAttachmentFile(ctx, draftID, -1, srcPath)
	return a, err
}

// addAttachmentFile is AddAttachment; a rev >= 0 adds only to that revision
// (else ErrStaleDraft). It returns the draft's revision after it.
func (s *Service) addAttachmentFile(ctx context.Context, draftID, rev int64, srcPath string) (DraftAttachment, int64, error) {
	st, err := os.Stat(srcPath)
	if err != nil {
		return DraftAttachment{}, 0, err
	}
	if st.Size() > attachLimit {
		return DraftAttachment{}, 0, ErrTooLarge
	}
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return DraftAttachment{}, 0, err
	}
	ct := mime.TypeByExtension(filepath.Ext(srcPath))
	if ct == "" {
		ct = "application/octet-stream"
	}
	return s.addAttachment(ctx, draftID, rev, filepath.Base(srcPath), ct, data)
}

// addAttachment adds the file, to revision rev only if rev >= 0 (else
// ErrStaleDraft), and returns the draft's revision after it (0 if unchanged).
func (s *Service) addAttachment(ctx context.Context, draftID, rev int64, filename, contentType string, data []byte) (DraftAttachment, int64, error) {
	if int64(len(data)) > attachLimit {
		return DraftAttachment{}, 0, ErrTooLarge
	}
	s.editMu.Lock()
	defer s.editMu.Unlock()
	if err := s.checkRevision(ctx, draftID, rev); err != nil {
		return DraftAttachment{}, 0, err
	}
	rs, err := s.db.Transaction(ctx, []sqlite.Statement{{SQL: "INSERT INTO draft_attachments(draft_id, filename, content_type, size) VALUES (?, ?, ?, ?)",
		Args: []any{draftID, filename, contentType, len(data)}}, bumpRevision(ctx, draftID)})
	if err != nil {
		return DraftAttachment{}, 0, err
	}
	id := rs[0].LastInsertID
	path := filepath.Join(s.draftDir(draftID), strconv.FormatInt(id, 10))
	err = store.WriteFileAtomic(path, data)
	if err == nil {
		_, err = s.db.Execute(ctx, "UPDATE draft_attachments SET path = ? WHERE id = ?", path, id)
	}
	if err != nil {
		os.Remove(path)
		_, derr := s.db.Transaction(context.WithoutCancel(ctx), []sqlite.Statement{
			{SQL: "DELETE FROM draft_attachments WHERE id = ?", Args: []any{id}}, bumpRevision(ctx, draftID)})
		now, _ := s.draftRevision(context.WithoutCancel(ctx), draftID)
		return DraftAttachment{}, now, errors.Join(err, derr)
	}
	now, err := s.draftRevision(ctx, draftID)
	s.programChanged(ctx, err)
	return DraftAttachment{ID: id, Filename: filename, ContentType: contentType, Size: len(data)}, now, err
}

// draftRevision is the draft's stored revision.
func (s *Service) draftRevision(ctx context.Context, draftID int64) (int64, error) {
	rows, err := s.db.Query(ctx, "SELECT revision FROM drafts WHERE id = ?", draftID)
	if err != nil {
		return 0, err
	}
	if len(rows.Values) == 0 {
		return 0, fmt.Errorf("draft %d not found", draftID)
	}
	return store.Int(rows.Values[0][0]), nil
}

// checkRevision is ErrStaleDraft unless the draft is writable and rev < 0 or
// the draft is at rev. The caller holds editMu, so the draft stays there until
// it releases it.
func (s *Service) checkRevision(ctx context.Context, draftID, rev int64) error {
	if err := s.checkWritable(ctx, draftID); err != nil || rev < 0 {
		return err
	}
	now, err := s.draftRevision(ctx, draftID)
	if err != nil {
		return err
	}
	if now != rev {
		return ErrStaleDraft
	}
	return nil
}

// bumpRevision marks the draft changed: an attachment change makes the
// proposals and saves bound to its old revision stale, as a body change does.
// A program's change marks it FromProgram.
func bumpRevision(ctx context.Context, draftID int64) sqlite.Statement {
	return sqlite.Statement{SQL: "UPDATE drafts SET revision = revision + 1, updated_at = ?, from_program = MAX(from_program, ?) WHERE id = ?",
		Args: []any{time.Now().Unix(), fromProgram(ctx), draftID}}
}

// RemoveAttachment deletes the attachment from the draft and its file.
func (s *Service) RemoveAttachment(ctx context.Context, draftID, attachmentID int64) error {
	_, err := s.removeAttachment(ctx, draftID, attachmentID, -1)
	return err
}

// removeAttachment is RemoveAttachment; a rev >= 0 removes only from that
// revision (else ErrStaleDraft). It returns the draft's revision after it.
func (s *Service) removeAttachment(ctx context.Context, draftID, attachmentID, rev int64) (int64, error) {
	s.editMu.Lock()
	defer s.editMu.Unlock()
	if err := s.checkRevision(ctx, draftID, rev); err != nil {
		return 0, err
	}
	rows, err := s.db.Query(ctx, "SELECT path FROM draft_attachments WHERE id = ? AND draft_id = ?", attachmentID, draftID)
	if err != nil {
		return 0, err
	}
	if len(rows.Values) == 0 {
		return 0, fmt.Errorf("attachment %d not found in draft %d", attachmentID, draftID)
	}
	if _, err := s.db.DurableTransaction(ctx, []sqlite.Statement{
		{SQL: "DELETE FROM draft_attachments WHERE id = ?", Args: []any{attachmentID}}, bumpRevision(ctx, draftID)}); err != nil {
		return 0, err
	}
	s.programChanged(ctx, nil) // removed: the file left behind changes nothing shown
	if err := os.Remove(store.Str(rows.Values[0][0])); err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	return s.draftRevision(ctx, draftID)
}

func (s *Service) deleteDraft(ctx context.Context, id int64) error {
	r, err := s.db.DurableExecute(ctx, "DELETE FROM drafts WHERE id = ?", id) // attachments cascade
	if err != nil {
		return err
	}
	if r.Changes == 0 {
		return fmt.Errorf("draft %d not found", id)
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

// errSendChanged: the draft or the checking plugins changed since the check.
var errSendChanged = errors.New("draft or send checks changed")

// Send moves the draft into the outbox; the next sync submits it. Nothing is
// sent and the draft stays when validation or building fails. Plugins'
// message.beforeSend handlers check it first (hooks.go): a block returns
// *SendBlockedError, warnings *SendWarningsError (see SendAcknowledged).
// A draft changed during the checks is checked again; changed twice in a
// row, Send returns ErrStaleDraft.
func (s *Service) Send(ctx context.Context, draftID int64) error {
	ctx, end, err := s.begin(ctx, 0) // Close cancels the checks and waits
	if err != nil {
		return err
	}
	defer end()
	return s.send(ctx, draftID, -1, apiSend(ctx, "Send", draftID, s.secrets.Redact))
}

// apiSend is a send over the Local API: queued, the mail leaves unattended,
// so its audit row (apiAudit) goes in with the outbox item; nil for none.
func apiSend(ctx context.Context, action string, draftID int64, redact func(string) string) *ruleSend {
	if e, ok := apiAudit(ctx, action, draftID); ok {
		return &ruleSend{stmts: []sqlite.Statement{gateway.Audit(e, redact)}}
	}
	return nil
}

// send is Send inside a begun call. A want >= 0 sends only that revision:
// any other is ErrStaleDraft. rs is a rule's send, nil for the user's.
func (s *Service) send(ctx context.Context, draftID, want int64, rs *ruleSend) error {
	for changes := 0; ; {
		cs := s.sendCheckers()
		rev := want // no checks: the draft as it is when queued (want < 0), or that revision
		if len(cs) > 0 {
			s.editMu.Lock()
			d, err := s.GetDraft(ctx, draftID)
			if err == nil {
				err = s.checkWritable(ctx, draftID) // queued already: nothing to check
			}
			s.editMu.Unlock()
			if err != nil {
				return err
			}
			if want >= 0 && d.Revision != want {
				return ErrStaleDraft
			}
			if err := s.expandDraft(ctx, &d); err != nil {
				return err // the checks see what is sent
			}
			if _, err := recipients(d); err != nil {
				return err // not sendable: nothing to check
			}
			warns, err := s.runSendChecks(ctx, d, cs)
			if err != nil {
				return err
			}
			if len(warns) > 0 {
				c := SendCheck{DraftID: draftID, Revision: d.Revision, Plugins: checkerIDs(cs), Warnings: warns}
				c.Digest = sendDigest(c)
				s.keepSendCheck(c)
				return &SendWarningsError{Check: c.clone()}
			}
			rev = d.Revision
		}
		err := s.enqueue(ctx, draftID, rev, checkerIDs(cs), rs)
		if !errors.Is(err, errSendChanged) {
			return err
		}
		if changes++; changes == 2 {
			return ErrStaleDraft
		}
	}
}

// enqueue queues the draft if it is still at revision rev (any, if rev < 0)
// and the checking plugins are still checked, else errSendChanged. Plugin
// changes wait from the comparison until the draft is queued. The item holds
// the draft, read-only, and is sent once UndoSendWindow has passed; a draft
// queued already is ErrStaleDraft. A rule's send (rs) carries its marker
// header and writes rs.stmts with the item, in one transaction.
func (s *Service) enqueue(ctx context.Context, draftID, rev int64, checked []string, rs *ruleSend) error {
	release, err := s.stopDraftRuns(ctx, draftID) // Wit proposes nothing for the queued draft
	if err != nil {
		return err
	}
	defer release()
	s.editMu.Lock() // enters the outbox once, with the draft as it is now
	defer s.editMu.Unlock()
	s.pluginsMu.RLock()
	defer s.pluginsMu.RUnlock()
	d, err := s.GetDraft(ctx, draftID)
	if err != nil {
		return err
	}
	if err := s.checkWritable(ctx, draftID); err != nil {
		return err
	}
	if (rev >= 0 && d.Revision != rev) || !slices.Equal(checkerIDs(s.sendCheckersLocked()), checked) {
		return errSendChanged
	}
	if afterDraftLoad != nil {
		afterDraftLoad()
	}
	if err := s.expandDraft(ctx, &d); err != nil {
		return err
	}
	rcpts, err := recipients(d) // the envelope has the restored addresses
	if err != nil {
		return err
	}
	imgs, err := s.signatureImages(ctx, true) // an unknown cid was dropped by GetDraft
	if err != nil {
		return err
	}
	var related []mail.Attachment
	total := int64(len(d.HTML))
	for _, id := range mail.CIDs(d.HTML) {
		if a, ok := imgs[id]; ok {
			delete(imgs, id) // each once
			related = append(related, a)
			total += int64(a.Size)
		}
	}
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
	if acc.ReceiveOnly {
		return ErrReceiveOnly
	}
	o := mail.Outgoing{From: (&netmail.Address{Name: acc.Name, Address: acc.Email}).String(), ReplyTo: d.ReplyTo,
		To: d.To, Cc: d.Cc, Bcc: d.Bcc, Subject: d.Subject, HTML: d.HTML, Related: related}
	var stmts []sqlite.Statement
	if rs != nil {
		stmts = rs.stmts
	}
	if rs != nil && rs.ruleID != 0 {
		o.Header = map[string]string{mail.RuleHeader: strconv.FormatInt(rs.ruleID, 10)}
	}
	if d.SourceMessageID != 0 && (d.Mode == "reply" || d.Mode == "reply_all" || d.Mode == "forward") {
		rows, err := s.db.Query(ctx, "SELECT message_id, refs FROM messages WHERE id = ?", d.SourceMessageID)
		if err != nil {
			return err
		}
		if len(rows.Values) > 0 && store.Str(rows.Values[0][0]) != "" {
			id := store.Str(rows.Values[0][0])
			if d.Mode == "forward" {
				// A forward is not a reply (RFC 5322 3.6.4), and its recipient may be new
				// to the thread: the original's ID keeps it in the conversation without
				// listing the earlier mail's. GetThread recognizes a forward by this shape.
				o.References = []string{id}
			} else {
				o.InReplyTo = id
				o.References = append(strings.Fields(store.Str(rows.Values[0][1])), id)
			}
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
	raw, messageID, err := mail.Build(o, s.clk().Now())
	if err != nil {
		return err
	}
	if int64(len(raw)) > attachLimit { // encoding, headers and the text part count too
		return ErrTooLarge
	}
	// A path of its own: an item taken back removes its file after the draft is
	// writable again, so it may be queued again first.
	rawPath := filepath.Join(s.dataDir, "outbox", strconv.FormatInt(draftID, 10)+"-"+rand.Text()+".eml")
	// Durable (file and directory entry) before the item is sent from it.
	if err := store.WriteFileAtomic(rawPath, raw); err != nil {
		return err
	}
	if d.SourceMessageID != 0 && (d.Mode == "reply" || d.Mode == "reply_all") {
		// last_insert_rowid() here is the outbox item, inserted just before.
		stmts = append([]sqlite.Statement{{SQL: `INSERT INTO reply_records(thread_id, outbox_id, at)
			SELECT thread_id, last_insert_rowid(), max(?, COALESCE(date, 0)) FROM messages WHERE id = ? AND thread_id IS NOT NULL`,
			Args: []any{s.clk().Now().Unix(), d.SourceMessageID}}}, stmts...)
	}
	if _, err := s.db.DurableTransaction(ctx, append([]sqlite.Statement{{SQL: `INSERT INTO outbox(account_id, message_id_hdr, raw_path, rcpts, state, subject, created_at,
		draft_id, send_after) VALUES (?, ?, ?, ?, 'queued', ?, ?, ?, ?)`,
		Args: []any{d.AccountID, messageID, rawPath, encodeList(rcpts), d.Subject, s.clk().Now().Unix(), draftID,
			s.clk().Now().Add(UndoSendWindow).UnixMilli()}}}, stmts...)); err != nil {
		os.Remove(rawPath)
		return err
	}
	s.bus.Publish(events.Event{Type: events.OutboxChanged, AccountID: d.AccountID})
	s.wakeWorker(d.AccountID) // its round sets the time the item is sent at
	return nil
}
