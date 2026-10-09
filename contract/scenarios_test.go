package contract_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/godwit-email/godwit/contract"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/events"
	"github.com/stretchr/testify/require"
)

var (
	day1 = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	day2 = time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
)

func firstMessage(t *testing.T, api contract.API) core.MessageSummary {
	t.Helper()
	page, err := api.ListMessages(t.Context(), core.View{Unified: "inbox"}, "", 0)
	require.NoError(t, err)
	require.NotEmpty(t, page.Items)
	return page.Items[len(page.Items)-1]
}

// waitOutbox syncs (the outbox is worked off by sync, and the Env has no
// background sync) until the account's outbox is empty, and returns the last list.
func waitOutbox(t *testing.T, api contract.API, env *coretest.Env, accountID int64) []core.OutboxItem {
	t.Helper()
	var list []core.OutboxItem
	require.Eventually(t, func() bool {
		require.NoError(t, env.Svc.SyncNow(t.Context(), accountID))
		var err error
		list, err = api.ListOutbox(t.Context(), accountID)
		require.NoError(t, err)
		return len(list) == 0
	}, 10*time.Second, 20*time.Millisecond)
	return list
}

func TestAccounts(t *testing.T) {
	runContract(t, func(t *testing.T, api contract.API, env *coretest.Env) any {
		ctx := t.Context()
		ih, ip, sh, sp, user, pass := env.StartServers(t)
		in := core.AccountInput{Email: "new@example.com", Name: "New", Username: user, Password: "wrong",
			IMAPHost: ih, IMAPPort: ip, IMAPSecurity: core.SecurityNone, SMTPHost: sh, SMTPPort: sp, SMTPSecurity: core.SecurityNone}
		// The fake servers' ports differ per Env: compare whether they came back.
		ports := func(a core.Account) core.Account {
			if a.IMAPPort == ip && a.SMTPPort == sp {
				a.IMAPPort, a.SMTPPort = -1, -1
			}
			return a
		}
		out := map[string]any{}
		out["addWrong"] = res(api.AddAccount(ctx, in))
		out["checkWrong"] = res(api.CheckAccount(ctx, in))
		flow := in
		flow.OAuthFlowID = "no-such-flow"
		out["addUnknownFlow"] = res(api.AddAccount(ctx, flow))
		in.Password = pass
		out["check"] = res(api.CheckAccount(ctx, in))
		acc, err := api.AddAccount(ctx, in)
		out["add"] = res(ports(acc), err)
		list, err := api.ListAccounts(ctx)
		for i := range list {
			list[i] = ports(list[i])
		}
		out["list"] = res(list, err)
		out["detect"] = res2(api.DetectProvider(ctx, "someone@gmail.com"))
		out["detectBad"] = res2(api.DetectProvider(ctx, "not an address"))
		return out
	})
}

func TestMailRead(t *testing.T) {
	runContract(t, func(t *testing.T, api contract.API, env *coretest.Env) any {
		ctx := t.Context()
		acc := env.AddAccount(t, "alice@example.com")
		env.Deliver(t, acc.ID, "INBOX", coretest.Msg("Bob <bob@example.com>", "alice@example.com", "Plans", "Meet on Friday.",
			coretest.MessageID("m1@contract"), coretest.Date(day1), coretest.HTML("<p>Meet on <b>Friday</b>.</p>"),
			coretest.Attachment("notes.txt", "text/plain", []byte("agenda: lunch"))))
		env.Deliver(t, acc.ID, "INBOX", coretest.Msg("carol@example.com", "alice@example.com", "Re: Plans", "Friday works.",
			coretest.MessageID("m2@contract"), coretest.InReplyTo("m1@contract"), coretest.Date(day2)))
		out := map[string]any{}
		out["folders"] = res(api.ListFolders(ctx, acc.ID))
		threads, err := api.ListThreads(ctx, core.View{Unified: "inbox"}, "", 0)
		require.NoError(t, err)
		require.Len(t, threads.Items, 1)
		out["threads"] = threads
		out["thread"] = res(api.GetThread(ctx, threads.Items[0].ID))
		page1, err := api.ListMessages(ctx, core.View{Unified: "inbox"}, "", 1)
		require.NoError(t, err)
		out["page1"] = page1
		out["page2"] = res(api.ListMessages(ctx, core.View{Unified: "inbox"}, page1.NextCursor, 1))
		first := firstMessage(t, api)
		out["message"] = res(api.GetMessage(ctx, first.ID, false))
		text, err := api.GetMessageText(ctx, first.ID)
		require.NoError(t, err)
		out["text"] = text
		out["search"] = res(api.Search(ctx, acc.ID, "Friday", 0))
		out["missing"] = res(api.GetMessage(ctx, 999999, false))

		require.Len(t, text.Attachments, 1)
		path, info, err := api.GetAttachment(ctx, text.Attachments[0].ID)
		require.NoError(t, err)
		data, readErr := os.ReadFile(path)
		out["attachment"] = map[string]any{"info": info, "base": filepath.Base(path), "abs": filepath.IsAbs(path),
			"content": string(data), "readErr": fmt.Sprint(readErr)}
		out["attachmentMissing"] = res2(api.GetAttachment(ctx, 999999))
		return out
	})
}

func TestCompose(t *testing.T) {
	runContract(t, func(t *testing.T, api contract.API, env *coretest.Env) any {
		ctx := t.Context()
		acc := env.AddAccount(t, "alice@example.com")
		out := map[string]any{}
		d, err := api.NewDraft(ctx, acc.ID, "new", 0)
		require.NoError(t, err)
		out["new"] = d
		d.To, d.Cc, d.Subject, d.HTML = []string{"dave@example.com"}, []string{"erin@example.com"}, "v1", "<p>hi</p>"
		d1, err := api.SaveDraft(ctx, d)
		require.NoError(t, err)
		out["save1"] = d1
		d1.Subject = "v2"
		d2, err := api.SaveDraft(ctx, d1)
		require.NoError(t, err)
		out["save2"] = d2
		d1.Subject = "v3" // still d1's revision
		out["stale"] = res(api.SaveDraft(ctx, d1))
		out["send"] = errInfo(api.Send(ctx, d2.ID))
		out["queued"] = res(api.ListOutbox(ctx, acc.ID))
		out["outbox"] = waitOutbox(t, api, env, acc.ID)
		sent := env.Sent(acc.ID)
		require.Len(t, sent, 1)
		out["sentSubject"] = strings.Contains(string(sent[0]), "Subject: v2")
		out["sendAgain"] = errInfo(api.Send(ctx, d2.ID))
		return out
	})
}

func TestSendWarnings(t *testing.T) {
	runContract(t, func(t *testing.T, api contract.API, env *coretest.Env) any {
		ctx := t.Context()
		acc := env.AddAccount(t, "alice@example.com")
		env.InstallPlugin(t, "send-warn")
		out := map[string]any{}
		d, err := api.SaveDraft(ctx, core.Draft{AccountID: acc.ID, Mode: "new", To: []string{"x@example.com"}, Subject: "Hello", HTML: "<p>x</p>"})
		require.NoError(t, err)
		err = api.Send(ctx, d.ID)
		out["send"] = errInfo(err)
		var we *core.SendWarningsError
		require.ErrorAs(t, err, &we)
		out["wrongDigest"] = errInfo(api.SendAcknowledged(ctx, d.ID, strings.Repeat("0", 64)))
		out["acknowledged"] = errInfo(api.SendAcknowledged(ctx, d.ID, we.Check.Digest))
		out["outbox"] = waitOutbox(t, api, env, acc.ID)
		out["sent"] = len(env.Sent(acc.ID))
		return out
	})
}

func TestSettings(t *testing.T) {
	runContract(t, func(t *testing.T, api contract.API, env *coretest.Env) any {
		ctx := t.Context()
		out := map[string]any{}
		before, err := api.GetSettings(ctx)
		require.NoError(t, err)
		out["before"] = before
		st := core.Settings{Theme: "dark", Density: "comfortable", FontSize: 15, BodyFontSize: 17,
			UILanguage: "en", ReadingLanguage: "de", ReplyLanguage: "reading", NoTranslate: []string{"fr", "es"},
			LaunchAtLogin: true, KeepInBackground: false, UnreadInMenuBar: true,
			NotifyNewMail: false, NotifySmartOnly: true, NotifyPreview: true,
			QuickReplies: false, Summary: false, ThreadView: false,
			DefaultModel: "svc/big", SmallModel: "svc/small"}
		bv, sv := reflect.ValueOf(before), reflect.ValueOf(st)
		for i := range sv.NumField() { // every field must make the round trip
			if reflect.DeepEqual(bv.Field(i).Interface(), sv.Field(i).Interface()) {
				t.Errorf("field %s keeps its default; change it", sv.Type().Field(i).Name)
			}
		}
		out["set"] = res(api.SetSettings(ctx, st))
		out["after"] = res(api.GetSettings(ctx))
		bad := st
		bad.Theme = "neon"
		out["invalid"] = res(api.SetSettings(ctx, bad))
		return out
	})
}

func TestModelService(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"b"},{"id":"a"}]}`)
	}))
	defer srv.Close()
	runContract(t, func(t *testing.T, api contract.API, env *coretest.Env) any {
		ctx := t.Context()
		in := core.ModelServiceInput{Name: "x", Kind: "openai", BaseURL: srv.URL, KeyAction: core.KeyReplace, APIKey: "good-key"}
		out := map[string]any{}
		out["ok"] = res(api.CheckModelService(ctx, 0, in))
		in.APIKey = "bad-key"
		out["auth"] = res(api.CheckModelService(ctx, 0, in))
		in.BaseURL = "http://example.com"
		out["scheme"] = res(api.CheckModelService(ctx, 0, in))
		in.BaseURL = srv.URL
		out["missing"] = res(api.CheckModelService(ctx, 999, in))
		return out
	})
}

func TestRunSkill(t *testing.T) {
	runContract(t, func(t *testing.T, api contract.API, env *coretest.Env) any {
		ctx := t.Context()
		l := env.LLM(t)
		defer l.Release()
		acc := env.AddAccount(t, "alice@example.com")
		env.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Plans", "Meet on Friday.",
			coretest.MessageID("m1@contract"), coretest.Date(day1)))
		msg := firstMessage(t, api)
		l.Script(coretest.Turn{Text: []string{"Friday ", "meeting"}})
		out := map[string]any{}
		ch, err := api.RunSkill(ctx, "summarize", core.Target{Kind: "message", ID: msg.ID})
		require.NoError(t, err)
		out["events"] = collect(t, ch)
		_, err = api.RunSkill(ctx, "no-such-skill", core.Target{Kind: "message", ID: msg.ID})
		out["unknown"] = errInfo(err)
		return out
	})
}

func TestAskApproval(t *testing.T) {
	runContract(t, func(t *testing.T, api contract.API, env *coretest.Env) any {
		ctx := t.Context()
		l := env.LLM(t)
		defer l.Release()
		env.AddAccount(t, "alice@example.com")
		out := map[string]any{}
		conv, err := api.NewConversation(ctx, "ask", 0)
		require.NoError(t, err)
		out["conversation"] = conv
		l.Script(coretest.Turn{Calls: []coretest.Call{{ID: "c1", Name: "create_draft",
			Args: `{"to":["x@example.com"],"subject":"s","html":"<p>x</p>"}`}}})
		ch, err := api.Ask(ctx, conv.ID, "draft one")
		require.NoError(t, err)
		asked := collect(t, ch)
		out["ask"] = asked
		require.NotEmpty(t, asked)
		require.Equal(t, "interrupted", asked[len(asked)-1].Kind)
		approvals, err := api.ListApprovals(ctx, conv.ID)
		require.NoError(t, err)
		require.Len(t, approvals, 1)
		out["approvals"] = approvals

		sub, unsub := api.SubscribeConversation(conv.ID) // before resolving, so no event is missed
		out["resolve"] = errInfo(api.ResolveApproval(ctx, approvals[0].ID, true))
		var evs []core.WitEvent
		timeout := time.After(15 * time.Second)
	read:
		for {
			select {
			case ev, ok := <-sub:
				require.True(t, ok, "subscription closed early")
				evs = append(evs, ev)
				if ev.Kind == "done" || ev.Kind == "error" || ev.Kind == "interrupted" {
					break read
				}
			case <-timeout:
				t.Fatal("no terminal event")
			}
		}
		unsub()
		out["subscribed"] = evs
		out["after"] = res(api.ListApprovals(ctx, conv.ID))
		out["resolveAgain"] = errInfo(api.ResolveApproval(ctx, approvals[0].ID, true))
		return out
	})
}

func TestEvents(t *testing.T) {
	runContract(t, func(t *testing.T, api contract.API, env *coretest.Env) any {
		acc := env.AddAccount(t, "alice@example.com")
		ch, unsub := api.Subscribe()
		env.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Plans", "Meet on Friday.",
			coretest.MessageID("m1@contract"), coretest.Date(day1)))
		var got events.Event
		timeout := time.After(10 * time.Second)
		for got.Type != events.MessageReceived {
			select {
			case ev, ok := <-ch:
				require.True(t, ok, "subscription closed early")
				got = ev
			case <-timeout:
				t.Fatal("no message.received")
			}
		}
		unsub()
		for range ch { // unsubscribing closes the channel
		}
		msg := firstMessage(t, api)
		return map[string]any{"event": got, "messageID": msg.ID, "threadID": msg.ThreadID, "folderID": msg.FolderID}
	})
}

func TestPluginAction(t *testing.T) {
	runContract(t, func(t *testing.T, api contract.API, env *coretest.Env) any {
		ctx := t.Context()
		acc := env.AddAccount(t, "alice@example.com")
		env.InstallPlugin(t, "egress-action")
		env.Deliver(t, acc.ID, "INBOX", coretest.Msg("shop@example.com", "alice@example.com", "Invoice 7", "pay",
			coretest.MessageID("m1@contract"), coretest.Date(day1)))
		target := core.Target{Kind: "message", ID: firstMessage(t, api).ID}
		out := map[string]any{}
		out["unapproved"] = res(api.RunAction(ctx, "egress-action/ping", target, core.RunActionOptions{}))
		out["hitsBefore"] = env.EgressHits()
		out["approved"] = res(api.RunAction(ctx, "egress-action/ping", target, core.RunActionOptions{ApproveEgress: true}))
		out["hitsAfter"] = env.EgressHits()
		out["unknown"] = res(api.RunAction(ctx, "nope/none", target, core.RunActionOptions{}))
		return out
	})
}

func TestOAuth(t *testing.T) {
	old := auth.MicrosoftClientID
	auth.MicrosoftClientID = "contract-client" // before any Env starts, so no goroutine races it
	defer func() { auth.MicrosoftClientID = old }()
	runContract(t, func(t *testing.T, api contract.API, env *coretest.Env) any {
		ctx := t.Context()
		out := map[string]any{}
		flowID, authURL, err := api.BeginOAuth(ctx, "outlook", "a@outlook.com")
		require.NoError(t, err)
		u, err := url.Parse(authURL)
		require.NoError(t, err)
		var params []string
		for k := range u.Query() {
			params = append(params, k)
		}
		sort.Strings(params)
		out["begin"] = map[string]any{"flowIDLen": len(flowID), "url": u.Scheme + "://" + u.Host + u.Path, "params": params,
			"clientID": u.Query().Get("client_id"), "loginHint": u.Query().Get("login_hint")}
		api.CancelOAuth(flowID)
		api.CancelOAuth("unknown-flow")
		out["notOAuth"] = res2(api.BeginOAuth(ctx, "gmail", ""))
		return out
	})
}

func TestOAuthNotConfigured(t *testing.T) {
	require.Empty(t, auth.MicrosoftClientID)
	runContract(t, func(t *testing.T, api contract.API, env *coretest.Env) any {
		return res2(api.BeginOAuth(t.Context(), "outlook", ""))
	})
}
