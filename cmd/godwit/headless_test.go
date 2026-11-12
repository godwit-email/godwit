package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/godwit-email/godwit/app"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/internal/syslang"
	"github.com/godwit-email/godwit/rpc"
)

type recordingSink struct {
	mu        sync.Mutex
	supported bool
	notes     []app.Notification
}

func (s *recordingSink) NotificationsSupported() bool    { return s.supported }
func (s *recordingSink) SignInCodeBannerSupported() bool { return false }

func (s *recordingSink) Notify(n app.Notification) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notes = append(s.notes, n)
	return nil
}

func (s *recordingSink) Notes() []app.Notification {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.notes)
}

func withHeadlessDesktop(t *testing.T, fn func() *headlessDesktop) {
	t.Helper()
	prev := chooseHeadlessDesktop
	chooseHeadlessDesktop = fn
	t.Cleanup(func() { chooseHeadlessDesktop = prev })
}

func addTestAccount(t *testing.T, env *coretest.Env, c *rpc.Client) (acc core.Account, deliver func(raw string)) {
	t.Helper()
	ref := env.AddAccount(t, "alice@example.com")
	im := env.IMAP(ref.ID)
	if err := im.User.Create("Junk", nil); err != nil {
		t.Fatalf("create Junk: %v", err)
	}
	ih, ip, sh, sp := env.Endpoints(ref.ID)
	ctx := context.Background()
	in := core.AccountInput{
		Name:      "test",
		Email:     "alice@example.com",
		Username:  "alice",
		Password:  "secret",
		Receiving: core.Server{Host: ih, Port: ip, Security: core.SecurityNone},
		Sending:   core.Server{Host: sh, Port: sp, Security: core.SecurityNone},
		IsDefault: true,
	}
	if err := c.Call(ctx, "AddAccount", &acc, in); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	if err := c.Call(ctx, "SyncNow", nil, acc.ID); err != nil {
		t.Fatalf("initial SyncNow: %v", err)
	}
	return acc, func(raw string) {
		t.Helper()
		if _, err := im.User.Append("INBOX", bytes.NewReader([]byte(raw)), &imap.AppendOptions{}); err != nil {
			t.Fatalf("append INBOX: %v", err)
		}
		if err := c.Call(ctx, "SyncNow", nil, acc.ID); err != nil {
			t.Fatalf("SyncNow: %v", err)
		}
	}
}

func TestHeadlessGraphicalNotifies(t *testing.T) {
	env := coretest.New(t, coretest.WithoutAutoSync())
	sink := &recordingSink{supported: true}
	withHeadlessDesktop(t, func() *headlessDesktop {
		return &headlessDesktop{
			newSink: func(*core.Service) app.NotificationSink { return sink },
			run: func(ctx context.Context, ready func(stop func())) (bool, error) {
				loopCtx, stop := context.WithCancel(ctx)
				defer stop()
				ready(stop)
				<-loopCtx.Done()
				return true, nil
			},
		}
	})

	dir := shortDir(t)
	c, stderr, stop := startHeadless(t, dir)
	_, deliver := addTestAccount(t, env, c)
	deliver("From: Sender <sender@example.com>\r\nTo: alice@example.com\r\nSubject: Lunch\r\nMessage-ID: <m1@example.com>\r\n\r\nSee you at noon.\r\n")

	deadline := time.Now().Add(10 * time.Second)
	for len(sink.Notes()) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("no notification arrived; stderr %q", stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := sink.Notes()
	if len(got) != 1 {
		t.Fatalf("got %d notifications: %+v", len(got), got)
	}
	n := got[0]
	if n.Title != "Sender" || n.Subtitle != "Lunch" || !strings.Contains(n.Body, "See you at noon.") || n.Sender != "Sender <sender@example.com>" || !strings.HasPrefix(n.ID, "message:") {
		t.Fatalf("unexpected notification: %+v", n)
	}
	if code := stop(); code != 0 {
		t.Fatalf("exit code %d; stderr %q", code, stderr.String())
	}
}

type fakeTranslator struct{}

func (fakeTranslator) Version() string              { return "test-1" }
func (fakeTranslator) InstalledLanguages() []string { return []string{"en", "zh-Hans"} }
func (fakeTranslator) Translate(_ context.Context, texts []string, _, _ string) ([]string, string, error) {
	return texts, "en", nil
}

func TestHeadlessGraphicalEngineOptions(t *testing.T) {
	env := coretest.New(t, coretest.WithoutAutoSync())
	llm := env.LLMService(t, "local", "openai")
	envModels, err := env.Svc.ListModelServices(context.Background())
	if err != nil || len(envModels) == 0 {
		t.Fatalf("ListModelServices: %v, %v", envModels, err)
	}
	llmURL := envModels[0].BaseURL

	var pdfCalls atomic.Int32
	var openedURL atomic.Pointer[string]
	withHeadlessDesktop(t, func() *headlessDesktop {
		return &headlessDesktop{
			translator: func() core.SystemTranslator { return fakeTranslator{} },
			openURL: func(u string) error {
				openedURL.Store(&u)
				return nil
			},
			printPDF: func(_ context.Context, html string) ([]byte, error) {
				if !strings.Contains(html, "Invoice #42") {
					t.Errorf("unexpected PDF HTML: %q", html)
				}
				pdfCalls.Add(1)
				return []byte("%PDF-1.4\nfake\n"), nil
			},
			newSink: func(*core.Service) app.NotificationSink { return &recordingSink{supported: true} },
			run: func(ctx context.Context, ready func(stop func())) (bool, error) {
				loopCtx, stop := context.WithCancel(ctx)
				defer stop()
				ready(stop)
				<-loopCtx.Done()
				return true, nil
			},
		}
	})

	dir := shortDir(t)
	c, stderr, stop := startHeadless(t, dir)
	ctx := context.Background()

	// 1. SystemLanguage matches syslang.Detect().
	var gotLang string
	if err := c.Call(ctx, "SystemLanguage", &gotLang); err != nil {
		t.Fatalf("SystemLanguage: %v", err)
	}
	if want := syslang.Detect(); gotLang != want || gotLang == "" {
		t.Fatalf("SystemLanguage = %q, want %q", gotLang, want)
	}

	// 2. SystemTranslator exposes the built-in system-translation plugin.
	var plugins []core.PluginInfo
	if err := c.Call(ctx, "ListPlugins", &plugins); err != nil {
		t.Fatalf("ListPlugins: %v", err)
	}
	if !slices.ContainsFunc(plugins, func(p core.PluginInfo) bool { return p.Name == "system-translation" }) {
		t.Fatalf("system-translation missing from plugins: %+v", plugins)
	}

	// 3. PrintPDF exports rule PDFs via the provided PrintPDF function.
	acc, deliver := addTestAccount(t, env, c)
	deliver("From: billing@example.com\r\nTo: alice@example.com\r\nSubject: Invoice #42\r\nMessage-ID: <inv42@example.com>\r\n\r\nInvoice #42 body.\r\n")
	pdfDir := t.TempDir()
	var ruleID int64
	err = c.Call(ctx, "SaveRule", &ruleID, core.Rule{
		Enabled: true,
		Doc: core.RuleDoc{
			Version: core.RuleVersion,
			Name:    "Export invoices",
			Trigger: core.TriggerReceive,
			Match:   core.MatchAll,
			Actions: []core.RuleAction{{Kind: core.ActionPDF, Dir: pdfDir, Naming: "{date} {subject}"}},
		},
	})
	if err != nil {
		t.Fatalf("SaveRule: %v", err)
	}
	var ran int
	if err := c.Call(ctx, "RunRuleOnExisting", &ran, ruleID); err != nil || ran != 1 {
		t.Fatalf("RunRuleOnExisting = %d, %v", ran, err)
	}
	var hits []core.RuleHit
	if err := c.Call(ctx, "RuleHits", &hits, ruleID); err != nil || len(hits) != 1 || len(hits[0].Actions) != 1 {
		t.Fatalf("RuleHits = %+v, %v", hits, err)
	}
	act := hits[0].Actions[0]
	if act.State != core.RuleActionDone || len(act.Paths) != 1 || pdfCalls.Load() != 1 {
		t.Fatalf("PDF rule action = %+v, pdfCalls = %d", act, pdfCalls.Load())
	}
	if data, err := os.ReadFile(act.Paths[0]); err != nil || string(data) != "%PDF-1.4\nfake\n" {
		t.Fatalf("written PDF = %q, %v", data, err)
	}

	// 4. OpenURL opens approved Wit links.
	var ms core.ModelService
	err = c.Call(ctx, "AddModelService", &ms, core.ModelServiceInput{
		Name: "local", Kind: "openai", BaseURL: llmURL, APIKey: "test-key", Models: []string{"m1"},
	})
	if err != nil {
		t.Fatalf("AddModelService: %v", err)
	}
	var st core.Settings
	if err := c.Call(ctx, "GetSettings", &st); err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	st.AutoLabel = false
	st.DefaultModel = "local/m1"
	if err := c.Call(ctx, "SetSettings", nil, st); err != nil {
		t.Fatalf("SetSettings: %v", err)
	}
	llm.Script(
		coretest.Turn{Calls: []coretest.Call{{Name: "open_link_in_browser", Args: `{"url":"https://example.com/hello"}`}}},
		coretest.Turn{Text: []string{"opened"}},
	)
	var conv core.Conversation
	if err := c.Call(ctx, "NewConversation", &conv, "ask", int64(0)); err != nil {
		t.Fatalf("NewConversation: %v", err)
	}
	stream, cancelStream, err := c.Stream(ctx, "Ask", conv.ID, "open example.com")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	var rawEvents []string
	for ev := range stream {
		rawEvents = append(rawEvents, string(ev))
	}
	if err := c.StreamErr(stream); err != nil {
		t.Fatalf("Ask stream err: %v", err)
	}
	cancelStream()
	var approvals []core.Approval
	if err := c.Call(ctx, "ListApprovals", &approvals, conv.ID); err != nil || len(approvals) != 1 {
		t.Fatalf("ListApprovals = %+v, %v; events = %v", approvals, err, rawEvents)
	}
	if err := c.Call(ctx, "ResolveApproval", nil, approvals[0].ID, true); err != nil {
		t.Fatalf("ResolveApproval: %v", err)
	}
	if got := openedURL.Load(); got == nil || *got != "https://example.com/hello" {
		t.Fatalf("OpenURL got %v", got)
	}

	// 5. RequestMCPSend under 每次确认 (default) still rejects with ErrMCPSendOff.
	var draft core.Draft
	if err := c.Call(ctx, "NewDraft", &draft, acc.ID, "new", int64(0)); err != nil {
		t.Fatalf("NewDraft: %v", err)
	}
	draft.To = []string{"bob@example.com"}
	draft.Subject = "Hello"
	draft.HTML = "<p>Hi</p>"
	if err := c.Call(ctx, "SaveDraft", &draft, draft); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	var sent bool
	err = c.Call(ctx, "RequestMCPSend", &sent, draft.ID, draft.Revision)
	if !errors.Is(err, core.ErrMCPSendOff) || sent {
		t.Fatalf("RequestMCPSend = (%v, %v), want ErrMCPSendOff", sent, err)
	}

	if code := stop(); code != 0 {
		t.Fatalf("exit code %d; stderr %q", code, stderr.String())
	}
}

func TestHeadlessWithoutGraphicalSession(t *testing.T) {
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("GODWIT_HEADLESS_DESKTOP", "0")

	env := coretest.New(t, coretest.WithoutAutoSync())
	dir := shortDir(t)
	c, stderr, stop := startHeadless(t, dir)
	ctx := context.Background()

	var gotLang string
	if err := c.Call(ctx, "SystemLanguage", &gotLang); err != nil || gotLang != syslang.Detect() {
		t.Fatalf("SystemLanguage = %q, %v; want %q", gotLang, err, syslang.Detect())
	}

	var plugins []core.PluginInfo
	if err := c.Call(ctx, "ListPlugins", &plugins); err != nil {
		t.Fatalf("ListPlugins: %v", err)
	}
	if slices.ContainsFunc(plugins, func(p core.PluginInfo) bool { return p.Name == "system-translation" }) {
		t.Fatalf("system-translation unexpectedly present without graphical session: %+v", plugins)
	}

	acc, deliver := addTestAccount(t, env, c)
	if err := c.Call(ctx, "BlockSender", nil, "spam@example.com"); err != nil {
		t.Fatalf("BlockSender: %v", err)
	}
	deliver("From: spam@example.com\r\nTo: alice@example.com\r\nSubject: Promo\r\nMessage-ID: <s1@example.com>\r\n\r\nBuy now.\r\n")
	deliver("From: friend@example.com\r\nTo: alice@example.com\r\nSubject: Hi\r\nMessage-ID: <f1@example.com>\r\n\r\nHello there.\r\n")

	var folders []core.Folder
	if err := c.Call(ctx, "ListFolders", &folders, acc.ID); err != nil {
		t.Fatalf("ListFolders: %v", err)
	}
	var inboxID, junkID int64
	for _, f := range folders {
		switch f.Role {
		case "inbox":
			inboxID = f.ID
		case "junk":
			junkID = f.ID
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		var junkPage core.ThreadPage
		if err := c.Call(ctx, "ListThreads", &junkPage, core.View{FolderID: junkID}, "", 10); err != nil {
			t.Fatalf("ListThreads junk: %v", err)
		}
		if len(junkPage.Items) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("blocked sender was not moved to junk; stderr %q", stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	var inboxPage core.ThreadPage
	if err := c.Call(ctx, "ListThreads", &inboxPage, core.View{FolderID: inboxID}, "", 10); err != nil || len(inboxPage.Items) != 1 {
		t.Fatalf("ListThreads inbox = %+v, %v", inboxPage, err)
	}

	// PDF export rule records no_pdf when running without a graphical session.
	var ruleID int64
	err := c.Call(ctx, "SaveRule", &ruleID, core.Rule{
		Enabled: true,
		Doc: core.RuleDoc{
			Version:    core.RuleVersion,
			Name:       "Export PDF",
			Trigger:    core.TriggerReceive,
			Match:      core.MatchAll,
			Conditions: []core.RuleCondition{{Field: core.FieldFolder, Op: core.OpIs, Value: "inbox"}},
			Actions:    []core.RuleAction{{Kind: core.ActionPDF, Dir: t.TempDir(), Naming: "{date} {subject}"}},
		},
	})
	if err != nil {
		t.Fatalf("SaveRule: %v", err)
	}
	var ran int
	if err := c.Call(ctx, "RunRuleOnExisting", &ran, ruleID); err != nil || ran != 1 {
		t.Fatalf("RunRuleOnExisting = %d, %v", ran, err)
	}
	var hits []core.RuleHit
	if err := c.Call(ctx, "RuleHits", &hits, ruleID); err != nil || len(hits) != 1 || len(hits[0].Actions) != 1 {
		t.Fatalf("RuleHits = %+v, %v", hits, err)
	}
	if act := hits[0].Actions[0]; act.State != core.RuleActionFailed || act.Failure != core.RuleFailNoPDF {
		t.Fatalf("PDF rule action without graphical session = %+v, want failed/%s", act, core.RuleFailNoPDF)
	}

	// RequestMCPSend under 每次确认 rejects with ErrMCPSendOff.
	var draft core.Draft
	if err := c.Call(ctx, "NewDraft", &draft, acc.ID, "new", int64(0)); err != nil {
		t.Fatalf("NewDraft: %v", err)
	}
	draft.To = []string{"bob@example.com"}
	draft.Subject = "Hello"
	draft.HTML = "<p>Hi</p>"
	if err := c.Call(ctx, "SaveDraft", &draft, draft); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	var sent bool
	err = c.Call(ctx, "RequestMCPSend", &sent, draft.ID, draft.Revision)
	if !errors.Is(err, core.ErrMCPSendOff) || sent {
		t.Fatalf("RequestMCPSend = (%v, %v), want ErrMCPSendOff", sent, err)
	}

	if code := stop(); code != 0 {
		t.Fatalf("exit code %d; stderr %q", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "notify:") || strings.Contains(stderr.String(), "level=ERROR") {
		t.Fatalf("unexpected error in stderr: %q", stderr.String())
	}
}

func TestHeadlessDesktopSelectionAndFallback(t *testing.T) {
	for _, tc := range []struct {
		display, wayland string
		want             bool
	}{
		{"", "", false},
		{":0", "", true},
		{"", "wayland-0", true},
		{":0", "wayland-0", true},
	} {
		got := linuxHasDisplay(func(k string) string {
			switch k {
			case "DISPLAY":
				return tc.display
			case "WAYLAND_DISPLAY":
				return tc.wayland
			}
			return ""
		})
		if got != tc.want {
			t.Errorf("linuxHasDisplay(%q, %q) = %v, want %v", tc.display, tc.wayland, got, tc.want)
		}
	}

	// Off the main entry (as in-process tests run), defaultHeadlessDesktop is nil.
	if d := defaultHeadlessDesktop(); d != nil {
		t.Fatalf("defaultHeadlessDesktop() off main = %+v, want nil", d)
	}

	prevMain := inMain
	inMain = true
	defer func() { inMain = prevMain }()

	t.Setenv("GODWIT_HEADLESS_DESKTOP", "0")
	if d := defaultHeadlessDesktop(); d != nil {
		t.Fatalf("defaultHeadlessDesktop() with GODWIT_HEADLESS_DESKTOP=0 = %+v, want nil", d)
	}

	t.Setenv("GODWIT_HEADLESS_DESKTOP", "")
	if got, want := defaultHeadlessDesktop() != nil, hasGraphicalSession(); got != want {
		t.Fatalf("defaultHeadlessDesktop() != nil is %v, hasGraphicalSession() is %v", got, want)
	}
	inMain = prevMain

	// When a desktop runner fails to initialize before ready, headless falls back cleanly.
	withHeadlessDesktop(t, func() *headlessDesktop {
		return &headlessDesktop{
			translator: func() core.SystemTranslator { return fakeTranslator{} },
			run: func(context.Context, func(func())) (bool, error) {
				return false, errors.New("mygo: cannot open display")
			},
		}
	})
	dir := shortDir(t)
	c, stderr, stop := startHeadless(t, dir)
	var plugins []core.PluginInfo
	if err := c.Call(context.Background(), "ListPlugins", &plugins); err != nil {
		t.Fatalf("ListPlugins: %v", err)
	}
	if slices.ContainsFunc(plugins, func(p core.PluginInfo) bool { return p.Name == "system-translation" }) {
		t.Fatalf("fallback kept desktop SystemTranslator: %+v", plugins)
	}
	if code := stop(); code != 0 {
		t.Fatalf("exit code %d; stderr %q", code, stderr.String())
	}
}

func TestSigintShutdownNoDesktop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no SIGINT to another process on Windows")
	}
	dir := shortDir(t)
	cmd := exec.Command(os.Args[0], "--headless", "--data-dir", dir)
	cmd.Env = append(os.Environ(), "GODWIT_TEST_MAIN=1", "GODWIT_HEADLESS_DESKTOP=0", "DISPLAY=", "WAYLAND_DISPLAY=")
	var stderr syncBuf
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill() })
	c := dialWait(t, dir)
	if err := c.Call(context.Background(), "ListAccounts", nil); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("exit: %v; stderr %q", err, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, rpc.SocketName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket left behind: %v", err)
	}
}
