package coretest_test

import (
	"testing"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setPOP3(t *testing.T, e *coretest.Env, on bool) {
	t.Helper()
	st, err := e.Svc.GetSettings(t.Context())
	require.NoError(t, err)
	st.ExperimentalPOP3 = on
	_, err = e.Svc.SetSettings(t.Context(), st)
	require.NoError(t, err)
}

// A POP3 account is added only with the experiment on, checked over POP3,
// and comes with a local trash, archive and sent folder beside its inbox.
func TestPOP3AddAccount(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	in := e.StartPOP3Servers(t, "alice@example.com")
	_, err := e.Svc.AddAccount(t.Context(), in)
	require.ErrorContains(t, err, "POP3 is experimental")
	require.ErrorContains(t, e.Svc.TestAccount(t.Context(), in), `receiving protocol "pop3" is not supported`)

	setPOP3(t, e, true)
	r, err := e.Svc.CheckAccount(t.Context(), in)
	require.NoError(t, err)
	require.True(t, r.OK(), "%+v", r.Steps)
	assert.Equal(t, "pop3", r.Steps[1].Name)
	acc, err := e.Svc.AddAccount(t.Context(), in)
	require.NoError(t, err)
	assert.Equal(t, "pop3", acc.Receiving.Protocol)

	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	fs, err := e.Svc.ListFolders(t.Context(), acc.ID)
	require.NoError(t, err)
	roles := map[string]string{}
	for _, f := range fs {
		roles[f.Name] = f.Role
	}
	assert.Equal(t, map[string]string{"INBOX": "inbox", "Trash": "trash", "Archive": "archive", "Sent": "sent"}, roles)

	// Turned off, the account still syncs.
	setPOP3(t, e, false)
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Hello POP3", "body"))
	ms, err := e.Svc.Search(t.Context(), acc.ID, "Hello", 10)
	require.NoError(t, err)
	require.Len(t, ms, 1)
}

// A sync round downloads the maildrop: what was there before the first
// round is existing mail, what came after is new mail a receive rule runs on.
func TestPOP3SyncArrives(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	acc := e.AddAccount(t, "alice@example.com", coretest.ViaPOP3())
	l, err := e.Svc.CreateLabel(t.Context(), core.LabelInput{Name: "Invoices", Color: "gray", Icon: "folder-3"})
	require.NoError(t, err)
	_, err = e.Svc.SaveRule(t.Context(), core.Rule{Enabled: true, Doc: core.RuleDoc{Version: core.RuleVersion, Name: "invoices",
		Trigger: core.TriggerReceive, Match: core.MatchAll,
		Conditions: []core.RuleCondition{{Field: core.FieldSubject, Op: core.OpContains, Value: "invoice"}},
		Actions:    []core.RuleAction{{Kind: core.ActionLabel, Label: l.Slug}}}})
	require.NoError(t, err)
	e.POP3(acc.ID).Deliver(coretest.Msg("billing@example.com", "alice@example.com", "Old invoice", "b"))
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("billing@example.com", "alice@example.com", "New invoice", "b"))

	labeled := func(subject string) bool {
		ms, err := e.Svc.Search(t.Context(), acc.ID, subject, 10)
		require.NoError(t, err)
		require.Len(t, ms, 1)
		got, _ := e.Svc.MessageLabels(t.Context(), []int64{ms[0].ID})
		return len(got[ms[0].ID]) > 0
	}
	require.Eventually(t, func() bool { return labeled("New invoice") }, 5*time.Second, 20*time.Millisecond)
	assert.False(t, labeled("Old invoice"), "mail already there is not new")
}
