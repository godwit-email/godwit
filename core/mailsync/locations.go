package mailsync

import "github.com/egoist/mygo/plugins/sqlite"

// A message's folders are its rows in message_locations (ADR 0032). On most
// servers a message is in one folder; on Gmail it is in each folder the server
// has it in, every row with its own UID. A move not yet pushed shows it at its
// destination, unconfirmed, and keeps it hidden where the server has it.

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
// (entered_at) and no longer in the folders out; with out nil, nowhere else.
func ShowIn(msg, folder int64, out []int64) []sqlite.Statement {
	hide, args := "UPDATE message_locations SET shown = 0 WHERE message_id = ? AND folder_id != ?", []any{msg, folder}
	if out != nil {
		hide, args = hide+" AND folder_id IN ("+marks(len(out))+")", append(args, anys(out)...)
	}
	return []sqlite.Statement{
		{SQL: hide, Args: args},
		{SQL: `INSERT INTO message_locations(message_id, folder_id, shown, entered_at, date) SELECT id, ?, 1, unixepoch(), date FROM messages WHERE id = ?
			ON CONFLICT(message_id, folder_id) DO UPDATE SET shown = 1, entered_at = excluded.entered_at`, Args: []any{folder, msg}},
		dropUnseen(msg),
	}
}

// settle returns the statements that, once no move of message msg is left to
// push, show it where the server has it and only there (a folder shown again
// restarts its entered_at); with a move still queued, which takes it from
// where the server has it, it shows there only when it shows nowhere. A
// message the server has nowhere keeps showing where it does.
func settle(msg int64) []sqlite.Statement {
	const settled = "NOT EXISTS (SELECT 1 FROM ops WHERE message_id = message_locations.message_id AND kind = 'move' AND state IN ('pending', 'awaiting_claim'))"
	return []sqlite.Statement{
		{SQL: `UPDATE message_locations SET shown = 0 WHERE message_id = ? AND shown AND uid IS NULL
			AND EXISTS (SELECT 1 FROM message_locations c WHERE c.message_id = message_locations.message_id AND c.uid IS NOT NULL) AND ` + settled,
			Args: []any{msg}},
		{SQL: `UPDATE message_locations SET shown = 1, entered_at = unixepoch() WHERE message_id = ? AND uid IS NOT NULL AND NOT shown
			AND (` + settled + ` OR NOT EXISTS (SELECT 1 FROM message_locations c WHERE c.message_id = message_locations.message_id AND c.shown))`,
			Args: []any{msg}},
		dropUnseen(msg),
	}
}

// locate returns the statements that give message msg the server location
// (folder, validity, uid); its other folders keep theirs.
func locate(msg, folder int64, validity, uid uint32) []sqlite.Statement {
	return []sqlite.Statement{
		{SQL: `INSERT INTO message_locations(message_id, folder_id, shown, uidvalidity, uid, entered_at, date)
			SELECT id, ?, 0, ?, ?, unixepoch(), date FROM messages WHERE id = ?
			ON CONFLICT(message_id, folder_id) DO UPDATE SET uidvalidity = excluded.uidvalidity, uid = excluded.uid`, Args: []any{folder, validity, uid, msg}},
		dropUnseen(msg),
	}
}

// unlocate returns the statements that clear message msg's server location in
// folder, which a move took it from.
func unlocate(msg, folder int64) []sqlite.Statement {
	return []sqlite.Statement{
		{SQL: "UPDATE message_locations SET uidvalidity = NULL, uid = NULL WHERE message_id = ? AND folder_id = ?", Args: []any{msg, folder}},
		dropUnseen(msg),
	}
}
