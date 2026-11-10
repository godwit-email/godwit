package core_test

import (
	"testing"

	"github.com/godwit-email/godwit/core/coretest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A POP3 account's Trash is local, so its retention policy purges what aged
// there: DELE on the server, then gone here.
func TestPOP3ScheduledPurge(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	acc := e.AddAccount(t, "alice@example.com", coretest.ViaPOP3())
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Aged out", "body"))
	ms, err := e.Svc.Search(t.Context(), acc.ID, "Aged", 10)
	require.NoError(t, err)
	require.Len(t, ms, 1)
	require.NoError(t, e.Svc.Delete(t.Context(), []int64{ms[0].ID}))
	require.NoError(t, e.Svc.SetPurge(t.Context(), acc.ID, "trash", 1))
	require.NoError(t, e.Svc.ExecForTest(`UPDATE message_locations SET entered_at = entered_at - 30 * 86400`))
	require.NoError(t, e.Svc.ExecForTest(`UPDATE folders SET role_since = role_since - 30 * 86400`))
	require.NoError(t, e.Svc.ExecForTest(`UPDATE purge_policies SET since = since - 30 * 86400`))

	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	assert.Empty(t, e.POP3(acc.ID).UIDs(), "deleted on the server")
	ms, err = e.Svc.Search(t.Context(), acc.ID, "Aged", 10)
	require.NoError(t, err)
	assert.Empty(t, ms)
}
