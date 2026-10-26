package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/theme"
)

// withAccount is a harness with one account, its initial load applied.
func withAccount(t *testing.T) (*harness, core.Account) {
	var acc core.Account
	h := newHarness(t, func(e *coretest.Env) { acc = e.AddAccount(t, "alice@example.com") })
	h.until("initial load", func() bool { return h.app.loaded })
	return h, acc
}

// settle waits until the App has received every event published so far:
// events arrive in order, so it publishes one more and waits for it.
func (h *harness) settle() {
	h.t.Helper()
	st, err := h.env.Svc.GetSettings(context.Background())
	if err == nil {
		_, err = h.env.Svc.SetSettings(context.Background(), st) // publishes settings.changed
	}
	if err != nil {
		h.t.Fatal(err)
	}
	h.until("events delivered", func() bool {
		return slices.ContainsFunc(h.app.dirty, func(ev events.Event) bool { return ev.Type == events.SettingsChanged })
	})
}

func TestFirstRunRoute(t *testing.T) {
	h := startedIdle(t)
	h.tt.Frame()
	if h.app.route.Page != PageOnboarding {
		t.Fatalf("route %+v", h.app.route)
	}
	if _, rail := h.tt.Find("邮件"); rail { // exact: the promises mention 邮件 too
		t.Fatalf("the rail shows on first run: %q", h.tt.Texts())
	}
}

func TestShellNavigation(t *testing.T) {
	h, _ := withAccount(t)
	h.see("邮件")
	if h.app.route.Page != PageMail {
		t.Fatalf("start route %+v", h.app.route)
	}
	want := func(step string, p Page) {
		t.Helper()
		if h.app.route.Page != p {
			t.Fatalf("after %s: page %d, want %d", step, h.app.route.Page, p)
		}
	}
	gen := h.app.gen
	h.click("Wit")
	want("Wit", PageWit)
	if h.app.gen == gen {
		t.Fatal("navigate did not bump gen")
	}
	h.click("后退")
	want("back", PageMail)
	h.click("前进")
	want("forward", PageWit)
	h.tt.Key(ui.Cmd, ui.KeyBracketLeft)
	h.drain()
	h.tt.Frame()
	want("Cmd+[", PageMail)
	h.tt.Key(ui.Cmd, ui.KeyBracketRight)
	h.drain()
	h.tt.Frame()
	want("Cmd+]", PageWit)
	h.app.Navigate(-1) // forwarded by the Wit page, which has the keyboard
	want("the page's Cmd+[", PageMail)
	h.app.Navigate(1)
	want("the page's Cmd+]", PageWit)
	h.click("后退")
	h.click("设置") // a new route clears the forward stack
	want("settings", PageSettings)
	h.app.forward()
	want("forward with an empty stack", PageSettings)
}

func TestSyncStatus(t *testing.T) {
	var acc core.Account // no background sync: a worker's sync would reset the age
	h := newHarnessWith(t, []coretest.Option{coretest.WithoutAutoSync()}, func(e *coretest.Env) { acc = e.AddAccount(t, "alice@example.com") })
	h.until("initial load", func() bool { return h.app.loaded })
	ctx := context.Background()
	if err := h.env.Svc.SyncNow(ctx, acc.ID); err != nil {
		t.Fatal(err)
	}
	h.settle()
	h.see("已同步 · 刚刚")
	h.advance(3 * time.Minute)
	h.see("3 分钟前")
	h.env.IMAP(acc.ID).SetPassword("x")
	if err := h.env.Svc.SyncNow(ctx, acc.ID); err == nil {
		t.Fatal("sync with a wrong password succeeded")
	}
	h.see("alice@example.com · 密码被拒绝") // the server refused it: named, red, not retried
}

func TestSyncRetryTurnsRed(t *testing.T) {
	var acc core.Account // no background sync: its result would replace the injected failures
	h := newHarnessWith(t, []coretest.Option{coretest.WithoutAutoSync()}, func(e *coretest.Env) { acc = e.AddAccount(t, "alice@example.com") })
	h.until("initial load", func() bool { return h.app.loaded })
	h.post(func() {
		h.app.onEvent(events.Event{Type: events.SyncFailed, AccountID: acc.ID, Error: "dial tcp: connection refused", Kind: "connect"})
	})
	h.see("alice@example.com · 重试中")
	h.click("alice@example.com · 重试中")
	h.see("自动重试中")
	h.see("会自动重试，一般不用处理")
	h.post(func() { // a later failure keeps when the failures began
		h.app.onEvent(events.Event{Type: events.SyncFailed, AccountID: acc.ID, Error: "dial tcp: connection refused", Kind: "connect"})
	})
	h.advance(syncGiveUp)
	h.see("alice@example.com · 同步失败")
	h.see("30 分钟前开始失败")
	h.post(func() { h.app.onEvent(events.Event{Type: events.SyncFinished, AccountID: acc.ID}) })
	h.see("已同步 · 刚刚")
}

func TestSyncWorstFirst(t *testing.T) {
	var a, b core.Account
	h := newHarnessWith(t, []coretest.Option{coretest.WithoutAutoSync()}, func(e *coretest.Env) {
		a, b = e.AddAccount(t, "alice@example.com"), e.AddAccount(t, "bob@example.com")
	})
	h.until("initial load", func() bool { return h.app.loaded })
	h.post(func() {
		h.app.onEvent(events.Event{Type: events.SyncFailed, AccountID: a.ID, Kind: "timeout"})
		h.app.onEvent(events.Event{Type: events.SyncFailed, AccountID: b.ID, Kind: "tls"})
		h.app.onEvent(events.Event{Type: events.SyncStarted, AccountID: a.ID}) // syncing does not hide a failure
	})
	h.see("bob@example.com · 同步失败")
	if hs := h.app.syncHealths(); hs[0].acc.ID != b.ID || hs[0].level != syncAction || hs[1].level != syncRetrying {
		t.Fatalf("healths %+v", hs)
	}
}

// TestSyncAllMultiAccountBatch: the status shows syncing until the last account
// of a Sync All returns, not when the first one does.
func TestSyncAllMultiAccountBatch(t *testing.T) {
	var a, b core.Account
	h := newHarnessWith(t, []coretest.Option{coretest.WithoutAutoSync()}, func(e *coretest.Env) {
		a, b = e.AddAccount(t, "alice@example.com"), e.AddAccount(t, "bob@example.com")
	})
	h.until("initial load", func() bool { return h.app.loaded })
	entered, releaseB := h.env.IMAP(b.ID).BlockLogin()
	defer releaseB()

	h.app.SyncAll()
	h.until("syncing shown", func() bool { return h.tt.HasText(i18n.T("shell.sync.syncing")) })
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("bob's login not held")
	}
	h.until("alice finished", func() bool { return !h.app.batchPending(a.ID) && h.app.batchPending(b.ID) })
	if !h.tt.HasText(i18n.T("shell.sync.syncing")) || !h.app.syncedAt.IsZero() {
		t.Fatalf("synced while bob pending; texts %q", h.tt.Texts())
	}

	releaseB()
	h.until("all finished", func() bool { return h.app.syncBatch == nil && h.tt.HasText(i18n.T("shell.sync.just_now")) })
}

// TestSyncAllPartialFailure: a Sync All with one account failing ends, shows that
// account, and keeps the other's sync time.
func TestSyncAllPartialFailure(t *testing.T) {
	var a, b core.Account
	h := newHarnessWith(t, []coretest.Option{coretest.WithoutAutoSync()}, func(e *coretest.Env) {
		a, b = e.AddAccount(t, "alice@example.com"), e.AddAccount(t, "bob@example.com")
	})
	h.until("initial load", func() bool { return h.app.loaded })
	h.env.IMAP(b.ID).SetPassword("wrong")

	h.app.SyncAll()
	h.until("bob's failure shown", func() bool { return h.app.syncBatch == nil && h.tt.HasText(b.Email) })
	// Alice's SyncFinished comes over the event bus, which may reach the UI after the batch's end.
	h.until("alice's sync time recorded", func() bool { return !h.app.syncedAtBy[a.ID].IsZero() })
}

// TestSyncAllFailureWhileOthersPending: an account failing early does not end
// the Sync All; once the rest return, the failure shows and the last sync time stays unset.
func TestSyncAllFailureWhileOthersPending(t *testing.T) {
	var a, b core.Account
	h := newHarnessWith(t, []coretest.Option{coretest.WithoutAutoSync()}, func(e *coretest.Env) {
		a, b = e.AddAccount(t, "alice@example.com"), e.AddAccount(t, "bob@example.com")
	})
	h.until("initial load", func() bool { return h.app.loaded })
	h.env.IMAP(a.ID).SetPassword("wrong")
	entered, releaseB := h.env.IMAP(b.ID).BlockLogin()
	defer releaseB()

	h.app.SyncAll()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("bob's login not held")
	}
	h.until("alice failed, bob pending", func() bool {
		return h.app.syncBatch != nil && h.app.syncBatch.failed[a.ID] && h.app.batchPending(b.ID)
	})
	if !h.tt.HasText(i18n.T("shell.sync.syncing")) {
		t.Fatalf("not syncing while bob pending; texts %q", h.tt.Texts())
	}

	releaseB()
	h.until("alice's failure shown", func() bool { return h.app.syncBatch == nil && h.tt.HasText(a.Email) })
	if h.tt.HasText(i18n.T("shell.sync.syncing")) {
		t.Fatalf("still syncing; texts %q", h.tt.Texts())
	}
	h.until("bob's sync time recorded", func() bool { return !h.app.syncedAtBy[b.ID].IsZero() }) // its event may come after the batch's end
}

// TestSyncAllWhileInFlight: clicking Sync All again before the batch ends starts
// no second SyncNow.
func TestSyncAllWhileInFlight(t *testing.T) {
	var a core.Account
	h := newHarnessWith(t, []coretest.Option{coretest.WithoutAutoSync()}, func(e *coretest.Env) {
		a = e.AddAccount(t, "alice@example.com")
	})
	h.until("initial load", func() bool { return h.app.loaded })
	logins := h.env.IMAP(a.ID).Logins()
	entered, release := h.env.IMAP(a.ID).BlockLogin()
	defer release()

	h.app.SyncAll()
	b := h.app.syncBatch
	h.app.SyncAll()
	if h.app.syncBatch != b {
		t.Fatal("a second Sync All replaced the batch in flight")
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("login not held")
	}
	release()
	h.until("batch finished", func() bool { return h.app.syncBatch == nil })
	if n := h.env.IMAP(a.ID).Logins() - logins; n != 1 {
		t.Fatalf("%d logins, want 1", n)
	}
}

// TestSyncAllAccountRemoved: an account removed mid-batch neither holds the Sync
// All open nor fails it.
func TestSyncAllAccountRemoved(t *testing.T) {
	var a, b core.Account
	h := newHarnessWith(t, []coretest.Option{coretest.WithoutAutoSync()}, func(e *coretest.Env) {
		a, b = e.AddAccount(t, "alice@example.com"), e.AddAccount(t, "bob@example.com")
	})
	h.until("initial load", func() bool { return h.app.loaded })
	entered, releaseB := h.env.IMAP(b.ID).BlockLogin()
	defer releaseB()

	h.app.SyncAll()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("bob's login not held")
	}
	h.until("alice finished", func() bool { return !h.app.batchPending(a.ID) })
	if err := h.env.Svc.RemoveAccount(t.Context(), b.ID); err != nil {
		t.Fatal(err)
	}
	h.app.load()
	h.until("batch finished", func() bool { return h.app.syncBatch == nil && len(h.app.accounts) == 1 })
	if h.app.syncedAt.IsZero() {
		t.Fatal("the removed account failed the batch")
	}
}

// TestSyncAllWithBackgroundSync: a Sync All racing the worker's own sync and new
// mail converges with the mail shown and the status synced.
func TestSyncAllWithBackgroundSync(t *testing.T) {
	var a core.Account
	h := newHarness(t, func(e *coretest.Env) { a = e.AddAccount(t, "alice@example.com") })
	h.until("initial load", func() bool { return h.app.loaded })

	h.app.SyncAll()
	h.app.onEvent(events.Event{Type: events.SyncStarted, AccountID: a.ID})
	h.app.SyncAll()
	h.env.Deliver(t, a.ID, "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Subject", "Body"))
	h.waitMail("mail shown and synced", func() bool {
		return h.app.syncBatch == nil && h.tt.HasText(i18n.T("shell.sync.just_now")) && h.tt.HasText("Subject")
	})
	h.until("sync time recorded", func() bool { return !h.app.syncedAtBy[a.ID].IsZero() }) // its event may come after the batch's end
}

func TestReauthStatus(t *testing.T) {
	h, acc := withAccount(t)
	h.env.IMAP(acc.ID).SetPassword("x")
	_ = h.env.Svc.SyncNow(context.Background(), acc.ID) // marks the account reauth_required
	h.until("reauth shown", func() bool {
		h.advance(250 * time.Millisecond) // the throttled refresh reloads the accounts
		return h.tt.HasText("alice@example.com · 密码被拒绝")
	})
	h.click("alice@example.com · 密码被拒绝")
	h.click("更新密码")
	want := Route{Page: PageSettings, Settings: fmt.Sprintf("account:%d", acc.ID)}
	if h.app.route != want {
		t.Fatalf("route %+v, want %+v", h.app.route, want)
	}
}

func TestEventBurstConverges(t *testing.T) {
	h, acc := withAccount(t)
	for i := range 30 {
		h.env.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", "alice@example.com", fmt.Sprintf("hi %d", i), "body"))
	}
	h.until("30 unread", func() bool {
		h.advance(250 * time.Millisecond)
		return h.app.unread[acc.ID] == 30
	})
	if h.app.events < 30 || h.app.refreshes*2 > h.app.events {
		t.Fatalf("%d refreshes for %d events", h.app.refreshes, h.app.events)
	}
}

func TestHumanizeErrors(t *testing.T) {
	wrap := func(err error) error { return fmt.Errorf("op: %w", err) }
	seen := map[string]bool{}
	for _, err := range []error{
		core.ErrStaleDraft, core.ErrInvalidState, core.ErrNoOutboxDraft, core.ErrRunActive, core.ErrStaleApproval,
		core.ErrNeedsGrant, core.ErrAINotConfigured, core.ErrModelNotFound, core.ErrTooLarge,
		core.ErrKeychainUnavailable,
		&core.CheckError{Report: core.CheckReport{Steps: []core.CheckStep{{Name: "imap", Detail: "login refused"}}}},
		&ai.Error{Code: ai.CodeUnauthorized}, &ai.Error{Code: ai.CodeRateLimited}, &ai.Error{Code: ai.CodeUnavailable},
		&ai.Error{Code: ai.CodeBadResponse}, &ai.Error{Code: ai.CodeInternal}, &ai.Error{Code: ai.CodeStale},
		&ai.Error{Code: ai.CodeIncomplete},
		&core.SendBlockedError{Plugin: "guard", Message: "no attachment"},
	} {
		got := humanize(wrap(err))
		if got == "" || strings.Contains(got, "出错了") || strings.HasPrefix(got, "error.") {
			t.Errorf("humanize(%v) = %q, want its own text", err, got)
		}
		if seen[got] {
			t.Errorf("humanize(%v) = %q, shared with another error", err, got)
		}
		seen[got] = true
	}
	if got := humanize(&core.CheckError{Report: core.CheckReport{Steps: []core.CheckStep{{Name: "imap", Detail: "login refused"}}}}); !strings.Contains(got, "IMAP") || !strings.Contains(got, "login refused") {
		t.Errorf("check error: %q", got)
	}
	if got := humanize(&core.SendBlockedError{Plugin: "guard", Message: "no attachment"}); !strings.Contains(got, "guard") || !strings.Contains(got, "no attachment") {
		t.Errorf("send blocked: %q", got)
	}
	for _, err := range []error{context.Canceled, wrap(context.Canceled), &ai.Error{Code: ai.CodeCanceled}, nil} {
		if got := humanize(err); got != "" {
			t.Errorf("humanize(%v) = %q, want silence", err, got)
		}
	}
	if got := humanize(errors.New("disk on fire")); !strings.Contains(got, "出错了") || !strings.Contains(got, "disk on fire") {
		t.Errorf("unknown error: %q", got)
	}
}

func TestToasts(t *testing.T) {
	h, _ := withAccount(t)
	h.app.toast("已保存")
	h.app.toastErr(core.ErrTooLarge)
	h.app.toastErr(context.Canceled)
	h.tt.Frame()
	h.see("已保存")
	h.see(humanize(core.ErrTooLarge))
}

func TestTitleBarInset(t *testing.T) {
	h, _ := withAccount(t)
	h.see("后退")
	h.tt.SetTitleBar(ui.TitleBar{Height: 28, Left: 78})
	r, ok := h.tt.Find("后退")
	if !ok || r.X < 78 {
		t.Fatalf("back button at %+v (found %v)", r, ok)
	}
}

func TestFirstLoadErrorRetry(t *testing.T) {
	h := newHarness(t, func(e *coretest.Env) { e.Svc.Close() })
	h.see("出错了") // the closed Service fails the read
	h.see("重试")
	h.app.fetch = func(context.Context) (core.Settings, []core.Account, error) {
		return core.Settings{Theme: "light"}, nil, nil
	}
	h.click("重试")
	h.until("the shell after a retry", func() bool { return h.app.loaded })
	h.tt.Frame()
	if h.tt.HasText("重试") || h.app.route.Page != PageOnboarding {
		t.Fatalf("route %+v texts %q", h.app.route, h.tt.Texts())
	}
}

func TestRefreshErrorToastsOnce(t *testing.T) {
	h, _ := withAccount(t)
	ok := h.app.fetch
	h.app.fetch = func(context.Context) (core.Settings, []core.Account, error) {
		return core.Settings{}, nil, errors.New("disk on fire")
	}
	h.app.refresh(nil)
	h.see("disk on fire")
	fail := func(msg string) {
		h.app.fetch = func(context.Context) (core.Settings, []core.Account, error) {
			return core.Settings{}, nil, errors.New(msg)
		}
		h.app.refresh(nil)
	}
	fail("disk on fire, again")
	h.until("the second failure", func() bool { return h.app.loadErr.Error() == "disk on fire, again" })
	if h.tt.HasText("disk on fire, again") {
		t.Fatal("a second toast in one failure streak")
	}
	h.app.fetch = ok
	h.app.refresh(nil)
	h.until("recovered", func() bool { return h.app.loadErr == nil })
	fail("disk melted")
	h.see("disk melted") // a new streak toasts again
}

func TestTitleBarHeight(t *testing.T) {
	h, _ := withAccount(t)
	for _, bar := range []ui.TitleBar{{Height: 28, Left: 78}, {Height: 40, Right: 138}} {
		h.tt.SetTitleBar(bar)
		barH, _ := chromeMetrics(bar.Height, theme.Light()) // 44 here, the strip's own height on Windows
		r, ok := h.tt.Find("后退")
		if !ok || r.Y+r.H/2 != barH/2 {
			t.Errorf("bar %+v: back button at %+v, want centered in a %v-high bar", bar, r, barH)
		}
	}
}

func TestCheckSettingsCopy(t *testing.T) {
	got := humanize(&core.CheckError{Report: core.CheckReport{Steps: []core.CheckStep{{Name: "settings", Detail: "password is empty"}}}})
	if got != "账户设置检查未通过：password is empty" {
		t.Fatalf("%q", got)
	}
}

func TestKitRailOnlyInDev(t *testing.T) {
	t.Run("production", func(t *testing.T) {
		if withAccountHarness(t, newHarness).tt.HasText("Kit") {
			t.Fatal("the Kit entry shows in a production build")
		}
	})
	t.Run("dev", func(t *testing.T) {
		d := withAccountHarness(t, newDevHarness)
		d.click("Kit")
		d.until("the gallery", func() bool { _, ok := d.tt.Find("section:Button"); return ok })
	})
}

// withAccountHarness is mk's harness with one account, its initial load applied (past onboarding).
func withAccountHarness(t *testing.T, mk func(*testing.T, ...func(*coretest.Env)) *harness) *harness {
	h := mk(t, func(e *coretest.Env) { e.AddAccount(t, "alice@example.com") })
	h.until("initial load", func() bool { return h.app.loaded })
	return h
}

// dragDivider drags the sidebars' divider by dx and lets the drag go.
func (h *harness) dragDivider(dx float32) {
	h.t.Helper()
	r, ok := h.tt.Find(i18n.T("shell.resize"))
	if !ok {
		h.t.Fatalf("no divider; texts %q", h.tt.Texts())
	}
	x, y := r.X+r.W/2, r.Y+r.H/2
	h.tt.Press(x, y)
	h.tt.Move(x+dx/2, y)
	h.tt.Move(x+dx, y)
	h.tt.Release(x+dx, y)
	h.drain()
	h.tt.Frame()
}

// TestSidebarWidthSharedAndSaved: a drag widens every page's sidebar, within
// 240 to 480, and the width is saved once the drag rests.
func TestSidebarWidthSharedAndSaved(t *testing.T) {
	h, _ := withAccount(t)
	h.see("全部收件箱")
	start, _ := h.tt.Find(i18n.T("shell.resize"))
	h.dragDivider(100)
	moved, _ := h.tt.Find(i18n.T("shell.resize"))
	if d := moved.X - start.X; d < 99 || d > 101 {
		t.Fatalf("divider moved %v, want 100", d)
	}
	h.openSettings("general")
	if r, _ := h.tt.Find(i18n.T("shell.resize")); r.X != moved.X {
		t.Errorf("settings' divider at %v, mail's at %v: want one width", r.X, moved.X)
	}
	h.advance(sidebarSaveDelay)
	h.until("the width saved", func() bool {
		st, _ := h.env.Svc.GetSettings(context.Background())
		return st.SidebarWidth == theme.SidebarWidth+100
	})

	h.dragDivider(1000)
	if h.app.sidebarW != theme.SidebarMaxWidth {
		t.Errorf("dragged far right: %v, want %v", h.app.sidebarW, float32(theme.SidebarMaxWidth))
	}
	h.dragDivider(-(theme.SidebarMaxWidth - theme.SidebarMinWidth + theme.SidebarCollapsePull/2))
	if h.app.sidebarW != theme.SidebarMinWidth || h.app.sidebarHidden {
		t.Errorf("dragged just past the least: %v (hidden %v), want %v shown", h.app.sidebarW, h.app.sidebarHidden, float32(theme.SidebarMinWidth))
	}
	h.dragDivider(-1000)
	if !h.app.sidebarHidden || h.app.sidebarW != theme.SidebarMinWidth {
		t.Errorf("dragged far left: hidden %v at %v, want folded away, back at the drag's start %v", h.app.sidebarHidden, h.app.sidebarW, float32(theme.SidebarMinWidth))
	}
	// Folded, the window's left edge has the same grip: a short pull is not
	// enough, a longer one brings the sidebar back.
	pull := func(dx float32) {
		r, ok := h.tt.Find(i18n.T("shell.resize")) // the edge grip comes before the list's
		if !ok {
			t.Fatal("no edge grip while the sidebar is folded")
		}
		x, y := r.X+r.W/2, r.Y+r.H/2
		h.tt.Press(x, y)
		h.tt.Move(x+dx/2, y)
		h.tt.Move(x+dx, y)
		h.tt.Release(x+dx, y)
		h.drain()
		h.tt.Frame()
	}
	pull(theme.SidebarCollapsePull / 2)
	if !h.app.sidebarHidden {
		t.Error("a short pull opened the sidebar")
	}
	pull(theme.SidebarCollapsePull * 2)
	if h.app.sidebarHidden {
		t.Error("a long pull left the sidebar folded")
	}
	h.app.sidebarHidden = true
	h.tt.Frame()
	h.tt.Advance(2 * theme.MotionBase)
	pull(0) // a click
	if h.app.sidebarHidden {
		t.Error("a click on the edge grip left the sidebar folded")
	}
}

type sidebarMotionPage struct{}

func (sidebarMotionPage) Title() (string, string) { return "", "" }
func (sidebarMotionPage) refresh([]events.Event)  {}
func (sidebarMotionPage) Sidebar(c *ui.Context)   { ui.Text(c, "sidebar") }
func (sidebarMotionPage) Content(c *ui.Context)   { ui.Box(c).Fill().Label("content") }

func TestSidebarToggleAnimates(t *testing.T) {
	for _, reduce := range []bool{false, true} {
		t.Run(fmt.Sprintf("reduce=%v", reduce), func(t *testing.T) {
			a := &App{route: Route{Page: PageSettings}, pages: map[Page]pageView{PageSettings: sidebarMotionPage{}}}
			tt := ui.NewTester(func(c *ui.Context) {
				theme.Apply(c, theme.Light())
				a.titleBar(c, theme.TopBarHeight)
				a.body(c)
			}, 700, 200)
			tt.SetPreferences(ui.Preferences{ReduceMotion: reduce})
			left := func() float32 { r, _ := tt.Find("content"); return r.X }
			shown := left()
			for _, hidden := range []bool{true, false} {
				before := left()
				label := "shell.sidebar.collapse"
				if !hidden {
					label = "shell.sidebar.expand"
				}
				if err := tt.Click(i18n.T(label)); err != nil {
					t.Fatal(err)
				}
				first := left()
				tt.Advance(theme.MotionFast / 2)
				middle := left()
				tt.Advance(2 * theme.MotionBase)
				last := left()
				if (first == last) != reduce || before == last {
					t.Errorf("hidden=%v: content jumped or stayed still: before %v, first %v, last %v", hidden, before, first, last)
				}
				if !reduce && (middle <= min(before, last) || middle >= max(before, last)) {
					t.Errorf("hidden=%v: no intermediate layout: before %v, middle %v, last %v", hidden, before, middle, last)
				}
				if !hidden && last != shown {
					t.Errorf("expanded content at %v, want original %v", last, shown)
				}
			}
			if !reduce {
				tt.Click(i18n.T("shell.sidebar.collapse"))
				tt.Advance(theme.MotionFast / 2)
				tt.Click(i18n.T("shell.sidebar.expand"))
				if left() >= shown {
					t.Error("reversing the animation jumped to fully expanded")
				}
				tt.Advance(2 * theme.MotionBase)
				if left() != shown || a.sidebarWidth() != theme.SidebarWidth {
					t.Errorf("toggle changed the saved width: content %v, sidebar %v", left(), a.sidebarWidth())
				}
			}
		})
	}
}

func TestSidebarDragDuringCollapse(t *testing.T) {
	a := &App{route: Route{Page: PageSettings}, pages: map[Page]pageView{PageSettings: sidebarMotionPage{}}, clock: newFakeClock()}
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, theme.Light())
		a.titleBar(c, theme.TopBarHeight)
		a.body(c)
	}, 700, 200)
	left := func() float32 { r, _ := tt.Find("content"); return r.X }
	shown, width := left(), a.sidebarWidth()
	if err := tt.Click(i18n.T("shell.sidebar.collapse")); err != nil {
		t.Fatal(err)
	}
	tt.Advance(theme.MotionFast / 2)
	if x := left(); x <= theme.GripWidth || x >= shown {
		t.Fatalf("not in the middle of collapsing: content at %v, shown %v", x, shown)
	}
	r, ok := tt.Find(i18n.T("shell.resize"))
	if !ok {
		t.Fatal("no grip during collapse")
	}
	x, y := r.X+r.W/2, r.Y+r.H/2
	tt.Press(x, y)
	if left() >= shown || a.sidebarWidth() != width {
		t.Fatal("pressing the grip jumped or changed the saved width")
	}
	x += theme.SidebarCollapsePull * 2
	tt.Move(x, y)
	if a.sidebarHidden {
		t.Fatal("dragging during collapse did not reopen the sidebar")
	}
	tt.Release(x, y)
	tt.Advance(2 * theme.MotionBase)
	if left() != shown || a.sidebarWidth() != width || a.sidebarSave != nil {
		t.Fatalf("reopening changed the width: content %v (want %v), width %v (want %v), save scheduled %v", left(), shown, a.sidebarWidth(), width, a.sidebarSave != nil)
	}
}

// TestVibrancyShowsThroughTopBar: with the window's material on, the top
// bar is drawn transparent, with the sidebar beside the content.
func TestVibrancyShowsThroughTopBar(t *testing.T) {
	h, _ := withAccount(t)
	h.app.vibrancy = true
	h.see("全部收件箱")
	if _, ok := h.tt.Find(i18n.T("shell.resize")); !ok {
		t.Fatal("no sidebar divider: the mail page should split")
	}
	h.tt.Frame()
	img := h.tt.Image()
	tint := float32(theme.MaterialTint)
	// The top bar's middle, above its text: light, the Window tint over the
	// material, which still shows a little; dark, the material alone.
	if px, want := img.RGBAAt(img.Bounds().Dx()/2, 4).A, uint8(255*tint); px < want-1 || px > want+1 {
		t.Errorf("top bar alpha %d: want %d, the material showing a little through the tint", px, want)
	}
	h.tt.SetDark(true)
	h.tt.Frame()
	img = h.tt.Image()
	if px := img.RGBAAt(img.Bounds().Dx()/2, 4); px.A != 0 {
		t.Errorf("dark top bar pixel %v: want transparent, the material showing", px)
	}
}

func TestWindowsChromeRadius(t *testing.T) {
	tok := theme.Light()
	for _, tc := range []struct {
		goos   string
		barH   float32
		radius float32
	}{{"windows", 40, tok.Radius.Lg}, {"darwin", 44, tok.Radius.Xl}, {"linux", 44, tok.Radius.Xl}} {
		old := goos
		goos = tc.goos
		bar, r := chromeMetrics(40, tok)
		goos = old
		if bar != tc.barH || r != tc.radius {
			t.Errorf("%s: bar %v radius %v, want %v %v", tc.goos, bar, r, tc.barH, tc.radius)
		}
	}
}

// TestDialogCoversMaterial: on a window material, the window paints itself
// opaque while a dialog shows, so that the backdrop's blur covers the
// sidebar's text, and lets the material show again once it closes.
func TestDialogCoversMaterial(t *testing.T) {
	h, _ := withAccount(t)
	h.app.vibrancy = true
	h.tt.SetVibrancy(true)
	h.tt.Frame()
	edge := func() uint8 { return h.tt.Image().RGBAAt(2, 400).A } // the window's left inset
	if a := edge(); a == 255 {
		t.Fatalf("material covered with no dialog: alpha %d", a)
	}
	h.app.link, h.app.linkOpen = "https://example.com/", true
	h.tt.Frame()
	if a := edge(); a != 255 {
		t.Errorf("material shows under the dialog: alpha %d", a)
	}
	h.tt.Key(0, ui.KeyEscape)
	h.tt.Frame()
	if a := edge(); a == 255 {
		t.Errorf("material still covered after closing: alpha %d", a)
	}
}
