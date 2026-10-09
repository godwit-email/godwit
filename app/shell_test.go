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
	if h.tt.HasText("邮件") {
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
	h.see("同步失败")
}

func TestReauthStatus(t *testing.T) {
	h, acc := withAccount(t)
	h.env.IMAP(acc.ID).SetPassword("x")
	_ = h.env.Svc.SyncNow(context.Background(), acc.ID) // marks the account reauth_required
	h.until("reauth shown", func() bool {
		h.advance(250 * time.Millisecond) // the throttled refresh reloads the accounts
		return h.tt.HasText("需要重新登录")
	})
	h.click("需要重新登录")
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
		core.ErrStaleDraft, core.ErrInvalidState, core.ErrRunActive, core.ErrStaleApproval,
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
