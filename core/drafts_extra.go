package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/store"
)

// DeleteDraft deletes the draft and its attachment files, cancelling the
// Wit runs of its conversations first. A draft in the outbox stays
// (ErrStaleDraft).
func (s *Service) DeleteDraft(ctx context.Context, id int64) error {
	release, err := s.stopDraftRuns(ctx, id)
	if err != nil {
		return err
	}
	defer release()
	s.editMu.Lock() // never while Send queues it
	defer s.editMu.Unlock()
	if err := s.checkWritable(ctx, id); err != nil {
		return err
	}
	return s.deleteDraft(ctx, id)
}

// stopDraftRuns cancels the runs in flight of draftID's conversations and
// waits for them, and for their approvals executing outside the ledger's lock, to end, so
// none settles into a deleted draft. It returns holding the ledger
// (runLedger.hold), so none starts until the caller has deleted the draft and
// releases it.
func (s *Service) stopDraftRuns(ctx context.Context, draftID int64) (release func(), err error) {
	return s.ledger.hold(ctx, func() ([]int64, error) {
		rows, err := s.db.Query(ctx, "SELECT r.id FROM runs r JOIN conversations c ON c.id = r.conversation_id WHERE c.draft_id = ?", draftID)
		if err != nil {
			return nil, err
		}
		ids := make([]int64, 0, len(rows.Values))
		for _, r := range rows.Values {
			ids = append(ids, store.Int(r[0]))
		}
		return ids, nil
	})
}

// SetDraftAccount moves the draft to accountID: the body's signature becomes
// that account's, attachments and the reply's source stay, and the revision
// grows by one. revision must be the stored one (ErrStaleDraft).
func (s *Service) SetDraftAccount(ctx context.Context, id, accountID, revision int64) (Draft, error) {
	s.sigMu.Lock() // the signature read stays the one stored, its images with it (NewDraft)
	defer s.sigMu.Unlock()
	acc, err := s.account(ctx, accountID)
	if err != nil {
		return Draft{}, err
	}
	if acc.Kind == KindLocalArchive {
		return Draft{}, ErrLocalArchive
	}
	if acc.ReceiveOnly {
		return Draft{}, ErrReceiveOnly
	}
	s.editMu.Lock()
	defer s.editMu.Unlock()
	d, err := s.GetDraft(ctx, id)
	if err != nil {
		return Draft{}, err
	}
	if err := s.checkWritable(ctx, id); err != nil {
		return Draft{}, err
	}
	if d.Revision != revision {
		return Draft{}, ErrStaleDraft
	}
	cids, err := s.signatureCIDs(ctx)
	if err != nil {
		return Draft{}, err
	}
	st, err := s.loadSettings(ctx)
	if err != nil {
		return Draft{}, err
	}
	ed, quote, _ := mail.SplitDraft(d.HTML)
	body := mail.SanitizeDraft(mail.JoinDraft(ed, quote, signatureHTML(acc.Signature, cids, st.SignatureDelimiter)), cids)
	r, err := s.db.Execute(ctx, `UPDATE drafts SET account_id = ?, body_html = ?, updated_at = ?, revision = revision + 1,
		from_program = MAX(from_program, ?) WHERE id = ? AND revision = ?`, accountID, body, time.Now().Unix(), fromProgram(ctx), id, revision)
	if err != nil {
		return Draft{}, err
	}
	if r.Changes == 0 {
		return Draft{}, fmt.Errorf("draft %d changed or was deleted meanwhile", id)
	}
	s.programChanged(ctx, nil)
	return s.GetDraft(ctx, id)
}

// SendRevision is Send for exactly that revision of the draft: if the stored
// one differs, nothing is sent and the error is ErrStaleDraft.
func (s *Service) SendRevision(ctx context.Context, draftID, revision int64) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	return s.send(ctx, draftID, max(revision, 0), apiSend(ctx, "SendRevision", draftID, s.secrets.Redact)) // a negative one would mean any
}

// ErrNotProgramDraft: ApproveDraft was asked for a draft no program wrote.
var ErrNotProgramDraft = errors.New("no program wrote this draft")

// ApproveDraft is the user's 批准并发送 on a draft a program wrote
// (FromProgram, ADR 0042): Send for exactly the revision the interface
// showed, else ErrStaleDraft, so a program's change after the user looked
// voids it; any other draft is ErrNotProgramDraft. Plugins' message.beforeSend
// checks run as for Send (warnings: SendAcknowledgedRevision, same revision),
// and the send is audited with its outbox item. Only the interface may call
// it (uiOnly): the approval stands for the user's click.
func (s *Service) ApproveDraft(ctx context.Context, draftID, revision int64) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	d, err := s.GetDraft(ctx, draftID)
	if err != nil {
		return err
	}
	if !d.FromProgram { // never cleared, so it holds until the send below
		return ErrNotProgramDraft
	}
	return s.send(ctx, draftID, max(revision, 0), apiSend(ctx, "ApproveDraft", draftID, s.secrets.Redact))
}

// SendAcknowledgedRevision is SendAcknowledged for exactly that revision of
// the draft: if the stored one differs, nothing is sent and the error is
// ErrStaleDraft.
func (s *Service) SendAcknowledgedRevision(ctx context.Context, draftID, revision int64, digest string) error {
	return s.sendAcknowledged(ctx, draftID, max(revision, 0), digest, apiSend(ctx, "SendAcknowledgedRevision", draftID, s.secrets.Redact))
}

// DeleteDraftIfEmpty deletes the draft, as DeleteDraft, if it is still at
// revision and holds nothing the user added: no attachments, no text
// outside the quote and the signature, and the addresses and subject a new
// draft of its mode starts with (none for a new one; a reply's own). A
// draft Wit is working on (an assist run active, or an approval pending) is
// in use, never empty; one in the outbox is ErrStaleDraft. deleted reports
// whether it went.
func (s *Service) DeleteDraftIfEmpty(ctx context.Context, id, revision int64) (deleted bool, err error) {
	release, err := s.ledger.hold(ctx, nil) // no run starts or approval appears until the delete
	if err != nil {
		return false, err
	}
	defer release()
	s.editMu.Lock()
	defer s.editMu.Unlock()
	d, err := s.GetDraft(ctx, id)
	if err != nil {
		return false, err
	}
	if err := s.checkWritable(ctx, id); err != nil {
		return false, err
	}
	busy, err := s.db.Query(ctx, `SELECT 1 FROM conversations c JOIN runs r ON r.conversation_id = c.id
		WHERE c.draft_id = ? AND (r.state IN ('running', 'interrupted', 'resuming')
		OR EXISTS (SELECT 1 FROM approvals a WHERE a.run_id = r.id AND a.state = 'pending')) LIMIT 1`, id)
	if err != nil {
		return false, err
	}
	if len(busy.Values) > 0 {
		return false, nil
	}
	ed, _, _ := mail.SplitDraft(d.HTML)
	if d.Revision != revision || len(d.Attachments) > 0 || strings.TrimSpace(mail.HTMLToText(ed)) != "" { // TrimSpace trims U+00A0 too
		return false, nil
	}
	from := d.AccountID
	if d.SourceMessageID != 0 { // a receive-only account's mail was answered from another (buildDraft): rebuild it the same way
		r, err := s.db.Query(ctx, "SELECT m.account_id FROM messages m JOIN accounts a ON a.id = m.account_id WHERE m.id = ? AND a.receive_only = 1", d.SourceMessageID)
		if err != nil {
			return false, err
		}
		if len(r.Values) > 0 {
			from = store.Int(r.Values[0][0])
		}
	}
	fresh, err := s.buildDraft(ctx, from, d.Mode, d.SourceMessageID)
	if err != nil { // e.g. the source is gone: keep it
		return false, nil
	}
	if !slices.Equal(d.To, fresh.To) || !slices.Equal(d.Cc, fresh.Cc) || !slices.Equal(d.Bcc, fresh.Bcc) ||
		d.ReplyTo != fresh.ReplyTo || d.Subject != fresh.Subject {
		return false, nil
	}
	err = s.deleteDraft(ctx, id)
	return err == nil, err
}

// AddAttachmentAt is AddAttachment for exactly that revision of the draft
// (else ErrStaleDraft, nothing added); it returns the new revision.
func (s *Service) AddAttachmentAt(ctx context.Context, draftID, revision int64, srcPath string) (DraftAttachment, int64, error) {
	return s.addAttachmentFile(ctx, draftID, max(revision, 0), srcPath)
}

// RemoveAttachmentAt is RemoveAttachment for exactly that revision of the
// draft (else ErrStaleDraft, nothing removed); it returns the new revision.
func (s *Service) RemoveAttachmentAt(ctx context.Context, draftID, attachmentID, revision int64) (int64, error) {
	return s.removeAttachment(ctx, draftID, attachmentID, max(revision, 0))
}
