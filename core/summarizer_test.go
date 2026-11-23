package core

import (
	"strings"
	"testing"

	"github.com/godwit-email/godwit/core/ai/mask"
	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// arriveEnv is a sendEnv with a model, 收到就总结 offered, and old@x, an
// unreplied mail from before it is chosen.
func arriveEnv(t *testing.T) (*sendEnv, *testllm.Server) {
	t.Helper()
	setFor(t, &SummaryOnArrival, true)
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("old@x", "bob@a.example", "alice@example.com", "old", e.viewAt(2), "Can we meet?"))
	e.sync(t)
	return e, e.witOn(t)
}

func (e *sendEnv) setSummaryMode(t *testing.T, level string) {
	t.Helper()
	st, err := e.s.GetSettings(bg)
	require.NoError(t, err)
	st.SummaryMode = level
	_, err = e.s.SetSettings(bg, st)
	require.NoError(t, err)
}

func (e *sendEnv) summarizeArrivals(t *testing.T) int {
	t.Helper()
	n, err := e.s.summarizeBatch(bg)
	require.NoError(t, err)
	return n
}

func replyByTurn(e *sendEnv, summary string) testllm.Turn {
	return testllm.Turn{Text: []string{summary + "\nREPLY BY: " + e.day(3) + "\nQUOTE: Please confirm the quote by Friday.\n"}}
}

// New unreplied mail is summarized once, in the background, through the
// request opening it makes, so opening it is then a cache hit. Mail from
// before 收到就总结 was chosen, and mail nobody waits a reply to, is not.
func TestSummarizeOnArrival(t *testing.T) {
	e, srv := arriveEnv(t)
	e.setSummaryMode(t, SummaryArrive)
	e.put(t, "INBOX", eml("q@x", "bob@a.example", "alice@example.com", "quote", e.viewAt(0.5), "Please confirm the quote by Friday."))
	e.put(t, "INBOX", eml("n@x", "news@a.example", "alice@example.com", "news", e.viewAt(0.5), "This week's news.", "List-Id: <n.a.example>"))
	e.sync(t)
	srv.Script(replyByTurn(e, "- Bob wants the quote confirmed."))

	assert.Equal(t, 1, e.summarizeArrivals(t))
	require.Len(t, srv.Requests(), 1)
	d := e.deadlineOf(t, "q@x")
	require.NotNil(t, d, "the summary's reply date")
	assert.Equal(t, DeadlineAI, d.Source)

	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM audit_log WHERE tool = 'skill:summarize' AND decision = 'run'"), "audited as a summary on open is")

	assert.Equal(t, "- Bob wants the quote confirmed.", strings.TrimSpace(e.summarize(t, "", Target{Kind: "message", ID: e.idOf(t, "q@x")})))
	assert.Len(t, srv.Requests(), 1, "opening it reads the cache")
	assert.Zero(t, e.summarizeArrivals(t), "once per newest message")

	e.put(t, "INBOX", eml("q2@x", "bob@a.example", "alice@example.com", "Re: quote", e.viewAt(0.2), "Any news?", "In-Reply-To: <q@x>", "References: <q@x>"))
	e.sync(t)
	srv.Script(replyByTurn(e, "PROGRESS\n- Bob asked again <mail id=\"1\"/>"))
	assert.Equal(t, 1, e.summarizeArrivals(t), "a newer message in it")
	require.Len(t, srv.Requests(), 2)
	e.summarize(t, "", Target{Kind: "thread", ID: e.threadOf(t, "q@x")})
	assert.Len(t, srv.Requests(), 2, "a thread of two: the thread's summary, as the reader asks for it")
}

// With 按会话显示 off the reader summarizes the message alone, so the
// background summary does too, and opening it reads the cache.
func TestSummarizeOnArrivalMessageView(t *testing.T) {
	e, srv := arriveEnv(t)
	st, err := e.s.GetSettings(bg)
	require.NoError(t, err)
	st.ThreadView, st.SummaryMode = false, SummaryArrive
	_, err = e.s.SetSettings(bg, st)
	require.NoError(t, err)
	e.put(t, "INBOX", eml("q@x", "bob@a.example", "alice@example.com", "quote", e.viewAt(0.5), "Please confirm the quote by Friday."))
	e.put(t, "INBOX", eml("q2@x", "bob@a.example", "alice@example.com", "Re: quote", e.viewAt(0.2), "Any news?", "In-Reply-To: <q@x>", "References: <q@x>"))
	e.sync(t)
	require.Equal(t, e.threadOf(t, "q@x"), e.threadOf(t, "q2@x"))
	srv.Script(replyByTurn(e, "- Bob asked again."))
	assert.Equal(t, 1, e.summarizeArrivals(t))
	e.summarize(t, "", Target{Kind: "message", ID: e.idOf(t, "q2@x")})
	assert.Len(t, srv.Requests(), 1, "the message's summary, as the reader asks for it")
}

// A failure that may pass (the service rejecting the key) holds up no other mail:
// the next look starts with the rest. After 3 failed attempts the message is
// given up as failed and not asked about again.
func TestSummarizeOnArrivalTransientFailure(t *testing.T) {
	e, srv := arriveEnv(t)
	e.setSummaryMode(t, SummaryArrive)
	e.put(t, "INBOX", eml("a@x", "bob@a.example", "alice@example.com", "first", e.viewAt(0.2), "Please confirm the quote by Friday."))
	e.put(t, "INBOX", eml("b@x", "carol@a.example", "alice@example.com", "second", e.viewAt(0.5), "Can you review this?"))
	e.sync(t)
	down := testllm.Turn{Status: 401} // the key rejected: the user may fix it
	srv.Script(down, replyByTurn(e, "- Carol asks for a review."), down, down)
	look := func() int {
		n, err := e.s.summarizeBatch(bg)
		assert.Error(t, err, "backs off")
		return n
	}
	assert.Zero(t, look(), "a@x, newest, fails")
	assert.Equal(t, 1, look(), "b@x first, then a@x fails again")
	assert.Equal(t, [][]any{{summaryDone}}, e.rows(t, "SELECT outcome FROM summary_checks WHERE message_id = ?", e.idOf(t, "b@x")))
	assert.Zero(t, look())
	assert.Equal(t, [][]any{{summaryFailed}}, e.rows(t, "SELECT outcome FROM summary_checks WHERE message_id = ?", e.idOf(t, "a@x")))
	assert.Zero(t, e.summarizeArrivals(t), "not asked about again")
	assert.Len(t, srv.Requests(), 4)
}

// Mail that arrives while 收到就总结 waits for the user to confirm a new
// model service is summarized once they do, however long the wait: past
// events_log's retention too.
func TestSummarizeOnArrivalAfterLongPause(t *testing.T) {
	e, srv := arriveEnv(t)
	e.setSummaryMode(t, SummaryArrive)
	other := testllm.Start(t, "openai")
	_, err := e.s.AddModelService(bg, ModelServiceInput{Name: "y", Kind: "openai", BaseURL: other.URL, APIKey: "k", Models: []string{"m2"}})
	require.NoError(t, err)
	st, err := e.s.GetSettings(bg)
	require.NoError(t, err)
	st.DefaultModel = "y/m2"
	_, err = e.s.SetSettings(bg, st)
	require.NoError(t, err)
	assert.Zero(t, e.summarizeArrivals(t), "paused")

	e.put(t, "INBOX", eml("q@x", "bob@a.example", "alice@example.com", "quote", e.viewAt(0.5), "Please confirm the quote by Friday."))
	e.sync(t)
	received := "SELECT count(*) FROM events_log WHERE type = 'message.received' AND message_id = ?"
	// A week on, the janitor keeps what the summarizer has yet to read...
	_, err = e.s.db.Execute(bg, "UPDATE events_log SET created_at = 0")
	require.NoError(t, err)
	require.NoError(t, e.s.cleanEvents(bg))
	require.Equal(t, 1, e.count(t, received, e.idOf(t, "q@x")))
	assert.Zero(t, e.summarizeArrivals(t), "still paused")
	// ...and, once it has, lets it go.
	_, err = e.s.db.Execute(bg, "DELETE FROM events_log")
	require.NoError(t, err)

	require.NoError(t, e.s.ConfirmSummaryService(bg))
	other.Script(replyByTurn(e, "- Bob wants the quote confirmed."))
	assert.Equal(t, 1, e.summarizeArrivals(t))
	assert.Len(t, other.Requests(), 1)
	assert.Empty(t, srv.Requests())
}

// Under the other levels nothing is summarized in the background.
func TestSummarizeOnArrivalOnlyWhenChosen(t *testing.T) {
	e, srv := arriveEnv(t)
	e.setSummaryMode(t, SummaryOpen)
	e.put(t, "INBOX", eml("q@x", "bob@a.example", "alice@example.com", "quote", e.viewAt(0.5), "Please confirm the quote by Friday."))
	e.sync(t)
	assert.Zero(t, e.summarizeArrivals(t))
	assert.Empty(t, srv.Requests())
}

// A message 脱敏 blocks is recorded as blocked and never asked about again.
func TestSummarizeOnArrivalBlocked(t *testing.T) {
	e, srv := arriveEnv(t)
	setMaskAction(t, e.s, map[string]string{mask.IDs: MaskBlock})
	e.setSummaryMode(t, SummaryArrive)
	e.put(t, "INBOX", eml("card@x", "bob@a.example", "alice@example.com", "card", e.viewAt(0.5), "Please pay with 4111 1111 1111 1111 by Friday."))
	e.sync(t)
	assert.Equal(t, 1, e.summarizeArrivals(t))
	assert.Empty(t, srv.Requests())
	assert.Equal(t, [][]any{{summaryBlocked}}, e.rows(t, "SELECT outcome FROM summary_checks WHERE message_id = ?", e.idOf(t, "card@x")))
	assert.Zero(t, e.summarizeArrivals(t), "not retried")
	n, err := e.s.CountUnrepliedToSummarize(bg)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "old@x only")
}

// A change of model service pauses 收到就总结 until the user confirms the
// new one; then it goes on with the mail that came meanwhile.
func TestSummarizeOnArrivalPausesOnServiceChange(t *testing.T) {
	e, srv := arriveEnv(t)
	e.setSummaryMode(t, SummaryArrive)
	other := testllm.Start(t, "openai")
	_, err := e.s.AddModelService(bg, ModelServiceInput{Name: "y", Kind: "openai", BaseURL: other.URL, APIKey: "k", Models: []string{"m2"}})
	require.NoError(t, err)
	st, err := e.s.GetSettings(bg)
	require.NoError(t, err)
	st.DefaultModel = "y/m2"
	_, err = e.s.SetSettings(bg, st)
	require.NoError(t, err)

	e.put(t, "INBOX", eml("q@x", "bob@a.example", "alice@example.com", "quote", e.viewAt(0.5), "Please confirm the quote by Friday."))
	e.sync(t)
	assert.Zero(t, e.summarizeArrivals(t))
	assert.Empty(t, srv.Requests())
	assert.Empty(t, other.Requests())
	got, err := e.s.SummaryArrivalState(bg)
	require.NoError(t, err)
	require.True(t, got.Paused)
	assert.Equal(t, "x", got.Service)
	assert.Equal(t, "y ("+strings.TrimPrefix(other.URL, "http://")+")", got.Current, "another service names where mail would go")

	require.NoError(t, e.s.ConfirmSummaryService(bg))
	got, err = e.s.SummaryArrivalState(bg)
	require.NoError(t, err)
	assert.Equal(t, SummaryArrival{Service: "y", Current: "y (" + strings.TrimPrefix(other.URL, "http://") + ")"}, got)
	other.Script(replyByTurn(e, "- Bob wants the quote confirmed."))
	assert.Equal(t, 1, e.summarizeArrivals(t))
	assert.Len(t, other.Requests(), 1)
	assert.Empty(t, srv.Requests())
}

// The consent to 收到就总结 is for where mail goes: pointing the chosen
// service at another endpoint pauses it until confirmed; renaming it does not.
func TestSummarizeOnArrivalPausesOnEndpointChange(t *testing.T) {
	e, srv := arriveEnv(t)
	e.setSummaryMode(t, SummaryArrive)
	list, err := e.s.ListModelServices(bg)
	require.NoError(t, err)
	require.Len(t, list, 1)
	x := list[0]
	edit := func(name, url string) {
		t.Helper()
		_, err := e.s.UpdateModelService(bg, x.ID, ModelServiceInput{Name: name, Kind: "openai", BaseURL: url, APIKey: "k", Models: []string{"m1"}})
		require.NoError(t, err)
	}
	edit("renamed", srv.URL)
	got, err := e.s.SummaryArrivalState(bg)
	require.NoError(t, err)
	assert.False(t, got.Paused, "a rename keeps the consent")

	other := testllm.Start(t, "openai")
	edit("renamed", other.URL)
	e.put(t, "INBOX", eml("q@x", "bob@a.example", "alice@example.com", "quote", e.viewAt(0.5), "Please confirm the quote by Friday."))
	e.sync(t)
	assert.Zero(t, e.summarizeArrivals(t))
	assert.Empty(t, srv.Requests())
	assert.Empty(t, other.Requests())
	got, err = e.s.SummaryArrivalState(bg)
	require.NoError(t, err)
	assert.True(t, got.Paused, "another endpoint needs the user's say")
	assert.Contains(t, got.Current, "127.0.0.1", "names where mail would go")

	require.NoError(t, e.s.ConfirmSummaryService(bg))
	other.Script(replyByTurn(e, "- Bob wants the quote confirmed."))
	assert.Equal(t, 1, e.summarizeArrivals(t))
	assert.Len(t, other.Requests(), 1)
}

// Older mail is summarized only when asked: the unreplied conversations of
// the last 30 days not summarized yet, counted first.
func TestSummarizeUnreplied(t *testing.T) {
	e, srv := arriveEnv(t)
	e.put(t, "INBOX", eml("ancient@x", "carol@a.example", "alice@example.com", "ancient", e.viewAt(40), "Still there?"))
	e.sync(t)
	_, err := e.s.SummarizeUnreplied(bg)
	assert.Error(t, err, "only under 收到就总结")
	e.setSummaryMode(t, SummaryArrive)
	assert.Zero(t, e.summarizeArrivals(t), "old mail waits to be asked for")

	n, err := e.s.CountUnrepliedToSummarize(bg)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "old@x; ancient@x is older than 30 days")
	srv.Script(replyByTurn(e, "- Bob asks to meet."))
	n, err = e.s.SummarizeUnreplied(bg)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, 1, e.summarizeArrivals(t))
	assert.Len(t, srv.Requests(), 1)
	n, err = e.s.CountUnrepliedToSummarize(bg)
	require.NoError(t, err)
	assert.Zero(t, n)
}

// 收到就总结 needs a model service to name: without one the level stays,
// and a program's raise that fails leaves no audit row (ADR 0003).
func TestSummaryOnArrivalNeedsModel(t *testing.T) {
	setFor(t, &SummaryOnArrival, true)
	e := newSendEnv(t, false)
	st, err := e.s.GetSettings(bg)
	require.NoError(t, err)
	st.SummaryMode = SummaryArrive
	_, err = e.s.SetSettings(WithExternalSource(bg, AuditLocalAPI), st)
	require.ErrorIs(t, err, ErrAINotConfigured)
	st, err = e.s.GetSettings(bg)
	require.NoError(t, err)
	assert.Equal(t, SummaryOff, st.SummaryMode)
	assert.Zero(t, e.count(t, "SELECT count(*) FROM audit_log WHERE tool = 'SetSettings'"))
}

// 收到就总结 is offered only behind SummaryOnArrival; one chosen while it
// was reads as 打开邮件时 without it, so the other settings still save.
func TestSummaryOnArrivalGate(t *testing.T) {
	e, _ := arriveEnv(t)
	e.setSummaryMode(t, SummaryArrive)
	setFor(t, &SummaryOnArrival, false)
	st, err := e.s.GetSettings(bg)
	require.NoError(t, err)
	assert.Equal(t, SummaryOpen, st.SummaryMode)
	_, err = e.s.SetSettings(bg, st)
	require.NoError(t, err)
	st.SummaryMode = SummaryArrive
	_, err = e.s.SetSettings(bg, st)
	assert.Error(t, err)
}
