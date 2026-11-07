package core

import (
	"testing"

	"github.com/godwit-email/godwit/core/internal/testmail"
	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// oldRows turns message id back into what a Gmail sync stored before #521:
// a row per folder, none with an X-GM-MSGID. id stays the row of the folder
// the old display preferred (inbox, else another, else All Mail); the map
// gives each folder's row.
func (e *syncEnv) oldRows(t *testing.T, id int64) map[string]int64 {
	t.Helper()
	_, err := e.s.db.Execute(bg, "UPDATE messages SET gmail_msgid = NULL WHERE id = ?", id)
	require.NoError(t, err)
	out := map[string]int64{}
	for i, r := range e.rows(t, `SELECT l.folder_id, f.name FROM message_locations l JOIN folders f ON f.id = l.folder_id
		WHERE l.message_id = ? ORDER BY CASE f.role WHEN 'inbox' THEN 0 WHEN 'all' THEN 2 ELSE 1 END, f.id`, id) {
		if i == 0 {
			out[store.Str(r[1])] = id
			continue
		}
		res, err := e.s.db.Execute(bg, `INSERT INTO messages(account_id, thread_id, message_id, subject, from_addr, to_addrs, date, flags, size, snippet, body_text, label_fp)
			SELECT account_id, thread_id, message_id, subject, from_addr, to_addrs, date, flags, size, snippet, body_text, label_fp FROM messages WHERE id = ?`, id)
		require.NoError(t, err)
		_, err = e.s.db.Execute(bg, "UPDATE message_locations SET message_id = ? WHERE message_id = ? AND folder_id = ?", res.LastInsertID, id, r[0])
		require.NoError(t, err)
		out[store.Str(r[1])] = res.LastInsertID
	}
	return out
}

// An upgrade with an archive queued on the INBOX row: the first sync applies
// it and leaves one message, in All Mail and its label.
func TestGmailUpgradeWithQueuedArchive(t *testing.T) {
	e := newSyncEnv(t, testmail.Gmail())
	require.NoError(t, e.im.User.Create("Work", nil))
	m := e.im.AddGmail(msg("a@x"), nil, "INBOX", "Work", testmail.GmailAllMail)
	e.sync(t)
	rows := e.oldRows(t, e.idOf(t, "a@x"))
	require.Len(t, rows, 3)

	require.NoError(t, e.s.MoveToRole(bg, []int64{rows["INBOX"]}, "archive"))
	e.sync(t)
	assert.Equal(t, []string{"done"}, e.opStates(t))
	assert.Equal(t, []string{"Work", testmail.GmailAllMail}, m.Folders())
	all := e.listed(t, testmail.GmailAllMail)
	require.Len(t, all, 1)
	assert.Equal(t, all, e.listed(t, "Work"))
	assert.Empty(t, e.listed(t, "INBOX"))
}

func TestGmailCOPYUIDMergeKeepsOtherRowsQueuedFlag(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []testmail.Option
	}{
		{"MOVE", nil}, {"labels", []testmail.Option{testmail.WithoutMove()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newSyncEnv(t, append([]testmail.Option{testmail.Gmail()}, tc.opts...)...)
			e.im.AddGmail(msg("a@x"), nil, "INBOX", testmail.GmailAllMail)
			e.sync(t)
			rows := e.oldRows(t, e.idOf(t, "a@x"))
			require.NoError(t, e.s.MoveToRole(bg, []int64{rows["INBOX"]}, "archive"))
			require.NoError(t, e.s.SetFlags(bg, []int64{rows[testmail.GmailAllMail]}, []string{`\Flagged`}, nil))
			e.sync(t)
			assert.Equal(t, []string{"done", "done"}, e.opStates(t))
			assert.Equal(t, []string{`a@x \Flagged`}, e.onServer(t, testmail.GmailAllMail))
		})
	}
}

// Labels of the old rows merge: a label put on one row and taken off the
// other by hand stays off; one put on by hand on either stays on.
func TestGmailMergeManualRemovalWins(t *testing.T) {
	e := newSyncEnv(t, testmail.Gmail())
	e.im.AddGmail(msg("a@x"), nil, "INBOX", testmail.GmailAllMail)
	e.sync(t)
	rows := e.oldRows(t, e.idOf(t, "a@x"))
	work, err := e.s.CreateLabel(bg, LabelInput{Name: "Work", Color: "gray", Icon: "folder-3"})
	require.NoError(t, err)
	family, err := e.s.CreateLabel(bg, LabelInput{Name: "Family", Color: "pink", Icon: "heart-3"})
	require.NoError(t, err)
	require.NoError(t, e.s.SetLabel(bg, []int64{rows["INBOX"]}, work.ID, true))
	require.NoError(t, e.s.SetLabel(bg, []int64{rows[testmail.GmailAllMail]}, work.ID, false))
	require.NoError(t, e.s.SetLabel(bg, []int64{rows[testmail.GmailAllMail]}, family.ID, true))

	e.sync(t)
	id := e.idOf(t, "a@x")
	got, err := e.s.MessageLabels(bg, []int64{id})
	require.NoError(t, err)
	assert.Equal(t, []AppliedLabel{{LabelID: family.ID}}, got[id])
}

// A rule that ran on one old row counts as run on the merged message: it
// does not run again.
func TestGmailMergeDoesNotRerunRule(t *testing.T) {
	e := newGmailRulesEnv(t)
	e.im.AddGmail(msg("a@x"), nil, "INBOX", testmail.GmailAllMail)
	e.sync(t)
	e.waitRuns(t, 1)
	rows := e.oldRows(t, e.idOf(t, "a@x"))
	_, err := e.s.db.Execute(bg, "UPDATE rule_runs SET message_id = ?", rows[testmail.GmailAllMail]) // it ran on the All Mail row
	require.NoError(t, err)

	e.sync(t)
	id := e.idOf(t, "a@x")
	_, err = e.s.db.Execute(bg, `INSERT INTO events_log(type, account_id, message_id, created_at) VALUES ('message.received', ?, ?, unixepoch())`, e.acc.ID, id)
	require.NoError(t, err)
	e.sync(t)
	e.rulesIdle(t)
	assert.Equal(t, [][]any{{id}}, e.rows(t, "SELECT message_id FROM rule_runs"))
}

// The ID of a row merged away still names the message it went into.
func TestGmailMergedIDResolves(t *testing.T) {
	e := newSyncEnv(t, testmail.Gmail())
	e.im.AddGmail(msg("a@x"), nil, "INBOX", testmail.GmailAllMail)
	e.sync(t)
	rows := e.oldRows(t, e.idOf(t, "a@x"))
	old := rows[testmail.GmailAllMail]

	e.sync(t)
	id := e.idOf(t, "a@x")
	require.NotEqual(t, old, id)
	got, err := e.s.GetMessage(bg, old, false)
	require.NoError(t, err)
	assert.Equal(t, id, got.ID)
	ok, err := e.s.MessageExists(bg, old)
	require.NoError(t, err)
	assert.True(t, ok)
	require.NoError(t, e.s.SetFlags(bg, []int64{old}, []string{`\Flagged`}, nil))
	e.sync(t)
	assert.Equal(t, []string{`a@x \Flagged`}, e.onServer(t, "INBOX"))
}
