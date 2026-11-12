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
	"strconv"
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
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/internal/syslang"
	"github.com/godwit-email/godwit/rpc"
	"github.com/godwit-email/godwit/rpcbind"
	"github.com/godwit-email/godwit/uiclient"
)

type recordingSink struct {
	mu              sync.Mutex
	supported       bool
	bannerSupported bool
	trayVisible     bool
	visibility      []bool
	unread          []int
	codesHistory    [][]core.SignInCode
	banners         []*core.SignInCode
	actions         trayActions
	quitStop        bool
	notes           []app.Notification
}

func (s *recordingSink) SetTrayQuitStop(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.quitStop = on
}

func (s *recordingSink) QuitStop() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.quitStop
}

func (s *recordingSink) NotificationsSupported() bool { return s.supported }
func (s *recordingSink) SignInCodeBannerSupported() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bannerSupported && s.trayVisible
}

func (s *recordingSink) BindTray(act trayActions) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.actions = act
}

func (s *recordingSink) SetTrayVisible(visible bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.trayVisible = visible
	s.visibility = append(s.visibility, visible)
}

func (s *recordingSink) SetTrayUnread(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unread = append(s.unread, n)
}

func (s *recordingSink) SetSignInCodes(codes []core.SignInCode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.codesHistory = append(s.codesHistory, slices.Clone(codes))
}

func (s *recordingSink) ShowSignInCode(code *core.SignInCode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if code == nil {
		s.banners = append(s.banners, nil)
		return
	}
	cp := *code
	s.banners = append(s.banners, &cp)
}

func (s *recordingSink) SetThemeSource(string)      {}
func (s *recordingSink) SetLanguage(string, string) {}

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

func (s *recordingSink) Visibility() []bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.visibility)
}

func (s *recordingSink) Unread() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.unread)
}

func (s *recordingSink) LastUnread() (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.unread) == 0 {
		return 0, false
	}
	return s.unread[len(s.unread)-1], true
}

func (s *recordingSink) Actions() trayActions {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.actions
}

func (s *recordingSink) LastCodes() ([]core.SignInCode, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.codesHistory) == 0 {
		return nil, false
	}
	return slices.Clone(s.codesHistory[len(s.codesHistory)-1]), true
}

func (s *recordingSink) Banners() []*core.SignInCode {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*core.SignInCode, len(s.banners))
	for i, b := range s.banners {
		if b != nil {
			cp := *b
			out[i] = &cp
		}
	}
	return out
}

type stepTimer struct {
	at      time.Time
	fn      func()
	stopped bool
}

type stepClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*stepTimer
}

func newStepClock(now time.Time) *stepClock {
	return &stepClock{now: now}
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *stepClock) AfterFunc(d time.Duration, fn func()) func() bool {
	c.mu.Lock()
	t := &stepTimer{at: c.now.Add(d), fn: fn}
	c.timers = append(c.timers, t)
	c.mu.Unlock()
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		if t.stopped {
			return false
		}
		t.stopped = true
		return true
	}
}

func (c *stepClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []func()
	for _, t := range c.timers {
		if !t.stopped && !t.at.After(c.now) {
			t.stopped = true
			due = append(due, t.fn)
		}
	}
	c.mu.Unlock()
	for _, fn := range due {
		go fn()
	}
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
			newPlatform: func(string, *core.Service) headlessPlatform { return sink },
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
	sink := &recordingSink{supported: true}
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
			newPlatform: func(string, *core.Service) headlessPlatform { return sink },
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

	// 4. RequestMCPSend under 每次确认 (default) with no interface connected
	// waits for the user behind a notification that names no mail content.
	var draft core.Draft
	if err := c.Call(ctx, "NewDraft", &draft, acc.ID, "new", int64(0)); err != nil {
		t.Fatalf("NewDraft: %v", err)
	}
	draft.To = []string{"bob@example.com"}
	draft.Subject = "Quarterly plan"
	draft.HTML = "<p>the plan body</p>"
	if err := c.Call(ctx, "SaveDraft", &draft, draft); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	var sent bool
	if err := c.Call(ctx, "RequestMCPSend", &sent, draft.ID, draft.Revision); err != nil || sent {
		t.Fatalf("RequestMCPSend = (%v, %v), want waiting", sent, err)
	}
	var pending []core.MCPSend
	if err := c.Call(ctx, "PendingMCPSends", &pending); err != nil || len(pending) != 1 {
		t.Fatalf("PendingMCPSends = %+v, %v", pending, err)
	}
	notes := slices.DeleteFunc(sink.Notes(), func(n app.Notification) bool { return !strings.HasPrefix(n.ID, "mcp_send:") })
	if len(notes) != 1 || notes[0].ID != "mcp_send:"+strconv.FormatInt(pending[0].ID, 10) || notes[0].Body == "" ||
		strings.Contains(notes[0].Body+notes[0].Title+notes[0].Subtitle, "plan") {
		t.Fatalf("notifications %+v", notes)
	}

	// 5. OpenURL opens approved Wit links; a Wit run waits on approvals, so it
	// needs an interface connected.
	uic, err := rpc.Dial(dir, rpc.RoleUI, "ui-test", rpcbind.NewCodec())
	if err != nil {
		t.Fatalf("Dial RoleUI: %v", err)
	}
	defer uic.Close()
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

	// Toggling tray settings and receiving a sign-in code without a graphical session produces no tray or errors.
	var st core.Settings
	if err := c.Call(ctx, "GetSettings", &st); err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	st.ShowTrayIcon = false
	st.UnreadInMenuBar = true
	if err := c.Call(ctx, "SetSettings", nil, st); err != nil {
		t.Fatalf("SetSettings: %v", err)
	}
	deliver("From: login@example.com\r\nTo: alice@example.com\r\nSubject: Verification code: 102938\r\nMessage-ID: <ng1@example.com>\r\n\r\nCode 102938.\r\n")

	if code := stop(); code != 0 {
		t.Fatalf("exit code %d; stderr %q", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "notify:") || strings.Contains(stderr.String(), "tray:") || strings.Contains(stderr.String(), "level=ERROR") {
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

	// When a desktop runner fails to initialize before ready, headless falls back cleanly without creating a tray.
	fallbackSink := &recordingSink{supported: true}
	withHeadlessDesktop(t, func() *headlessDesktop {
		return &headlessDesktop{
			translator:  func() core.SystemTranslator { return fakeTranslator{} },
			newPlatform: func(string, *core.Service) headlessPlatform { return fallbackSink },
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
	if got := fallbackSink.Visibility(); len(got) != 0 {
		t.Fatalf("fallback created tray: %v", got)
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

func waitCond(t *testing.T, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", desc)
		}
		time.Sleep(15 * time.Millisecond)
	}
}

func TestHeadlessTrayVisibilityAndUnread(t *testing.T) {
	env := coretest.New(t, coretest.WithoutAutoSync())
	var currentSink atomic.Pointer[recordingSink]
	withHeadlessDesktop(t, func() *headlessDesktop {
		return &headlessDesktop{
			newPlatform: func(string, *core.Service) headlessPlatform { return currentSink.Load() },
			run: func(ctx context.Context, ready func(stop func())) (bool, error) {
				loopCtx, stop := context.WithCancel(ctx)
				defer stop()
				ready(stop)
				<-loopCtx.Done()
				return true, nil
			},
		}
	})

	sink1 := &recordingSink{supported: true, bannerSupported: true}
	currentSink.Store(sink1)

	dir := shortDir(t)
	c, stderr, stop := startHeadless(t, dir)
	ctx := context.Background()

	// Initial startup sets tray visible and clears badge (0).
	if got := sink1.Visibility(); !slices.Equal(got, []bool{true}) {
		t.Fatalf("initial visibility = %v, want [true]", got)
	}
	if n, ok := sink1.LastUnread(); !ok || n != 0 {
		t.Fatalf("initial unread = (%d, %v), want (0, true)", n, ok)
	}

	acc, deliver := addTestAccount(t, env, c)
	deliver("From: a@example.com\r\nTo: alice@example.com\r\nSubject: One\r\nMessage-ID: <u1@example.com>\r\n\r\nFirst.\r\n")
	// UnreadInMenuBar defaults to false: badge stays 0 until enabled.
	var st core.Settings
	if err := c.Call(ctx, "GetSettings", &st); err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	st.UnreadInMenuBar = true
	if err := c.Call(ctx, "SetSettings", nil, st); err != nil {
		t.Fatalf("SetSettings UnreadInMenuBar=true: %v", err)
	}
	waitCond(t, "unread=1", func() bool { n, ok := sink1.LastUnread(); return ok && n == 1 })

	deliver("From: b@example.com\r\nTo: alice@example.com\r\nSubject: Two\r\nMessage-ID: <u2@example.com>\r\n\r\nSecond.\r\n")
	waitCond(t, "unread=2", func() bool { n, ok := sink1.LastUnread(); return ok && n == 2 })

	// Mark one message read -> unread=1.
	var folders []core.Folder
	if err := c.Call(ctx, "ListFolders", &folders, acc.ID); err != nil {
		t.Fatalf("ListFolders: %v", err)
	}
	inboxIdx := slices.IndexFunc(folders, func(f core.Folder) bool { return f.Role == "inbox" })
	var page core.MessagePage
	if err := c.Call(ctx, "ListMessages", &page, core.View{FolderID: folders[inboxIdx].ID}, "", 10); err != nil || len(page.Items) != 2 {
		t.Fatalf("ListMessages = %+v, %v", page, err)
	}
	if err := c.Call(ctx, "SetFlags", nil, []int64{page.Items[0].ID}, []string{`\Seen`}, []string(nil)); err != nil {
		t.Fatalf("SetFlags: %v", err)
	}
	waitCond(t, "unread=1 after SetFlags", func() bool { n, ok := sink1.LastUnread(); return ok && n == 1 })

	// Turn off UnreadInMenuBar -> badge clears to 0 while tray remains visible.
	st.UnreadInMenuBar = false
	if err := c.Call(ctx, "SetSettings", nil, st); err != nil {
		t.Fatalf("SetSettings UnreadInMenuBar=false: %v", err)
	}
	waitCond(t, "unread=0 when UnreadInMenuBar=false", func() bool { n, ok := sink1.LastUnread(); return ok && n == 0 })
	if got := sink1.Visibility(); !slices.Equal(got, []bool{true}) {
		t.Fatalf("visibility changed unexpectedly: %v", got)
	}

	// Re-enable UnreadInMenuBar -> badge restores to 1.
	st.UnreadInMenuBar = true
	if err := c.Call(ctx, "SetSettings", nil, st); err != nil {
		t.Fatalf("SetSettings UnreadInMenuBar=true: %v", err)
	}
	waitCond(t, "unread=1 when UnreadInMenuBar=true", func() bool { n, ok := sink1.LastUnread(); return ok && n == 1 })

	// Hide tray via ShowTrayIcon=false -> SetTrayVisible(false) and badge clears to 0.
	st.ShowTrayIcon = false
	if err := c.Call(ctx, "SetSettings", nil, st); err != nil {
		t.Fatalf("SetSettings ShowTrayIcon=false: %v", err)
	}
	waitCond(t, "tray hidden", func() bool { return slices.Equal(sink1.Visibility(), []bool{true, false}) })
	waitCond(t, "unread=0 when ShowTrayIcon=false", func() bool { n, ok := sink1.LastUnread(); return ok && n == 0 })

	// Re-show tray -> SetTrayVisible(true) and badge restores to 1.
	st.ShowTrayIcon = true
	if err := c.Call(ctx, "SetSettings", nil, st); err != nil {
		t.Fatalf("SetSettings ShowTrayIcon=true: %v", err)
	}
	waitCond(t, "tray re-shown", func() bool { return slices.Equal(sink1.Visibility(), []bool{true, false, true}) })
	waitCond(t, "unread=1 restored", func() bool { n, ok := sink1.LastUnread(); return ok && n == 1 })

	// Persist ShowTrayIcon=false and restart backend on the same dataDir -> starts hidden with badge 0.
	st.ShowTrayIcon = false
	if err := c.Call(ctx, "SetSettings", nil, st); err != nil {
		t.Fatalf("SetSettings ShowTrayIcon=false before restart: %v", err)
	}
	waitCond(t, "tray hidden before restart", func() bool { return slices.Equal(sink1.Visibility(), []bool{true, false, true, false}) })
	_ = c.Close()
	if code := stop(); code != 0 {
		t.Fatalf("first exit code %d; stderr %q", code, stderr.String())
	}

	sink2 := &recordingSink{supported: true, bannerSupported: true}
	currentSink.Store(sink2)
	_, stderr2, stop2 := startHeadless(t, dir)
	if got := sink2.Visibility(); !slices.Equal(got, []bool{false}) {
		t.Fatalf("restarted visibility = %v, want [false]", got)
	}
	if n, ok := sink2.LastUnread(); !ok || n != 0 {
		t.Fatalf("restarted unread = (%d, %v), want (0, true)", n, ok)
	}
	if code := stop2(); code != 0 {
		t.Fatalf("second exit code %d; stderr %q", code, stderr2.String())
	}
}

func TestHeadlessTrayActions(t *testing.T) {
	env := coretest.New(t, coretest.WithoutAutoSync())
	sink := &recordingSink{supported: true, bannerSupported: true}
	withHeadlessDesktop(t, func() *headlessDesktop {
		return &headlessDesktop{
			newPlatform: func(string, *core.Service) headlessPlatform { return sink },
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
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	stderr := &syncBuf{}
	done := make(chan int, 1)
	go func() { done <- run(runCtx, []string{"--headless", "--data-dir", dir}, nil, &syncBuf{}, stderr) }()
	c := dialWait(t, dir)
	ctx := context.Background()

	// Add an IMAP account (which already has Archive) plus a Local Archive account.
	ref := env.AddAccount(t, "alice@example.com")
	im := env.IMAP(ref.ID)
	ih, ip, sh, sp := env.Endpoints(ref.ID)
	var acc core.Account
	err := c.Call(ctx, "AddAccount", &acc, core.AccountInput{
		Name:      "work",
		Email:     "alice@example.com",
		Username:  "alice",
		Password:  "secret",
		Receiving: core.Server{Host: ih, Port: ip, Security: core.SecurityNone},
		Sending:   core.Server{Host: sh, Port: sp, Security: core.SecurityNone},
		IsDefault: true,
	})
	if err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	if err := c.Call(ctx, "SyncNow", nil, acc.ID); err != nil {
		t.Fatalf("initial SyncNow: %v", err)
	}
	var localAcc core.Account
	if err := c.Call(ctx, "AddLocalArchive", &localAcc, "Old Mail"); err != nil {
		t.Fatalf("AddLocalArchive: %v", err)
	}

	var folders []core.Folder
	if err := c.Call(ctx, "ListFolders", &folders, acc.ID); err != nil {
		t.Fatalf("ListFolders: %v", err)
	}
	archiveIdx := slices.IndexFunc(folders, func(f core.Folder) bool { return f.Name == "Archive" })
	if archiveIdx < 0 {
		t.Fatalf("Archive folder not synced: %+v", folders)
	}
	archiveFolderID := folders[archiveIdx].ID

	// Set UI foreground=false so background rounds stay inbox-only, and let the
	// initial AddAccount worker round settle before appending to Archive.
	uiClient, err := rpc.Dial(dir, rpc.RoleUI, "ui-test", rpcbind.NewCodec())
	if err != nil {
		t.Fatalf("Dial RoleUI: %v", err)
	}
	defer uiClient.Close()
	if err := uiClient.Call(ctx, "SetUIForeground", nil, false); err != nil {
		t.Fatalf("SetUIForeground: %v", err)
	}
	time.Sleep(150 * time.Millisecond)

	// Append directly to Archive on the IMAP server: background worker is resting
	// on INBOX IDLE and inbox-only, so only tray Sync All (SyncNow) fetches Archive.
	raw := "From: colleague@example.com\r\nTo: alice@example.com\r\nSubject: Archived Note\r\nMessage-ID: <arc1@example.com>\r\n\r\nBody.\r\n"
	if _, err := im.User.Append("Archive", bytes.NewReader([]byte(raw)), &imap.AppendOptions{}); err != nil {
		t.Fatalf("append Archive: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	var beforePage core.MessagePage
	if err := c.Call(ctx, "ListMessages", &beforePage, core.View{FolderID: archiveFolderID}, "", 10); err != nil || len(beforePage.Items) != 0 {
		t.Fatalf("Archive fetched before Sync All: %+v, %v", beforePage, err)
	}

	sink.Actions().syncAll()
	waitCond(t, "Sync All fetches Archive message", func() bool {
		var p core.MessagePage
		return c.Call(ctx, "ListMessages", &p, core.View{FolderID: archiveFolderID}, "", 10) == nil && len(p.Items) == 1
	})

	// Hide Tray Icon updates Settings.ShowTrayIcon=false and hides tray.
	sink.Actions().hide()
	waitCond(t, "Hide Tray Icon hides tray", func() bool {
		return slices.Equal(sink.Visibility(), []bool{true, false})
	})
	var st core.Settings
	if err := c.Call(ctx, "GetSettings", &st); err != nil || st.ShowTrayIcon {
		t.Fatalf("GetSettings after Hide Tray Icon = %+v, %v", st, err)
	}

	// With no interface connected, Open and Compose start one (TestBackendShowsUI).
	_ = c.Close()
	_ = uiClient.Close()
	launched := launchesUIs(t)
	opened := func(what string) func() bool {
		return func() bool {
			return slices.ContainsFunc(launched.all(), func(args []string) bool { return slices.Contains(args, what) })
		}
	}
	waitCond(t, "Open starts the interface", func() bool { sink.Actions().open(); return opened(app.OpenWindow)() })
	sink.Actions().compose()
	waitCond(t, "Compose starts the interface", opened(app.OpenCompose))

	// Quit cleanly shuts down the backend without cancelling runCtx; with no
	// interface connected it waits for none (TestTrayQuitAsksInterfaceFirst).
	sink.Actions().quit()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("Quit exit code %d; stderr %q", code, stderr.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("backend did not exit after tray Quit; stderr %q", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, rpc.SocketName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket left behind after Quit: %v", err)
	}
	if strings.Contains(stderr.String(), "tray:") || strings.Contains(stderr.String(), "level=ERROR") {
		t.Fatalf("unexpected tray warning/error in stderr: %q", stderr.String())
	}
}

// Under 一直继续 the tray offers 退出并停止收信 and its plain Quit asks the
// interface to quit, the backend staying; the setting takes effect at once.
func TestHeadlessTrayQuitUnderAlways(t *testing.T) {
	sink := &recordingSink{supported: true}
	withHeadlessDesktop(t, func() *headlessDesktop {
		return &headlessDesktop{
			newPlatform: func(string, *core.Service) headlessPlatform { return sink },
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
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	stderr := &syncBuf{}
	done := make(chan int, 1)
	go func() { done <- run(runCtx, []string{"--headless", "--data-dir", dir}, nil, &syncBuf{}, stderr) }()
	c := dialWait(t, dir)
	ctx := context.Background()
	if sink.QuitStop() {
		t.Fatal("退出并停止收信 shown under the default")
	}
	setReceive := func(mode string) {
		t.Helper()
		var st core.Settings
		if err := c.Call(ctx, "GetSettings", &st); err != nil {
			t.Fatal(err)
		}
		st.BackgroundReceive = mode
		if err := c.Call(ctx, "SetSettings", nil, st); err != nil {
			t.Fatal(err)
		}
	}
	setReceive(core.BackgroundReceiveAlways)
	waitCond(t, "退出并停止收信 shown", sink.QuitStop)

	ui, err := uiclient.Dial(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	evs, unsub := ui.Subscribe()
	defer unsub()
	sink.Actions().quit()
	for ev := range evs {
		if ev.Type == events.QuitRequested {
			break
		}
	}
	ui.Close()
	time.Sleep(200 * time.Millisecond)
	select {
	case code := <-done:
		t.Fatalf("Quit stopped the backend under 一直继续 (code %d)", code)
	default:
	}
	if err := c.Call(ctx, "GetSettings", new(core.Settings)); err != nil {
		t.Fatalf("backend gone after Quit: %v", err)
	}

	setReceive(core.BackgroundReceiveUntilQuit)
	waitCond(t, "退出并停止收信 hidden", func() bool { return !sink.QuitStop() })
	setReceive(core.BackgroundReceiveAlways)
	waitCond(t, "退出并停止收信 shown again", sink.QuitStop)
	sink.Actions().quitStop()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit code %d; stderr %q", code, stderr.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("退出并停止收信 left the backend running")
	}
}

func TestHeadlessTraySignInCodesAndBanner(t *testing.T) {
	env := coretest.New(t, coretest.WithoutAutoSync())
	clk := newStepClock(time.Now())
	sink := &recordingSink{supported: true, bannerSupported: true}
	withHeadlessDesktop(t, func() *headlessDesktop {
		return &headlessDesktop{
			clock:       clk,
			newPlatform: func(string, *core.Service) headlessPlatform { return sink },
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

	if codes, ok := sink.LastCodes(); !ok || len(codes) != 0 {
		t.Fatalf("initial sign-in codes = (%+v, %v), want empty", codes, ok)
	}

	_, deliver := addTestAccount(t, env, c)

	// 1. Background arrival with NotifyNewMail && NotifyPreview && bannerSupported:
	// updates tray recent codes, shows floating banner, suppresses duplicate notification.
	deliver("From: Login <login@example.com>\r\nTo: alice@example.com\r\nSubject: Verification code: 482913\r\nMessage-ID: <code1@example.com>\r\n\r\nYour verification code is 482913.\r\n")
	waitCond(t, "sign-in code 482913 in tray", func() bool {
		codes, ok := sink.LastCodes()
		return ok && len(codes) == 1 && codes[0].Code == "482913"
	})
	waitCond(t, "banner shown for 482913", func() bool {
		bs := sink.Banners()
		return slices.ContainsFunc(bs, func(b *core.SignInCode) bool { return b != nil && b.Code == "482913" })
	})
	clk.Advance(app.NotifyWait)
	time.Sleep(80 * time.Millisecond)
	if notes := sink.Notes(); len(notes) != 0 {
		t.Fatalf("duplicate notification sent when banner shown: %+v", notes)
	}

	// 2. UI in foreground suppresses banner (and notifications) while still updating tray submenu.
	uiClient, err := rpc.Dial(dir, rpc.RoleUI, "ui-test", rpcbind.NewCodec())
	if err != nil {
		t.Fatalf("Dial RoleUI: %v", err)
	}
	defer uiClient.Close()
	if err := uiClient.Call(ctx, "SetUIForeground", nil, true); err != nil {
		t.Fatalf("SetUIForeground(true): %v", err)
	}
	bannersBefore := len(sink.Banners())
	deliver("From: Login <login@example.com>\r\nTo: alice@example.com\r\nSubject: Verification code: 739104\r\nMessage-ID: <code2@example.com>\r\n\r\nYour verification code is 739104.\r\n")
	waitCond(t, "sign-in code 739104 in tray", func() bool {
		codes, ok := sink.LastCodes()
		return ok && len(codes) == 2 && codes[0].Code == "739104"
	})
	for _, b := range sink.Banners()[bannersBefore:] {
		if b != nil {
			t.Fatalf("banner shown while UIForeground()=true: %+v", b)
		}
	}
	clk.Advance(app.NotifyWait)
	time.Sleep(80 * time.Millisecond)
	if notes := sink.Notes(); len(notes) != 0 {
		t.Fatalf("notification sent while UIForeground()=true: %+v", notes)
	}

	// 3. With NotifyPreview=false (and !UIForeground()), recent codes still populate tray submenu,
	// no banner is shown, and an ordinary generic notification is sent instead.
	if err := uiClient.Call(ctx, "SetUIForeground", nil, false); err != nil {
		t.Fatalf("SetUIForeground(false): %v", err)
	}
	var st core.Settings
	if err := c.Call(ctx, "GetSettings", &st); err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	st.NotifyPreview = false
	if err := c.Call(ctx, "SetSettings", nil, st); err != nil {
		t.Fatalf("SetSettings NotifyPreview=false: %v", err)
	}
	waitCond(t, "banner dismissed when NotifyPreview=false", func() bool {
		bs := sink.Banners()
		return len(bs) > 0 && bs[len(bs)-1] == nil
	})
	bannersBefore = len(sink.Banners())
	deliver("From: Login <login@example.com>\r\nTo: alice@example.com\r\nSubject: Verification code: 918273\r\nMessage-ID: <code3@example.com>\r\n\r\nYour verification code is 918273.\r\n")
	waitCond(t, "sign-in code 918273 in tray", func() bool {
		codes, ok := sink.LastCodes()
		return ok && len(codes) == 3 && codes[0].Code == "918273"
	})
	for _, b := range sink.Banners()[bannersBefore:] {
		if b != nil {
			t.Fatalf("banner shown when NotifyPreview=false: %+v", b)
		}
	}
	clk.Advance(app.NotifyWait)
	waitCond(t, "generic notification when NotifyPreview=false", func() bool {
		return len(sink.Notes()) == 1
	})
	if n := sink.Notes()[0]; n.Title != "Godwit" {
		t.Fatalf("expected generic notification, got %+v", n)
	}

	// 4. Advancing past the 15-minute expiry window fires the expiry timer and clears codes + banner.
	for range 32 {
		clk.Advance(30 * time.Second)
		codes, ok := sink.LastCodes()
		if ok && len(codes) == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	waitCond(t, "expired sign-in codes cleared", func() bool {
		codes, ok := sink.LastCodes()
		return ok && len(codes) == 0
	})
	if bs := sink.Banners(); len(bs) == 0 || bs[len(bs)-1] != nil {
		t.Fatalf("expected final banner call to dismiss (nil), got %+v", bs)
	}

	if code := stop(); code != 0 {
		t.Fatalf("exit code %d; stderr %q", code, stderr.String())
	}
}
