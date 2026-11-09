package mailsync

import (
	"context"
	"errors"
	"math"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/godwit-email/godwit/backend/imapbackend"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/store"
)

// refreshAuthenticationResults upgrades old cached messages in bounded
// header-only batches, folder by folder. No message body or account setting
// is needed to recover the current receiving service's data. more reports a
// full batch: the next round goes on.
//
// shortcut: reads over the caller's IMAP connection; #603 moves it to the
// backend's Fetch of header fields.
func refreshAuthenticationResults(ctx context.Context, db *store.DB, c *imapclient.Client, accountID int64, bus *events.Bus) (more bool, err error) {
	var r result
	defer r.publish(bus, accountID)
	rows, err := db.Query(ctx, "SELECT "+folderCols+" FROM folders WHERE account_id = ? AND server_ref IS NOT NULL AND cursor IS NOT NULL ORDER BY id", accountID)
	if err != nil {
		return false, err
	}
	for _, v := range rows.Values {
		again, err := refreshFolder(ctx, db, c, accountID, scanFolder(v), &r)
		if err != nil {
			return more, err
		}
		more = more || again
	}
	return more, nil
}

func refreshFolder(ctx context.Context, db *store.DB, c *imapclient.Client, accountID int64, f *folder, r *result) (bool, error) {
	validity, _ := imapbackend.Decode(f.cursor)
	rows, err := db.Query(ctx, `SELECT m.id,l.server_ref,m.from_addr FROM message_locations l JOIN messages m ON m.id=l.message_id
		WHERE m.account_id=? AND l.folder_id=? AND l.server_ref BETWEEN ? AND ? AND m.auth_results IS NULL ORDER BY l.server_ref DESC LIMIT 50`,
		accountID, f.id, imapbackend.Encode(validity, 0), imapbackend.Encode(validity, math.MaxUint32))
	if err != nil || len(rows.Values) == 0 {
		return false, err
	}
	sel, err := c.Select(f.name, &imap.SelectOptions{ReadOnly: true}).Wait()
	if no := (*imap.Error)(nil); errors.As(err, &no) {
		return false, nil // refused: the sync reported it
	}
	if err != nil || sel.UIDValidity != validity { // recreated: the next sync resets it
		return false, err
	}
	uids := imap.UIDSet{}
	ids := make(map[imap.UID]int64)
	senders := make(map[imap.UID]string)
	for _, row := range rows.Values {
		_, uid := imapbackend.Decode(store.Str(row[1]))
		uids.AddNum(imap.UID(uid))
		ids[imap.UID(uid)] = store.Int(row[0])
		senders[imap.UID(uid)] = store.Str(row[2])
	}
	section := &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader, HeaderFields: headerFields, Peek: true}
	fetched, err := c.Fetch(uids, &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{section}}).Collect()
	if err != nil {
		return false, err
	}
	// a scanned fraud verdict and a user's classification both outlive late headers
	keep := "(security_status='warning' OR COALESCE(security_reason, '') IN " + mail.ManualSecuritySQL + ")"
	var stmts []sqlite.Statement
	for _, m := range fetched {
		id, ok := ids[m.UID]
		if !ok {
			continue
		}
		p, err := mail.Parse(m.FindBodySection(section))
		if err != nil {
			continue
		}
		result := p.AuthenticationResults
		verifyDKIM := mail.NeedsDKIMVerification(senders[m.UID], result, p.DKIMSigned)
		status, reason := mail.SecurityReportToDB(mail.EvaluateSecurity(senders[m.UID], result))
		if f.role == "sent" || f.role == "drafts" || f.role == "trash" {
			status, reason = "neutral", "unverified"
			verifyDKIM = false
		}
		stmts = append(stmts, sqlite.Statement{SQL: `UPDATE messages SET auth_results=?,
   security_verify_dkim=?,
   security_status=CASE WHEN ` + keep + ` THEN security_status ELSE ? END,
   security_reason=CASE WHEN ` + keep + ` THEN security_reason ELSE ? END,
   security_scanned_at=CASE WHEN ` + keep + ` THEN security_scanned_at ELSE 0 END
   WHERE id=? AND EXISTS (SELECT 1 FROM message_locations WHERE message_id=messages.id AND folder_id=? AND server_ref=?) AND auth_results IS NULL`,
			Args: []any{result, verifyDKIM, status, reason, id, f.id, imapbackend.Encode(validity, uint32(m.UID))}})
		r.changed = append(r.changed, id)
	}
	if len(stmts) > 0 {
		if _, err := db.Transaction(ctx, stmts); err != nil {
			return false, err
		}
	}
	return len(rows.Values) == 50 && len(stmts) > 0, nil
}
