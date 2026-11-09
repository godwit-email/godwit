package mailsync

import (
	"context"
	"errors"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/store"
)

// refreshAuthenticationResults upgrades old cached messages in bounded
// header-only batches, folder by folder. No message body or account setting
// is needed to recover the current receiving service's data. more reports a
// full batch: the next round goes on.
func refreshAuthenticationResults(ctx context.Context, db *store.DB, recv backend.Receiver, accountID int64, bus *events.Bus) (more bool, err error) {
	var r result
	defer r.publish(bus, accountID)
	rows, err := db.Query(ctx, "SELECT "+folderCols+" FROM folders WHERE account_id = ? AND server_ref IS NOT NULL AND cursor IS NOT NULL ORDER BY id", accountID)
	if err != nil {
		return false, err
	}
	for _, v := range rows.Values {
		again, err := refreshFolder(ctx, db, recv, accountID, scanFolder(v), &r)
		if err != nil {
			return more, err
		}
		more = more || again
	}
	return more, nil
}

func refreshFolder(ctx context.Context, db *store.DB, recv backend.Receiver, accountID int64, f *folder, r *result) (bool, error) {
	// Newest first: a backend's refs need not sort by age, IMAP's do within a folder.
	rows, err := db.Query(ctx, `SELECT m.id,l.server_ref,m.from_addr FROM message_locations l JOIN messages m ON m.id=l.message_id
		WHERE m.account_id=? AND l.folder_id=? AND l.server_ref IS NOT NULL AND m.auth_results IS NULL ORDER BY l.server_ref DESC LIMIT 50`,
		accountID, f.id)
	if err != nil || len(rows.Values) == 0 {
		return false, err
	}
	refs := make([]backend.MessageRef, len(rows.Values))
	for i, row := range rows.Values {
		refs[i] = backend.MessageRef(store.Str(row[1]))
	}
	headers, err := recv.Headers(ctx, f.ref, refs, headerFields)
	var refused *backend.FolderError
	if errors.As(err, &refused) {
		return false, nil // the folder's own sync reports the refusal; the others go on
	}
	if err != nil {
		return false, err
	}
	// a scanned fraud verdict and a user's classification both outlive late headers
	keep := "(security_status='warning' OR COALESCE(security_reason, '') IN " + mail.ManualSecuritySQL + ")"
	var stmts []sqlite.Statement
	for i, row := range rows.Values {
		id, ref, sender := store.Int(row[0]), refs[i], store.Str(row[2])
		h, ok := headers[ref]
		if !ok {
			continue // gone, or the folder recreated: the next sync drops it
		}
		p, err := mail.Parse(h)
		if err != nil {
			continue
		}
		result := p.AuthenticationResults
		verifyDKIM := mail.NeedsDKIMVerification(sender, result, p.DKIMSigned)
		status, reason := mail.SecurityReportToDB(mail.EvaluateSecurity(sender, result))
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
			Args: []any{result, verifyDKIM, status, reason, id, f.id, string(ref)}})
		r.changed = append(r.changed, id)
	}
	if len(stmts) > 0 {
		if _, err := db.Transaction(ctx, stmts); err != nil {
			return false, err
		}
	}
	return len(rows.Values) == 50 && len(stmts) > 0, nil
}
