package uiclient_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godwit-email/godwit/app"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/ai/mask/guard"
	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/rpc"
	"github.com/godwit-email/godwit/rpcbind"
	"github.com/godwit-email/godwit/uiclient"
	"github.com/godwit-email/godwit/webui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// uiService is the combined surface of app.Service and webui.Service, satisfied
// by both *core.Service (in-process) and *uiclient.Client (over socket).
type uiService interface {
	app.Service
	webui.Service
}

var (
	_ uiService = (*core.Service)(nil)
	_ uiService = (*uiclient.Client)(nil)
)

type contractScenario func(t *testing.T, svc uiService, env *coretest.Env) any

func runUIContract(t *testing.T, makeOpts func(t *testing.T) []coretest.Option, s contractScenario) {
	t.Helper()
	var got [2]string
	optsFor := func(t *testing.T) []coretest.Option {
		opts := []coretest.Option{coretest.WithoutAutoSync()}
		if makeOpts != nil {
			opts = append(opts, makeOpts(t)...)
		}
		return opts
	}

	t.Run("inproc", func(t *testing.T) {
		start := time.Now()
		env := coretest.New(t, optsFor(t)...)
		got[0] = normalizeContract(t, s(t, env.Svc, env), start, time.Now())
	})

	t.Run("uiclient", func(t *testing.T) {
		logged := captureSlog(t)
		start := time.Now()
		env := coretest.New(t, optsFor(t)...)
		_, cl, dir := startUIServer(t, env.Svc)
		tok, err := os.ReadFile(filepath.Join(dir, rpc.UITokenName))
		require.NoError(t, err)
		v := s(t, cl, env)
		got[1] = normalizeContract(t, v, start, time.Now())
		assert.Empty(t, cl.StreamErrs(), "uiclient streams ended abnormally")
		logOut := logged()
		for _, secret := range []string{string(tok), "secret", "test-key", "good-key", "bad-key"} {
			assert.NotContains(t, logOut, secret)
		}
	})

	assert.Equal(t, got[0], got[1], "in-process *core.Service vs *uiclient.Client over socket")
}

func captureSlog(t *testing.T) func() string {
	var mu sync.Mutex
	var b strings.Builder
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(writerFn(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return b.Write(p)
	}), nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return func() string { mu.Lock(); defer mu.Unlock(); return b.String() }
}

type writerFn func([]byte) (int, error)

func (f writerFn) Write(p []byte) (int, error) { return f(p) }

var msgIDKeys = map[string]bool{"MessageID": true, "InReplyTo": true, "References": true}
var msgIDRE = regexp.MustCompile(`<[^<>\s]+@[^<>\s]+>`)

func normalizeContract(t *testing.T, v any, start, end time.Time) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var tree any
	require.NoError(t, d.Decode(&tree))
	n := &contractNormalizer{seen: map[string]string{}, count: map[string]int{}, start: start.Truncate(time.Second), end: end}
	out, err := json.MarshalIndent(n.walk(tree, false), "", "  ")
	require.NoError(t, err)
	return string(out)
}

type contractNormalizer struct {
	seen       map[string]string
	count      map[string]int
	start, end time.Time
}

func (n *contractNormalizer) placeholder(kind, value string) string {
	key := kind + "\x00" + value
	if p, ok := n.seen[key]; ok {
		return p
	}
	n.count[kind]++
	p := fmt.Sprintf("%s#%d", kind, n.count[kind])
	n.seen[key] = p
	return p
}

func (n *contractNormalizer) walk(v any, id bool) any {
	switch v := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(map[string]any, len(v))
		for _, k := range keys {
			if str, ok := v[k].(string); ok && msgIDKeys[k] {
				out[k] = msgIDRE.ReplaceAllStringFunc(str, func(mid string) string { return n.placeholder("msgid", mid) })
				continue
			}
			out[k] = n.walk(v[k], strings.HasSuffix(k, "ID") || strings.HasSuffix(k, "IDs"))
		}
		return out
	case []any:
		for i := range v {
			v[i] = n.walk(v[i], id)
		}
		return v
	case json.Number:
		if id && v.String() != "0" {
			return n.placeholder("id", v.String())
		}
	case string:
		if o, ok := strings.CutPrefix(v, "opaque:"); ok {
			return n.placeholder("opaque", o)
		}
		if ts, err := time.Parse(time.RFC3339Nano, v); err == nil && !ts.IsZero() {
			if !ts.Before(n.start) && !ts.After(n.end) {
				return "time:now"
			}
			return n.placeholder("time", v)
		}
	}
	return v
}

func errSummary(err error) any {
	if err == nil {
		return nil
	}
	var codes []string
	for _, e := range rpcbind.All() {
		if e.Code != "" && errors.Is(err, e.Err) {
			codes = append(codes, e.Code)
		}
	}
	sort.Strings(codes)
	as := map[string]any{}
	var ce *core.CheckError
	if errors.As(err, &ce) {
		as["CheckError"] = ce.Report
	}
	var we *core.SendWarningsError
	if errors.As(err, &we) {
		as["SendWarningsError"] = we.Check
	}
	var be *core.SendBlockedError
	if errors.As(err, &be) {
		as["SendBlockedError"] = *be
	}
	var ea *core.EgressApprovalRequired
	if errors.As(err, &ea) {
		as["EgressApprovalRequired"] = *ea
	}
	var nf *core.ErrNoRoleFolder
	if errors.As(err, &nf) {
		as["ErrNoRoleFolder"] = *nf
	}
	var me *core.ModelError
	if errors.As(err, &me) {
		as["ModelError"] = *me
	}
	var ae *ai.Error
	if errors.As(err, &ae) {
		as["ai.Error"] = *ae
	}
	return map[string]any{"key": rpcbind.Key(err), "is": codes, "as": as}
}

func resVal[T any](v T, err error) any {
	return map[string]any{"value": v, "err": errSummary(err)}
}

func resVal2[A, B any](a A, b B, err error) any {
	return map[string]any{"values": []any{a, b}, "err": errSummary(err)}
}

func drainChan[T any](t *testing.T, ch <-chan T) []T {
	t.Helper()
	out := []T{}
	deadline := time.After(15 * time.Second)
	for {
		select {
		case v, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, v)
		case <-deadline:
			t.Fatal("stream did not close in time")
		}
	}
}

var contractDay1 = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
var contractDay2 = time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)

func TestContractAccountsAndOAuth(t *testing.T) {
	runUIContract(t, nil, func(t *testing.T, svc uiService, env *coretest.Env) any {
		ctx := t.Context()
		ih, ip, sh, sp, user, pass := env.StartServers(t)
		out := map[string]any{}

		prov, src, err := svc.DetectProvider(ctx, "alice@gmail.com")
		out["detect"] = resVal2(prov.ID, src, err)

		accIn := core.AccountInput{
			Email:     "alice@example.com",
			Name:      "Alice",
			Username:  user,
			Password:  pass,
			Receiving: core.Server{Host: ih, Port: ip, Security: core.SecurityNone},
			Sending:   core.Server{Host: sh, Port: sp, Security: core.SecurityNone},
		}
		out["checkOK"] = resVal(svc.CheckAccount(ctx, accIn))

		bad := accIn
		bad.Password = "wrong-pw-123456"
		out["checkBad"] = resVal(svc.CheckAccount(ctx, bad))

		acc, err := svc.AddAccount(ctx, accIn)
		require.NoError(t, err)
		ports := func(a core.Account) core.Account {
			if a.Receiving.Port == ip && a.Sending.Port == sp {
				a.Receiving.Port, a.Sending.Port = -1, -1
			}
			return a
		}
		out["added"] = ports(acc)
		list, err := svc.ListAccounts(ctx)
		for i := range list {
			list[i] = ports(list[i])
		}
		out["accounts"] = resVal(list, err)

		rawExport, err := svc.ExportAccounts(ctx)
		require.NoError(t, err)
		var exported core.AccountExport
		require.NoError(t, json.Unmarshal(rawExport, &exported))
		out["exportedEmails"] = len(exported.Accounts)

		_, _, oauthErr := svc.BeginOAuth(ctx, "google", "alice@gmail.com", core.OAuthClient{})
		out["oauthUnconfigured"] = errSummary(oauthErr)
		svc.CancelOAuth("nonexistent-flow")
		_ = auth.Source("")
		return out
	})
}

func TestContractMailReadAttachmentsAndInline(t *testing.T) {
	runUIContract(t, nil, func(t *testing.T, svc uiService, env *coretest.Env) any {
		ctx := t.Context()
		acc := env.AddAccount(t, "alice@example.com")
		pic := makePNG(t, 4, 4)
		env.Deliver(t, acc.ID, "INBOX", coretest.Msg("Bob <bob@example.com>", "alice@example.com", "Plans", "Meet on Friday.",
			coretest.MessageID("m1@uiclient"), coretest.Date(contractDay1),
			coretest.HTML(`<p>Meet on <b>Friday</b> <img src="cid:logo@uiclient"></p>`),
			coretest.Inline("logo@uiclient", "image/png", pic),
			coretest.Attachment("notes.txt", "text/plain", []byte("agenda: lunch")),
			coretest.Attachment("extra.txt", "text/plain", []byte("slides"))))
		env.Deliver(t, acc.ID, "INBOX", coretest.Msg("carol@example.com", "alice@example.com", "Re: Plans", "Friday works.",
			coretest.MessageID("m2@uiclient"), coretest.InReplyTo("m1@uiclient"), coretest.Date(contractDay2)))

		out := map[string]any{}
		out["folders"] = resVal(svc.ListFolders(ctx, acc.ID))
		threads, err := svc.ListThreads(ctx, core.View{Unified: "inbox"}, "", 0)
		require.NoError(t, err)
		require.Len(t, threads.Items, 1)
		out["threads"] = threads
		out["thread"] = resVal(svc.GetThread(ctx, threads.Items[0].ID))

		page1, err := svc.ListMessages(ctx, core.View{Unified: "inbox"}, "", 1)
		require.NoError(t, err)
		out["page1"] = page1
		page2, err := svc.ListMessages(ctx, core.View{Unified: "inbox"}, page1.NextCursor, 1)
		require.NoError(t, err)
		out["page2"] = page2

		m1ID := page2.Items[0].ID
		out["message"] = resVal(svc.GetMessage(ctx, m1ID, false))
		out["lazy"] = resVal(svc.GetMessageLazyImages(ctx, m1ID, false))
		out["search"] = resVal(svc.SearchWith(ctx, core.SearchQuery{AccountID: acc.ID, Text: "Friday"}, 0))

		txt, err := svc.GetMessageText(ctx, m1ID)
		require.NoError(t, err)
		out["text"] = txt
		require.Len(t, txt.Attachments, 3)

		p1, info1, err := svc.GetAttachment(ctx, txt.Attachments[1].ID)
		require.NoError(t, err)
		b1, rerr := os.ReadFile(p1)
		out["singleAtt"] = map[string]any{"info": info1, "abs": filepath.IsAbs(p1), "data": string(b1), "readErr": fmt.Sprint(rerr)}

		var batch []any
		err = svc.FetchAttachments(ctx, []int64{txt.Attachments[1].ID, txt.Attachments[2].ID}, func(p string, info core.AttachmentInfo) error {
			b, ferr := os.ReadFile(p)
			batch = append(batch, map[string]any{"info": info, "abs": filepath.IsAbs(p), "data": string(b), "err": fmt.Sprint(ferr)})
			return nil
		})
		out["batchErr"] = errSummary(err)
		out["batch"] = batch

		var inlines []any
		err = svc.InlineImages(ctx, m1ID, func(cid string, a mail.Attachment) {
			inlines = append(inlines, map[string]any{
				"cid":         cid,
				"contentType": a.ContentType,
				"equal":       bytes.Equal(a.Data, pic),
			})
		})
		out["inlineErr"] = errSummary(err)
		out["inlines"] = inlines
		return out
	})
}

func TestContractComposeAndSendWarnings(t *testing.T) {
	runUIContract(t, nil, func(t *testing.T, svc uiService, env *coretest.Env) any {
		ctx := t.Context()
		acc := env.AddAccount(t, "alice@example.com")
		env.InstallPlugin(t, "send-warn")

		srcFile := filepath.Join(t.TempDir(), "hello.txt")
		require.NoError(t, os.WriteFile(srcFile, []byte("attachment-data"), 0o600))

		out := map[string]any{}
		d, err := svc.NewDraft(ctx, acc.ID, "new", 0)
		require.NoError(t, err)

		att, rev, err := svc.AddAttachmentAt(ctx, d.ID, d.Revision, srcFile)
		require.NoError(t, err)
		out["addedAtt"] = att
		rev, err = svc.RemoveAttachmentAt(ctx, d.ID, att.ID, rev)
		require.NoError(t, err)
		d, err = svc.GetDraft(ctx, d.ID)
		require.NoError(t, err)
		assert.Equal(t, rev, d.Revision)

		d.To = []string{"dave@example.com"}
		d.Subject = "Hello"
		d.HTML = "<p>body</p>"
		saved, err := svc.SaveDraft(ctx, d)
		require.NoError(t, err)
		out["saved"] = saved

		d.Subject = "stale"
		out["staleSave"] = resVal(svc.SaveDraft(ctx, d))

		warnErr := svc.SendRevision(ctx, saved.ID, saved.Revision)
		var we *core.SendWarningsError
		require.ErrorAs(t, warnErr, &we)
		out["warn"] = errSummary(warnErr)
		out["badDigest"] = errSummary(svc.SendAcknowledgedRevision(ctx, saved.ID, saved.Revision, strings.Repeat("0", 64)))
		out["ackSend"] = errSummary(svc.SendAcknowledgedRevision(ctx, saved.ID, saved.Revision, we.Check.Digest))

		queued, err := svc.ListOutbox(ctx, acc.ID)
		require.NoError(t, err)
		require.Len(t, queued, 1)
		out["cancelOutbox"] = errSummary(svc.CancelOutbox(ctx, queued[0].ID))
		out["afterCancel"] = resVal(svc.ListOutbox(ctx, acc.ID))
		return out
	})
}

type fakeContractTranslator struct{ langs []string }

func (f *fakeContractTranslator) Translate(_ context.Context, texts []string, _, _ string) ([]string, string, error) {
	return texts, "en", nil
}
func (f *fakeContractTranslator) Version() string              { return "1" }
func (f *fakeContractTranslator) InstalledLanguages() []string { return f.langs }

func TestContractSettingsImagesTranslatorAndRecentLog(t *testing.T) {
	runUIContract(t, func(*testing.T) []coretest.Option {
		return []coretest.Option{coretest.WithSystemTranslator(&fakeContractTranslator{langs: []string{"de", "en", "zh-Hans"}})}
	}, func(t *testing.T, svc uiService, _ *coretest.Env) any {
		ctx := t.Context()
		out := map[string]any{}

		st, err := svc.GetSettings(ctx)
		require.NoError(t, err)
		st.Theme = "dark"
		st.ReadingLanguage = "ja"
		out["setSettings"] = resVal(svc.SetSettings(ctx, st))
		out["readingLang"] = svc.ReadingLanguage(ctx)
		out["sysLang"] = svc.SystemLanguage()
		out["translatorLangs"] = svc.SystemTranslatorLanguages()

		require.NoError(t, svc.TrustImageSender(ctx, "Bob <bob@example.com>"))
		require.NoError(t, svc.BlockSender(ctx, "Spam <spam@example.com>"))
		out["trusted"] = resVal(svc.TrustedImageSenders(ctx))
		out["blocked"] = resVal(svc.BlockedSenders(ctx))

		pic := makePNG(t, 10, 6)
		var fail atomic.Bool
		fail.Store(true)
		imgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if fail.Load() {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			_, _ = w.Write(pic)
		}))
		defer imgSrv.Close()

		src := imgSrv.URL + "/banner.png"
		out["dims0"] = svc.CachedImageDims(src)
		_, cerr := svc.CachedImage(ctx, src)
		out["firstErr"] = cerr != nil
		fail.Store(false)
		svc.ForgetImageFailure([]string{src})
		ci, cerr := svc.CachedImage(ctx, src)
		require.NoError(t, cerr)
		diskBytes, rerr := os.ReadFile(ci.Path)
		out["cached"] = map[string]any{
			"format": ci.Format,
			"w":      ci.Width,
			"h":      ci.Height,
			"abs":    filepath.IsAbs(ci.Path),
			"equal":  bytes.Equal(diskBytes, pic),
			"err":    fmt.Sprint(rerr),
		}
		out["dims1"] = svc.CachedImageDims(src)
		return out
	})
}

func TestContractWitStreamsEventsActionsAndProgress(t *testing.T) {
	recFiles := map[string][]byte{}
	for i, name := range guard.Files {
		recFiles[name] = bytes.Repeat([]byte{byte('k' + i)}, 256+i)
	}
	core.SetRecognitionModelFilesForTest(t, recFiles)

	runUIContract(t, func(t *testing.T) []coretest.Option {
		rs := coretest.NewRegistryServer(t)
		scriptRel := rs.ScriptRelease("hello", "0.1.0", `{}`, strings.Repeat("// code\n", 16))
		modelBytes := bytes.Repeat([]byte("W"), 256)
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
		rs.Publish(t, coretest.Registry(contractDay1,
			map[string]any{"name": "hello", "title": "Hello", "summary": "S", "releases": []any{scriptRel}},
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
	}, func(t *testing.T, svc uiService, env *coretest.Env) any {
		ctx := t.Context()
		l := env.LLM(t)
		defer l.Release()

		acc := env.AddAccount(t, "alice@example.com")
		require.NoError(t, svc.SyncNow(ctx, acc.ID))
		env.InstallPlugin(t, "egress-action")

		sub, unsub := svc.Subscribe()
		env.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Plans", "Meet on Friday.",
			coretest.MessageID("m-act@uiclient"), coretest.Date(contractDay1)))

		var received events.Event
		deadline := time.After(10 * time.Second)
		for received.Type != events.MessageReceived {
			select {
			case ev, ok := <-sub:
				require.True(t, ok)
				received = ev
			case <-deadline:
				t.Fatal("missing MessageReceived")
			}
		}
		unsub()
		for range sub {
		}

		msgs, err := svc.ListMessages(ctx, core.View{Unified: "inbox"}, "", 0)
		require.NoError(t, err)
		msgID := msgs.Items[0].ID

		out := map[string]any{}
		out["receivedEvent"] = received

		// RunSkill
		l.Script(coretest.Turn{Text: []string{"Summary ", "text"}})
		skCh, err := svc.RunSkill(ctx, "summarize", core.Target{Kind: "message", ID: msgID})
		require.NoError(t, err)
		out["skillEvents"] = drainChan(t, skCh)

		// RunAction egress gate + approval
		target := core.Target{Kind: "message", ID: msgID}
		out["actionUnapproved"] = resVal(svc.RunAction(ctx, "egress-action/ping", target, core.RunActionOptions{}))
		out["actionApproved"] = resVal(svc.RunAction(ctx, "egress-action/ping", target, core.RunActionOptions{ApproveEgress: true}))

		// Wit AskWith + approval + SubscribeConversation + timezone
		conv, err := svc.NewConversation(ctx, "ask", 0)
		require.NoError(t, err)
		l.Script(coretest.Turn{Calls: []coretest.Call{{
			ID:   "c1",
			Name: "create_draft",
			Args: `{"to":["x@example.com"],"subject":"s","html":"<p>x</p>"}`,
		}}})
		askCh, err := svc.AskWith(core.WithTimeZone(ctx, "Asia/Tokyo"), conv.ID, "draft one", nil)
		require.NoError(t, err)
		out["askInterrupted"] = drainChan(t, askCh)

		reqs := l.RequestLog()
		reqJSON, err := json.Marshal(reqs[len(reqs)-1])
		require.NoError(t, err)
		out["promptHasTokyoZone"] = strings.Contains(string(reqJSON), "JST")

		approvals, err := svc.ListApprovals(ctx, conv.ID)
		require.NoError(t, err)
		require.Len(t, approvals, 1)
		out["approvals"] = approvals

		convSub, stopConv := svc.SubscribeConversation(conv.ID)
		out["resolveApproval"] = errSummary(svc.ResolveApproval(ctx, approvals[0].ID, true))
		var resumed []core.WitEvent
		waitDone := time.After(15 * time.Second)
	collectConv:
		for {
			select {
			case ev, ok := <-convSub:
				require.True(t, ok)
				resumed = append(resumed, ev)
				if ev.Kind == "done" || ev.Kind == "error" || ev.Kind == "interrupted" {
					break collectConv
				}
			case <-waitDone:
				t.Fatal("missing terminal conversation event")
			}
		}
		stopConv()
		out["resumedEvents"] = resumed

		// Progress callback wrappers parity
		require.NoError(t, svc.RefreshPluginRegistry(ctx))
		var stages []string
		pinfo, err := svc.InstallPluginFromRegistryWithProgress(ctx, "hello", func(p core.PluginInstallProgress) {
			stages = append(stages, p.Stage)
		})
		require.NoError(t, err)
		out["installedPlugin"] = pinfo
		out["installStages"] = stages

		_, err = svc.InstallPluginFromRegistryWithProgress(ctx, "dlx", nil)
		require.NoError(t, err)
		var prepStates []core.ModelState
		mst, err := svc.PrepareModelWithProgress(ctx, "plugin/dlx/m-q4", func(s core.ModelStatus) {
			prepStates = append(prepStates, s.State)
		})
		require.NoError(t, err)
		out["preparedModel"] = mst
		out["prepStatesCount"] = len(prepStates) > 0

		var recLast [2]int64
		require.NoError(t, svc.DownloadRecognitionModel(ctx, func(got, total int64) {
			recLast = [2]int64{got, total}
		}))
		out["recProgress"] = recLast
		out["recInstalled"] = svc.RecognitionModelInstalled()
		return out
	})
}
