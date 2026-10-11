package mailsync

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/store"
)

// OlderPage is how many messages one FetchOlder imports. Tests lower it.
var OlderPage = 200

// ErrNotSynced is FetchOlder on a folder whose first sync (at the server's
// UIDVALIDITY) has not completed: the next sync, not a page, fills it.
var ErrNotSynced = errors.New("folder not synced yet")

// FetchOlder imports one page of the folder's mail that the sync range left
// out: the newest server UIDs up to last_uid that the folder does not store.
// It keeps what it imports; the range's backfill and filled_since only ever
// add what is missing, so the next sync neither refetches nor deletes it.
// more reports whether the server holds still more the folder lacks.
func FetchOlder(ctx context.Context, db *store.DB, c *imapclient.Client, accountID, folderID int64, bus *events.Bus) (more bool, err error) {
	var r result
	defer r.publish(bus, accountID)
	a, err := loadAccount(ctx, db, accountID)
	if err != nil {
		return false, err
	}
	rows, err := db.Query(ctx, "SELECT "+folderCols+" FROM folders WHERE id = ? AND account_id = ?", folderID, accountID)
	if err != nil {
		return false, err
	}
	if len(rows.Values) == 0 {
		return false, fmt.Errorf("folder %d not found", folderID)
	}
	f := scanFolder(rows.Values[0])
	sel, err := c.Select(f.name, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return false, err
	}
	if f.lastUID == 0 || !f.synced || f.uidValidity != sel.UIDValidity {
		return false, ErrNotSynced
	}
	var upto imap.UIDSet
	upto.AddRange(1, imap.UID(f.lastUID))
	found, err := c.UIDSearch(&imap.SearchCriteria{UID: []imap.UIDSet{upto}}, nil).Wait()
	if err != nil {
		return false, err
	}
	have, err := db.Query(ctx, "SELECT server_uid FROM messages WHERE server_folder_id = ?", f.id)
	if err != nil {
		return false, err
	}
	known := map[imap.UID]bool{}
	for _, v := range have.Values {
		known[imap.UID(store.Int(v[0]))] = true
	}
	uids := slices.DeleteFunc(found.AllUIDs(), func(u imap.UID) bool { return known[u] })
	if len(uids) == 0 {
		return false, nil
	}
	slices.SortFunc(uids, func(x, y imap.UID) int { return cmp.Compare(y, x) })
	more = len(uids) > OlderPage
	msgs, err := c.Fetch(imap.UIDSetNum(uids[:min(len(uids), OlderPage)]...), importItems()).Collect()
	if err != nil {
		return false, err
	}
	slices.SortFunc(msgs, func(x, y *imapclient.FetchMessageBuffer) int { return byUID(y, x) })
	return more, importAll(ctx, db, c, a, f, msgs, false, &r) // older mail, not new arrivals
}
