package mailsync

import (
	"context"
	"errors"
	"slices"
	"strconv"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/godwit-email/godwit/core/store"
)

// pushGmailLabels applies a recorded move with additive label writes. Every
// attempt reads membership by Gmail identity first, including after a lost reply.
func pushGmailLabels(ctx context.Context, db *store.DB, c *imapclient.Client, o op) (string, error) {
	r, err := db.Query(ctx, `SELECT gmail_msgid FROM messages WHERE id = ?`, o.msg)
	if err != nil {
		return "", err
	}
	if len(r.Values) == 0 {
		return "the message no longer exists", finish(ctx, db, o, "conflict", "the message no longer exists")
	}
	id, _ := strconv.ParseUint(store.Str(r.Values[0][0]), 10, 64)
	folders, err := db.Query(ctx, "SELECT id, name, role FROM folders WHERE id IN (?, ?)", o.src, o.dst)
	if err != nil {
		return "", err
	}
	var srcName, srcRole, dstName, dstRole string
	for _, f := range folders.Values {
		if store.Int(f[0]) == o.src {
			srcName, srcRole = store.Str(f[1]), store.Str(f[2])
		}
		if store.Int(f[0]) == o.dst {
			dstName, dstRole = store.Str(f[1]), store.Str(f[2])
		}
	}
	if o.dst != 0 && dstName == "" {
		return gmailFailure(ctx, db, o, errNoDestination)
	}
	if id == 0 && srcName != "" { // An upgrade can still have rows not fetched for Gmail identity.
		loc, err := db.Query(ctx, "SELECT uid, uidvalidity FROM message_locations WHERE message_id = ? AND folder_id = ? AND uid IS NOT NULL", o.msg, o.src)
		if err != nil {
			return "", err
		}
		if len(loc.Values) > 0 {
			sel, err := c.Select(srcName, nil).Wait()
			if err != nil {
				return gmailFailure(ctx, db, o, err)
			}
			if sel.UIDValidity != uint32(store.Int(loc.Values[0][1])) {
				msg := "the source folder was recreated before Gmail identity was known"
				return msg, finish(ctx, db, o, "conflict", msg)
			}
			got, err := c.Fetch(imap.UIDSetNum(imap.UID(store.Int(loc.Values[0][0]))), &imap.FetchOptions{UID: true, GmailMsgID: true}).Collect()
			if err != nil {
				return gmailFailure(ctx, db, o, err)
			}
			if len(got) > 0 {
				id = got[0].GmailMsgID
			}
			if id != 0 {
				if _, err := db.Execute(ctx, "UPDATE messages SET gmail_msgid = ? WHERE id = ?", strconv.FormatUint(id, 10), o.msg); err != nil {
					return "", err
				}
			}
		}
	}
	if id == 0 {
		return lostSource(ctx, db, o, o.attempts > 0)
	}
	addOnly := o.src == 0 || srcRole == "all"
	completed := o
	if srcRole == "all" {
		completed.src = 0 // All Mail is implicit membership, never a removable label.
	}
	if o.src == 0 {
		anchors, err := db.Query(ctx, `SELECT f.name FROM message_locations l JOIN folders f ON f.id = l.folder_id WHERE l.message_id = ? AND l.uid IS NOT NULL ORDER BY l.folder_id`, o.msg)
		if err != nil {
			return "", err
		}
		// An add-only intent may use any identity-checked location; none is removed.
		for _, a := range anchors.Values {
			_, uid, err := gmailLocation(c, store.Str(a[0]), id)
			if err != nil {
				return gmailFailure(ctx, db, o, err)
			}
			if uid != 0 {
				srcName = store.Str(a[0])
				break
			}
		}
	}
	srcValidity, srcUID, err := gmailLocation(c, srcName, id)
	if err != nil {
		return gmailFailure(ctx, db, o, err)
	}
	var dstValidity, dstUID uint32
	if dstName != "" {
		dstValidity, dstUID, err = gmailLocation(c, dstName, id)
		if err != nil {
			return gmailFailure(ctx, db, o, err)
		}
		if dstValidity == 0 {
			return gmailFailure(ctx, db, o, errNoDestination)
		}
	}
	removeOnly := o.dst == 0 || dstRole == "all"
	if (addOnly && dstUID != 0) || o.src == o.dst {
		return "", gmailMoved(ctx, db, completed, dstValidity, dstUID)
	}
	if srcUID == 0 {
		if removeOnly || dstUID != 0 {
			return "", gmailMoved(ctx, db, completed, dstValidity, dstUID)
		}
		msg := "the message left the folder it was moved from and is absent from the destination"
		return msg, finish(ctx, db, o, "conflict", msg)
	}
	if err := reselect(c, srcName, srcValidity); err != nil {
		return gmailFailure(ctx, db, o, err)
	}
	if _, err := db.Execute(ctx, "UPDATE ops SET attempts = attempts + 1 WHERE id = ?", o.id); err != nil {
		return "", err
	}
	o.attempts++
	purges, err := db.Query(ctx, "SELECT 1 FROM ops WHERE message_id = ? AND kind = 'purge' AND attempts > 0", o.msg)
	if err != nil {
		return "", err
	}
	if len(purges.Values) > 0 {
		if err := undelete(c, imap.UID(srcUID)); err != nil {
			return gmailFailure(ctx, db, o, err)
		}
	}
	set := imap.UIDSetNum(imap.UID(srcUID))
	binned := dstRole == "trash" || dstRole == "junk"
	if !removeOnly && (dstUID == 0 || binned) {
		if err := c.StoreGmailLabels(set, &imap.StoreGmailLabels{Op: imap.StoreFlagsAdd, Silent: true, Labels: []string{gmailLabel(dstName, dstRole)}}).Close(); err != nil {
			return gmailFailure(ctx, db, o, err)
		}
	}
	// Adding Trash/Spam is the special transition; Gmail removes other membership.
	if !binned && !addOnly {
		if err := c.StoreGmailLabels(set, &imap.StoreGmailLabels{Op: imap.StoreFlagsDel, Silent: true, Labels: []string{gmailLabel(srcName, srcRole)}}).Close(); err != nil {
			return gmailFailure(ctx, db, o, err)
		}
	}
	// A successful STORE may match no UID if another client moved it meanwhile.
	_, source, err := gmailLocation(c, srcName, id)
	if err != nil {
		return gmailFailure(ctx, db, o, err)
	}
	if dstName != "" {
		dstValidity, dstUID, err = gmailLocation(c, dstName, id)
		if err != nil {
			return gmailFailure(ctx, db, o, err)
		}
	}
	if (!addOnly && source != 0) || (!removeOnly && dstUID == 0) {
		return gmailFailure(ctx, db, o, errors.New("Gmail move postcondition was not observed"))
	}
	return "", gmailMoved(ctx, db, completed, dstValidity, dstUID)
}

func gmailLabel(name, role string) string {
	switch role {
	case "inbox":
		return `\Inbox`
	case "sent":
		return `\Sent`
	case "drafts":
		return `\Draft`
	case "trash":
		return `\Trash`
	case "junk":
		return `\Spam`
	}
	return name
}

// gmailLocation searches the recorded folder, never a substitute source.
func gmailLocation(c *imapclient.Client, folder string, id uint64) (validity, uid uint32, err error) {
	if folder == "" {
		return 0, 0, nil
	}
	sel, err := c.Select(folder, nil).Wait()
	if err != nil {
		var refused *imap.Error
		if errors.As(err, &refused) && refused.Type == imap.StatusResponseTypeNo {
			listed, listErr := c.List("", folder, nil).Collect()
			if listErr != nil {
				return 0, 0, listErr
			}
			if !slices.ContainsFunc(listed, func(f *imap.ListData) bool { return f.Mailbox == folder && selectable(f) }) {
				return 0, 0, nil
			}
		}
		return 0, 0, err
	}
	found, err := c.UIDSearch(&imap.SearchCriteria{GmailMsgID: []uint64{id}}, nil).Wait()
	if err != nil {
		return 0, 0, err
	}
	return sel.UIDValidity, single(found.All), nil
}

func gmailMoved(ctx context.Context, db *store.DB, o op, validity, uid uint32) error {
	if o.dst != 0 && uid != 0 {
		return moved(ctx, db, o, validity, uid)
	}
	// All Mail can be hidden: removing INBOX is complete without a target UID.
	_, err := db.Transaction(ctx, append(unlocate(o.msg, o.src), sqlite.Statement{SQL: "UPDATE ops SET state = 'done', last_error = NULL WHERE id = ?", Args: []any{o.id}}))
	return err
}

// An uncertain write stays pending even after the ordinary retry budget; only
// a later membership check can resolve it, not the number of lost responses.
func gmailFailure(ctx context.Context, db *store.DB, o op, cause error) (string, error) {
	if cause == errNoDestination {
		if _, err := db.Execute(ctx, "UPDATE ops SET attempts = attempts + 1 WHERE id = ?", o.id); err != nil {
			return "", err
		}
		o.attempts++
		return fail(ctx, db, o, cause)
	}
	_, err := db.Execute(ctx, "UPDATE ops SET last_error = ? WHERE id = ?", cause.Error(), o.id)
	var refused *imap.Error
	if errors.As(cause, &refused) {
		return "", err
	}
	return "", errors.Join(cause, err)
}
