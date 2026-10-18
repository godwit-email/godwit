package core

import (
	"testing"

	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An interrupted run that an older Godwit stored with the service's name
// resumes after the upgrade (slug backfill) and a rename.
func TestResumeAfterUpgradeAndRename(t *testing.T) {
	e := newApprovalEnv(t)
	conv := e.newConv(t, "ask", 0)
	in := e.interrupt(t, conv, testllm.Call{Name: "fake_draft", Args: `{"subject":"Hi"}`})
	// Downgrade the rows to what the old schema held.
	for _, q := range []string{
		"UPDATE model_services SET name = 'Old GW', slug = NULL",
		"UPDATE runs SET model = 'Old GW/m1'",
	} {
		_, err := e.s.db.Execute(t.Context(), q)
		require.NoError(t, err)
	}
	setRawSettings(t, e.s, `{"DefaultModel":"Old GW/m1"}`)
	e.restart(t)
	assert.Equal(t, "old-gw/m1", queryStr(t, e.s, "SELECT model FROM runs WHERE id = ?", in.RunID))
	assert.Equal(t, `{"DefaultModel":"old-gw/m1"}`, queryStr(t, e.s, "SELECT value FROM settings WHERE key = 'app'"))

	_, err := e.s.UpdateModelService(t.Context(), e.ms.ID, ModelServiceInput{Name: "New GW", Kind: "openai", BaseURL: e.srv.URL, Models: []string{"m1"}})
	require.NoError(t, err)
	as, err := e.s.ListApprovals(t.Context(), conv.ID)
	require.NoError(t, err)
	require.Len(t, as, 1)
	require.NoError(t, e.s.ResolveApproval(t.Context(), as[0].ID, true))
	e.waitRun(t, in.RunID, "done")
	assert.Equal(t, int32(1), e.applied.Load())
}

// A run stores the slug form of the reference it ran with.
func TestRunStoresSlugRef(t *testing.T) {
	e := newApprovalEnv(t)
	_, err := e.s.UpdateModelService(t.Context(), e.ms.ID, ModelServiceInput{Name: "X GW", Kind: "openai", BaseURL: e.srv.URL, Models: []string{"m1"}})
	require.NoError(t, err)
	_, err = e.s.db.Execute(t.Context(), "UPDATE model_services SET slug = 'x-gw'")
	require.NoError(t, err)
	_, err = e.s.db.Execute(t.Context(), "INSERT INTO model_service_aliases(name, service_id) VALUES ('X GW', ?)", e.ms.ID)
	require.NoError(t, err)
	st := defaultSettings()
	st.DefaultModel = "X GW/m1" // the legacy name form
	_, err = e.s.SetSettings(t.Context(), st)
	require.NoError(t, err)
	conv := e.newConv(t, "ask", 0)
	e.srv.Script(testllm.Turn{Text: []string{"Hi."}})
	e.start(t, conv, "hello")
	assert.Equal(t, "x-gw/m1", queryStr(t, e.s, "SELECT model FROM runs WHERE conversation_id = ?", conv.ID))
}

// A conversation's model overrides the default for its runs; "" goes back to
// the default and a ref that does not resolve is refused.
func TestSetConversationModel(t *testing.T) {
	e := newApprovalEnv(t)
	ms, err := e.s.UpdateModelService(t.Context(), e.ms.ID, ModelServiceInput{Name: e.ms.Name, Kind: "openai", BaseURL: e.srv.URL, Models: []string{"m1", "m2"}})
	require.NoError(t, err)
	conv := e.newConv(t, "ask", 0)
	require.Error(t, e.s.SetConversationModel(t.Context(), conv.ID, ms.Slug+"/nope"))
	require.Error(t, e.s.SetConversationModel(t.Context(), conv.ID+100, ""))
	require.NoError(t, e.s.SetConversationModel(t.Context(), conv.ID, ms.Slug+"/m2"))
	e.srv.Script(testllm.Turn{Text: []string{"Hi."}})
	e.start(t, conv, "hello")
	assert.Equal(t, ms.Slug+"/m2", queryStr(t, e.s, "SELECT model FROM runs WHERE conversation_id = ? ORDER BY id DESC", conv.ID))

	require.NoError(t, e.s.SetConversationModel(t.Context(), conv.ID, ""))
	c, err := e.s.GetConversation(t.Context(), conv.ID)
	require.NoError(t, err)
	assert.Empty(t, c.Model)
}
