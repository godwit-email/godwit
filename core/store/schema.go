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
}, {
	// The service's reference name in "slug/model" refs; it never changes on rename.
	// Rows from before are filled in at startup (Service.backfillSlugs). NULLs do not collide.
	`ALTER TABLE model_services ADD COLUMN slug TEXT`,
	`CREATE UNIQUE INDEX model_services_slug ON model_services(slug)`,
}, {
	// The names services had when slugs came (Service.backfillSlugs): skill references
	// in the "name/model" form keep resolving to that service, and only to it.
	`CREATE TABLE model_service_aliases (name TEXT PRIMARY KEY, service_id INTEGER NOT NULL REFERENCES model_services(id) ON DELETE CASCADE)`,
	// The keychain entry holding the service's key: 0 none, 1 the unversioned entry
	// older Godwits wrote, n the entry "…:v<n>". A new key goes to a new entry first,
	// then one UPDATE switches address and entry together (UpdateModelService).
	`ALTER TABLE model_services ADD COLUMN key_version INTEGER NOT NULL DEFAULT 1`,
	// Key entries that may exist but no row uses (any more): deleted at once, and again
	// at startup when that failed or the process stopped first. No foreign key: a
	// removed service's entry stays listed until it is gone.
	`CREATE TABLE model_key_trash (service_id INTEGER NOT NULL, key_version INTEGER NOT NULL, PRIMARY KEY(service_id, key_version))`,
}, {
	// Citation.Quote: a suspect message's text from where the suspicion starts.
	`ALTER TABLE run_citations ADD COLUMN quote TEXT NOT NULL DEFAULT ''`,
}, {
	// Model capabilities: kind stays the generate format ('' = generate off). provider is
	// the add dialog's tile, inferred for older rows from the address; judge starts off.
	`ALTER TABLE model_services ADD COLUMN provider TEXT NOT NULL DEFAULT 'other'`,
	`UPDATE model_services SET provider = 'openai' WHERE kind IN ('openai', 'openai-responses')
		AND (base_url = 'https://api.openai.com' OR base_url LIKE 'https://api.openai.com/%')`,
	`UPDATE model_services SET provider = 'anthropic' WHERE kind = 'anthropic'
		AND (base_url = 'https://api.anthropic.com' OR base_url LIKE 'https://api.anthropic.com/%')`,
	`ALTER TABLE model_services ADD COLUMN judge_format TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE model_services ADD COLUMN judge_model TEXT NOT NULL DEFAULT ''`,
	// The last test of each capability ('gen', 'judge') as codes, never error text. Kept
	// apart from model_services so writing it never bumps a service's version.
	`CREATE TABLE model_service_status (service_id INTEGER NOT NULL REFERENCES model_services(id) ON DELETE CASCADE,
		cap TEXT NOT NULL, state TEXT NOT NULL, latency_ms INTEGER NOT NULL DEFAULT 0, http_status INTEGER NOT NULL DEFAULT 0,
		code TEXT NOT NULL DEFAULT '', at INTEGER NOT NULL, PRIMARY KEY(service_id, cap))`,
}, {
	// With sync_limit 0, the months a sync fetches; both 0 is all mail.
	`ALTER TABLE accounts ADD COLUMN sync_since_months INTEGER NOT NULL DEFAULT 0`,
	// The SINCE cutoff (unix; 0 for all) a range backfill last completed down to; NULL for none.
	`ALTER TABLE folders ADD COLUMN filled_since INTEGER`,
}, {
	// When Godwit first saw the message in its folder (unix): what a purge measures,
	// never the Date header. Every write of folder_id sets it; existing rows start now.
	`ALTER TABLE messages ADD COLUMN entered_at INTEGER NOT NULL DEFAULT 0`,
	`UPDATE messages SET entered_at = unixepoch()`,
	// The user's role for the folder (drafts, sent, junk, trash, archive), or 'none'
	// for no role; '' follows detection (role). Sync never writes it.
	`ALTER TABLE folders ADD COLUMN role_override TEXT NOT NULL DEFAULT ''`,
	// When role or role_override last changed: a purge counts from it too.
	`ALTER TABLE folders ADD COLUMN role_since INTEGER NOT NULL DEFAULT 0`,
	`UPDATE folders SET role_since = unixepoch()`,
	`CREATE TRIGGER folders_role_since AFTER UPDATE OF role, role_override ON folders
		WHEN old.role IS NOT new.role OR old.role_override IS NOT new.role_override BEGIN
		UPDATE folders SET role_since = unixepoch() WHERE id = new.id;
	END`,
	// When a purge last ran on the folder: at most once a day.
	`ALTER TABLE folders ADD COLUMN purged_at INTEGER NOT NULL DEFAULT 0`,
	// The junk and trash purges: days 0 is never. since starts the clock (turning it on,
	// or the role's folder changing); folder_id is the folder the clock runs for.
	// blocked is 1 while the server lacks UIDPLUS, so the purge is skipped.
	`CREATE TABLE purge_policies (account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
		role TEXT NOT NULL, days INTEGER NOT NULL DEFAULT 0, since INTEGER NOT NULL, folder_id INTEGER,
		blocked INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(account_id, role))`,
}, {
	// Signature images, content-addressed per account: cid is sig-<hash>@godwit,
	// as the signature and drafts refer to it (cid:<cid>).
	`CREATE TABLE signature_images (account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
		cid TEXT NOT NULL, content_type TEXT NOT NULL, data BLOB NOT NULL, PRIMARY KEY(account_id, cid))`,
}, {
	// Mail labels (ADR 0014): slug never changes, so a later sync can use it as
	// an IMAP keyword. A message_labels row set by hand (source manual) wins over the
	// classifier; active 0 is a label the user took off. labeled_at: NULL new mail
	// waiting for the classifier, 0 a rescan's target, else when it was classified.
	`CREATE TABLE labels (id INTEGER PRIMARY KEY AUTOINCREMENT, slug TEXT NOT NULL UNIQUE, name TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '', color TEXT NOT NULL, icon TEXT NOT NULL,
		builtin INTEGER NOT NULL DEFAULT 0, position INTEGER NOT NULL)`,
	`CREATE TABLE message_labels (message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
		label_id INTEGER NOT NULL REFERENCES labels(id) ON DELETE CASCADE,
		source TEXT NOT NULL, active INTEGER NOT NULL, PRIMARY KEY(message_id, label_id))`,
	`CREATE INDEX message_labels_label ON message_labels(label_id, active)`,
	`ALTER TABLE messages ADD COLUMN labeled_at INTEGER`,
	`CREATE INDEX messages_labeled ON messages(folder_id, labeled_at)`,
}, {
	// Labels set by hand outlive the cached rows they are on (a UIDVALIDITY reset,
	// a move the server did not report): a row dropped stashes them by account
	// and label_fp, and hands them to the other copies; a copy inserted later
	// takes the stash back. SetLabel keeps the stash at the user's last choice.
	// label_fp is a SHA-256 the import computes (of the Message-ID, else the
	// headers and the whole text), so the stash holds no mail content.
	`ALTER TABLE messages ADD COLUMN label_fp TEXT`,
	`CREATE INDEX messages_label_fp ON messages(account_id, label_fp)`,
	`CREATE TABLE label_keep (account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE, fp TEXT NOT NULL,
		label_id INTEGER NOT NULL REFERENCES labels(id) ON DELETE CASCADE, active INTEGER NOT NULL, PRIMARY KEY(account_id, fp, label_id))`,
	`CREATE TRIGGER messages_label_stash BEFORE DELETE ON messages
		WHEN old.label_fp IS NOT NULL AND EXISTS (SELECT 1 FROM message_labels WHERE message_id = old.id AND source = 'manual')
			AND EXISTS (SELECT 1 FROM accounts WHERE id = old.account_id) BEGIN
		INSERT OR REPLACE INTO label_keep(account_id, fp, label_id, active)
			SELECT old.account_id, old.label_fp, label_id, active FROM message_labels WHERE message_id = old.id AND source = 'manual';
		INSERT OR REPLACE INTO message_labels(message_id, label_id, source, active)
			SELECT m.id, ml.label_id, 'manual', ml.active FROM messages m JOIN message_labels ml ON ml.message_id = old.id AND ml.source = 'manual'
			WHERE m.account_id = old.account_id AND m.label_fp = old.label_fp AND m.id != old.id;
	END`,
	`CREATE TRIGGER messages_label_keep AFTER INSERT ON messages
		WHEN new.label_fp IS NOT NULL AND EXISTS (SELECT 1 FROM label_keep WHERE account_id = new.account_id AND fp = new.label_fp) BEGIN
		INSERT OR REPLACE INTO message_labels(message_id, label_id, source, active)
			SELECT new.id, label_id, 'manual', active FROM label_keep WHERE account_id = new.account_id AND fp = new.label_fp;
	END`,
}, {
	// Subscription model services (ChatGPT, Grok) signed in with OAuth:
	// oauth_email is shown in the UI and sent as login_hint on re-login;
	// oauth_client_id is the dynamic client_id ChatGPT issued at sign-in.
	`ALTER TABLE model_services ADD COLUMN oauth_email TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE model_services ADD COLUMN oauth_client_id TEXT NOT NULL DEFAULT ''`,
}, {
	// Native plugins (docs/adr/0016-signed-plugin-registry.md):
	// trust is how the current file came to be allowed ('' not allowed, 'user'
	// the user allowed it); library_hash is the SHA-256 of its shared library.
	`ALTER TABLE plugins ADD COLUMN trust TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE plugins ADD COLUMN library_hash TEXT NOT NULL DEFAULT ''`,
}, {
	// The hierarchy delimiter LIST gives a folder's name ('' when flat), which
	// the sidebar splits user folders on into a tree.
	`ALTER TABLE folders ADD COLUMN delim TEXT NOT NULL DEFAULT ''`,
}, {
	// Registry-hosted assets (docs/adr/0020-plugin-models-host-sdk.md):
	// the digest of the installed release's asset descriptions on this
	// platform ('' none). A registry that changes any of them revokes the plugin.
	`ALTER TABLE plugins ADD COLUMN assets_digest TEXT NOT NULL DEFAULT ''`,
}, {
	// The tokens a run's model calls took (summed over its resumes), and the
	// input of its last call: how much history the model was last shown.
	`ALTER TABLE runs ADD COLUMN input_tokens INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE runs ADD COLUMN output_tokens INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE runs ADD COLUMN context_tokens INTEGER NOT NULL DEFAULT 0`,
	// The tokens of the runs a rewind dropped (RewindConversation): the
	// conversation took them all the same.
	`ALTER TABLE conversations ADD COLUMN spent_input INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE conversations ADD COLUMN spent_output INTEGER NOT NULL DEFAULT 0`,
}, {
	// The reasoning effort a conversation's runs ask for: '' asks for none.
	`ALTER TABLE conversations ADD COLUMN effort TEXT NOT NULL DEFAULT ''`,
}, {
	// Signatures accounts share (one each by default, signature_id). An
	// account's signature becomes one of its own, named as the sidebar names
	// the account, with the account's id. html is as accounts.signature was:
	// marked editor HTML, or plain text.
	`CREATE TABLE signatures (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, html TEXT NOT NULL DEFAULT '')`,
	// Named as core.accountLabel names one: trimmed, the first not empty, at
	// most 64 characters (core.MaxSignatureName).
	`INSERT INTO signatures(id, name, html) SELECT id, substr(COALESCE(NULLIF(trim(alias, ' '||char(9, 10, 13)), ''),
		NULLIF(trim(name, ' '||char(9, 10, 13)), ''), trim(email)), 1, 64), signature
		FROM accounts WHERE COALESCE(signature, '') <> ''`,
	`ALTER TABLE accounts ADD COLUMN signature_id INTEGER REFERENCES signatures(id) ON DELETE SET NULL`,
	`UPDATE accounts SET signature_id = id WHERE id IN (SELECT id FROM signatures)`,
	`ALTER TABLE accounts DROP COLUMN signature`,
	// Signature images belong to a signature now (by id; no foreign key: an
	// image a draft shows outlives its signature). The images move with their
	// account's id, and go once neither that signature nor a draft shows them.
	`CREATE TABLE signature_images_by_sig (signature_id INTEGER NOT NULL, cid TEXT NOT NULL, content_type TEXT NOT NULL,
		data BLOB NOT NULL, PRIMARY KEY(signature_id, cid))`,
	`INSERT INTO signature_images_by_sig SELECT account_id, cid, content_type, data FROM signature_images`,
	`DROP TABLE signature_images`,
	`ALTER TABLE signature_images_by_sig RENAME TO signature_images`,
}, {
	// An account's icon: '' its provider's mark, 'label:<name>' (core.LabelIcons),
	// 'brand:<provider id>', or 'image:<n>' for its account_icons row, n bumped per upload.
	`ALTER TABLE accounts ADD COLUMN icon TEXT NOT NULL DEFAULT ''`,
	`CREATE TABLE account_icons (account_id INTEGER PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
		content_type TEXT NOT NULL, data BLOB NOT NULL)`,
}, {
	// The accounts' order in the sidebars, set by dragging; ties (every account
	// before this) go by id, the creation order.
	`ALTER TABLE accounts ADD COLUMN position INTEGER NOT NULL DEFAULT 0`,
}, {
	// A conversation's compaction (core.compactLater): summary stands in for
	// its state's messages after the first through summary_upto. A rewind
	// clears it and bumps summary_epoch, so one begun before is not saved.
	`ALTER TABLE conversations ADD COLUMN summary TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE conversations ADD COLUMN summary_upto INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE conversations ADD COLUMN summary_epoch INTEGER NOT NULL DEFAULT 0`,
}, {
	// How long a run worked, in milliseconds: each of its drives adds its
	// time (core.drive), not the time waiting on the user's decisions.
	`ALTER TABLE runs ADD COLUMN active_ms INTEGER NOT NULL DEFAULT 0`,
}, {
	// A conversation's messages one row each (ADR 0021), by seq: a seq is
	// never reused, next_seq is the next one. core.backfillHistories moves
	// conversations.state, the old one-array history, into it; a state it
	// cannot read stays for the next start to try again.
	`CREATE TABLE conversation_messages (conversation_id INTEGER NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
		seq INTEGER NOT NULL, data BLOB NOT NULL, PRIMARY KEY (conversation_id, seq))`,
	`ALTER TABLE conversations ADD COLUMN next_seq INTEGER NOT NULL DEFAULT 1`,
	// A question asked again (core.AskAgain) hides the messages [rewind_from,
	// rewind_to) while its run is active and drops them once it is done; a
	// run that ends otherwise drops what it added instead. rewind_summary: the
	// summary goes with them.
	`ALTER TABLE runs ADD COLUMN rewind_from INTEGER`,
	`ALTER TABLE runs ADD COLUMN rewind_to INTEGER`,
	`ALTER TABLE runs ADD COLUMN rewind_summary INTEGER NOT NULL DEFAULT 0`,
	// What a citation with no call is: mail the user quoted, or an assist
	// run's context ('' for a tool call's).
	`ALTER TABLE run_citations ADD COLUMN source TEXT NOT NULL DEFAULT ''`,
	`UPDATE run_citations SET source = CASE WHEN (SELECT c.kind FROM runs r JOIN conversations c ON c.id = r.conversation_id
		WHERE r.id = run_citations.run_id) = 'assist' THEN 'context' ELSE 'quote' END WHERE call_id = ''`,
	// A resumed assist run saved its context's citations again.
	`DELETE FROM run_citations WHERE call_id = '' AND id NOT IN (SELECT MIN(id) FROM run_citations WHERE call_id = '' GROUP BY run_id, message_id)`,
}}

// HistoryRowsVersion is the schema version that keeps a conversation's
// messages one row each; Open backs the database up before migrating to it.
const HistoryRowsVersion = 32
