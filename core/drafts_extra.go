package core

import (
	"context"
	"fmt"
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
