package core

import (
	"context"
	"errors"

	"github.com/godwit-email/godwit/backend"
)

// RecheckSending runs the account's connection check with its stored
// servers and credentials. If it passes, a receive-only account sends again
// and its worker wakes; otherwise it stays receive-only and the report says
// which step failed. It logs in to the user's own servers and sends nothing.
func (s *Service) RecheckSending(ctx context.Context, id int64) (CheckReport, error) {
	ctx, end, err := s.begin(ctx, id)
	if err != nil {
		return CheckReport{}, err
	}
	defer end()
	acc, err := s.account(ctx, id)
	if err != nil {
		return CheckReport{}, err
	}
	if acc.Kind == KindLocalArchive {
		return CheckReport{}, ErrLocalArchive
	}
	return s.recheckSending(ctx, acc)
}

// recheckSending is RecheckSending inside a begun call: a failing check is
// the report, not an error.
func (s *Service) recheckSending(ctx context.Context, acc Account) (CheckReport, error) {
	in := serverInput(acc.Email, acc.servers())
	r, err := s.checkStored(ctx, acc, &in)
	var ce *CheckError
	if err != nil && !errors.As(err, &ce) {
		return r, err
	}
	if err != nil || !acc.ReceiveOnly {
		return r, ctx.Err()
	}
	if _, err := s.db.Execute(ctx, "UPDATE accounts SET receive_only = 0 WHERE id = ?", acc.ID); err != nil {
		return r, err
	}
	s.wakeWorker(acc.ID)
	return r, nil
}

// checkStored checks in with acc's stored credentials, an OAuth account's
// Graph fallback included. A failing check is a *CheckError; other errors
// come from reading the credentials.
func (s *Service) checkStored(ctx context.Context, acc Account, in *AccountInput) (CheckReport, error) {
	pw, tok, _, err := s.credentials(ctx, acc, "")
	if err != nil {
		return CheckReport{}, err
	}
	in.Password = pw
	if tok != "" {
		in.graph = func(ctx context.Context) error { _, _, err := s.graphToken(ctx, acc); return err }
	}
	r, err := s.check(ctx, *in, tok)
	if tok != "" && errors.Is(err, backend.ErrAuthFailed) { // the cached token may be stale
		if _, tok, _, err = s.credentials(ctx, acc, tok); err != nil {
			return r, err
		}
		r, err = s.check(ctx, *in, tok)
	}
	if err != nil {
		return r, &CheckError{Report: r, err: err}
	}
	return r, nil
}
