package core

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/internal/testmail"
	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// toPreBackendSchema rewrites a synced database as the schema before protocol
// backends (ADR 0037) held it: what the migration upgrades.
var toPreBackendSchema = []string{
	`CREATE TABLE ml_old (
		message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
		folder_id INTEGER NOT NULL REFERENCES folders(id) ON DELETE CASCADE,
		shown INTEGER NOT NULL, uidvalidity INTEGER, uid INTEGER, entered_at INTEGER NOT NULL, date INTEGER,
		PRIMARY KEY(message_id, folder_id), UNIQUE(folder_id, uid))`,
	`INSERT INTO ml_old SELECT message_id, folder_id, shown, CAST(substr(server_ref, 1, 10) AS INTEGER), CAST(substr(server_ref, 12) AS INTEGER),
		entered_at, date FROM message_locations`,
	`DROP TABLE message_locations`,
	`ALTER TABLE ml_old RENAME TO message_locations`,
	`CREATE INDEX message_locations_list ON message_locations(folder_id, shown, date DESC, message_id DESC)`,
	`ALTER TABLE folders ADD COLUMN uidvalidity INTEGER`,
	`ALTER TABLE folders ADD COLUMN last_uid INTEGER DEFAULT 0`,
	`UPDATE folders SET uidvalidity = CAST(substr(cursor, 1, 10) AS INTEGER), last_uid = CAST(substr(cursor, 12) AS INTEGER) WHERE cursor IS NOT NULL`,
	`DROP INDEX folders_server_ref`,
	`ALTER TABLE folders DROP COLUMN server_ref`,
	`ALTER TABLE folders DROP COLUMN cursor`,
	`DROP INDEX messages_server_id`,
	`ALTER TABLE messages RENAME COLUMN server_id TO gmail_msgid`,
	`CREATE INDEX messages_gmail ON messages(account_id, gmail_msgid) WHERE gmail_msgid IS NOT NULL`,
	`ALTER TABLE accounts ADD COLUMN imap_gmail INTEGER`,
	`UPDATE accounts SET imap_gmail = (json_extract(features, '$.Identity') = 'stable')`,
	`ALTER TABLE accounts RENAME COLUMN recv_host TO imap_host`,
	`ALTER TABLE accounts RENAME COLUMN recv_port TO imap_port`,
	`ALTER TABLE accounts RENAME COLUMN recv_security TO imap_security`,
	`ALTER TABLE accounts RENAME COLUMN recv_auth TO auth_type`,
	`ALTER TABLE accounts RENAME COLUMN send_host TO smtp_host`,
	`ALTER TABLE accounts RENAME COLUMN send_port TO smtp_port`,
	`ALTER TABLE accounts RENAME COLUMN send_security TO smtp_security`,
	`ALTER TABLE accounts DROP COLUMN recv_protocol`,
	`ALTER TABLE accounts DROP COLUMN send_protocol`,
	`ALTER TABLE accounts DROP COLUMN send_auth`,
	`ALTER TABLE accounts DROP COLUMN cursor`,
	`ALTER TABLE accounts DROP COLUMN features`,
	`ALTER TABLE accounts DROP COLUMN recv_path`,
	`DROP TABLE label_icons`,
	fmt.Sprintf("PRAGMA user_version = %d", len(store.Migrations)-2),
}

// Upgrading a synced account to protocol backends re-imports nothing: its
// first round finds every message where it was, and only new mail arrives.
func TestBackendMigrationFirstSyncImportsNothingAgain(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []testmail.Option
	}{{"imap", nil}, {"gmail", []testmail.Option{testmail.Gmail()}}} {
		t.Run(tc.name, func(t *testing.T) {
			e := newSyncEnv(t, tc.opts...)
			gmail := len(tc.opts) > 0
			for _, id := range []string{"a", "b", "c"} {
				if gmail {
					e.im.AddGmail(msg(id), nil, "INBOX", testmail.GmailAllMail)
				} else {
					e.put(t, "INBOX", msg(id))
				}
			}
			e.sync(t)
			const all = "SELECT id, message_id FROM messages ORDER BY id"
			before := e.rows(t, all)
			require.Len(t, before, 3)
			locations := e.count(t, "SELECT count(*) FROM message_locations")
			received := e.count(t, "SELECT count(*) FROM events_log WHERE type = 'message.received'")
			require.NoError(t, e.s.Close())

			db, err := store.Open(bg, filepath.Join(e.dir, "godwit.db"))
			require.NoError(t, err)
			var stmts []sqlite.Statement
			for _, q := range toPreBackendSchema {
				stmts = append(stmts, sqlite.Statement{SQL: q})
			}
			_, err = db.Transaction(bg, stmts)
			require.NoError(t, err)
			require.NoError(t, db.Close())

			e.s, err = New(bg, Options{DataDir: e.dir, ImageClient: loopbackImageClient()})
			require.NoError(t, err)
			t.Cleanup(func() { e.s.Close() })
			e.sync(t)
			assert.Equal(t, before, e.rows(t, all), "the same messages, none imported again")
			assert.Equal(t, locations, e.count(t, "SELECT count(*) FROM message_locations"))
			assert.Equal(t, received, e.count(t, "SELECT count(*) FROM events_log WHERE type = 'message.received'"))
			for _, r := range before { // each message is still found where the server has it
				raw, err := e.s.GetRawMessage(bg, store.Int(r[0]))
				require.NoError(t, err)
				assert.Contains(t, string(raw), store.Str(r[1]))
			}

			if gmail {
				e.im.AddGmail(msg("d"), nil, "INBOX", testmail.GmailAllMail)
			} else {
				e.put(t, "INBOX", msg("d"))
			}
			e.sync(t)
			assert.Len(t, e.rows(t, all), 4, "new mail still arrives")
			assert.Equal(t, received+1, e.count(t, "SELECT count(*) FROM events_log WHERE type = 'message.received'"))
		})
	}
}
