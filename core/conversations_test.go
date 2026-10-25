package core

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/godwit-email/godwit/core/ai"
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
	grant := grantKey{conv: conv.ID, tool: "create_draft"}
	e.s.sessionGrants.Store(grant, true)
	require.NoError(t, e.s.CancelRun(t.Context(), ask().RunID))
	assert.Equal(t, before, texts(), "stopped: nothing is replaced")
	_, granted := e.s.sessionGrants.Load(grant)
	assert.True(t, granted, "nor are the grants it set aside gone")
	e.s.sessionGrants.Delete(grant)

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

// #234: a conversation saved as Genkit messages converts to pi messages, a
// tool message to one row per result: the seqs of its runs, summary and run
// snapshots move with them, and it goes on, on pi. One that cannot convert
// keeps its messages and still opens.
func TestBackfillPi(t *testing.T) {
	e := newSkillEnv(t)
	conv := e.askConv(t)
	broken := e.askConv(t)
	text := func(role, text string) string { return `{"role":"` + role + `","content":[{"text":"` + text + `"}]}` }
	rows := []string{text("user", "q1"),
		`{"role":"model","content":[{"toolRequest":{"ref":"c1","name":"search_mail","input":{"query":"a"}}},{"toolRequest":{"ref":"c2","name":"search_mail","input":{"query":"b"}}}]}`,
		`{"role":"tool","content":[{"toolResponse":{"ref":"c1","name":"search_mail","output":{"n":1}}},{"toolResponse":{"ref":"c2","name":"search_mail","output":{"n":2}}}]}`,
		text("model", "a1"), text("user", "q2"), text("model", "a2"), text("user", "q3"), text("model", "a3")}
	snap, _ := json.Marshal(snapshot{Summary: &ai.Summary{Text: "SUM-A", Upto: 4}})
	stmts := []string{"UPDATE conversations SET next_seq = 9, summary = 'SUM-A', summary_upto = 4 WHERE id = ?"}
	args := [][]any{{conv.ID}}
	for i, r := range rows {
		stmts = append(stmts, "INSERT INTO conversation_messages(conversation_id, seq, data) VALUES (?, ?, ?)")
		args = append(args, []any{conv.ID, i + 1, []byte(r)})
	}
	for _, r := range [][]any{{101, 1, 5}, {102, 5, 7}, {103, 7, 9}} {
		stmts = append(stmts, "INSERT INTO runs(id, conversation_id, state, model, tools, snapshot, hist_from, hist_to) VALUES (?, ?, 'done', '', '[]', ?, ?, ?)")
		args = append(args, []any{r[0], conv.ID, snap, r[1], r[2]})
	}
	lost := `{"role":"user","content":[{"text":"q"},{"media":{"url":"data:image/png;base64,AA=="}}]}`
	stmts = append(stmts, "INSERT INTO conversation_messages(conversation_id, seq, data) VALUES (?, 1, ?)", "UPDATE conversations SET next_seq = 2 WHERE id = ?")
	args = append(args, []any{broken.ID, []byte(lost)}, []any{broken.ID})
	for i, q := range stmts {
		_, err := e.s.db.Execute(t.Context(), q, args[i]...)
		require.NoError(t, err, q)
	}
	require.NoError(t, e.s.backfillPi(t.Context()))

	assert.Equal(t, 9, e.count(t, `SELECT COUNT(*) FROM conversation_messages WHERE conversation_id = ? AND json_extract(data, '$.timestamp') IS NOT NULL`, conv.ID))
	var shown []string
	for _, m := range savedMessages(t, e.s, conv.ID) {
		shown = append(shown, fmt.Sprintf("%d%s%d%d", m.Seq, m.Text, len(m.Calls), len(m.Results)))
	}
	assert.Equal(t, []string{"1q100", "220", "301", "401", "5a100", "6q200", "7a200", "8q300", "9a300"}, shown)
	assert.Equal(t, []span{{ID: 101, From: 1, To: 6}, {ID: 102, From: 6, To: 8}, {ID: 103, From: 8, To: 10}}, spans(t, e.s, conv.ID))
	sum, _, err := e.s.convSummary(t.Context(), conv.ID)
	require.NoError(t, err)
	assert.Equal(t, ai.Summary{Text: "SUM-A", Upto: 5}, sum, "a1's seq")
	assert.Equal(t, "10", queryStr(t, e.s, "SELECT next_seq FROM conversations WHERE id = ?", conv.ID))
	var got snapshot
	require.NoError(t, json.Unmarshal([]byte(queryStr(t, e.s, "SELECT snapshot FROM runs WHERE id = 103")), &got))
	assert.EqualValues(t, 5, got.Summary.Upto)
	assert.Equal(t, lost, queryStr(t, e.s, "SELECT data FROM conversation_messages WHERE conversation_id = ?", broken.ID), "kept as it was")
	c, err := e.s.GetConversation(t.Context(), broken.ID)
	require.NoError(t, err)
	require.Len(t, c.Turns, 1, "and still opens")

	e.setPi(t, true)
	e.srv.Script(testllm.Turn{Text: []string{"a4"}})
	evs := e.ask(t, conv.ID, "q4")
	require.Equal(t, "done", evs[len(evs)-1].Kind, "%+v", evs[len(evs)-1].Err)
	reqs := e.srv.Requests()
	req := reqs[len(reqs)-1]
	assert.Equal(t, "Godwit", req.Header.Get("User-Agent"), "on pi")
	p := promptOf(t, req)
	for _, want := range []string{"q1", "SUM-A", "q2", "a2", "q3", "a3", "q4"} {
		assert.Contains(t, p, want)
	}
	assert.NotContains(t, p, "a1", "the summary stands in for it")
	c, err = e.s.GetConversation(t.Context(), conv.ID)
	require.NoError(t, err)
	assert.Len(t, c.Turns, 4)
}

// #234: a Genkit-format conversation waiting for an approval converts with
// its interrupt: approving it resumes the run where it stopped (on Genkit,
// where it began, #233), the draft is made once, and the conversation goes
// on, on pi.
func TestBackfillPiPendingApproval(t *testing.T) {
	e := newSkillEnv(t)
	conv := e.askConv(t)
	for i, r := range []string{`{"role":"user","content":[{"text":"q0"}]}`, `{"role":"model","content":[{"text":"a0"}]}`} {
		_, err := e.s.db.Execute(t.Context(), "INSERT INTO conversation_messages(conversation_id, seq, data) VALUES (?, ?, ?)", conv.ID, i+1, []byte(r))
		require.NoError(t, err)
	}
	_, err := e.s.db.Execute(t.Context(), "UPDATE conversations SET next_seq = 3 WHERE id = ?", conv.ID)
	require.NoError(t, err)
	e.srv.Script(testllm.Turn{Calls: []testllm.Call{{Name: "search_mail", Args: `{"query":"harbour"}`},
		{Name: "create_draft", Args: `{"to":["x@y.example"],"subject":"S","html":"<p>b</p>"}`}}}, testllm.Turn{Text: []string{"Drafted."}})
	evs := e.ask(t, conv.ID, "draft it") // the switch off: on Genkit, saved as Genkit messages
	require.Equal(t, "interrupted", evs[len(evs)-1].Kind, "%+v", evs[len(evs)-1].Err)
	require.Zero(t, e.count(t, `SELECT COUNT(*) FROM conversation_messages WHERE conversation_id = ? AND json_extract(data, '$.timestamp') IS NOT NULL`, conv.ID))
	searches := len(witResults(evs, "search_mail"))

	require.NoError(t, e.s.backfillPi(t.Context()))
	assert.Zero(t, e.count(t, `SELECT COUNT(*) FROM conversation_messages WHERE conversation_id = ? AND json_extract(data, '$.timestamp') IS NULL`, conv.ID))
	e.setPi(t, true)
	drafts := e.count(t, "SELECT COUNT(*) FROM drafts")
	sub, stop := e.s.SubscribeConversation(conv.ID)
	defer stop()
	require.NoError(t, e.s.ResolveApproval(t.Context(), evs[len(evs)-1].Approvals[0].ID, true))
	var resumed []WitEvent
	for ev := range sub {
		resumed = append(resumed, ev)
		if ev.Kind == "done" || ev.Kind == "error" {
			break
		}
	}
	require.Equal(t, "done", resumed[len(resumed)-1].Kind, "%+v", resumed[len(resumed)-1].Err)
	assert.Empty(t, witResults(resumed, "search_mail"), "the search that ran before the interrupt does not run again")
	assert.Equal(t, 1, searches)
	assert.Equal(t, drafts+1, e.count(t, "SELECT COUNT(*) FROM drafts"), "drafted once")
	calls, results := toolPairs(t, e.srv.Requests()[len(e.srv.Requests())-1])
	assert.Len(t, calls, 2)
	assert.Equal(t, calls, results)

	e.srv.Script(testllm.Turn{Text: []string{"a2"}})
	evs = e.ask(t, conv.ID, "thanks")
	require.Equal(t, "done", evs[len(evs)-1].Kind, "%+v", evs[len(evs)-1].Err)
	reqs := e.srv.Requests()
	assert.Equal(t, "Godwit", reqs[len(reqs)-1].Header.Get("User-Agent"), "on pi")
	for _, want := range []string{"q0", "a0", "draft it", "Drafted.", "thanks"} {
		assert.Contains(t, promptOf(t, reqs[len(reqs)-1]), want)
	}
	c, err := e.s.GetConversation(t.Context(), conv.ID)
	require.NoError(t, err)
	assert.Len(t, c.Turns, 3)
}

// #219: an Ask run that keeps calling tools stops at witMaxTurns: the call
// past it never runs, one more request asks for an answer with tools off,
// and the conversation keeps that answer and counts its tokens.
func TestAskAnswersAtToolLimit(t *testing.T) {
	e := newApprovalEnv(t)
	conv := e.newConv(t, "ask", 0)
	var turns []testllm.Turn
	for range 21 {
		turns = append(turns, testllm.Turn{Calls: []testllm.Call{{Name: "get_message", Args: `{"id":999}`}}, Usage: [2]int{10, 1}})
	}
	evs := e.ask(t, conv, append(turns, testllm.Turn{Text: []string{"Here is what I found."}, Usage: [2]int{10, 1}})...)
	require.Equal(t, "done", evs[len(evs)-1].Kind, "%+v", evs[len(evs)-1].Err)
	results := 0
	for _, ev := range evs {
		if ev.Kind == "tool_result" {
			results++
		}
	}
	assert.Equal(t, 20, results)
	reqs := e.srv.Requests()
	require.Len(t, reqs, 22)
	assert.Contains(t, string(reqs[21].Body), `"tool_choice":"none"`)
	msgs := savedMessages(t, e.s, conv.ID)
	assert.Equal(t, "Here is what I found.", msgs[len(msgs)-1].Text)
	assert.Equal(t, "220", queryStr(t, e.s, "SELECT input_tokens FROM runs WHERE conversation_id = ?", conv.ID))
}

// #219: a compacted conversation's summary goes to the model as the user's
// context, marked as data, once the conversation has read mail; before that,
// as the model's own words.
func TestSummaryRoleFollowsReadMail(t *testing.T) {
	for _, readMail := range []bool{false, true} {
		t.Run(strconv.FormatBool(readMail), func(t *testing.T) {
			e := newApprovalEnv(t)
			conv := e.newConv(t, "ask", 0)
			for range 3 {
				e.ask(t, conv, testllm.Turn{Text: []string{"Answer."}})
			}
			_, err := e.s.db.Execute(t.Context(), "UPDATE conversations SET summary = 'SUMMARY-TEXT', summary_upto = 2, read_mail = ? WHERE id = ?", readMail, conv.ID)
			require.NoError(t, err)
			e.ask(t, conv, testllm.Turn{Text: []string{"Answer."}})
			reqs := e.srv.Requests()
			var msgs []struct {
				Role    string
				Content json.RawMessage
			}
			require.NoError(t, json.Unmarshal(reqs[len(reqs)-1].Messages, &msgs))
			var role, text string
			for _, m := range msgs {
				var s string
				if json.Unmarshal(m.Content, &s) != nil { // an array of text parts
					var parts []struct{ Text string }
					json.Unmarshal(m.Content, &parts)
					for _, p := range parts {
						s += p.Text
					}
				}
				if strings.Contains(s, "SUMMARY-TEXT") {
					role, text = m.Role, s
				}
			}
			if readMail {
				assert.Equal(t, "user", role)
				assert.Equal(t, ai.SummaryMailNote+"\n\nSUMMARY-TEXT", text)
			} else {
				assert.Equal(t, "assistant", role)
				assert.Equal(t, ai.SummaryNote+"\n\nSUMMARY-TEXT", text)
			}
		})
	}
}
