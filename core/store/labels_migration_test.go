package store_test

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/require"
)

// Older synced mail waiting for auto-labeling is let go (#567); new mail
// (received, or still waiting for its round) and rescan targets keep waiting.
func TestMigrationLetsGoOlderUnlabeledMail(t *testing.T) {
	all := store.Migrations
	i := slices.IndexFunc(all, func(m []string) bool {
		return strings.Contains(strings.Join(m, "\n"), "SET labeled_at = unixepoch() WHERE labeled_at IS NULL")
	})
	require.Positive(t, i)
	path := filepath.Join(t.TempDir(), "labels.db")
	store.Migrations = all[:i]
	db, err := store.Open(ctx, path)
	store.Migrations = all
	require.NoError(t, err)
	for _, q := range []string{
		`INSERT INTO accounts(id, email) VALUES (1, 'me@x')`,
		`INSERT INTO messages(id, account_id) VALUES (1, 1)`,                  // older mail
		`INSERT INTO messages(id, account_id) VALUES (2, 1)`,                  // received
		`INSERT INTO messages(id, account_id) VALUES (3, 1)`,                  // round not done
		`INSERT INTO messages(id, account_id, labeled_at) VALUES (4, 1, 0)`,   // rescan target
		`INSERT INTO messages(id, account_id, labeled_at) VALUES (5, 1, 123)`, // classified
		`INSERT INTO events_log(type, account_id, message_id, created_at) VALUES ('message.received', 1, 2, 1)`,
		`INSERT INTO message_imports(message_id, arrival) VALUES (3, 1)`,
	} {
		_, err := db.Execute(ctx, q)
		require.NoError(t, err)
	}
	require.NoError(t, db.Close())
	db, err = store.Open(ctx, path)
	require.NoError(t, err)
	defer db.Close()
	rows, err := db.Query(ctx, "SELECT id, labeled_at IS NULL, labeled_at FROM messages WHERE id > 1 ORDER BY id")
	require.NoError(t, err)
	require.Equal(t, [][]any{{int64(2), int64(1), nil}, {int64(3), int64(1), nil}, {int64(4), int64(0), int64(0)}, {int64(5), int64(0), int64(123)}}, rows.Values)
	rows, err = db.Query(ctx, "SELECT labeled_at FROM messages WHERE id = 1")
	require.NoError(t, err)
	require.Positive(t, store.Int(rows.Values[0][0]))
}
