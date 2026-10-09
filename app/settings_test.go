package app

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/theme"
)

// openSettings shows the settings section sec ("general", "account:<id>"…).
func (h *harness) openSettings(sec string) *settingsPage {
	h.t.Helper()
	h.app.navigate(Route{Page: PageSettings, Settings: sec})
	h.drain()
	h.tt.Frame()
	return h.app.pages[PageSettings].(*settingsPage)
}

func (h *harness) account(id int64) core.Account {
	h.t.Helper()
	accs, err := h.env.Svc.ListAccounts(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	for _, a := range accs {
		if a.ID == id {
			return a
		}
	}
	h.t.Fatalf("no account %d", id)
	return core.Account{}
}

func (h *harness) called(call string) {
	h.t.Helper()
	h.until(call, func() bool { return slices.Contains(h.plat.Calls(), call) })
}

func TestSettingsAccountEdit(t *testing.T) {
	h, acc := withAccount(t)
	h.openSettings(accountRoute(acc.ID))
	h.see("基本信息")
	h.see("登录与服务器")
	h.fill("发件人名称", "Alice Liddell")
	h.fill("侧栏显示名称", "个人")
	h.fill("签名内容", "Alice")
	h.advance(599 * time.Millisecond)
	h.drain()
	if got := h.account(acc.ID); got.Name != "alice" {
		t.Fatalf("saved before 600 ms of quiet: %+v", got)
	}
	h.advance(time.Millisecond)
	h.until("fields saved", func() bool {
		got := h.account(acc.ID)
		return got.Name == "Alice Liddell" && got.Alias == "个人" && got.Signature == "Alice"
	})
	h.until("the form settles", func() bool { return !h.app.pages[PageSettings].(*settingsPage).forms[acc.ID].pending })

	// Sync range and Sync now.
	h.click("每个文件夹最近 2000 封（推荐）")
	h.click("最近 10000 封")
	h.until("sync limit saved", func() bool { return h.account(acc.ID).SyncLimit == 10000 })
	h.click("立即同步")
	h.see("已同步 · 刚刚")
}

func TestSettingsAccountEditFailureReverts(t *testing.T) {
	h, acc := withAccount(t)
	p := h.openSettings(accountRoute(acc.ID))
	p.update = func(context.Context, int64, core.AccountPatch) (core.Account, error) {
		return core.Account{}, errors.New("disk full")
	}
	h.fill("发件人名称", "Nope")
	h.advance(600 * time.Millisecond)
	h.see("出错了：disk full")
	h.until("name reverted", func() bool { return p.forms[acc.ID].name == "alice" })
}

func TestSettingsDefaultAccount(t *testing.T) {
	var a, b core.Account
	h := newHarness(t, func(e *coretest.Env) {
		a = e.AddAccount(t, "alice@example.com")
		b = e.AddAccount(t, "bob@example.com")
	})
	h.until("initial load", func() bool { return h.app.loaded })
	h.openSettings(accountRoute(b.ID))
	h.see("bob@example.com")
	if _, badge := h.tt.Find("默认发件"); badge {
		t.Fatalf("bob shows as the default: %q", h.tt.Texts())
	}
	h.click("设为默认发件账户（在「全部收件箱」里写邮件时使用）")
	h.until("bob is the default", func() bool { return h.account(b.ID).IsDefault && !h.account(a.ID).IsDefault })
	h.until("the badge", func() bool { _, badge := h.tt.Find("默认发件"); return badge })
}

func TestSettingsRemoveAccount(t *testing.T) {
	h, acc := withAccount(t)
	h.openSettings(accountRoute(acc.ID))
	h.click("删除 alice@example.com")
	h.see("服务器上的邮件不受影响。")
	h.click("删除")
	h.until("onboarding", func() bool { return h.app.route.Page == PageOnboarding })
	if accs, err := h.env.Svc.ListAccounts(context.Background()); err != nil || len(accs) != 0 {
		t.Fatalf("accounts %+v, %v", accs, err)
	}
}

func TestSettingsReauthBanner(t *testing.T) {
	h, acc := withAccount(t)
	h.openSettings(accountRoute(acc.ID))
	h.see("状态正常")
	h.env.IMAP(acc.ID).SetPassword("x")
	_ = h.env.Svc.SyncNow(context.Background(), acc.ID)
	banner := "登录已过期，已停止同步。本地已有的邮件仍然可以查看和搜索，写好的邮件会留在发件箱，重新登录后自动发出。"
	h.until("the banner", func() bool {
		h.advance(refreshEvery)
		return h.tt.HasText(banner)
	})
	r, ok := h.tt.Find("需要处理")
	if !ok {
		t.Fatalf("no amber dot: %q", h.tt.Texts())
	}
	got, want := h.tt.Image().RGBAAt(int(r.X+r.W/2), int(r.Y+r.H/2)), theme.Light().Attention.SRGB()
	if d := max(diff(got.R, want.R), diff(got.G, want.G), diff(got.B, want.B)); d > 8 {
		t.Fatalf("dot %v, want amber %v", got, want)
	}

	h.env.IMAP(acc.ID).SetPassword("secret") // the user changed it back on the server
	h.click("更新密码")
	h.fill("密码", "secret")
	h.click("保存")
	h.until("signed in again", func() bool { return h.account(acc.ID).Status == "ok" })
	h.until("the banner goes", func() bool { return !h.tt.HasText(banner) })
}

func diff(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

func TestSettingsCache(t *testing.T) {
	h, acc := withAccount(t)
	ctx := context.Background()
	h.env.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Report", "see attached",
		coretest.Attachment("report.txt", "text/plain", []byte("quarterly numbers"))))
	if err := h.env.Svc.SyncNow(ctx, acc.ID); err != nil {
		t.Fatal(err)
	}
	page, err := h.env.Svc.ListMessages(ctx, core.View{Unified: "inbox"}, "", 10)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("messages %+v, %v", page, err)
	}
	m, err := h.env.Svc.GetMessage(ctx, page.Items[0].ID, false)
	if err != nil || len(m.Attachments) != 1 {
		t.Fatalf("message %+v, %v", m, err)
	}
	if _, _, err := h.env.Svc.GetAttachment(ctx, m.Attachments[0].ID); err != nil {
		t.Fatal(err)
	}
	p := h.openSettings(accountRoute(acc.ID))
	h.until("usage", func() bool { return p.usage[acc.ID] > 0 })
	h.see("本地占用 " + formatBytes(p.usage[acc.ID]))
	h.click("清理附件缓存")
	h.see("清理附件缓存？")
	h.click("清理")
	h.until("cleared", func() bool { return p.usage[acc.ID] == 0 })
	h.see("本地占用 0 B")
	if n, err := h.env.Svc.AttachmentCacheUsage(ctx, acc.ID); err != nil || n != 0 {
		t.Fatalf("usage %d, %v", n, err)
	}
}

func TestSettingsGeneralApplies(t *testing.T) {
	t.Cleanup(func() { i18n.SetLanguage("zh-CN") })
	h, _ := withAccount(t)
	h.openSettings("general")
	h.see("外观")
	h.click("深色")
	h.called("SetThemeSource dark")
	px := h.tt.Image().RGBAAt(railWidth/2, 400) // the rail shows the window's color
	if int(px.R)+int(px.G)+int(px.B) > 3*96 {
		t.Fatalf("window pixel %v is not dark", px)
	}
	h.click("开机时自动启动")
	h.called("SetOpenAtLogin true")
	h.until("system language", func() bool { return h.app.sysLang != "" })
	h.click(i18n.Tf("settings.ui_language.system", langName(h.app.sysLang)))
	h.click("English")
	h.see("General")
	h.see("Privacy & security")
	h.until("saved", func() bool {
		st, err := h.env.Svc.GetSettings(context.Background())
		return err == nil && st.Theme == "dark" && st.LaunchAtLogin && st.UILanguage == "en"
	})
}

func TestSettingsRapidTogglesOrdered(t *testing.T) {
	h, _ := withAccount(t)
	release := make(chan struct{})
	var calls atomic.Int32
	save := h.app.saveSettings
	h.app.saveSettings = func(ctx context.Context, st core.Settings) (core.Settings, error) {
		if calls.Add(1) == 1 {
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		return save(ctx, st)
	}
	released := false
	t.Cleanup(func() {
		if !released {
			close(release)
		}
	})
	h.openSettings("general")
	h.click("开机时自动启动")
	h.until("the first save blocks", func() bool { return calls.Load() == 1 })
	h.click("在菜单栏显示未读数")
	h.click("通知里显示邮件预览")
	h.click("开机时自动启动")
	close(release)
	released = true
	h.until("saves done", func() bool { return h.app.writes.keys["settings"] == nil })
	st, err := h.env.Svc.GetSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.LaunchAtLogin || !st.UnreadInMenuBar || !st.NotifyPreview {
		t.Fatalf("saved %+v", st)
	}
	if ui := h.app.settings; ui.LaunchAtLogin != st.LaunchAtLogin || ui.UnreadInMenuBar != st.UnreadInMenuBar || ui.NotifyPreview != st.NotifyPreview {
		t.Fatalf("UI %+v, saved %+v", ui, st)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("%d saves, want 2 (the waiting ones coalesce)", n)
	}
}

func TestSettingsSaveFailureReverts(t *testing.T) {
	h, _ := withAccount(t)
	h.app.saveSettings = func(context.Context, core.Settings) (core.Settings, error) {
		return core.Settings{}, errors.New("disk full")
	}
	h.openSettings("general")
	h.click("开机时自动启动")
	h.called("SetOpenAtLogin true")
	h.see("出错了：disk full")
	if h.app.settings.LaunchAtLogin {
		t.Fatal("the failed setting stayed on")
	}
	h.called("SetOpenAtLogin false")
}

func TestSettingsPrivacyAndAbout(t *testing.T) {
	h, _ := withAccount(t)
	h.openSettings("privacy")
	h.see("密码只存在系统钥匙串")
	h.see("系统钥匙串可用")
	h.openSettings("about")
	h.see("版本 " + Version)
	h.click("github.com/godwit-email/godwit")
	h.called("OpenURL https://github.com/godwit-email/godwit")
}
