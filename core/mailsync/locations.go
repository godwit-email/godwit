package mailsync

import "github.com/egoist/mygo/plugins/sqlite"

// A message's folders are its rows in message_locations (ADR 0032). For now
// a message shows in exactly one folder and the server has it in at most one:
// a move not yet pushed shows it at its destination, unconfirmed, and keeps it
// hidden where the server has it. These are the only writes that move it.

// serverIn is the condition on messages (unaliased) that the server has the
// message in the folder given as its argument.
const serverIn = "EXISTS (SELECT 1 FROM message_locations WHERE message_id = messages.id AND folder_id = ? AND uid IS NOT NULL)"

// unlocated is the condition on messages (unaliased) that the server has the
// message nowhere Godwit knows: a move without COPYUID awaiting its claim.
const unlocated = "NOT EXISTS (SELECT 1 FROM message_locations WHERE message_id = messages.id AND uid IS NOT NULL)"

// dropUnseen removes the message's rows that neither show it nor locate it on
// the server.
func dropUnseen(msg int64) sqlite.Statement {
	return sqlite.Statement{SQL: "DELETE FROM message_locations WHERE message_id = ? AND shown = 0 AND uid IS NULL", Args: []any{msg}}
}

// ShowIn returns the statements that show message msg in folder from now
// (entered_at) and no longer where it showed.
func ShowIn(msg, folder int64) []sqlite.Statement {
	return []sqlite.Statement{
		{SQL: "UPDATE message_locations SET shown = 0 WHERE message_id = ? AND folder_id != ?", Args: []any{msg, folder}},
		{SQL: `INSERT INTO message_locations(message_id, folder_id, shown, entered_at, date) SELECT id, ?, 1, unixepoch(), date FROM messages WHERE id = ?
			ON CONFLICT(message_id, folder_id) DO UPDATE SET shown = 1, entered_at = excluded.entered_at`, Args: []any{folder, msg}},
		dropUnseen(msg),
	}
}

// locate returns the statements that give message msg the server location
// (folder, validity, uid) in place of the one it had.
func locate(msg, folder int64, validity, uid uint32) []sqlite.Statement {
	return []sqlite.Statement{
		{SQL: "UPDATE message_locations SET uidvalidity = NULL, uid = NULL WHERE message_id = ? AND folder_id != ?", Args: []any{msg, folder}},
		{SQL: `INSERT INTO message_locations(message_id, folder_id, shown, uidvalidity, uid, entered_at, date)
			SELECT id, ?, 0, ?, ?, unixepoch(), date FROM messages WHERE id = ?
			ON CONFLICT(message_id, folder_id) DO UPDATE SET uidvalidity = excluded.uidvalidity, uid = excluded.uid`, Args: []any{folder, validity, uid, msg}},
		dropUnseen(msg),
	}
}

// unlocate returns the statements that clear message msg's server location,
// until a fetch claims it.
func unlocate(msg int64) []sqlite.Statement {
	return []sqlite.Statement{
		{SQL: "UPDATE message_locations SET uidvalidity = NULL, uid = NULL WHERE message_id = ?", Args: []any{msg}},
		dropUnseen(msg),
	}
}
