package core

import (
	"context"
	"fmt"

	"github.com/godwit-email/godwit/core/mailsync"
	"github.com/godwit-email/godwit/core/store"
)

// ErrNotSynced is LoadOlder on a folder whose first sync has not completed.
var ErrNotSynced = mailsync.ErrNotSynced

// LoadOlder imports one page (about 200) of the folder's mail that the sync
// range left out, newest first, and keeps it. more is false once the server
// holds nothing more the folder lacks. It takes the account's sync lock, so a
// sync runs before or after it, never alongside; cancelling ctx stops it.
func (s *Service) LoadOlder(ctx context.Context, folderID int64) (more bool, err error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return false, err
	}
	defer end()
	rows, err := s.db.Query(ctx, "SELECT account_id FROM folders WHERE id = ?", folderID)
	if err != nil {
		return false, err
	}
	if len(rows.Values) == 0 {
		return false, fmt.Errorf("folder %d not found", folderID)
	}
	accountID := store.Int(rows.Values[0][0])
	if err := s.bind(ctx, accountID); err != nil {
		return false, err
	}
	unlock, err := s.lockAccount(ctx, accountID)
	if err != nil {
		return false, err
	}
	defer unlock()
	c, release, _, err := s.dialAccount(ctx, accountID, nil)
	if err != nil {
		return false, err
	}
	defer release()
	more, err = mailsync.FetchOlder(ctx, s.db, c, accountID, folderID, s.bus)
	if err != nil && ctx.Err() != nil {
		return false, ctx.Err()
	}
	return more, err
}
