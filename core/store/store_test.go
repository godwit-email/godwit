package store_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
	rows, _ = db.Query(ctx, "PRAGMA synchronous")
	assert.Equal(t, int64(0), store.Int(rows.Values[0][0]))
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

func TestMigration12(t *testing.T) {
	path := filepath.Join(t.TempDir(), "godwit.db")
	all := store.Migrations
	store.Migrations = all[:11]
	db, err := store.Open(ctx, path)
	store.Migrations = all
	require.NoError(t, err)
	for _, n := range []string{"a", "b", "c"} {
		_, err = db.Execute(ctx, "INSERT INTO model_services(name, kind, base_url) VALUES (?, 'openai', 'https://x')", n)
		require.NoError(t, err)
	}
	db.Close()

	db, err = store.Open(ctx, path)
	require.NoError(t, err)
	defer db.Close()
	rows, err := db.Query(ctx, "SELECT count(*) FROM model_services WHERE slug IS NULL")
	require.NoError(t, err)
	assert.Equal(t, int64(3), store.Int(rows.Values[0][0]), "slugs are backfilled at startup, not by the migration")
	_, err = db.Execute(ctx, "UPDATE model_services SET slug = 'x' WHERE name = 'a'")
	require.NoError(t, err)
	_, err = db.Execute(ctx, "UPDATE model_services SET slug = 'x' WHERE name = 'b'")
	assert.Error(t, err, "slugs are unique")
}

func TestMigration17(t *testing.T) {
	path := filepath.Join(t.TempDir(), "godwit.db")
	all := store.Migrations
	store.Migrations = all[:16]
	db, err := store.Open(ctx, path)
	store.Migrations = all
	require.NoError(t, err)
	for _, sql := range []string{
		"INSERT INTO accounts(email) VALUES ('a@x')",
		"INSERT INTO folders(account_id, name, role) VALUES (1, 'Trash', 'trash')",
		"INSERT INTO messages(account_id, folder_id, date) VALUES (1, 1, 0)",
	} {
		_, err = db.Execute(ctx, sql)
		require.NoError(t, err)
	}
	db.Close()

	db, err = store.Open(ctx, path)
	require.NoError(t, err)
	defer db.Close()
	rows, err := db.Query(ctx, "SELECT m.entered_at, f.role_since, unixepoch() FROM messages m JOIN folders f ON f.id = m.folder_id")
	require.NoError(t, err)
	now := store.Int(rows.Values[0][2])
	assert.InDelta(t, now, store.Int(rows.Values[0][0]), 5, "mail already there counts from the upgrade, never as old")
	assert.InDelta(t, now, store.Int(rows.Values[0][1]), 5)

	_, err = db.Execute(ctx, "UPDATE folders SET role_since = 0")
	require.NoError(t, err)
	_, err = db.Execute(ctx, "UPDATE folders SET role = 'trash'")
	require.NoError(t, err)
	rows, _ = db.Query(ctx, "SELECT role_since FROM folders")
	assert.Zero(t, store.Int(rows.Values[0][0]), "an unchanged role keeps its clock")
	_, err = db.Execute(ctx, "UPDATE folders SET role_override = 'junk'")
	require.NoError(t, err)
	rows, _ = db.Query(ctx, "SELECT role_since FROM folders")
	assert.InDelta(t, now, store.Int(rows.Values[0][0]), 5, "a role change restarts it")
}

func TestMigrationSignatures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "godwit.db")
	all := store.Migrations
	store.Migrations = all[:26]
	db, err := store.Open(ctx, path)
	store.Migrations = all
	require.NoError(t, err)
	for _, sql := range []string{
		"INSERT INTO accounts(id, email, name, alias, signature) VALUES (1, 'a@x', 'Ann', 'Personal', '<!--godwit:html--><p>A</p><img src=\"cid:sig-1@godwit\">')",
		"INSERT INTO accounts(id, email, name, signature) VALUES (2, 'b@x', 'Bob', 'Bob plain')",
		"INSERT INTO accounts(id, email, signature) VALUES (3, 'c@x', '')",
		"INSERT INTO accounts(id, email, alias, name, signature) VALUES (5, 'e@x', '  ', ' Eve ', 'E')",
		"INSERT INTO accounts(id, email, alias, signature) VALUES (6, 'f@x', '" + strings.Repeat("长", 70) + "', 'F')",
		"INSERT INTO accounts(id, email) VALUES (4, 'd@x')",
		"INSERT INTO signature_images(account_id, cid, content_type, data) VALUES (1, 'sig-1@godwit', 'image/png', x'00')",
	} {
		_, err = db.Execute(ctx, sql)
		require.NoError(t, err)
	}
	db.Close()

	db, err = store.Open(ctx, path)
	require.NoError(t, err)
	defer db.Close()
	rows, err := db.Query(ctx, "SELECT id, name, html FROM signatures ORDER BY id")
	require.NoError(t, err)
	assert.Equal(t, [][]any{
		{int64(1), "Personal", `<!--godwit:html--><p>A</p><img src="cid:sig-1@godwit">`},
		{int64(2), "Bob", "Bob plain"},
		{int64(5), "Eve", "E"},
		{int64(6), strings.Repeat("长", 64), "F"},
	}, rows.Values, "named by alias, else name (trimmed, at most 64 characters); an empty signature makes none")
	rows, err = db.Query(ctx, "SELECT id, signature_id FROM accounts ORDER BY id")
	require.NoError(t, err)
	assert.Equal(t, [][]any{{int64(1), int64(1)}, {int64(2), int64(2)}, {int64(3), nil}, {int64(4), nil}, {int64(5), int64(5)}, {int64(6), int64(6)}}, rows.Values, "each its own, by default")
	rows, err = db.Query(ctx, "SELECT signature_id, cid FROM signature_images")
	require.NoError(t, err)
	assert.Equal(t, [][]any{{int64(1), "sig-1@godwit"}}, rows.Values)
	_, err = db.Execute(ctx, "INSERT INTO signatures(name) VALUES ('new')")
	require.NoError(t, err)
	rows, _ = db.Query(ctx, "SELECT max(id) FROM signatures")
	assert.Equal(t, int64(7), store.Int(rows.Values[0][0]), "new ids follow the migrated ones")
}
