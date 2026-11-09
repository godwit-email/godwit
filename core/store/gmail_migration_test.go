package store_test

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/require"
)

func TestMigrationGmailCapabilityUsesCurrentEndpoint(t *testing.T) {
	all := store.Migrations
	i := slices.IndexFunc(all, func(m []string) bool { return strings.Contains(strings.Join(m, "\n"), "ADD COLUMN imap_gmail") })
	require.Positive(t, i)
	path := filepath.Join(t.TempDir(), "gmail.db")
	store.Migrations = all[:i]
	db, err := store.Open(ctx, path)
	store.Migrations = all
	require.NoError(t, err)
	for i, tc := range []struct {
		host, security string
		port           int
	}{
		{"imap.gmail.com", "tls", 993},
		{"imap.googlemail.com", "tls", 993},
		{"localhost", "tls", 993},
		{"imap.gmail.com.example.org", "tls", 993},
		{"imap.gmail.com", "tls", 1993},
		{"imap.gmail.com", "none", 993},
	} {
		_, err := db.Execute(ctx, `INSERT INTO accounts(email, provider, imap_host, imap_port, imap_security) VALUES (?, 'gmail', ?, ?, ?)`, fmt.Sprintf("legacy-%d@example.com", i), tc.host, tc.port, tc.security)
		require.NoError(t, err)
	}
	_, err = db.Execute(ctx, `INSERT INTO accounts(id, email, imap_host, imap_port, imap_security) VALUES (7, 'changed@example.com', 'localhost', 993, 'tls')`)
	require.NoError(t, err)
	_, err = db.Execute(ctx, `INSERT INTO messages(account_id, gmail_msgid) VALUES (7, '18000000000000000001')`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	db, err = store.Open(ctx, path)
	require.NoError(t, err)
	defer db.Close()
	rows, err := db.Query(ctx, "SELECT features FROM accounts ORDER BY id")
	require.NoError(t, err)
	require.Equal(t, [][]any{{"X-GM-EXT-1"}, {"X-GM-EXT-1"}, {nil}, {nil}, {nil}, {nil}, {nil}}, rows.Values)
}
