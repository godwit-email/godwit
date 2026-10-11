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

func TestTriggersMaintainFTS(t *testing.T) {
	db := open(t)
	seedMessage(t, db)
	match := func(q string) int {
		rows, err := db.Query(ctx, "SELECT rowid FROM messages_fts WHERE messages_fts MATCH ?", q)
		require.NoError(t, err)
		return len(rows.Values)
	}
	assert.Equal(t, 1, match("alpha"))
	assert.Zero(t, count(t, db, "events_log"), "sync writes message.received itself (migration 7)")

	_, err := db.Execute(ctx, "UPDATE messages SET body_text = 'bravo content' WHERE id = 1")
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

// A version 4 database with an account upgrades with empty provider and status_detail.
func TestMigration5(t *testing.T) {
	path := filepath.Join(t.TempDir(), "godwit.db")
	all := store.Migrations
	store.Migrations = all[:4]
	db, err := store.Open(ctx, path)
	store.Migrations = all
	require.NoError(t, err)
	_, err = db.Execute(ctx, "INSERT INTO accounts(id, email, status) VALUES (1, 'a@b.c', 'reauth_required')")
	require.NoError(t, err)
	db.Close()

	db, err = store.Open(ctx, path)
	require.NoError(t, err)
	defer db.Close()
	rows, err := db.Query(ctx, "PRAGMA user_version")
	require.NoError(t, err)
	assert.Equal(t, int64(len(store.Migrations)), store.Int(rows.Values[0][0]))
	rows, err = db.Query(ctx, "SELECT provider, status_detail, status FROM accounts WHERE id = 1")
	require.NoError(t, err)
	assert.Equal(t, []any{"", "", "reauth_required"}, rows.Values[0])
}

func TestMigration6(t *testing.T) {
	path := filepath.Join(t.TempDir(), "godwit.db")
	all := store.Migrations
	store.Migrations = all[:5]
	db, err := store.Open(ctx, path)
	store.Migrations = all
	require.NoError(t, err)
	_, err = db.Execute(ctx, "INSERT INTO accounts(id, email) VALUES (1, 'a@b.c')")
	require.NoError(t, err)
	_, err = db.Execute(ctx, "INSERT INTO drafts(id, account_id, mode) VALUES (1, 1, 'new')")
	require.NoError(t, err)
	db.Close()

	db, err = store.Open(ctx, path)
	require.NoError(t, err)
	defer db.Close()
	rows, err := db.Query(ctx, "PRAGMA user_version")
	require.NoError(t, err)
	assert.Equal(t, int64(len(store.Migrations)), store.Int(rows.Values[0][0]))
	rows, err = db.Query(ctx, "SELECT revision, ai_approval_id FROM drafts WHERE id = 1")
	require.NoError(t, err)
	assert.Equal(t, int64(1), store.Int(rows.Values[0][0]))
	for _, tbl := range []string{"settings", "model_services", "ai_cache", "conversations", "runs", "approvals", "audit_log"} {
		_, err = db.Query(ctx, "SELECT * FROM "+tbl+" LIMIT 1")
		assert.NoError(t, err, tbl)
	}
	rows, err = db.Query(ctx, "SELECT name FROM sqlite_master WHERE name = 'runs_one_active'")
	require.NoError(t, err)
	assert.Len(t, rows.Values, 1)
}

func TestMigration7(t *testing.T) {
	path := filepath.Join(t.TempDir(), "godwit.db")
	all := store.Migrations
	store.Migrations = all[:6]
	db, err := store.Open(ctx, path)
	store.Migrations = all
	require.NoError(t, err)
	rows, err := db.Query(ctx, "SELECT name FROM sqlite_master WHERE name = 'messages_ai_event'")
	require.NoError(t, err)
	require.Len(t, rows.Values, 1)
	db.Close()

	db, err = store.Open(ctx, path)
	require.NoError(t, err)
	defer db.Close()
	rows, err = db.Query(ctx, "PRAGMA user_version")
	require.NoError(t, err)
	assert.Equal(t, int64(len(store.Migrations)), store.Int(rows.Values[0][0]))
	rows, err = db.Query(ctx, "SELECT name FROM sqlite_master WHERE name = 'messages_ai_event'")
	require.NoError(t, err)
	assert.Empty(t, rows.Values, "the message.received trigger is gone")
	seedMessage(t, db)
	assert.Zero(t, count(t, db, "events_log"))

	for _, q := range []string{
		"SELECT subject, from_addr, msg_date FROM events_log",
		"SELECT name, version, manifest, source_hash, permission_hash, config_version, enabled, granted_hash, failures, disabled_reason, installed_at FROM plugins",
		"SELECT plugin, key, value FROM plugin_config",
		"SELECT plugin, key, value FROM plugin_kv",
		"SELECT plugin, seq FROM plugin_cursors",
		"SELECT id, name, transport, command, args, env_keys, url, header_keys, version, enabled, granted_version, created_at FROM mcp_servers",
		"SELECT server_id, tool, definition_hash FROM mcp_tool_trust",
		"SELECT extension, call_id, destination, reason FROM audit_log",
		"SELECT exec_mode FROM approvals",
	} {
		_, err := db.Query(ctx, q)
		assert.NoError(t, err, q)
	}
	_, err = db.Execute(ctx, "INSERT INTO plugins(name, version, manifest, source_hash, permission_hash) VALUES ('p', '1', '{}', 'h', 'ph')")
	require.NoError(t, err)
	_, err = db.Execute(ctx, "INSERT INTO plugin_kv(plugin, key, value) VALUES ('p', 'k', 'v')")
	require.NoError(t, err)
	rows, err = db.Query(ctx, "SELECT enabled, config_version, failures, disabled_reason, granted_hash FROM plugins")
	require.NoError(t, err)
	assert.Equal(t, []any{int64(0), int64(1), int64(0), "", nil}, rows.Values[0])
	_, err = db.Execute(ctx, "DELETE FROM plugins WHERE name = 'p'")
	require.NoError(t, err)
	assert.Zero(t, count(t, db, "plugin_kv"), "plugin rows cascade")
}

func TestMigration8(t *testing.T) {
	path := filepath.Join(t.TempDir(), "godwit.db")
	all := store.Migrations
	store.Migrations = all[:7]
	db, err := store.Open(ctx, path)
	store.Migrations = all
	require.NoError(t, err)
	_, err = db.Execute(ctx, "INSERT INTO plugins(name, version, manifest, source_hash, permission_hash) VALUES ('p', '1', '{}', 'h', 'ph')")
	require.NoError(t, err)
	db.Close()

	db, err = store.Open(ctx, path)
	require.NoError(t, err)
	defer db.Close()
	rows, err := db.Query(ctx, "SELECT install_dir FROM plugins WHERE name = 'p'")
	require.NoError(t, err)
	assert.Equal(t, "", store.Str(rows.Values[0][0]))
}
