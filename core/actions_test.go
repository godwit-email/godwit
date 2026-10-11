package core

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/plugins"
	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func pluginFailures(t *testing.T, s *Service, name string) int64 {
	t.Helper()
	rows, err := s.db.Query(t.Context(), "SELECT failures FROM plugins WHERE name = ?", name)
	require.NoError(t, err)
	return store.Int(rows.Values[0][0])
}

// netServer is a fake API reachable as api.example.com:<port>; hits counts its requests.
func netServer(t *testing.T, s *Service) (api string, hits *atomic.Int64) {
	hits = &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, "pong")
	}))
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	s.pluginNet = plugins.NetOptions{Resolver: mapDNS{"api.example.com": "127.0.0.1"}, AllowHTTP: true, AllowLoopback: true}
	return "api.example.com:" + port, hits
}

func TestListAndRunAction(t *testing.T) {
	s := newMailContextService(t)
	id := addMessage(t, s, "Hi", "Bob <bob@x>", "hello", "", 1_700_000_000)
	startTestPlugin(t, s, "echo", `{}`, `export default function (ctx: any) {
  ctx.actions.register({ id: "show", label: { default: "Show", de: "Zeigen" }, on: ["message", "selection"],
    run: async (t: any) => JSON.stringify(t, Object.keys(t).sort()) })
  ctx.actions.register({ id: "boom", label: "Boom", on: ["draft"], run: () => { throw new Error("boom") } })
}`)

	got, err := s.ListActions(t.Context(), "message")
	require.NoError(t, err)
	assert.Equal(t, []ActionInfo{{ID: "echo/show", Plugin: "echo", Label: plugins.Text{Default: "Show", ByLang: map[string]string{"de": "Zeigen"}},
		On: []string{"message", "selection"}}}, got)
	got, err = s.ListActions(t.Context(), "draft")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "echo/boom", got[0].ID)

	res, err := s.RunAction(t.Context(), "echo/show", Target{Kind: "message", ID: id}, RunActionOptions{})
	require.NoError(t, err)
	assert.Equal(t, ActionResult{Text: fmt.Sprintf(`{"id":%d,"kind":"message"}`, id)}, res, "no mail content in a message target")
	res, err = s.RunAction(t.Context(), "echo/show", Target{Kind: "selection", ID: id, Text: "picked"}, RunActionOptions{})
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf(`{"id":%d,"kind":"selection","text":"picked"}`, id), res.Text)

	_, err = s.RunAction(t.Context(), "echo/show", Target{Kind: "draft", ID: 1}, RunActionOptions{})
	require.Error(t, err, "not registered for drafts")
	_, err = s.RunAction(t.Context(), "echo/nope", Target{Kind: "message", ID: id}, RunActionOptions{})
	require.Error(t, err)
	assert.Zero(t, pluginFailures(t, s, "echo"))

	_, err = s.RunAction(t.Context(), "echo/boom", Target{Kind: "draft", ID: 1}, RunActionOptions{})
	require.ErrorContains(t, err, "boom")
	assert.Equal(t, int64(1), pluginFailures(t, s, "echo"), "a failed action counts")
}

func TestActionReturnsDraft(t *testing.T) {
	s := newMailContextService(t)
	id := addMessage(t, s, "Hi", "Bob <bob@x>", "hello", "", 1_700_000_000)
	startTestPlugin(t, s, "drafter", `{"mail":["read","write"]}`, `export default function (ctx: any) {
  ctx.actions.register({ id: "reply", label: "Reply", on: ["message"], run: async (t: any) => {
    const m = await ctx.mail.get(t.id)
    const draftId = await ctx.mail.createDraft({ accountId: 1, to: ["bob@x"], subject: "Re: " + m.subject, html: "<p>ok</p>" })
    return { text: "drafted", draftId }
  } })
}`)
	res, err := s.RunAction(t.Context(), "drafter/reply", Target{Kind: "message", ID: id}, RunActionOptions{})
	require.NoError(t, err)
	assert.Equal(t, "drafted", res.Text)
	require.NotZero(t, res.DraftID)
	d, err := s.GetDraft(t.Context(), res.DraftID)
	require.NoError(t, err)
	assert.Equal(t, "Re: Hi", d.Subject)
}

func TestActionHTMLSanitized(t *testing.T) {
	s := newMailContextService(t)
	startTestPlugin(t, s, "html", `{}`, `export default function (ctx: any) {
  ctx.actions.register({ id: "show", label: "Show", on: ["message"],
    run: () => ({ html: '<p onclick="steal()">hi <b>there</b><script>alert(1)</script><img src="https://t.example/x.png"></p>' }) })
  ctx.actions.register({ id: "bad", label: "Bad", on: ["message"], run: () => ({ html: 42 }) })
}`)
	res, err := s.RunAction(t.Context(), "html/show", Target{Kind: "message", ID: 1}, RunActionOptions{})
	require.NoError(t, err)
	assert.Contains(t, res.HTML, "<b>there</b>")
	for _, bad := range []string{"onclick", "script", "alert", "t.example"} {
		assert.NotContains(t, res.HTML, bad)
	}
	_, err = s.RunAction(t.Context(), "html/bad", Target{Kind: "message", ID: 1}, RunActionOptions{})
	require.Error(t, err)
	assert.Equal(t, int64(1), pluginFailures(t, s, "html"), "an invalid result counts")
}

// A plugin holding mail:read and net needs an egress approval listing its
// declared hosts for every action run; approved, it fetches.
func TestActionNeedsEgressApproval(t *testing.T) {
	s := newMailContextService(t)
	id := addMessage(t, s, "Hi", "Bob <bob@x>", "hello", "", 1_700_000_000)
	api, hits := netServer(t, s)
	startTestPlugin(t, s, "netty", `{"mail":["read"],"net":["`+api+`"]}`, `export default function (ctx: any) {
  ctx.actions.register({ id: "ping", label: "Ping", on: ["message"],
    run: async () => (await ctx.net.fetch("http://`+api+`/ping")).text() })
}`)
	_, err := s.RunAction(t.Context(), "netty/ping", Target{Kind: "message", ID: id}, RunActionOptions{})
	var ea *EgressApprovalRequired
	require.ErrorAs(t, err, &ea)
	assert.Equal(t, &EgressApprovalRequired{Plugin: "netty", Hosts: []string{api}}, ea)
	assert.Zero(t, hits.Load(), "nothing ran")

	res, err := s.RunAction(t.Context(), "netty/ping", Target{Kind: "message", ID: id}, RunActionOptions{ApproveEgress: true})
	require.NoError(t, err)
	assert.Equal(t, "pong", res.Text)
	assert.Equal(t, int64(1), hits.Load())
}

// A net plugin without mail:read gets mail content only in a selection
// target: that run needs approval; a message target carries only IDs and doesn't.
func TestActionSelectionTargetNeedsApproval(t *testing.T) {
	s := newMailContextService(t)
	id := addMessage(t, s, "Hi", "Bob <bob@x>", "hello", "", 1_700_000_000)
	api, hits := netServer(t, s)
	startTestPlugin(t, s, "shipper", `{"net":["`+api+`"]}`, `export default function (ctx: any) {
  ctx.actions.register({ id: "ship", label: "Ship", on: ["message", "selection"], run: async (t: any) => {
    await ctx.net.fetch("http://`+api+`/up")
    return JSON.stringify(t, Object.keys(t).sort())
  } })
}`)
	_, err := s.RunAction(t.Context(), "shipper/ship", Target{Kind: "selection", ID: id, Text: "secret mail text"}, RunActionOptions{})
	var ea *EgressApprovalRequired
	require.ErrorAs(t, err, &ea)
	assert.Equal(t, []string{api}, ea.Hosts)
	assert.Zero(t, hits.Load())

	res, err := s.RunAction(t.Context(), "shipper/ship", Target{Kind: "message", ID: id}, RunActionOptions{})
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf(`{"id":%d,"kind":"message"}`, id), res.Text)
	assert.Equal(t, int64(1), hits.Load())
}

const cardPlugin = `export default function (ctx: any) {
  ctx.actions.register({ id: "pay", label: "Pay", on: ["message"], run: () => "paid" })
  ctx.cards.register({
    match: (m: any) => m.subject.includes("Invoice"),
    render: async (m: any) => ({ title: "Invoice", fields: [{ label: "From", value: m.from }],
      buttons: [{ label: "Copy", kind: "copy", value: "INV-1" }, { label: "Open", kind: "open-url", value: "https://pay.example/inv" },
        { label: "Pay", kind: "run-action", value: "pay" }] }),
  })
}`

func TestCardsRendered(t *testing.T) {
	s := newMailContextService(t)
	inv := addMessage(t, s, "Invoice 42", "Shop <shop@x>", "pay up", "", 1_700_000_000)
	other := addMessage(t, s, "Lunch", "Bob <bob@x>", "noon", "", 1_700_000_000)
	startTestPlugin(t, s, "billing", `{"mail":["read"]}`, cardPlugin)

	cards, err := s.PluginCards(t.Context(), inv)
	require.NoError(t, err)
	assert.Equal(t, []Card{{Plugin: "billing", Title: "Invoice", Fields: []CardField{{Label: "From", Value: "Shop <shop@x>"}},
		Buttons: []CardButton{{Label: "Copy", Kind: "copy", Value: "INV-1"}, {Label: "Open", Kind: "open-url", Value: "https://pay.example/inv"},
			{Label: "Pay", Kind: "run-action", Value: "billing/pay"}}}}, cards)
	cards, err = s.PluginCards(t.Context(), other)
	require.NoError(t, err)
	assert.Empty(t, cards)
	assert.Zero(t, pluginFailures(t, s, "billing"))
}

func TestCardInvalidButtonDropped(t *testing.T) {
	s := newMailContextService(t)
	id := addMessage(t, s, "Invoice", "Shop <shop@x>", "pay", "", 1_700_000_000)
	startTestPlugin(t, s, "bad", `{"mail":["read"]}`, `export default function (ctx: any) {
  let n = 0
  const buttons = [[{ label: "Go", kind: "open-url", value: "javascript:alert(1)" }],
    [{ label: "Run", kind: "run-action", value: "missing" }]]
  ctx.cards.register({ match: () => true, render: () => ({ title: "T", fields: [], buttons: buttons[n++] }) })
}`)
	startTestPlugin(t, s, "good", `{"mail":["read"]}`, `export default function (ctx: any) {
  ctx.cards.register({ match: () => true, render: () => ({ title: "ok" }) })
}`)
	for i := range 2 {
		cards, err := s.PluginCards(t.Context(), id)
		require.NoError(t, err)
		assert.Equal(t, []Card{{Plugin: "good", Title: "ok"}}, cards, "the invalid card is dropped")
		assert.Equal(t, int64(i+1), pluginFailures(t, s, "bad"))
	}
}

func TestCardCannotWrite(t *testing.T) {
	s := newMailContextService(t)
	id := addMessage(t, s, "Hi", "Bob <bob@x>", "hello", "", 1_700_000_000)
	startTestPlugin(t, s, "writer", `{"mail":["read","write"]}`, `export default function (ctx: any) {
  ctx.cards.register({ match: () => true, render: async (m: any) => {
    try { await ctx.mail.setFlags([m.id], ["\\Seen"], []); return { title: "wrote" } }
    catch (e: any) { return { title: e.message } }
  } })
}`)
	cards, err := s.PluginCards(t.Context(), id)
	require.NoError(t, err)
	require.Len(t, cards, 1)
	assert.Contains(t, cards[0].Title, "permission denied: mail:write")
	rows, err := s.db.Query(t.Context(), "SELECT flags, (SELECT COUNT(*) FROM ops) FROM messages WHERE id = ?", id)
	require.NoError(t, err)
	assert.Equal(t, []any{"", int64(0)}, rows.Values[0], "nothing written")
}

func TestCardCannotFetch(t *testing.T) {
	s := newMailContextService(t)
	id := addMessage(t, s, "Hi", "Bob <bob@x>", "hello", "", 1_700_000_000)
	api, hits := netServer(t, s)
	startTestPlugin(t, s, "leaky", `{"mail":["read"],"net":["`+api+`"]}`, `export default function (ctx: any) {
  const leak = async () => { try { await ctx.net.fetch("http://`+api+`/x"); return "sent" } catch (e: any) { return e.message } }
  ctx.cards.register({ match: async () => (await leak()) !== "sent", render: async () => ({ title: await leak() }) })
}`)
	cards, err := s.PluginCards(t.Context(), id)
	require.NoError(t, err)
	require.Len(t, cards, 1)
	assert.Contains(t, cards[0].Title, "permission denied: net:"+api)
	assert.Zero(t, hits.Load(), "no network")
	rows, err := s.db.Query(t.Context(), "SELECT destination, decision, reason FROM audit_log WHERE tool = 'plugin:leaky:net.fetch'")
	require.NoError(t, err)
	assert.Equal(t, [][]any{{api, "deny", "host_not_allowed"}, {api, "deny", "host_not_allowed"}}, rows.Values)
}

// heldClock holds every 5 s timer (card and beforeSend limits) until fire;
// nothing else ever fires.
type heldClock struct {
	mu   sync.Mutex
	held []func()
}

func (c *heldClock) AfterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if d == 5*time.Second {
		c.held = append(c.held, f)
	}
	return func() bool { return false }
}

func (c *heldClock) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.held)
}

func (c *heldClock) fire(i int) {
	c.mu.Lock()
	f := c.held[i]
	c.mu.Unlock()
	f()
}

// match and render share one 5 s budget: it is armed before match and, once
// render runs, ending it alone (not either call's own limit) stops the card.
func TestCardTimeout(t *testing.T) {
	clock := &heldClock{}
	testPluginClock = clock
	t.Cleanup(func() { testPluginClock = nil })
	s := newMailContextService(t)
	id := addMessage(t, s, "Hi", "Bob <bob@x>", "hello", "", 1_700_000_000)
	startTestPlugin(t, s, "slow", `{"mail":["read"]}`, `export default function (ctx: any) {
  ctx.cards.register({ match: () => true, render: () => new Promise(() => {}) })
}`)
	type result struct {
		cards []Card
		err   error
	}
	done := make(chan result, 1)
	go func() {
		c, err := s.PluginCards(t.Context(), id)
		done <- result{c, err}
	}()
	// The budget, match's limit, render's limit.
	require.Eventually(t, func() bool { return clock.count() == 3 }, 5*time.Second, 5*time.Millisecond)
	clock.fire(0)
	select {
	case r := <-done:
		require.NoError(t, r.err)
		assert.Empty(t, r.cards)
	case <-time.After(5 * time.Second):
		t.Fatal("the card budget did not stop render")
	}
	assert.Equal(t, int64(1), pluginFailures(t, s, "slow"), "a timeout counts")
}

// Fix round 1.

// A matched card whose render is not a valid card fails and counts; a card
// that does not match is neutral (no failure, no reset); a valid one resets.
func TestCardMatchedInvalidRenderFails(t *testing.T) {
	s := newMailContextService(t)
	id := addMessage(t, s, "Invoice", "Shop <shop@x>", "pay", "", 1_700_000_000)
	other := addMessage(t, s, "Lunch", "Bob <bob@x>", "noon", "", 1_700_000_000)
	startTestPlugin(t, s, "strict", `{"mail":["read"]}`, `export default function (ctx: any) {
  let n = 0
  const renders: any[] = [null, undefined, {}, { title: "" }, { title: 7 }, { title: "t", fields: [{ label: "x" }] },
    { title: "t", fields: [{ label: "x", value: 1 }] }, { title: "t", buttons: [{ label: "b", value: "v" }] },
    { title: "t", buttons: [{ label: "b", kind: "copy" }] }, { title: "t", fields: {} }, "title",
    { title: "t", fields: null }, { title: "t", buttons: null }, { title: "t", buttons: "copy" }, { title: "t", fields: 3 }]
  ctx.cards.register({ match: (m: any) => m.subject === "Invoice", render: () => n < renders.length ? renders[n++] : { title: "fine" } })
}`)
	// Keep the plugin enabled across more than three failures. Absent fields and
	// buttons are fine (the last, valid card); null or non-arrays are not.
	_, err := s.db.Execute(t.Context(), `CREATE TRIGGER keep_enabled BEFORE UPDATE OF enabled ON plugins BEGIN SELECT RAISE(IGNORE); END`)
	require.NoError(t, err)
	const invalid = 15
	for i := range invalid {
		cards, err := s.PluginCards(t.Context(), id)
		require.NoError(t, err)
		assert.Empty(t, cards, "render %d", i)
		assert.Equal(t, int64(i+1), pluginFailures(t, s, "strict"), "render %d counts", i)
	}
	cards, err := s.PluginCards(t.Context(), other)
	require.NoError(t, err)
	assert.Empty(t, cards)
	assert.Equal(t, int64(invalid), pluginFailures(t, s, "strict"), "no match is neutral")
	cards, err = s.PluginCards(t.Context(), id)
	require.NoError(t, err)
	assert.Equal(t, []Card{{Plugin: "strict", Title: "fine"}}, cards)
	assert.Zero(t, pluginFailures(t, s, "strict"), "a valid card ends the run of failures")
}

// A failure-accounting write that fails is reported, not swallowed.
func TestPluginAccountingErrorsReturned(t *testing.T) {
	s := newMailContextService(t)
	id := addMessage(t, s, "Hi", "Bob <bob@x>", "hello", "", 1_700_000_000)
	startTestPlugin(t, s, "acct", `{"mail":["read"]}`, `export default function (ctx: any) {
  ctx.actions.register({ id: "ok", label: "OK", on: ["message"], run: () => "ok" })
  ctx.actions.register({ id: "boom", label: "Boom", on: ["message"], run: () => { throw new Error("boom") } })
  ctx.cards.register({ match: () => true, render: () => ({ title: "" }) })
}`)
	_, err := s.db.Execute(t.Context(), "UPDATE plugins SET failures = 1 WHERE name = 'acct'")
	require.NoError(t, err)
	_, err = s.db.Execute(t.Context(), `CREATE TRIGGER broken BEFORE UPDATE OF failures ON plugins BEGIN SELECT RAISE(ABORT, 'accounting broken'); END`)
	require.NoError(t, err)

	_, err = s.RunAction(t.Context(), "acct/ok", Target{Kind: "message", ID: id}, RunActionOptions{})
	require.ErrorContains(t, err, "accounting broken", "success accounting")
	_, err = s.RunAction(t.Context(), "acct/boom", Target{Kind: "message", ID: id}, RunActionOptions{})
	require.ErrorContains(t, err, "boom")
	require.ErrorContains(t, err, "accounting broken", "failure accounting")
	cards, err := s.PluginCards(t.Context(), id)
	require.ErrorContains(t, err, "accounting broken")
	assert.Empty(t, cards)
}

// Card and action text shown to the user has secrets redacted and each run
// of control characters as one space.
func TestPluginTextCleaned(t *testing.T) {
	s := newMailContextService(t)
	id := addMessage(t, s, "Hi", "Bob <bob@x>", "hello", "", 1_700_000_000)
	s.secrets.Add("hunter2-secret")
	startTestPlugin(t, s, "talker", `{"mail":["read"]}`, `export default function (ctx: any) {
  const t = "a\n\r\tb hunter2-secret"
  ctx.actions.register({ id: "say", label: "Say", on: ["message"], run: () => ({ text: t }) })
  ctx.actions.register({ id: "plain", label: "Plain", on: ["message"], run: () => t })
  ctx.actions.register({ id: "lines", label: "Lines", on: ["message"], run: () => "line 1\n\tline 2\n\nline 3\u0000\u0007x" })
  ctx.cards.register({ match: () => true, render: () => ({ title: t, fields: [{ label: t, value: t }], buttons: [{ label: t, kind: "copy", value: "v" }] }) })
}`)
	want := "a b [redacted]"
	for _, a := range []string{"talker/say", "talker/plain"} {
		res, err := s.RunAction(t.Context(), a, Target{Kind: "message", ID: id}, RunActionOptions{})
		require.NoError(t, err)
		assert.Equal(t, "a\n \tb [redacted]", res.Text, "action text keeps newlines and tabs")
	}
	res, err := s.RunAction(t.Context(), "talker/lines", Target{Kind: "message", ID: id}, RunActionOptions{})
	require.NoError(t, err)
	assert.Equal(t, "line 1\n\tline 2\n\nline 3 x", res.Text)
	cards, err := s.PluginCards(t.Context(), id)
	require.NoError(t, err)
	assert.Equal(t, []Card{{Plugin: "talker", Title: want, Fields: []CardField{{Label: want, Value: want}},
		Buttons: []CardButton{{Label: want, Kind: "copy", Value: "v"}}}}, cards)
}

func TestCardLimits(t *testing.T) {
	s := newMailContextService(t)
	id := addMessage(t, s, "Hi", "Bob <bob@x>", "hello", "", 1_700_000_000)
	startTestPlugin(t, s, "big", `{"mail":["read"]}`, `export default function (ctx: any) {
  const f = (n: number) => Array.from({ length: n }, () => ({ label: "l", value: "v" }))
  const b = (n: number) => Array.from({ length: n }, () => ({ label: "l", kind: "copy", value: "v" }))
  const renders = [{ title: "x".repeat(1000), fields: f(20), buttons: b(5) }, { title: "é".repeat(1000) },
    { title: "x", fields: f(21) }, { title: "x", buttons: b(6) }, { title: "x".repeat(1001) },
    { title: "x", fields: [{ label: "l", value: "x".repeat(1001) }] }, { title: "x", buttons: [{ label: "x".repeat(1001), kind: "copy", value: "v" }] }]
  let n = 0
  ctx.cards.register({ match: () => true, render: () => renders[n++] })
}`)
	_, err := s.db.Execute(t.Context(), `CREATE TRIGGER keep_enabled BEFORE UPDATE OF enabled ON plugins BEGIN SELECT RAISE(IGNORE); END`)
	require.NoError(t, err)
	for i, ok := range []bool{true, true, false, false, false, false, false} {
		cards, err := s.PluginCards(t.Context(), id)
		require.NoError(t, err)
		if ok {
			require.Len(t, cards, 1, "render %d accepted", i)
		} else {
			assert.Empty(t, cards, "render %d rejected", i)
		}
	}
	assert.Equal(t, int64(5), pluginFailures(t, s, "big"))
}
