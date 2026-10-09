package coretest_test

import (
	"os"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	coretest.ServeMCPIfChild()
	os.Exit(m.Run())
}

func TestCoretestDeliverAndSend(t *testing.T) {
	e := coretest.New(t)
	acc := e.AddAccount(t, "alice@example.com")
	other := e.AddAccount(t, "bob@example.com")
	assert.True(t, acc.IsDefault)
	assert.False(t, other.IsDefault)
	ih, ip, sh, sp := e.Endpoints(acc.ID)
	assert.Equal(t, "127.0.0.1", ih)
	assert.NotZero(t, ip)
	assert.Equal(t, "127.0.0.1", sh)
	assert.NotEqual(t, ip, sp)

	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("Carol <carol@example.com>", "alice@example.com", "你好", "hello there",
		coretest.MessageID("m1@x"), coretest.HTML("<p>hello <b>there</b></p>"),
		coretest.Attachment("a.txt", "text/plain", []byte("abc"))))
	page, err := e.Svc.ListThreads(t.Context(), core.View{Unified: "inbox"}, "", 0)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, "你好", page.Items[0].Subject)
	assert.True(t, page.Items[0].HasAttachments)

	d, err := e.Svc.NewDraft(t.Context(), acc.ID, "new", 0)
	require.NoError(t, err)
	d.To, d.Subject, d.HTML = []string{"dave@example.com"}, "out", "<p>hi</p>"
	_, err = e.Svc.SaveDraft(t.Context(), d)
	require.NoError(t, err)
	require.NoError(t, e.Svc.Send(t.Context(), d.ID))
	require.Eventually(t, func() bool { return len(e.Sent(acc.ID)) == 1 }, 5*time.Second, 20*time.Millisecond)
	assert.Contains(t, string(e.Sent(acc.ID)[0]), "Subject: out")
	assert.Empty(t, e.Sent(other.ID))
}

func TestCoretestLLM(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	l := e.LLM(t)
	st, err := e.Svc.GetSettings(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "test/m1", st.DefaultModel)
	acc := e.AddAccount(t, "alice@example.com")
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Plans", "Meet on Friday."))
	page, err := e.Svc.ListMessages(t.Context(), core.View{Unified: "inbox"}, "", 0)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)

	l.Script(coretest.Turn{Text: []string{"Friday ", "meeting"}})
	ch, err := e.Svc.RunSkill(t.Context(), "summarize", core.Target{Kind: "message", ID: page.Items[0].ID})
	require.NoError(t, err)
	var text string
	for ev := range ch {
		text += ev.Text
	}
	assert.Contains(t, text, "Friday meeting")
	assert.Equal(t, 1, l.Requests())
	var log []coretest.Request = l.RequestLog()
	require.Len(t, log, 1)
	assert.Equal(t, "m1", log[0].Model)
	assert.Equal(t, "/chat/completions", log[0].Path)
	assert.Contains(t, string(log[0].Body), `"model":"m1"`)
	l.Release()
}

func TestCoretestPluginFixture(t *testing.T) {
	dir := coretest.PluginFixture(t, "deepl-translate") // from core/plugins/testdata
	assert.FileExists(t, dir+"/plugin.json")

	e := coretest.New(t, coretest.WithoutAutoSync())
	acc := e.AddAccount(t, "alice@example.com")
	for _, n := range []string{"config-demo", "egress-action", "card-demo", "send-warn", "send-block"} {
		e.InstallPlugin(t, n)
	}
	list, err := e.Svc.ListPlugins(t.Context())
	require.NoError(t, err)
	require.Len(t, list, 5)
	for _, p := range list {
		assert.True(t, p.Enabled, p.Name)
	}
	cfg, err := e.Svc.GetPluginConfig(t.Context(), "config-demo")
	require.NoError(t, err)
	var keys []string
	for _, c := range cfg {
		keys = append(keys, c.Key)
	}
	assert.ElementsMatch(t, []string{"apiKey", "plan", "endpoint"}, keys)

	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("shop@example.com", "alice@example.com", "Invoice 7", "pay"))
	page, err := e.Svc.ListMessages(t.Context(), core.View{Unified: "inbox"}, "", 0)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	id := page.Items[0].ID

	cards, err := e.Svc.PluginCards(t.Context(), id)
	require.NoError(t, err)
	require.Len(t, cards, 1)
	var kinds []string
	for _, b := range cards[0].Buttons {
		kinds = append(kinds, b.Kind)
	}
	assert.Equal(t, []string{"copy", "open-url"}, kinds)

	_, err = e.Svc.RunAction(t.Context(), "egress-action/ping", core.Target{Kind: "message", ID: id}, core.RunActionOptions{})
	var ea *core.EgressApprovalRequired
	require.ErrorAs(t, err, &ea)
	assert.Zero(t, e.EgressHits())
	res, err := e.Svc.RunAction(t.Context(), "egress-action/ping", core.Target{Kind: "message", ID: id}, core.RunActionOptions{ApproveEgress: true})
	require.NoError(t, err)
	assert.Equal(t, "pong", res.Text)
	assert.Equal(t, 1, e.EgressHits())

	d, err := e.Svc.SaveDraft(t.Context(), core.Draft{AccountID: acc.ID, Mode: "new", To: []string{"x@example.com"}, Subject: "s", HTML: "<p>x</p>"})
	require.NoError(t, err)
	err = e.Svc.Send(t.Context(), d.ID)
	var be *core.SendBlockedError
	require.ErrorAs(t, err, &be)
	assert.Equal(t, "send-block", be.Plugin)
	require.NoError(t, e.Svc.SetPluginEnabled(t.Context(), "send-block", false))
	err = e.Svc.Send(t.Context(), d.ID)
	var we *core.SendWarningsError
	require.ErrorAs(t, err, &we)
	require.Len(t, we.Check.Warnings, 1)
	assert.Equal(t, "send-warn", we.Check.Warnings[0].Plugin)
}

func TestCoretestMCP(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	cmd, args, env := coretest.MCPCommand(t)
	srv, err := e.Svc.AddMCPServer(t.Context(), core.MCPServerInput{Name: "fake", Transport: "stdio", Command: cmd, Args: args, Env: env})
	require.NoError(t, err)
	require.NoError(t, e.Svc.GrantMCPServer(t.Context(), srv.ID))
	require.NoError(t, e.Svc.SetMCPServerEnabled(t.Context(), srv.ID, true))
	list, err := e.Svc.ListMCPServers(t.Context())
	require.NoError(t, err)
	require.Len(t, list, 1)
	var names []string
	for _, tl := range list[0].Tools {
		names = append(names, tl.Name)
	}
	assert.Equal(t, []string{"lookup"}, names, list[0].Error)
	assert.True(t, list[0].Tools[0].ReadOnlyHint)
}

func TestCoretestMsg(t *testing.T) {
	when := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	raw := string(coretest.Msg("a@x", "b@x", "s", "body", coretest.InReplyTo("p@x"), coretest.MessageID("m@x"), coretest.Date(when)))
	for _, want := range []string{"Message-ID: <m@x>", "In-Reply-To: <p@x>", "Date: Thu, 01 Oct 2026 09:00:00 +0000", "From: a@x", "To: b@x"} {
		assert.Contains(t, raw, want)
	}
	assert.Contains(t, raw, "\r\n\r\n")
}

func TestCoretestStartServers(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	ih, ip, sh, sp, user, pass := e.StartServers(t)
	acc, err := e.Svc.AddAccount(t.Context(), core.AccountInput{Email: "new@example.com", Username: user, Password: pass,
		IMAPHost: ih, IMAPPort: ip, IMAPSecurity: core.SecurityNone, SMTPHost: sh, SMTPPort: sp, SMTPSecurity: core.SecurityNone})
	require.NoError(t, err)
	assert.Equal(t, "new@example.com", acc.Email)
}
