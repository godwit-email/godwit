package core

import (
	"context"
	"errors"

	"github.com/godwit-email/godwit/core/conn"
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
	pw, tok, _, err := s.credentials(ctx, acc, "")
	if err != nil {
		return CheckReport{}, err
	}
	in.Password = pw
	r, err := s.check(ctx, in, tok)
	if tok != "" && errors.Is(err, conn.ErrAuthFailed) { // the cached token may be stale
		if _, tok, _, err = s.credentials(ctx, acc, tok); err != nil {
			return r, err
		}
		r, err = s.check(ctx, in, tok)
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
