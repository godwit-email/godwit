package contract_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godwit-email/godwit/contract"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/ai/mask"
	"github.com/godwit-email/godwit/core/ai/mask/guard"
	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/internal/imagecache"
	"github.com/stretchr/testify/assert"
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

// waitOutbox lets the undo window pass and syncs (the outbox is worked off by
// sync, and the Env has no background sync) until the account's outbox is
// empty, and returns the last list.
func waitOutbox(t *testing.T, api contract.API, env *coretest.Env, accountID int64) []core.OutboxItem {
	t.Helper()
	env.AdvanceClock(core.UndoSendWindow)
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
			Receiving: core.Server{Host: ih, Port: ip, Security: core.SecurityNone}, Sending: core.Server{Host: sh, Port: sp, Security: core.SecurityNone}}
		// The fake servers' ports differ per Env: compare whether they came back.
		ports := func(a core.Account) core.Account {
			if a.Receiving.Port == ip && a.Sending.Port == sp {
				a.Receiving.Port, a.Sending.Port = -1, -1
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
		queued, err := api.ListOutbox(ctx, acc.ID)
		require.NoError(t, err)
		require.Len(t, queued, 1)
		out["cancel"] = errInfo(api.CancelOutbox(ctx, queued[0].ID)) // undo send: the draft is back
		out["canceled"] = res(api.ListOutbox(ctx, acc.ID))
		out["sendBack"] = errInfo(api.Send(ctx, d2.ID))
		out["outbox"] = waitOutbox(t, api, env, acc.ID)
		out["sendAgain"] = errInfo(api.Send(ctx, d2.ID))

		// A reply to mail with a generated Message-ID: the product generates the
		// reply's own and threads it to the incoming one.
		raw := coretest.Msg("bob@example.com", "alice@example.com", "Plans", "Meet on Friday.", coretest.Date(day1))
		out["incoming"] = headers(t, raw)
		env.Deliver(t, acc.ID, "INBOX", raw)
		r, err := api.NewDraft(ctx, acc.ID, "reply", firstMessage(t, api).ID)
		require.NoError(t, err)
		out["reply"] = r
		r.HTML = "<p>Friday works.</p>"
		r, err = api.SaveDraft(ctx, r)
		require.NoError(t, err)
		out["sendReply"] = errInfo(api.Send(ctx, r.ID))
		out["outboxAfterReply"] = waitOutbox(t, api, env, acc.ID)
		var sent []any
		for _, m := range env.Sent(acc.ID) {
			sent = append(sent, headers(t, m))
		}
		out["sent"] = sent
		return out
	})
}

// headers are a raw message's identifying headers.
func headers(t *testing.T, raw []byte) map[string]string {
	t.Helper()
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	require.NoError(t, err)
	return map[string]string{"MessageID": m.Header.Get("Message-ID"), "InReplyTo": m.Header.Get("In-Reply-To"),
		"References": m.Header.Get("References"), "Subject": m.Header.Get("Subject"), "To": m.Header.Get("To"),
		"Cc": m.Header.Get("Cc"), "From": m.Header.Get("From")}
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
	logged := runContract(t, func(t *testing.T, api contract.API, env *coretest.Env) any {
		ctx := t.Context()
		env.LLM(t).Release() // a model service whose key the server knows to redact
		out := map[string]any{}
		before, err := api.GetSettings(ctx)
		require.NoError(t, err)
		out["before"] = before
		st := core.Settings{Theme: "dark", Density: "comfortable", FontSize: 15, BodyFontSize: 17, ImageRetentionDays: 14,
			SidebarOrder: "accounts,labels,views", ListRowOrder: core.ListRowSubject, ReaderWidth: core.ReaderWidthWide,
			SecurityAI: true, RuleAI: true,
			UILanguage: "en", ReadingLanguage: "de", ReplyLanguage: "reading", NoTranslate: []string{"fr", "es"},
			LaunchAtLogin: true, BackgroundReceive: core.BackgroundReceiveStopOnClose, ShowTrayIcon: false, UnreadInMenuBar: true,
			NotifyNewMail: false, NotifyPreview: false,
			QuickReplies: true, Summary: true, ThreadView: false,
			DefaultModel: "svc/big", SmallModel: "svc/small", JudgeService: "judge", JudgeThreshold: 0.85, Translator: "plugin/dlx", LocalAPI: true, ExperimentalJMAP: true, ExperimentalEWS: true, ExperimentalPOP3: true, ExperimentalGraph: true, ExperimentalGmailAPI: true, SidebarWidth: 320, ListWidth: 400, SignatureDelimiter: true,
			MaskCategories: core.MaskCategories{Names: core.MaskCategory{On: true, Action: core.MaskWarn}},
			MaskWords:      []string{"Falcon"}, MaskWordsCase: true, MaskWordsWhole: true,
			MaskRules: []mask.Rule{{Kind: "EMPLOYEE_ID", Regex: `E\d{6}`}}, MaskAllow: []string{"GitHub"}, RecognitionModel: true,
			ProxyMode: core.ProxyCustom, ProxyURL: "socks5://127.0.0.1:7890", ProxyBypass: []string{"*.corp.example"},
			ProxyUser: "alice", ProxyPassword: "pr0xy-pass", ProxyPasswordSet: true}
		// 识别模型 turns on only with its files here, as a download leaves them
		models := filepath.Join(env.DataDir, "cache", "guard", guard.Revision)
		require.NoError(t, os.MkdirAll(models, 0o700))
		for _, f := range guard.Files {
			require.NoError(t, os.WriteFile(filepath.Join(models, f), nil, 0o600))
		}
		bv, sv := reflect.ValueOf(before), reflect.ValueOf(st)
		for i := range sv.NumField() { // every field must make the round trip
			if reflect.DeepEqual(bv.Field(i).Interface(), sv.Field(i).Interface()) {
				t.Errorf("field %s keeps its default; change it", sv.Type().Field(i).Name)
			}
		}
		require.NoError(t, api.TrustImageSender(ctx, "Bob <bob@example.com>"))
		require.NoError(t, api.BlockSender(ctx, "Spammer <spammer@example.com>"))
		require.NoError(t, env.Svc.SetPluginEnabled(ctx, core.SignInCodesPlugin, false))
		out["set"] = res(api.SetSettings(ctx, st))
		out["after"] = res(api.GetSettings(ctx))
		out["trusted"] = res(api.TrustedImageSenders(ctx)) // saving the settings leaves the lists alone
		out["blocked"] = res(api.BlockedSenders(ctx))
		require.NoError(t, api.UntrustImageSender(ctx, "bob@example.com"))
		require.NoError(t, api.UnblockSender(ctx, "spammer@example.com"))
		out["untrusted"] = res(api.TrustedImageSenders(ctx))
		out["unblocked"] = res(api.BlockedSenders(ctx))
		bad := st
		bad.Theme = "neon"
		out["invalid"] = res(api.SetSettings(ctx, bad))
		bad.Theme = "test-key" // the model service's API key: logged, so redacted
		out["invalidSecret"] = res(api.SetSettings(ctx, bad))
		return out
	})
	assert.Contains(t, logged, `invalid theme \"[redacted]\"`)
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
		judge := core.ModelServiceInput{Name: "ts", Provider: core.ProviderTypeSafe, JudgeFormat: "systemone", BaseURL: srv.URL,
			KeyAction: core.KeyReplace, APIKey: "good-key"}
		out["judgeOnly"] = res(api.CheckModelService(ctx, 0, judge))
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
		core.WithUISession(ctx, env.Svc) // an interface to take the approval (ErrNoUI)
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

// With no interface connected, a Wit run and an action needing approval are
// refused before they start; an action needing none runs.
func TestApprovalsNeedUI(t *testing.T) {
	runContract(t, func(t *testing.T, api contract.API, env *coretest.Env) any {
		ctx := core.WithExternalSource(t.Context(), core.AuditLocalAPI) // as the Local API marks it
		acc := env.AddAccount(t, "alice@example.com")
		env.InstallPlugin(t, "egress-action")
		env.Deliver(t, acc.ID, "INBOX", coretest.Msg("shop@example.com", "alice@example.com", "Invoice 7", "pay",
			coretest.MessageID("m1@contract"), coretest.Date(day1)))
		conv, err := api.NewConversation(ctx, "ask", 0)
		require.NoError(t, err)
		_, err = api.Ask(ctx, conv.ID, "draft one")
		out := map[string]any{"ask": errInfo(err)}
		target := core.Target{Kind: "message", ID: firstMessage(t, api).ID}
		out["action"] = res(api.RunAction(ctx, "egress-action/ping", target, core.RunActionOptions{ApproveEgress: true}))
		out["hits"] = env.EgressHits()
		return out
	})
}

func TestEvents(t *testing.T) {
	runContract(t, func(t *testing.T, api contract.API, env *coretest.Env) any {
		acc := env.AddAccount(t, "alice@example.com")
		require.NoError(t, env.Svc.SyncNow(t.Context(), acc.ID)) // the first sync is not new mail
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
		core.WithUISession(ctx, env.Svc) // an interface to take the approval (ErrNoUI)
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
	const flowFormat, b64Format = `^[A-Z2-7]{26}$`, `^[A-Za-z0-9_-]{43}$` // rand.Text; 32 bytes, base64url
	runContract(t, func(t *testing.T, api contract.API, env *coretest.Env) any {
		ctx := t.Context()
		out := map[string]any{}
		flowID, authURL, err := api.BeginOAuth(ctx, "outlook", "a@outlook.com", core.OAuthClient{})
		require.NoError(t, err)
		u, err := url.Parse(authURL)
		require.NoError(t, err)
		params := map[string]any{}
		for k, vs := range u.Query() {
			require.Len(t, vs, 1, k)
			params[k] = vs[0]
		}
		// Random per flow: compared by format and association.
		q := u.Query()
		params["state"] = opaque(b64Format, q.Get("state"))
		params["code_challenge"] = opaque(b64Format, q.Get("code_challenge"))
		redirect, err := url.Parse(q.Get("redirect_uri"))
		require.NoError(t, err)
		params["redirect_uri"] = map[string]any{"url": redirect.Scheme + "://" + redirect.Hostname() + redirect.Path,
			"port": opaque(`^[0-9]+$`, redirect.Port())}
		out["begin"] = map[string]any{"flowID": opaque(flowFormat, flowID),
			"url": u.Scheme + "://" + u.Host + u.Path, "params": params}

		// The flow ID names this flow: cancelling it closes its redirect listener.
		listening := func() bool {
			c, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", redirect.Port()))
			if err == nil {
				c.Close()
			}
			return err == nil
		}
		out["listeningBefore"] = listening()
		api.CancelOAuth("unknown-flow")
		out["listeningAfterUnknown"] = listening()
		api.CancelOAuth(flowID)
		require.Eventually(t, func() bool { return !listening() }, 5*time.Second, 10*time.Millisecond)

		second, _, err := api.BeginOAuth(ctx, "outlook", "", core.OAuthClient{})
		require.NoError(t, err)
		out["second"] = map[string]any{"flowID": opaque(flowFormat, second), "err": errInfo(err)}
		api.CancelOAuth(second)
		out["notOAuth"] = res2(api.BeginOAuth(ctx, "gmail", "", core.OAuthClient{}))
		return out
	})
}

func TestOAuthNotConfigured(t *testing.T) {
	old := auth.MicrosoftClientID
	auth.MicrosoftClientID = "" // before any Env starts, so no goroutine races it
	defer func() { auth.MicrosoftClientID = old }()
	runContract(t, func(t *testing.T, api contract.API, env *coretest.Env) any {
		return res2(api.BeginOAuth(t.Context(), "outlook", "", core.OAuthClient{}))
	})
}

type contractSysTranslator struct{ langs []string }

func (c *contractSysTranslator) Translate(_ context.Context, texts []string, _, _ string) ([]string, string, error) {
	return texts, "en", nil
}
func (c *contractSysTranslator) Version() string              { return "1" }
func (c *contractSysTranslator) InstalledLanguages() []string { return c.langs }

func contractPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))))
	return b.Bytes()
}

func TestUIInboxOnlyAndSystemTranslator(t *testing.T) {
	t.Run("no_translator", func(t *testing.T) {
		runUIContract(t, nil, func(t *testing.T, api contract.UIAPI, _ *coretest.Env) any {
			before := api.InboxOnly()
			api.SetInboxOnly(true)
			during := api.InboxOnly()
			api.SetInboxOnly(false)
			after := api.InboxOnly()
			langs := api.SystemTranslatorLanguages()
			return map[string]any{
				"before": before,
				"during": during,
				"after":  after,
				"isNil":  langs == nil,
				"langs":  langs,
			}
		})
	})
	t.Run("empty_installed", func(t *testing.T) {
		runUIContract(t, func(*testing.T) []coretest.Option {
			return []coretest.Option{coretest.WithSystemTranslator(&contractSysTranslator{})}
		}, func(t *testing.T, api contract.UIAPI, _ *coretest.Env) any {
			langs := api.SystemTranslatorLanguages()
			return map[string]any{"isNil": langs == nil, "langs": langs}
		})
	})
	t.Run("installed_languages", func(t *testing.T) {
		runUIContract(t, func(*testing.T) []coretest.Option {
			return []coretest.Option{coretest.WithSystemTranslator(&contractSysTranslator{langs: []string{"de", "en", "zh-Hans"}})}
		}, func(t *testing.T, api contract.UIAPI, _ *coretest.Env) any {
			langs := api.SystemTranslatorLanguages()
			return map[string]any{"isNil": langs == nil, "langs": langs}
		})
	})
}

func TestUIStateReport(t *testing.T) {
	runUIContract(t, nil, func(t *testing.T, api contract.UIAPI, env *coretest.Env) any {
		ctx := t.Context()
		acc := env.AddAccount(t, "alice@example.com")

		st, err := api.GetSettings(ctx)
		require.NoError(t, err)
		st.MCPSend = core.MCPSendDirect
		_, err = api.SetSettings(ctx, st)
		require.NoError(t, err)

		d, err := api.NewDraft(ctx, acc.ID, "new", 0)
		require.NoError(t, err)
		d.To, d.Subject, d.HTML = []string{"bob@example.com"}, "hello", "<p>hi</p>"
		d, err = api.SaveDraft(ctx, d)
		require.NoError(t, err)

		fgBgErr := errInfo(api.SetUIForeground(ctx, false))
		afterBg := api.InboxOnly()
		fgFrontErr := errInfo(api.SetUIForeground(ctx, true))
		afterFront := api.InboxOnly()

		openErr := errInfo(api.SetComposeOpen(ctx, []int64{d.ID}))
		blocked := res(api.RequestMCPSend(ctx, d.ID, d.Revision))
		clearErr := errInfo(api.SetComposeOpen(ctx, nil))
		allowed := res(api.RequestMCPSend(ctx, d.ID, d.Revision))

		return map[string]any{
			"fgBgErr":    fgBgErr,
			"afterBg":    afterBg,
			"fgFrontErr": fgFrontErr,
			"afterFront": afterFront,
			"openErr":    openErr,
			"blocked":    blocked,
			"clearErr":   clearErr,
			"allowed":    allowed,
		}
	})
}

func TestUIReaderAndAttachments(t *testing.T) {
	runUIContract(t, nil, func(t *testing.T, api contract.UIAPI, env *coretest.Env) any {
		ctx := t.Context()
		acc := env.AddAccount(t, "alice@example.com")
		pic := contractPNG(t, 4, 4)
		env.Deliver(t, acc.ID, "INBOX", coretest.Msg("Bob <bob@example.com>", "alice@example.com", "Assets", "See logo.",
			coretest.MessageID("ui-m1@contract"), coretest.Date(day1),
			coretest.HTML(`<p>Logo: <img src="cid:logo@contract"></p>`),
			coretest.Inline("logo@contract", "image/png", pic),
			coretest.Attachment("first.txt", "text/plain", []byte("one")),
			coretest.Attachment("second.txt", "text/plain", []byte("two"))))

		msg := firstMessage(t, api)
		out := map[string]any{}
		lazy, err := api.GetMessageLazyImages(ctx, msg.ID, false)
		out["lazy"] = res(lazy, err)
		out["lazyMissing"] = res(api.GetMessageLazyImages(ctx, 999999, false))

		_, missingErr := api.InlineImagesStream(ctx, 999999)
		out["inlineMissing"] = errInfo(missingErr)

		imgCh, err := api.InlineImagesStream(ctx, msg.ID)
		require.NoError(t, err)
		var inlineOut []any
		for _, it := range collect(t, imgCh) {
			if it.StreamError() != nil {
				inlineOut = append(inlineOut, map[string]any{"err": errInfo(it.StreamError())})
				continue
			}
			b, rerr := os.ReadFile(it.Path)
			inlineOut = append(inlineOut, map[string]any{
				"cid":         it.CID,
				"contentType": it.ContentType,
				"abs":         filepath.IsAbs(it.Path),
				"bytesEqual":  bytes.Equal(b, pic),
				"readErr":     fmt.Sprint(rerr),
			})
		}
		out["inline"] = inlineOut

		txt, err := api.GetMessageText(ctx, msg.ID)
		require.NoError(t, err)
		require.Len(t, txt.Attachments, 3)

		attCh, err := api.FetchAttachmentsStream(ctx, []int64{txt.Attachments[1].ID, txt.Attachments[2].ID})
		require.NoError(t, err)
		var attOut []any
		for _, a := range collect(t, attCh) {
			if a.StreamError() != nil {
				attOut = append(attOut, map[string]any{"err": errInfo(a.StreamError())})
				continue
			}
			b, rerr := os.ReadFile(a.Path)
			attOut = append(attOut, map[string]any{
				"info":    a.Info,
				"base":    filepath.Base(a.Path),
				"abs":     filepath.IsAbs(a.Path),
				"content": string(b),
				"readErr": fmt.Sprint(rerr),
			})
		}
		out["fetched"] = attOut

		badCh, err := api.FetchAttachmentsStream(ctx, []int64{txt.Attachments[1].ID, 999999})
		require.NoError(t, err)
		var partial []any
		for _, a := range collect(t, badCh) {
			if a.StreamError() != nil {
				partial = append(partial, map[string]any{"err": errInfo(a.StreamError())})
				continue
			}
			partial = append(partial, map[string]any{"filename": a.Info.Filename})
		}
		out["fetchedPartial"] = partial
		return out
	})
}

func TestUIImageCache(t *testing.T) {
	runUIContract(t, nil, func(t *testing.T, api contract.UIAPI, _ *coretest.Env) any {
		ctx := t.Context()
		pic := contractPNG(t, 8, 5)
		var fail atomic.Bool
		fail.Store(true)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if fail.Load() {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			w.Write(pic)
		}))
		defer srv.Close()

		src := srv.URL + "/banner.png"
		out := map[string]any{}
		out["dimsBefore"] = api.CachedImageDims(src)
		_, err := api.CachedImage(ctx, src)
		out["firstFail"] = errInfo(err)

		fail.Store(false)
		_, err = api.CachedImage(ctx, src)
		out["cachedFail"] = errInfo(err)

		api.ForgetImageFailure([]string{src})
		got, err := api.CachedImage(ctx, src)
		require.NoError(t, err)
		b, rerr := os.ReadFile(got.Path)
		out["cached"] = map[string]any{
			"format":     got.Format,
			"width":      got.Width,
			"height":     got.Height,
			"abs":        filepath.IsAbs(got.Path),
			"bytesEqual": bytes.Equal(b, pic),
			"readErr":    fmt.Sprint(rerr),
		}
		out["dimsAfter"] = api.CachedImageDims(src)
		return out
	})
}

func TestUIAvatarsAndPluginReadme(t *testing.T) {
	runUIContract(t, func(t *testing.T) []coretest.Option {
		rs := coretest.NewRegistryServer(t)
		rel := rs.ScriptRelease("hello", "0.1.0", `{}`, "// ok\n")
		rs.Serve("/org/hello/HEAD/README.zh-CN.md", []byte("# 你好插件\n"))
		rs.Serve("/org/hello/HEAD/README.md", []byte("# Hello Plugin\n"))
		u, err := url.Parse(rs.URL(""))
		require.NoError(t, err)
		tr := rs.Src.Client.Transport.(*http.Transport).Clone()
		tr.TLSClientConfig.ServerName = u.Hostname()
		tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, u.Host)
		}
		t.Cleanup(tr.CloseIdleConnections)
		rs.Src.Client = &http.Client{Transport: tr}
		rs.Publish(t, coretest.Registry(day1, map[string]any{
			"name":     "hello",
			"title":    "Hello",
			"summary":  "Demo",
			"homepage": "https://github.com/org/hello",
			"releases": []any{rel},
		}))
		return []coretest.Option{coretest.WithRegistry(rs.Src)}
	}, func(t *testing.T, api contract.UIAPI, env *coretest.Env) any {
		ctx := t.Context()
		acc := env.AddAccount(t, "alice@example.com")
		env.Deliver(t, acc.ID, "INBOX", coretest.Msg("Bob <bob@example.com>", "alice@example.com", "Hi", "Hello.", coretest.Date(day1)))
		msg := firstMessage(t, api)

		icon := contractPNG(t, 64, 64)
		cache := env.Svc.ImageCache()
		_, err := cache.PutAvatar(ctx, imagecache.AvatarKey("favicon", "example.com"), icon, "favicon", cache.AvatarGeneration())
		require.NoError(t, err)

		ms, err := env.Svc.AddModelService(ctx, core.ModelServiceInput{
			Name: "Custom", Kind: "openai", BaseURL: "https://example.com/v1", APIKey: "good-key", Models: []string{"m1"},
		})
		require.NoError(t, err)

		require.NoError(t, env.Svc.RefreshPluginRegistry(ctx))

		out := map[string]any{}
		out["senderBlocked"] = res(api.SenderAvatar(ctx, msg.ID, false))
		out["senderAllowed"] = res(api.SenderAvatar(ctx, msg.ID, true))
		out["modelAvatar"] = res(api.ModelServiceAvatar(ctx, ms.ID))
		out["modelMissing"] = res(api.ModelServiceAvatar(ctx, 999999))
		out["readmeZh"] = res(api.PluginReadme(ctx, "hello", "zh-CN"))
		out["readmeFallback"] = res(api.PluginReadme(ctx, "hello", "fr"))
		out["readmeUnknown"] = res(api.PluginReadme(ctx, "absent", "en"))
		return out
	})
}

func TestUIProgressStreams(t *testing.T) {
	recFiles := map[string][]byte{}
	for i, name := range guard.Files {
		recFiles[name] = bytes.Repeat([]byte{byte('a' + i)}, 256+i)
	}
	core.SetRecognitionModelFilesForTest(t, recFiles)

	runUIContract(t, func(t *testing.T) []coretest.Option {
		rs := coretest.NewRegistryServer(t)
		scriptRel := rs.ScriptRelease("hello", "0.1.0", `{}`, strings.Repeat("// code\n", 32))
		badRel := rs.ScriptRelease("badsum", "0.1.0", `{}`, strings.Repeat("// code\n", 32))
		badRel["artifacts"].(map[string]any)["any"].(map[string]any)["sha256"] = strings.Repeat("0", 64)

		modelBytes := []byte(strings.Repeat("M", 512))
		modelSum := sha256.Sum256(modelBytes)
		rs.Serve("/assets/model.gguf", modelBytes)
		nativeRel := rs.NativeModelsRelease(t, "dlx", "0.1.0", filepath.Join(t.TempDir(), "rec"), []any{
			map[string]any{"id": "m-q4", "name": "M", "transport": "plugin", "operation": "judge", "assets": map[string]string{"model": "q4"}},
		})
		nativeRel["assets"] = []any{
			map[string]any{
				"name": "model",
				"variants": []any{
					map[string]any{
						"id": "q4",
						"files": []any{
							map[string]any{
								"name":   "model.gguf",
								"url":    rs.URL("/assets/model.gguf"),
								"sha256": hex.EncodeToString(modelSum[:]),
								"size":   len(modelBytes),
							},
						},
					},
				},
			},
		}
		rs.Publish(t, coretest.Registry(day1,
			map[string]any{"name": "hello", "title": "Hello", "summary": "S", "releases": []any{scriptRel}},
			map[string]any{"name": "badsum", "title": "Bad", "summary": "B", "releases": []any{badRel}},
			map[string]any{"name": "dlx", "title": "DLX", "summary": "D", "releases": []any{nativeRel}},
		))

		recSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, ok := recFiles[strings.TrimPrefix(r.URL.Path, "/int8/")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(body))
		}))
		t.Cleanup(recSrv.Close)

		return []coretest.Option{
			coretest.WithRegistry(rs.Src),
			coretest.WithRecognitionModelSources(recSrv.URL + "/"),
		}
	}, func(t *testing.T, api contract.UIAPI, env *coretest.Env) any {
		ctx := t.Context()
		require.NoError(t, env.Svc.RefreshPluginRegistry(ctx))
		out := map[string]any{}

		// 1. InstallPluginFromRegistryStream (success + mismatch + unknown)
		_, unknownInstallErr := api.InstallPluginFromRegistryStream(ctx, "no-such-plugin")
		out["installUnknown"] = errInfo(unknownInstallErr)

		ich, err := api.InstallPluginFromRegistryStream(ctx, "hello")
		require.NoError(t, err)
		var installEvents []any
		for _, ev := range collect(t, ich) {
			if ev.StreamError() != nil {
				installEvents = append(installEvents, map[string]any{"err": errInfo(ev.StreamError())})
				continue
			}
			installEvents = append(installEvents, ev)
		}
		out["installHello"] = installEvents

		bch, err := api.InstallPluginFromRegistryStream(ctx, "badsum")
		require.NoError(t, err)
		var badEvents []any
		for _, ev := range collect(t, bch) {
			if ev.StreamError() != nil {
				badEvents = append(badEvents, map[string]any{"err": errInfo(ev.StreamError())})
				continue
			}
			badEvents = append(badEvents, ev.Progress.Stage)
		}
		out["installBadsum"] = badEvents

		// 2. PrepareModelStream (install native plugin first, then prepare model + unknown model)
		_, err = env.Svc.InstallPluginFromRegistry(ctx, "dlx")
		require.NoError(t, err)

		_, badModelErr := api.PrepareModelStream(ctx, "plugin/dlx/missing")
		out["prepareMissing"] = errInfo(badModelErr)

		pch, err := api.PrepareModelStream(ctx, "plugin/dlx/m-q4")
		require.NoError(t, err)
		var prepEvents []any
		for _, ev := range collect(t, pch) {
			if ev.StreamError() != nil {
				prepEvents = append(prepEvents, map[string]any{"err": errInfo(ev.StreamError())})
				continue
			}
			prepEvents = append(prepEvents, ev)
		}
		out["prepareModel"] = prepEvents

		// 3. DownloadRecognitionModelStream
		rch, err := api.DownloadRecognitionModelStream(ctx)
		require.NoError(t, err)
		var recEvents []any
		for _, ev := range collect(t, rch) {
			if ev.StreamError() != nil {
				recEvents = append(recEvents, map[string]any{"err": errInfo(ev.StreamError())})
				continue
			}
			recEvents = append(recEvents, ev)
		}
		out["downloadRecognition"] = recEvents
		out["recognitionInstalled"] = env.Svc.RecognitionModelInstalled()
		return out
	})
}

func TestExportAccounts(t *testing.T) {
	runContract(t, func(t *testing.T, api contract.API, env *coretest.Env) any {
		ctx := t.Context()
		ih, ip, sh, sp, user, pass := env.StartServers(t)
		acc, err := api.AddAccount(ctx, core.AccountInput{
			Email:     "alice@example.com",
			Name:      "Alice",
			Username:  user,
			Password:  pass,
			Signature: "Best,\nAlice",
			Sync:      &core.SyncRange{Months: 3},
			Receiving: core.Server{Host: ih, Port: ip, Security: core.SecurityNone},
			Sending:   core.Server{Host: sh, Port: sp, Security: core.SecurityNone},
		})
		require.NoError(t, err)
		_, err = env.Svc.AddLocalArchive(ctx, "Local Archive")
		require.NoError(t, err)
		env.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Private Subject", "Private Body",
			coretest.MessageID("m-exp@contract"), coretest.Date(day1)))

		raw, err := api.ExportAccounts(ctx)
		require.NoError(t, err)
		text := string(raw)
		assert.NotContains(t, text, pass)
		assert.NotContains(t, text, "Private Subject")
		assert.NotContains(t, text, "Private Body")
		assert.NotContains(t, text, "Local Archive")

		var exported core.AccountExport
		require.NoError(t, json.Unmarshal(raw, &exported))
		for i := range exported.Accounts {
			if exported.Accounts[i].Receiving.Port == ip && exported.Accounts[i].Sending.Port == sp {
				exported.Accounts[i].Receiving.Port, exported.Accounts[i].Sending.Port = -1, -1
			}
		}
		return map[string]any{
			"indented": strings.HasPrefix(text, "{\n  \"version\": 1,\n"),
			"newline":  strings.HasSuffix(text, "\n"),
			"export":   exported,
		}
	})
}

func TestImportAccounts(t *testing.T) {
	runContract(t, func(t *testing.T, api contract.API, env *coretest.Env) any {
		ctx := t.Context()
		ih, ip, sh, sp, user, pass := env.StartServers(t)

		alice, err := api.AddAccount(ctx, core.AccountInput{
			Email:     "alice@example.com",
			Name:      "Alice",
			Alias:     "Work",
			Username:  user,
			Password:  pass,
			Signature: "Best,\nAlice",
			Sync:      &core.SyncRange{Months: 6},
			Receiving: core.Server{Host: ih, Port: ip, Security: core.SecurityNone},
			Sending:   core.Server{Host: sh, Port: sp, Security: core.SecurityNone},
		})
		require.NoError(t, err)
		bob, err := api.AddAccount(ctx, core.AccountInput{
			Email:     "bob@example.com",
			Name:      "Bob",
			Username:  user,
			Password:  pass,
			Sync:      &core.SyncRange{},
			Receiving: core.Server{Host: ih, Port: ip, Security: core.SecurityNone},
			Sending:   core.Server{Host: sh, Port: sp, Security: core.SecurityNone},
		})
		require.NoError(t, err)

		raw, err := api.ExportAccounts(ctx)
		require.NoError(t, err)

		// Clear accounts and signatures to simulate importing into a fresh installation.
		require.NoError(t, api.RemoveAccount(ctx, alice.ID))
		require.NoError(t, api.RemoveAccount(ctx, bob.ID))
		sigs, err := env.Svc.ListSignatures(ctx)
		require.NoError(t, err)
		for _, sg := range sigs {
			require.NoError(t, env.Svc.DeleteSignature(ctx, sg.ID))
		}

		// Sentinel errors cross RPC and write zero accounts.
		badJSON := res(api.ImportAccounts(ctx, []byte(`{corrupted`)))
		badVer := res(api.ImportAccounts(ctx, []byte(`{"version":99,"accounts":[]}`)))
		emptyAfterErr, err := api.ListAccounts(ctx)
		require.NoError(t, err)
		require.Empty(t, emptyAfterErr)

		scrubPreview := func(p core.AccountImportPreview) core.AccountImportPreview {
			for i := range p.Accounts {
				if p.Accounts[i].ReceivingPort == ip {
					p.Accounts[i].ReceivingPort = -1
				}
				if p.Accounts[i].SendingPort == sp {
					p.Accounts[i].SendingPort = -1
				}
			}
			return p
		}
		scrubAccount := func(a core.Account) core.Account {
			if a.Receiving.Port == ip {
				a.Receiving.Port = -1
			}
			if a.Sending.Port == sp {
				a.Sending.Port = -1
			}
			a.Features = nil
			return a
		}
		scrubResult := func(r core.AccountImportResult) core.AccountImportResult {
			for i := range r.Imported {
				r.Imported[i] = scrubAccount(r.Imported[i])
			}
			return r
		}

		prev1, err := api.PreviewAccountImport(ctx, raw)
		require.NoError(t, err)
		imp1, err := api.ImportAccounts(ctx, raw)
		require.NoError(t, err)

		// Export after import matches original export byte-for-byte.
		reexported, err := api.ExportAccounts(ctx)
		require.NoError(t, err)
		assert.Equal(t, string(raw), string(reexported))

		// Second preview and import skip duplicates.
		prev2, err := api.PreviewAccountImport(ctx, raw)
		require.NoError(t, err)
		imp2, err := api.ImportAccounts(ctx, raw)
		require.NoError(t, err)

		// Supplying password clears reauth_required on the imported account.
		require.NoError(t, api.UpdatePassword(ctx, imp1.Imported[0].ID, pass))
		listed, err := api.ListAccounts(ctx)
		require.NoError(t, err)
		for i := range listed {
			listed[i] = scrubAccount(listed[i])
		}

		// An address already here on another protocol is skipped; the rest of the file still imports.
		imapSlot := core.ExportedServer{Protocol: auth.IMAP, Host: "127.0.0.1", Port: 993, Security: core.SecurityNone, Auth: core.AuthPassword}
		smtpSlot := core.ExportedServer{Protocol: auth.SMTP, Host: "127.0.0.1", Port: 587, Security: core.SecurityNone, Auth: core.AuthPassword}
		jmapSlot := core.ExportedServer{Protocol: auth.JMAP, Host: "127.0.0.1", Port: 8080, Security: core.SecurityNone, Auth: core.AuthAPIToken}
		clash, err := json.Marshal(core.AccountExport{Version: core.AccountExportVersion, Accounts: []core.ExportedAccount{
			{Email: "carol@example.com", Receiving: imapSlot, Sending: smtpSlot},
			{Email: "bob@example.com", Receiving: jmapSlot, Sending: jmapSlot},
		}})
		require.NoError(t, err)
		prev3, err := api.PreviewAccountImport(ctx, clash)
		require.NoError(t, err)
		require.Len(t, prev3.Accounts, 2)
		assert.Equal(t, "imap", prev3.Accounts[1].ExistingProtocol)
		imp3, err := api.ImportAccounts(ctx, clash)
		require.NoError(t, err)
		require.Len(t, imp3.Imported, 1)
		assert.Equal(t, []core.AccountImportSkip{{Email: "bob@example.com", ExistingProtocol: "imap"}}, imp3.Skipped)

		return map[string]any{
			"badJSON": badJSON,
			"badVer":  badVer,
			"prev1":   scrubPreview(prev1),
			"imp1":    scrubResult(imp1),
			"prev2":   scrubPreview(prev2),
			"imp2":    scrubResult(imp2),
			"listed":  listed,
			"prev3":   prev3,
			"imp3":    scrubResult(imp3),
		}
	})
}
