package core

import (
	"encoding/json"
	"strconv"
	"strings"
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
// the default and a ref that does not resolve, or an unknown effort, is refused.
func TestSetConversationModel(t *testing.T) {
	e := newApprovalEnv(t)
	ms, err := e.s.UpdateModelService(t.Context(), e.ms.ID, ModelServiceInput{Name: e.ms.Name, Kind: "openai", BaseURL: e.srv.URL, Models: []string{"m1", "m2"}})
	require.NoError(t, err)
	conv := e.newConv(t, "ask", 0)
	require.Error(t, e.s.SetConversationModel(t.Context(), conv.ID, ms.Slug+"/nope", ""))
	require.Error(t, e.s.SetConversationModel(t.Context(), conv.ID+100, "", ""))
	require.NoError(t, e.s.SetConversationModel(t.Context(), conv.ID, ms.Slug+"/m2", ""))
	e.srv.Script(testllm.Turn{Text: []string{"Hi."}})
	e.start(t, conv, "hello")
	assert.Equal(t, ms.Slug+"/m2", queryStr(t, e.s, "SELECT model FROM runs WHERE conversation_id = ? ORDER BY id DESC", conv.ID))

	require.NoError(t, e.s.SetConversationModel(t.Context(), conv.ID, "", ""))
	c, err := e.s.GetConversation(t.Context(), conv.ID)
	require.NoError(t, err)
	assert.Empty(t, c.Model)

	// Either one refused keeps neither.
	require.Error(t, e.s.SetConversationModel(t.Context(), conv.ID, ms.Slug+"/m2", "max"))
	require.Error(t, e.s.SetConversationModel(t.Context(), conv.ID, ms.Slug+"/nope", "high"))
	c, err = e.s.GetConversation(t.Context(), conv.ID)
	require.NoError(t, err)
	assert.Empty(t, c.Model)
	assert.Empty(t, c.Effort)
	require.NoError(t, e.s.SetConversationModel(t.Context(), conv.ID, ms.Slug+"/m2", "high"))
	c, err = e.s.GetConversation(t.Context(), conv.ID)
	require.NoError(t, err)
	assert.Equal(t, [2]string{ms.Slug + "/m2", "high"}, [2]string{c.Model, c.Effort})
}

// Only a question can be asked again, and not while a run is active.
func TestAskAgainRefuses(t *testing.T) {
	e := newApprovalEnv(t)
	conv := e.newConv(t, "ask", 0)
	e.srv.Script(testllm.Turn{Text: []string{"One."}})
	e.start(t, conv, "first")
	c, err := e.s.GetConversation(t.Context(), conv.ID)
	require.NoError(t, err)
	require.Len(t, savedMessages(t, e.s, c.ID), 2)
	_, err = e.s.AskAgain(t.Context(), conv.ID, strconv.FormatInt(savedMessages(t, e.s, c.ID)[1].Seq, 10), "x", nil)
	require.Error(t, err, "an answer is no question")
	_, err = e.s.AskAgain(t.Context(), conv.ID, "99", "x", nil)
	require.Error(t, err)

	e.interrupt(t, conv, testllm.Call{Name: "fake_draft", Args: `{"subject":"Hi"}`})
	_, err = e.s.AskAgain(t.Context(), conv.ID, c.Turns[0].ID, "x", nil)
	assert.ErrorIs(t, err, ErrRunActive)
}

// A run cut off still counts the tokens its model call took.
func TestFailedRunUsage(t *testing.T) {
	e := newApprovalEnv(t)
	conv := e.newConv(t, "ask", 0)
	e.srv.Script(testllm.Turn{Text: []string{"cut"}, Finish: "length", Usage: [2]int{50, 9}})
	e.start(t, conv, "hello")
	assert.Equal(t, "50", queryStr(t, e.s, "SELECT input_tokens FROM runs WHERE conversation_id = ?", conv.ID))
	assert.Equal(t, "9", queryStr(t, e.s, "SELECT output_tokens FROM runs WHERE conversation_id = ?", conv.ID))
}

// AskAgain replaces a question only once its run can start: one refused (a
// model that no longer resolves, a quote that does not exist) leaves the
// conversation as it was.
func TestAskAgain(t *testing.T) {
	e := newApprovalEnv(t)
	conv := e.newConv(t, "ask", 0)
	e.srv.Script(testllm.Turn{Text: []string{"One."}}, testllm.Turn{Text: []string{"Two."}})
	e.start(t, conv, "first")
	e.start(t, conv, "second")
	messages := func() []string {
		c, err := e.s.GetConversation(t.Context(), conv.ID)
		require.NoError(t, err)
		var out []string
		for _, m := range savedMessages(t, e.s, c.ID) {
			out = append(out, m.Text)
		}
		return out
	}
	before := messages()
	c, err := e.s.GetConversation(t.Context(), conv.ID)
	require.NoError(t, err)
	first, second := c.Turns[0].ID, c.Turns[1].ID

	_, err = e.s.AskAgain(t.Context(), conv.ID, second, "second, edited", []int64{999999})
	require.Error(t, err)
	assert.Equal(t, before, messages(), "a missing quote")
	_, err = e.s.db.Execute(t.Context(), "UPDATE conversations SET model = 'gone/m9' WHERE id = ?", conv.ID)
	require.NoError(t, err)
	_, err = e.s.AskAgain(t.Context(), conv.ID, second, "second, edited", nil)
	require.Error(t, err)
	assert.Equal(t, before, messages(), "a model that does not resolve")

	require.NoError(t, e.s.SetConversationModel(t.Context(), conv.ID, "", ""))
	// accepted, but its run saves nothing (cut off): nothing is replaced either
	grant := grantKey{conv: conv.ID, tool: "fake_draft"}
	e.s.sessionGrants.Store(grant, true)
	e.srv.Script(testllm.Turn{Text: []string{"cut"}, Finish: "length"})
	ch, err := e.s.AskAgain(t.Context(), conv.ID, second, "second, edited", nil)
	require.NoError(t, err)
	evs := drainWit(t, ch)
	require.Equal(t, "error", evs[len(evs)-1].Kind)
	assert.Equal(t, before, messages(), "a run that failed")
	_, granted := e.s.sessionGrants.Load(grant)
	assert.True(t, granted, "nor are the conversation's grants gone")

	e.srv.Script(testllm.Turn{Text: []string{"Edited."}})
	ch, err = e.s.AskAgain(t.Context(), conv.ID, second, "second, edited", nil)
	require.NoError(t, err)
	drainWit(t, ch)
	assert.Equal(t, []string{"first", "One.", "second, edited", "Edited."}, messages())

	// in place of the first question: the question asked titles it, in the rewind's transaction
	e.srv.Script(testllm.Turn{Text: []string{"Anew."}})
	ch, err = e.s.AskAgain(t.Context(), conv.ID, first, "first, edited", nil)
	require.NoError(t, err)
	drainWit(t, ch)
	assert.Equal(t, "first, edited", queryStr(t, e.s, "SELECT title FROM conversations WHERE id = ?", conv.ID))
}

// A question asked again drops the runs it replaces, not what they took:
// the conversation's totals keep their tokens.
func TestRewindKeepsUsage(t *testing.T) {
	e := newApprovalEnv(t)
	conv := e.newConv(t, "ask", 0)
	e.srv.Script(testllm.Turn{Text: []string{"One."}, Usage: [2]int{10, 2}}, testllm.Turn{Text: []string{"Two."}, Usage: [2]int{30, 4}})
	e.start(t, conv, "first")
	e.start(t, conv, "second")
	c, err := e.s.GetConversation(t.Context(), conv.ID)
	require.NoError(t, err)
	e.srv.Script(testllm.Turn{Text: []string{"Again."}, Usage: [2]int{5, 1}})
	ch, err := e.s.AskAgain(t.Context(), conv.ID, c.Turns[1].ID, "second, again", nil)
	require.NoError(t, err)
	drainWit(t, ch)
	c, err = e.s.GetConversation(t.Context(), conv.ID)
	require.NoError(t, err)
	require.Len(t, spans(t, e.s, c.ID), 2, "the replaced run goes")
	assert.Equal(t, 45, c.Usage.Input)
	assert.Equal(t, 7, c.Usage.Output)
	assert.Equal(t, 5, c.Usage.Last, "the history the model was last shown is the new run's")
}

// A question asked again hides what it replaces while its run waits for
// approval; stopped, the conversation is as it was, and done, it replaces it.
func TestAskAgainInterrupted(t *testing.T) {
	e := newSkillEnv(t)
	conv := e.askConv(t)
	e.srv.Script(testllm.Turn{Text: []string{"One."}}, testllm.Turn{Text: []string{"Two."}})
	e.ask(t, conv.ID, "first")
	e.ask(t, conv.ID, "second")
	texts := func() []string {
		c, err := e.s.GetConversation(t.Context(), conv.ID)
		require.NoError(t, err)
		var out []string
		for _, m := range savedMessages(t, e.s, c.ID) {
			if m.Text != "" {
				out = append(out, m.Text)
			}
		}
		return out
	}
	c, err := e.s.GetConversation(t.Context(), conv.ID)
	require.NoError(t, err)
	second := c.Turns[1].ID
	before := texts()

	ask := func() WitEvent {
		e.srv.Script(testllm.Turn{Calls: []testllm.Call{{Name: "create_draft", Args: `{"to":["a@b.c"],"subject":"Re","html":"<p>x</p>"}`}}})
		ch, err := e.s.AskAgain(t.Context(), conv.ID, second, "second, edited", nil)
		require.NoError(t, err)
		evs := drainWit(t, ch)
		require.Equal(t, "interrupted", evs[len(evs)-1].Kind)
		assert.Equal(t, []string{"first", "One.", "second, edited"}, texts(), "what it replaces is hidden")
		return evs[len(evs)-1]
	}
	require.NoError(t, e.s.CancelRun(t.Context(), ask().RunID))
	assert.Equal(t, before, texts(), "stopped: nothing is replaced")

	ev := ask()
	e.srv.Script(testllm.Turn{Text: []string{"Edited."}})
	sub, stop := e.s.SubscribeConversation(conv.ID)
	defer stop()
	require.NoError(t, e.s.ResolveApproval(t.Context(), ev.Approvals[0].ID, true))
	for ev := range sub {
		if ev.Kind == "done" {
			break
		}
	}
	assert.Equal(t, []string{"first", "One.", "second, edited", "Edited."}, texts())
}

// A conversation saved as one array moves into message rows: spans and the
// summary's reach become seqs, and a skill run's command goes where it ran.
// One that cannot be read stays, failing to load until a later start reads it.
func TestBackfillHistories(t *testing.T) {
	e := newSkillEnv(t)
	d := e.newDraft(t, "Hi", "<p>x</p>")
	conv, err := e.s.newAssistConversation(t.Context(), d.ID, "")
	require.NoError(t, err)
	msg := func(role, text string) string { return `{"role":"` + role + `","content":[{"text":"` + text + `"}]}` }
	state := "[" + strings.Join([]string{msg("user", "q1"), msg("model", "a1"), msg("user", "q2"), msg("model", "a2")}, ",") + "]"
	snap := func(host bool) string { b, _ := json.Marshal(snapshot{Host: host}); return string(b) }
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{"UPDATE conversations SET state = ?, summary = 's', summary_upto = 1 WHERE id = ?", []any{[]byte(state), conv.ID}},
		{"INSERT INTO runs(id, conversation_id, state, model, tools, snapshot, hist_from, hist_to) VALUES (101, ?, 'done', '', '[]', ?, 0, 2)", []any{conv.ID, snap(false)}},
		{"INSERT INTO runs(id, conversation_id, state, model, tools, snapshot, hist_from, hist_to) VALUES (102, ?, 'done', '', '[]', ?, 0, 2)", []any{conv.ID, snap(true)}},
		{"INSERT INTO runs(id, conversation_id, state, model, tools, snapshot, hist_from, hist_to) VALUES (103, ?, 'done', '', '[]', ?, 2, 4)", []any{conv.ID, snap(false)}},
		{"INSERT INTO approvals(run_id, seq, call_id, tool, args, summary, state) VALUES (102, 0, 'skill', 'update_draft', '{}', ?, 'done')", []any{kindTranslate}},
		{"INSERT INTO conversations(id, kind, title, model, state) VALUES (900, 'ask', '', '', ?)", []any{[]byte("not json")}},
	} {
		_, err := e.s.db.Execute(t.Context(), q.sql, q.args...)
		require.NoError(t, err, q.sql)
	}
	require.NoError(t, e.s.backfillHistories(t.Context()))

	c, err := e.s.GetConversation(t.Context(), conv.ID)
	require.NoError(t, err)
	var shown []string
	for _, m := range savedMessages(t, e.s, c.ID) {
		if m.Command != nil {
			shown = append(shown, "/"+m.Command.Skill)
		} else {
			shown = append(shown, m.Text)
		}
	}
	assert.Equal(t, []string{"q1", "a1", "/translate-draft", "q2", "a2"}, shown)
	assert.Equal(t, []span{{ID: 101, From: 1, To: 3}, {ID: 102, From: 3, To: 4}, {ID: 103, From: 4, To: 6}}, spans(t, e.s, c.ID))
	sum, _, err := e.s.convSummary(t.Context(), conv.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 2, sum.Upto, "a1's seq")
	assert.Equal(t, "6", queryStr(t, e.s, "SELECT next_seq FROM conversations WHERE id = ?", conv.ID))

	_, err = e.s.GetConversation(t.Context(), 900)
	assert.ErrorIs(t, err, errUnreadableHistory)
	assert.Equal(t, "not json", queryStr(t, e.s, "SELECT state FROM conversations WHERE id = 900"), "kept for the next start")
}
