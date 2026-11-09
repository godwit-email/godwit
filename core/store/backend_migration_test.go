package store_test

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/require"
)

// The protocol backend migration (ADR 0037) encodes IMAP's server locations,
// cursors, Gmail identity and account servers into the generic columns.
func TestMigrationEncodesIMAPDataForProtocolBackends(t *testing.T) {
	all := store.Migrations
	i := slices.IndexFunc(all, func(m []string) bool { return strings.Contains(strings.Join(m, "\n"), "ADD COLUMN server_ref") })
	require.Positive(t, i)
	path := filepath.Join(t.TempDir(), "backends.db")
	store.Migrations = all[:i]
	db, err := store.Open(ctx, path)
	store.Migrations = all
	require.NoError(t, err)
	for _, q := range []string{
		`INSERT INTO accounts(id, email, auth_type, username, imap_host, imap_port, imap_security, smtp_host, smtp_port, smtp_security, imap_gmail)
			VALUES (1, 'gmail@x', 'oauth', 'me', 'imap.gmail.com', 993, 'tls', 'smtp.gmail.com', 465, 'tls', 1)`,
		`INSERT INTO accounts(id, email, imap_host, imap_port, imap_security, smtp_host, smtp_port, smtp_security, imap_gmail)
			VALUES (2, 'plain@x', 'imap.x', 143, 'starttls', 'smtp.x', 587, 'starttls', 0)`,
		`INSERT INTO accounts(id, email, imap_host, imap_port, imap_security) VALUES (3, 'old@x', 'imap.y', 993, 'tls')`,
		`INSERT INTO accounts(id, email, auth_type, imap_gmail, kind) VALUES (4, 'archive@local.invalid', 'none', 0, 'local_archive')`,
		`INSERT INTO folders(id, account_id, name, uidvalidity, last_uid) VALUES (1, 1, 'INBOX', 7, 42)`,
		`INSERT INTO folders(id, account_id, name) VALUES (2, 1, '[Gmail]/Sent Mail')`,
		`INSERT INTO folders(id, account_id, name, uidvalidity, last_uid) VALUES (3, 2, 'INBOX', 4294967295, 4294967295)`,
		`INSERT INTO folders(id, account_id, name) VALUES (4, 4, 'INBOX')`,
		`INSERT INTO messages(id, account_id, gmail_msgid) VALUES (1, 1, '18000000000000000001')`,
		`INSERT INTO messages(id, account_id) VALUES (2, 2)`,
		`INSERT INTO message_locations(message_id, folder_id, shown, uidvalidity, uid, entered_at) VALUES (1, 1, 1, 7, 42, 1)`,
		`INSERT INTO message_locations(message_id, folder_id, shown, entered_at) VALUES (1, 2, 1, 1)`, // a move not yet pushed
		`INSERT INTO message_locations(message_id, folder_id, shown, uidvalidity, uid, entered_at) VALUES (2, 3, 1, 4294967295, 9, 1)`,
	} {
		_, err := db.Execute(ctx, q)
		require.NoError(t, err, q)
	}
	require.NoError(t, db.Close())
	db, err = store.Open(ctx, path)
	require.NoError(t, err)
	defer db.Close()

	rows, err := db.Query(ctx, `SELECT recv_protocol, recv_host, recv_port, recv_security, recv_auth,
		send_protocol, send_host, send_port, send_security, send_auth, features, cursor FROM accounts ORDER BY id`)
	require.NoError(t, err)
	require.Equal(t, [][]any{
		{"imap", "imap.gmail.com", int64(993), "tls", "oauth", "smtp", "smtp.gmail.com", int64(465), "tls", "oauth", "X-GM-EXT-1", nil},
		{"imap", "imap.x", int64(143), "starttls", "password", "smtp", "smtp.x", int64(587), "starttls", "password", "", nil},
		{"imap", "imap.y", int64(993), "tls", "password", "smtp", nil, nil, nil, "password", nil, nil}, // capabilities never seen
		{"", nil, nil, nil, "none", "", nil, nil, nil, "none", "", nil},
	}, rows.Values)

	rows, err = db.Query(ctx, "SELECT server_ref, cursor FROM folders ORDER BY id")
	require.NoError(t, err)
	require.Equal(t, [][]any{
		{"INBOX", "0000000007:0000000042"},
		{"[Gmail]/Sent Mail", nil},
		{"INBOX", "4294967295:4294967295"},
		{nil, nil}, // a local archive's folder is on no server
	}, rows.Values)

	rows, err = db.Query(ctx, "SELECT server_id FROM messages ORDER BY id")
	require.NoError(t, err)
	require.Equal(t, [][]any{{"18000000000000000001"}, {nil}}, rows.Values)

	rows, err = db.Query(ctx, "SELECT message_id, folder_id, shown, server_ref, entered_at FROM message_locations ORDER BY message_id, folder_id")
	require.NoError(t, err)
	require.Equal(t, [][]any{
		{int64(1), int64(1), int64(1), "0000000007:0000000042", int64(1)},
		{int64(1), int64(2), int64(1), nil, int64(1)},
		{int64(2), int64(3), int64(1), "4294967295:0000000009", int64(1)},
	}, rows.Values)

	// A server location and a folder's server reference are each unique.
	_, err = db.Execute(ctx, `INSERT INTO message_locations(message_id, folder_id, shown, server_ref, entered_at) VALUES (2, 1, 1, '0000000007:0000000042', 1)`)
	require.Error(t, err)
	_, err = db.Execute(ctx, `INSERT INTO folders(account_id, name, server_ref) VALUES (1, 'other', 'INBOX')`)
	require.Error(t, err)
	for _, q := range []string{"SELECT uid FROM message_locations", "SELECT last_uid FROM folders", "SELECT gmail_msgid FROM messages", "SELECT imap_gmail FROM accounts"} {
		_, err = db.Query(ctx, q)
		require.Error(t, err, q)
	}
}
