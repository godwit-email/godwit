package core

import (
	"strconv"
	"testing"

	"github.com/godwit-email/godwit/core/internal/testmail"
	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/assert"
)

// A Gmail sync keeps each stored copy's X-GM-MSGID (ADR 0032): the copies of
// one message share it, and a message with the same Message-ID and content
// has its own.
func TestGmailSyncStoresMsgID(t *testing.T) {
	e := newSyncEnv(t, testmail.Gmail())
	m := e.im.AddGmail(msg("a"), nil, "INBOX", testmail.GmailAllMail)
	twin := e.im.AddGmail(msg("a"), nil, testmail.GmailAllMail)
	e.sync(t)

	got := map[string][]string{}
	for _, r := range e.rows(t, "SELECT f.name, m.gmail_msgid FROM messages m JOIN folders f ON f.id = m.folder_id ORDER BY m.server_uid") {
		got[store.Str(r[0])] = append(got[store.Str(r[0])], store.Str(r[1]))
	}
	id, twinID := strconv.FormatUint(m.ID, 10), strconv.FormatUint(twin.ID, 10)
	assert.Equal(t, map[string][]string{"INBOX": {id}, testmail.GmailAllMail: {id, twinID}}, got)
}

// Another server is never asked for X-GM-MSGID, and its messages have none.
func TestSyncWithoutGmailHasNoMsgID(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", msg("a"))
	e.sync(t)
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM messages WHERE gmail_msgid IS NULL"))
}
