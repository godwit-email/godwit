package mailsync

import (
	"context"
	"fmt"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/store"
)

// OlderPage is how many messages one FetchOlder imports. Tests lower it.
var OlderPage = 200

// ErrNotSynced is FetchOlder on a folder whose first sync (at the server's
// current state) has not completed: the next sync, not a page, fills it.
var ErrNotSynced = backend.ErrNotSynced

// FetchOlder imports one page of the folder's mail that the sync range left
// out, through recv. It keeps what it imports; the range's
// backfill only ever adds what is missing, so the next sync neither refetches
// nor deletes it. more reports whether the server holds still more the folder
// lacks. originals is Fetch's.
func FetchOlder(ctx context.Context, db *store.DB, recv backend.Receiver, accountID, folderID int64, bus *events.Bus, originals ...Originals) (more bool, err error) {
	rows, err := db.Query(ctx, "SELECT server_ref FROM folders WHERE id = ? AND account_id = ? AND server_ref IS NOT NULL", folderID, accountID)
	if err != nil {
		return false, err
	}
	if len(rows.Values) == 0 {
		return false, fmt.Errorf("folder %d not found", folderID)
	}
	return run(ctx, db, recv, accountID, bus, []backend.FolderRef{backend.FolderRef(store.Str(rows.Values[0][0]))}, OlderPage, originals...)
}
