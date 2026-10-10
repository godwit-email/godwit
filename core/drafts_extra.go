package core

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/godwit-email/godwit/core/mail"
)

// DeleteDraft deletes the draft and its attachment files. A draft already
// queued is in the outbox, not here, and stays.
func (s *Service) DeleteDraft(ctx context.Context, id int64) error {
	s.editMu.Lock() // never while Send queues it
	defer s.editMu.Unlock()
	return s.deleteDraft(ctx, id)
}

// SetDraftAccount moves the draft to accountID: the body's signature becomes
// that account's, attachments and the reply's source stay, and the revision
// grows by one. revision must be the stored one (ErrStaleDraft).
func (s *Service) SetDraftAccount(ctx context.Context, id, accountID, revision int64) (Draft, error) {
	acc, err := s.account(ctx, accountID)
	if err != nil {
		return Draft{}, err
	}
	s.editMu.Lock()
	defer s.editMu.Unlock()
	d, err := s.GetDraft(ctx, id)
	if err != nil {
		return Draft{}, err
	}
	if d.Revision != revision {
		return Draft{}, ErrStaleDraft
	}
	ed, quote, _ := mail.SplitDraft(d.HTML)
	body := mail.SanitizeCompose(mail.JoinDraft(ed, quote, signatureHTML(acc.Signature)))
	r, err := s.db.Execute(ctx, `UPDATE drafts SET account_id = ?, body_html = ?, updated_at = ?, revision = revision + 1
		WHERE id = ? AND revision = ?`, accountID, body, time.Now().Unix(), id, revision)
	if err != nil {
		return Draft{}, err
	}
	if r.Changes == 0 {
		return Draft{}, fmt.Errorf("draft %d changed or was deleted meanwhile", id)
	}
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
	return s.send(ctx, draftID, max(revision, 0)) // a negative one would mean any
}

// SendAcknowledgedRevision is SendAcknowledged for exactly that revision of
// the draft: if the stored one differs, nothing is sent and the error is
// ErrStaleDraft.
func (s *Service) SendAcknowledgedRevision(ctx context.Context, draftID, revision int64, digest string) error {
	return s.sendAcknowledged(ctx, draftID, max(revision, 0), digest)
}

// DeleteDraftIfEmpty deletes the draft, as DeleteDraft, if it is still at
// revision and holds nothing the user added: no attachments, no text
// outside the quote and the signature, and the addresses and subject a new
// draft of its mode starts with (none for a new one; a reply's own).
// deleted reports whether it went.
func (s *Service) DeleteDraftIfEmpty(ctx context.Context, id, revision int64) (deleted bool, err error) {
	s.editMu.Lock()
	defer s.editMu.Unlock()
	d, err := s.GetDraft(ctx, id)
	if err != nil {
		return false, err
	}
	ed, _, _ := mail.SplitDraft(d.HTML)
	if d.Revision != revision || len(d.Attachments) > 0 || strings.TrimSpace(mail.HTMLToText(ed)) != "" { // TrimSpace trims U+00A0 too
		return false, nil
	}
	fresh, err := s.buildDraft(ctx, d.AccountID, d.Mode, d.SourceMessageID)
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
