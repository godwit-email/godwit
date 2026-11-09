package core

import (
	"testing"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/godwit-email/godwit/core/internal/testmail"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Signing in records what the server can do on the account: a server without
// UIDPLUS cannot delete permanently, and says why.
func TestSignInRecordsFeatures(t *testing.T) {
	e := newSyncEnv(t, testmail.WithoutUIDPlus())
	e.sync(t)
	acc, err := e.s.account(bg, e.acc.ID)
	require.NoError(t, err)
	assert.Equal(t, "imap.no_uidplus", acc.Unsupported(backend.FeaturePurge))
	assert.Empty(t, acc.Unsupported(backend.FeatureSeen))

	e = newSyncEnv(t)
	e.sync(t)
	acc, err = e.s.account(bg, e.acc.ID)
	require.NoError(t, err)
	assert.Empty(t, acc.Unsupported(backend.FeaturePurge))
	assert.Equal(t, backend.LevelSupported, acc.Features[backend.FeaturePurge].Level)
}

// Wit's tools are the same on every account; on one whose server cannot do
// the action, calling it answers unsupported with the reason.
func TestWitUnsupportedAction(t *testing.T) {
	e := newSkillEnv(t)
	run := func(want string, end ...testllm.Turn) []WitEvent {
		e.srv.Script(append([]testllm.Turn{{Calls: []testllm.Call{{Name: "search_mail", Args: `{"query":"harbour"}`}}},
			{Calls: []testllm.Call{{Name: "move_to_trash", Args: `{"ids":[1]}`}}}}, end...)...)
		evs := e.ask(t, e.askConv(t).ID, "Trash it")
		require.Equal(t, want, evs[len(evs)-1].Kind, "%+v", evs[len(evs)-1].Err)
		return evs
	}
	run("interrupted") // waits for the user's approval
	before := e.srv.Requests()[0].Tools

	_, err := e.s.db.Execute(bg, "UPDATE accounts SET features = ? WHERE id = 1", encodeFeatures(backend.Description{Identity: backend.IdentityLocation,
		Features: map[backend.Feature]backend.Support{backend.FeatureMove: {Level: backend.LevelUnsupported, Reason: "imap.no_move"}}}))
	require.NoError(t, err)
	evs := run("done", testllm.Turn{Text: []string{"ok"}})
	rs := witResults(evs, "move_to_trash")
	require.Len(t, rs, 1)
	assert.Contains(t, rs[0].Result.Error+string(rs[0].Result.Output), "unsupported: ")
	assert.Contains(t, rs[0].Result.Error+string(rs[0].Result.Output), i18n.TIn("en", "feature.reason.imap.no_move"))
	reqs := e.srv.Requests()
	assert.JSONEq(t, string(before), string(reqs[len(reqs)-1].Tools), "the tools do not change with the account")
}

// An account not signed in yet has no recorded features: nothing is refused.
func TestUnknownFeaturesRefuseNothing(t *testing.T) {
	assert.Empty(t, Account{}.Unsupported(backend.FeaturePurge))
}
