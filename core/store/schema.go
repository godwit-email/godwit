package store

// Migrations[i] holds the statements of schema version i+1, one statement each.
var Migrations = [][]string{{
	`CREATE TABLE accounts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		email TEXT NOT NULL UNIQUE,
		name TEXT, alias TEXT, signature TEXT,
		is_default INTEGER DEFAULT 0,
		sync_limit INTEGER DEFAULT 2000,
		auth_type TEXT DEFAULT 'password',
		status TEXT DEFAULT 'ok',
		username TEXT,
		imap_host TEXT, imap_port INTEGER, imap_security TEXT,
		smtp_host TEXT, smtp_port INTEGER, smtp_security TEXT,
		created_at INTEGER)`,
	`CREATE TABLE folders (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
		name TEXT,
		role TEXT NOT NULL DEFAULT '',
		uidvalidity INTEGER,
		last_uid INTEGER DEFAULT 0,
		UNIQUE(account_id, name))`,
	`CREATE TABLE threads (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
		subject TEXT,
		last_date INTEGER)`,
	`CREATE INDEX threads_account_date ON threads(account_id, last_date DESC)`,
	`CREATE TABLE messages (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
		folder_id INTEGER NOT NULL REFERENCES folders(id) ON DELETE CASCADE,
		server_folder_id INTEGER REFERENCES folders(id) ON DELETE SET NULL,
		server_uidvalidity INTEGER,
		server_uid INTEGER,
		thread_id INTEGER REFERENCES threads(id) ON DELETE CASCADE,
		message_id TEXT, in_reply_to TEXT, refs TEXT,
		subject TEXT, from_addr TEXT, to_addrs TEXT, cc_addrs TEXT, reply_to TEXT,
		date INTEGER,
		flags TEXT NOT NULL DEFAULT '',
		size INTEGER,
		snippet TEXT, body_text TEXT, body_html TEXT,
		body_truncated INTEGER DEFAULT 0,
		has_attachments INTEGER,
		UNIQUE(server_folder_id, server_uid))`,
	`CREATE INDEX messages_folder_date ON messages(folder_id, date DESC, id DESC)`,
	`CREATE INDEX messages_thread ON messages(thread_id)`,
	`CREATE INDEX messages_account_mid ON messages(account_id, message_id)`,
	`CREATE TABLE attachments (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
		part TEXT, encoding TEXT, charset TEXT, filename TEXT, content_type TEXT,
		size INTEGER, content_id TEXT, inline INTEGER, cache_path TEXT, last_used INTEGER)`,
	`CREATE TABLE thread_refs (
		account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
		message_id_hdr TEXT NOT NULL,
		thread_id INTEGER NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
		PRIMARY KEY(account_id, message_id_hdr))`,
	`CREATE TABLE events_log (
		seq INTEGER PRIMARY KEY AUTOINCREMENT,
		type TEXT, account_id INTEGER, message_id INTEGER, created_at INTEGER)`,
	`CREATE VIRTUAL TABLE messages_fts USING fts5(subject, from_addr, body, tokenize='trigram')`,
	`CREATE TRIGGER messages_ai AFTER INSERT ON messages BEGIN
		INSERT INTO messages_fts(rowid, subject, from_addr, body)
		VALUES (new.id, new.subject, new.from_addr, new.body_text);
	END`,
	`CREATE TRIGGER messages_ai_event AFTER INSERT ON messages BEGIN
		INSERT INTO events_log(type, account_id, message_id, created_at)
		VALUES ('message.received', new.account_id, new.id, unixepoch());
	END`,
	`CREATE TRIGGER messages_au AFTER UPDATE OF subject, from_addr, body_text ON messages BEGIN
		UPDATE messages_fts SET subject = new.subject, from_addr = new.from_addr, body = new.body_text
		WHERE rowid = old.id;
	END`,
	`CREATE TRIGGER messages_ad AFTER DELETE ON messages BEGIN
		DELETE FROM messages_fts WHERE rowid = old.id;
	END`,
	`CREATE TABLE drafts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
		mode TEXT,
		source_message_id INTEGER REFERENCES messages(id) ON DELETE SET NULL,
		to_addrs TEXT, cc_addrs TEXT, bcc_addrs TEXT, reply_to TEXT,
		subject TEXT, body_html TEXT, body_text TEXT, updated_at INTEGER)`,
	`CREATE TABLE draft_attachments (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		draft_id INTEGER NOT NULL REFERENCES drafts(id) ON DELETE CASCADE,
		filename TEXT, content_type TEXT, size INTEGER, path TEXT)`,
	`CREATE TABLE ops (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
		message_id INTEGER REFERENCES messages(id) ON DELETE SET NULL,
		kind TEXT,
		dst_folder_id INTEGER,
		payload TEXT,
		depends_on INTEGER,
		state TEXT DEFAULT 'pending',
		attempts INTEGER DEFAULT 0,
		last_error TEXT,
		created_at INTEGER)`,
	`CREATE TABLE outbox (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
		message_id_hdr TEXT, raw_path TEXT, rcpts TEXT,
		state TEXT,
		attempts INTEGER DEFAULT 0,
		last_error TEXT,
		created_at INTEGER)`,
}, {
	`ALTER TABLE outbox ADD COLUMN subject TEXT`,
}, {
	`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
}, {
	// 1 while SMTP refuses the password: the outbox pauses (IMAP sync goes on)
	// until an SMTP login on an explicit SyncNow succeeds. status is IMAP's.
	`ALTER TABLE accounts ADD COLUMN smtp_reauth INTEGER NOT NULL DEFAULT 0`,
}, {
	`ALTER TABLE accounts ADD COLUMN provider TEXT NOT NULL DEFAULT ''`,
	// Why status is not ok: credentials_missing, keychain_unavailable or auth_rejected.
	`ALTER TABLE accounts ADD COLUMN status_detail TEXT NOT NULL DEFAULT ''`,
}, {
	`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
	`CREATE TABLE model_services (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE, kind TEXT NOT NULL,
		base_url TEXT NOT NULL, models TEXT NOT NULL DEFAULT '[]', version INTEGER NOT NULL DEFAULT 1, created_at INTEGER)`,
	`CREATE TABLE ai_cache (key TEXT PRIMARY KEY, account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
		output TEXT NOT NULL, created_at INTEGER)`,
	`CREATE TABLE conversations (id INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT NOT NULL,
		draft_id INTEGER REFERENCES drafts(id) ON DELETE CASCADE, title TEXT, model TEXT, state BLOB,
		created_at INTEGER, updated_at INTEGER)`,
	`CREATE TABLE runs (id INTEGER PRIMARY KEY AUTOINCREMENT,
		conversation_id INTEGER NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
		state TEXT NOT NULL, model TEXT NOT NULL, tools TEXT NOT NULL, snapshot BLOB, created_at INTEGER, updated_at INTEGER)`,
	`CREATE UNIQUE INDEX runs_one_active ON runs(conversation_id) WHERE state IN ('running','interrupted','resuming')`,
	`CREATE TABLE approvals (id INTEGER PRIMARY KEY AUTOINCREMENT, run_id INTEGER NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
		seq INTEGER NOT NULL, call_id TEXT NOT NULL, tool TEXT NOT NULL, args TEXT NOT NULL, summary TEXT,
		target_kind TEXT, target_id INTEGER, target_version INTEGER, state TEXT NOT NULL, result TEXT, undo TEXT,
		created_at INTEGER, updated_at INTEGER, UNIQUE(run_id, seq))`,
	`CREATE TABLE audit_log (id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER, source TEXT, tool TEXT,
		target_kind TEXT, target_id INTEGER, decision TEXT)`,
	`ALTER TABLE drafts ADD COLUMN revision INTEGER NOT NULL DEFAULT 1`,
	`ALTER TABLE drafts ADD COLUMN ai_approval_id INTEGER`,
}, {
	`CREATE TABLE plugins (name TEXT PRIMARY KEY, version TEXT NOT NULL, manifest TEXT NOT NULL, source_hash TEXT NOT NULL,
		permission_hash TEXT NOT NULL, config_version INTEGER NOT NULL DEFAULT 1, enabled INTEGER NOT NULL DEFAULT 0, granted_hash TEXT,
		failures INTEGER NOT NULL DEFAULT 0, disabled_reason TEXT NOT NULL DEFAULT '', installed_at INTEGER)`,
	`CREATE TABLE plugin_config (plugin TEXT NOT NULL REFERENCES plugins(name) ON DELETE CASCADE, key TEXT NOT NULL,
		value TEXT NOT NULL, PRIMARY KEY(plugin, key))`,
	`CREATE TABLE plugin_kv (plugin TEXT NOT NULL REFERENCES plugins(name) ON DELETE CASCADE, key TEXT NOT NULL,
		value TEXT NOT NULL, PRIMARY KEY(plugin, key))`,
	// It logged every insert, first syncs and rebuild imports included; sync writes message.received itself.
	`DROP TRIGGER messages_ai_event`,
	`ALTER TABLE events_log ADD COLUMN subject TEXT`,
	`ALTER TABLE events_log ADD COLUMN from_addr TEXT`,
	`ALTER TABLE events_log ADD COLUMN msg_date INTEGER`,
	`CREATE TABLE plugin_cursors (plugin TEXT PRIMARY KEY REFERENCES plugins(name) ON DELETE CASCADE, seq INTEGER NOT NULL)`,
	`CREATE TABLE mcp_servers (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE, transport TEXT NOT NULL,
		command TEXT, args TEXT NOT NULL DEFAULT '[]', env_keys TEXT NOT NULL DEFAULT '[]', url TEXT,
		header_keys TEXT NOT NULL DEFAULT '[]', version INTEGER NOT NULL DEFAULT 1, enabled INTEGER NOT NULL DEFAULT 0,
		granted_version INTEGER NOT NULL DEFAULT 0, created_at INTEGER)`,
	`CREATE TABLE mcp_tool_trust (server_id INTEGER NOT NULL REFERENCES mcp_servers(id) ON DELETE CASCADE, tool TEXT NOT NULL,
		definition_hash TEXT NOT NULL, PRIMARY KEY(server_id, tool))`,
	`ALTER TABLE audit_log ADD COLUMN extension TEXT`,
	`ALTER TABLE audit_log ADD COLUMN call_id TEXT`,
	`ALTER TABLE audit_log ADD COLUMN destination TEXT`,
	`ALTER TABLE audit_log ADD COLUMN reason TEXT`,
	`ALTER TABLE approvals ADD COLUMN exec_mode TEXT NOT NULL DEFAULT 'tx'`, // tx or external
}, {
	// The active version's directory under DataDir/plugins/<name>/ (written once per install).
	`ALTER TABLE plugins ADD COLUMN install_dir TEXT NOT NULL DEFAULT ''`,
	// JSON list of the config keys that may have a keychain entry (deleted by uninstall).
	`ALTER TABLE plugins ADD COLUMN secret_keys TEXT NOT NULL DEFAULT '[]'`,
	// An approval's egress destinations (JSON list of host[:port]), shown on its card.
	`ALTER TABLE approvals ADD COLUMN destinations TEXT NOT NULL DEFAULT '[]'`,
	// What an external approval was bound to when proposed (plugin or MCP versions); a change makes it stale.
	`ALTER TABLE approvals ADD COLUMN binding TEXT NOT NULL DEFAULT ''`,
	// The proposing tool's source and extension, for the audit rows of its decision.
	`ALTER TABLE approvals ADD COLUMN source TEXT NOT NULL DEFAULT 'wit'`,
	`ALTER TABLE approvals ADD COLUMN extension TEXT NOT NULL DEFAULT ''`,
}, {
	// 1 once a first sync of the folder (at its current UIDVALIDITY) completed:
	// from then on what sync imports above last_uid is new mail (message.received).
	`ALTER TABLE folders ADD COLUMN synced INTEGER NOT NULL DEFAULT 0`,
	`UPDATE folders SET synced = 1 WHERE last_uid > 0`,
}, {
	// 1 once mail content entered the conversation (a run's scope read mail):
	// every later run of it starts as having read mail.
	`ALTER TABLE conversations ADD COLUMN read_mail INTEGER NOT NULL DEFAULT 0`,
	// 1 once the plugin was handed mail content: its storage may hold mail, so
	// its action and tool egress needs approval until it is uninstalled.
	`ALTER TABLE plugins ADD COLUMN holds_mail INTEGER NOT NULL DEFAULT 0`,
}, {
	// The mail each run's events cited, in the order they came (Conversation.Citations).
	// call_id is the tool call that showed it; '' for the run's context.
	`CREATE TABLE run_citations (id INTEGER PRIMARY KEY AUTOINCREMENT, run_id INTEGER NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
		call_id TEXT NOT NULL DEFAULT '', message_id INTEGER NOT NULL, subject TEXT NOT NULL, from_addr TEXT NOT NULL, date INTEGER, suspect INTEGER NOT NULL DEFAULT 0)`,
	`CREATE INDEX run_citations_run ON run_citations(run_id)`,
	// The part of the conversation's history the run added: messages [hist_from, hist_to) (Conversation.Runs).
	`ALTER TABLE runs ADD COLUMN hist_from INTEGER`,
	`ALTER TABLE runs ADD COLUMN hist_to INTEGER`,
}}
