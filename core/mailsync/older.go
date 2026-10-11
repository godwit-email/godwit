package mailsync

import (
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
	if !f.synced || f.uidValidity != sel.UIDValidity {
		return false, ErrNotSynced
	}
	if f.lastUID == 0 { // synced empty: nothing older
		return false, nil
	}
	have, err := db.Query(ctx, "SELECT server_uid FROM messages WHERE server_folder_id = ?", f.id)
	if err != nil {
		return false, err
	}
	known := map[imap.UID]bool{}
	for _, v := range have.Values {
		known[imap.UID(store.Int(v[0]))] = true
	}
	// Down from the newest by sequence number, a window of UIDs at a time,
	// until a page (and one more, for more) the folder lacks: no UID SEARCH
	// of the whole folder per page.
	var uids []imap.UID
	window := uint32(5 * OlderPage)
	for hi := sel.NumMessages; hi > 0 && len(uids) <= OlderPage; {
		lo := max(1, int64(hi)-int64(window)+1)
		var seq imap.SeqSet
		seq.AddRange(uint32(lo), hi)
		got, err := c.Fetch(seq, &imap.FetchOptions{UID: true}).Collect()
		if err != nil {
			return false, err
		}
		slices.SortFunc(got, func(x, y *imapclient.FetchMessageBuffer) int { return byUID(y, x) })
		for _, m := range got {
			if m.UID <= imap.UID(f.lastUID) && !known[m.UID] {
				known[m.UID] = true // once, should an expunge shift a window
				uids = append(uids, m.UID)
			}
		}
		hi = uint32(lo) - 1
	}
	if len(uids) == 0 {
		return false, nil
	}
	more = len(uids) > OlderPage
	msgs, err := c.Fetch(imap.UIDSetNum(uids[:min(len(uids), OlderPage)]...), importItems()).Collect()
	if err != nil {
		return false, err
	}
	slices.SortFunc(msgs, func(x, y *imapclient.FetchMessageBuffer) int { return byUID(y, x) })
	return more, importAll(ctx, db, c, a, f, msgs, false, &r) // older mail, not new arrivals
}
