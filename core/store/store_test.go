package store_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var ctx = context.Background()

func open(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "godwit.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return db
}

func count(t *testing.T, db *store.DB, table string) int64 {
	t.Helper()
	rows, err := db.Query(ctx, "SELECT count(*) FROM "+table)
	require.NoError(t, err)
	return store.Int(rows.Values[0][0])
}

func seedMessage(t *testing.T, db *store.DB) {
	t.Helper()
	for _, s := range []string{
		"INSERT INTO accounts(id, email) VALUES (1, 'a@b.c')",
		"INSERT INTO folders(id, account_id, name) VALUES (1, 1, 'INBOX')",
		"INSERT INTO messages(id, account_id, folder_id, subject, from_addr, body_text) VALUES (1, 1, 1, 'hello', 'x@y.z', 'alpha content')",
	} {
		_, err := db.Execute(ctx, s)
		require.NoError(t, err)
	}
}

func TestOpenMigrates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "godwit.db")
	db, err := store.Open(ctx, path)
	require.NoError(t, err)
	rows, _ := db.Query(ctx, "PRAGMA user_version")
	assert.Equal(t, int64(len(store.Migrations)), store.Int(rows.Values[0][0]))
	rows, _ = db.Query(ctx, "PRAGMA foreign_keys")
	assert.Equal(t, int64(1), store.Int(rows.Values[0][0]))
	db.Close()
	db, err = store.Open(ctx, path) // 重新打开不重复迁移
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Execute(ctx, "INSERT INTO messages_fts(rowid, subject, from_addr, body) VALUES (1, '通知', 'a@b.c', '请查收本月报销单')")
	require.NoError(t, err)
	rows, _ = db.Query(ctx, "SELECT rowid FROM messages_fts WHERE messages_fts MATCH '报销单'")
	assert.Len(t, rows.Values, 1)
}

func TestRefuseNewerDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "godwit.db")
	db, err := store.Open(ctx, path)
	require.NoError(t, err)
	_, err = db.Execute(ctx, "PRAGMA user_version = 999")
	require.NoError(t, err)
	db.Close()
	before, err := os.Stat(path)
	require.NoError(t, err)
	time.Sleep(20 * time.Millisecond)

	_, err = store.Open(ctx, path)
	assert.ErrorIs(t, err, store.ErrNewerDatabase)
	after, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, before.Size(), after.Size())
	assert.Equal(t, before.ModTime(), after.ModTime())
}

func TestForeignKeysCascade(t *testing.T) {
	db := open(t)
	seedMessage(t, db)
	_, err := db.Execute(ctx, "INSERT INTO attachments(message_id, part, filename) VALUES (1, '2', 'f.txt')")
	require.NoError(t, err)
	assert.Equal(t, int64(1), count(t, db, "messages_fts"))

	_, err = db.Execute(ctx, "DELETE FROM accounts WHERE id = 1")
	require.NoError(t, err)
	for _, tb := range []string{"accounts", "folders", "messages", "attachments", "messages_fts"} {
		assert.Zero(t, count(t, db, tb), tb)
	}
}

func TestTriggersMaintainFTSAndEventLog(t *testing.T) {
	db := open(t)
	seedMessage(t, db)
	match := func(q string) int {
		rows, err := db.Query(ctx, "SELECT rowid FROM messages_fts WHERE messages_fts MATCH ?", q)
		require.NoError(t, err)
		return len(rows.Values)
	}
	assert.Equal(t, 1, match("alpha"))
	rows, err := db.Query(ctx, "SELECT type, account_id, message_id FROM events_log")
	require.NoError(t, err)
	require.Len(t, rows.Values, 1)
	assert.Equal(t, "message.received", store.Str(rows.Values[0][0]))
	assert.Equal(t, int64(1), store.Int(rows.Values[0][2]))

	_, err = db.Execute(ctx, "UPDATE messages SET body_text = 'bravo content' WHERE id = 1")
	require.NoError(t, err)
	assert.Equal(t, 1, match("bravo"))
	assert.Equal(t, 0, match("alpha"))

	_, err = db.Execute(ctx, "DELETE FROM messages WHERE id = 1")
	require.NoError(t, err)
	assert.Zero(t, count(t, db, "messages_fts"))
}

func TestIDsNeverReused(t *testing.T) {
	db := open(t)
	seedMessage(t, db)
	_, err := db.Execute(ctx, "DELETE FROM messages WHERE id = 1")
	require.NoError(t, err)
	res, err := db.Execute(ctx, "INSERT INTO messages(account_id, folder_id) VALUES (1, 1)")
	require.NoError(t, err)
	assert.Greater(t, res.LastInsertID, int64(1), "a deleted message's id must never point at another message")
}
