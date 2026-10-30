package store_test

import (
	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"testing"
)

func TestMigrationSecurityTrustInvalidatesOldBadges(t *testing.T) {
	all := store.Migrations
	store.Migrations = all[:len(all)-1]
	defer func() { store.Migrations = all }()
	path := filepath.Join(t.TempDir(), "security.db")
	db, err := store.Open(t.Context(), path)
	require.NoError(t, err)
	_, err = db.Execute(t.Context(), "INSERT INTO accounts(id,email) VALUES (1,'me@example.com')")
	require.NoError(t, err)
	_, err = db.Execute(t.Context(), "INSERT INTO folders(id,account_id,name,role) VALUES (1,1,'INBOX','inbox')")
	require.NoError(t, err)
	for _, tc := range []struct{ status, reason string }{{"certified_brand", "bimi_pass"}, {"verified_domain", "dmarc_pass"}, {"warning", "auth_failed"}, {"warning", "spoofing"}, {"warning", "ai_fraud"}} {
		_, err = db.Execute(t.Context(), "INSERT INTO messages(account_id,folder_id,subject,date,security_status,security_reason,security_scanned_at) VALUES (1,1,'probe',1,?,?,1)", tc.status, tc.reason)
		require.NoError(t, err)
	}
	require.NoError(t, db.Close())
	store.Migrations = all
	db, err = store.Open(t.Context(), path)
	require.NoError(t, err)
	defer db.Close()
	rows, err := db.Query(t.Context(), "SELECT security_status,security_reason,security_scanned_at FROM messages ORDER BY id")
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		require.Nil(t, rows.Values[i][0])
		require.Nil(t, rows.Values[i][1])
		require.Equal(t, int64(0), store.Int(rows.Values[i][2]))
	}
	require.Equal(t, "spoofing", store.Str(rows.Values[3][1]))
	require.Equal(t, "ai_fraud", store.Str(rows.Values[4][1]))
}
