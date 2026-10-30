package mailsync

import (
	"context"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/store"
)

// Upgrade old cached messages in bounded header-only batches. No message body
// or account setting is needed to recover the current receiving service's data.
func refreshAuthenticationResults(ctx context.Context, db *store.DB, c *imapclient.Client, a *account, f *folder, r *result) error {
	rows, err := db.Query(ctx, `SELECT id,server_uid,from_addr FROM messages WHERE account_id=? AND server_folder_id=? AND server_uidvalidity=? AND server_uid IS NOT NULL AND auth_results IS NULL ORDER BY server_uid DESC LIMIT 50`, a.id, f.id, f.uidValidity)
	if err != nil || len(rows.Values) == 0 {
		return err
	}
	uids := imap.UIDSet{}
	ids := make(map[imap.UID]int64)
	senders := make(map[imap.UID]string)
	for _, row := range rows.Values {
		uid := imap.UID(store.Int(row[1]))
		uids.AddNum(uid)
		ids[uid] = store.Int(row[0])
		senders[uid] = store.Str(row[2])
	}
	fetched, err := c.Fetch(uids, &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{headerSection()}}).Collect()
	if err != nil {
		return err
	}
	var stmts []sqlite.Statement
	for _, m := range fetched {
		id, ok := ids[m.UID]
		if !ok {
			continue
		}
		p, err := mail.Parse(m.FindBodySection(headerSection()))
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
   security_status=CASE WHEN security_status='warning' THEN security_status ELSE ? END,
   security_reason=CASE WHEN security_status='warning' THEN security_reason ELSE ? END,
   security_scanned_at=CASE WHEN security_status='warning' THEN security_scanned_at ELSE 0 END
   WHERE id=? AND server_folder_id=? AND server_uidvalidity=? AND server_uid=? AND auth_results IS NULL`, Args: []any{result, verifyDKIM, status, reason, id, f.id, f.uidValidity, uint32(m.UID)}})
		r.changed = append(r.changed, id)
	}
	if len(stmts) > 0 {
		if _, err := db.Transaction(ctx, stmts); err != nil {
			return err
		}
	}
	if len(rows.Values) == 50 && len(stmts) > 0 {
		r.more = true
	}
	return nil
}
