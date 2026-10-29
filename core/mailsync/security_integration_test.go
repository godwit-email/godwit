package mailsync_test

import (
	"path/filepath"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	gomail "github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/require"
)

func TestAutomaticAuthenticationRefreshForCachedMessages(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	acc := e.AddAccount(t, "alice@example.com")
	raw := append([]byte("Authentication-Results: receiver-service.test; dkim=pass header.d=company.example\r\nReceived: from sender.invalid by mx.receiver-service.test; Thu, 29 Oct 2026 00:00:00 +0000\r\n"), coretest.Msg("Alice <a@company.example>", acc.Email, "receiver-check", "hi")...)
	e.Deliver(t, acc.ID, "INBOX", raw)
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	page, err := e.Svc.ListMessages(t.Context(), core.View{Unified: "inbox"}, "", 20)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Equal(t, gomail.SecurityVerifiedDomain, page.Items[0].Security.Status)
	db, err := store.Open(t.Context(), filepath.Join(e.DataDir, "godwit.db"))
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Execute(t.Context(), "UPDATE messages SET auth_results=NULL,security_status=NULL,security_reason=NULL,security_scanned_at=0 WHERE id=?", page.Items[0].ID)
	require.NoError(t, err)
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	m, err := e.Svc.GetMessage(t.Context(), page.Items[0].ID, false)
	require.NoError(t, err)
	require.Equal(t, gomail.SecurityVerifiedDomain, m.Security.Status)
}
