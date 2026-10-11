package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
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
	h.fillRow("发件人名称", "Alice Liddell")
	h.fillRow("侧栏显示名称", "个人")
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
	h.until("the form settles", func() bool {
		f := h.app.pages[PageSettings].(*settingsPage).forms[acc.ID]
		return !f.edits[0].pending && !f.edits[1].pending && !f.edits[2].pending
	})

	// Sync range and Sync now.
	h.click("每个文件夹最近 2000 封")
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
	h.fillRow("发件人名称", "Nope")
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
	h.click("设为默认发件账户")
	h.until("bob is the default", func() bool { return h.account(b.ID).IsDefault && !h.account(a.ID).IsDefault })
	h.until("the badge", func() bool { _, badge := h.tt.Find("默认发件"); return badge })
}

func TestSettingsRemoveAccount(t *testing.T) {
	h, acc := withAccount(t)
	h.openSettings(accountRoute(acc.ID))
	h.clickRemove()
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
	h.see("已同步")
	h.env.IMAP(acc.ID).SetPassword("x")
	_ = h.env.Svc.SyncNow(context.Background(), acc.ID)
	banner := "。本地邮件仍可查看和搜索，写好的邮件会留在发件箱，重新登录后自动发出。"
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
	h.see(formatBytes(p.usage[acc.ID]))
	h.click("清理附件缓存")
	h.see("清理附件缓存？")
	h.click("清理")
	h.until("cleared", func() bool { return p.usage[acc.ID] == 0 })
	h.see("0 B")
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
	px := h.tt.Image().RGBAAt(theme.RailWidth/2, 400) // the rail shows the window's color
	if int(px.R)+int(px.G)+int(px.B) > 3*96 {
		t.Fatalf("window pixel %v is not dark", px)
	}
	h.click("开机时自动启动")
	h.called("SetOpenAtLogin true")
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

// blockingUpdate is the Service's UpdateAccount, whose first call waits
// for release; it records each call's IMAP host as it starts and ends.
type blockingUpdate struct {
	svc     *core.Service
	mu      sync.Mutex
	log     []string
	release chan struct{}
	calls   int
}

func (b *blockingUpdate) update(ctx context.Context, id int64, p core.AccountPatch) (core.Account, error) {
	b.mu.Lock()
	b.calls++
	n := b.calls
	host := p.Servers.IMAPHost
	b.log = append(b.log, "start "+host)
	b.mu.Unlock()
	if n == 1 {
		<-b.release
	}
	acc, err := b.svc.UpdateAccount(ctx, id, p)
	b.mu.Lock()
	b.log = append(b.log, "end "+host)
	b.mu.Unlock()
	return acc, err
}

func (b *blockingUpdate) Log() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.log)
}

// saveServers opens the servers dialog, sets the hosts and saves.
func (h *harness) saveServers(imap, smtp string) {
	h.t.Helper()
	h.click("编辑")
	h.fill("IMAP 服务器", imap)
	h.fill("SMTP 服务器", smtp)
	h.click("保存")
}

func TestSettingsAccountWritesOrdered(t *testing.T) {
	h, acc := withAccount(t)
	p := h.openSettings(accountRoute(acc.ID))
	b := &blockingUpdate{svc: h.env.Svc, release: make(chan struct{})}
	p.update = b.update
	h.saveServers("localhost", "127.0.0.1")
	h.until("the first save blocks", func() bool { return len(b.Log()) == 1 })
	h.click("取消")
	h.saveServers("127.0.0.1", "localhost")
	h.drain()
	if got := b.Log(); len(got) != 1 {
		t.Fatalf("the second save started while the first ran: %q", got)
	}
	close(b.release)
	want := []string{"start localhost", "end localhost", "start 127.0.0.1", "end 127.0.0.1"}
	h.until("both saves", func() bool { return slices.Equal(b.Log(), want) })
	h.until("the dialog closes", func() bool { return p.dialog == "" })
	if got := h.account(acc.ID); got.IMAPHost != "127.0.0.1" || got.SMTPHost != "localhost" {
		t.Fatalf("stored IMAP %q SMTP %q, want the second save's 127.0.0.1 / localhost", got.IMAPHost, got.SMTPHost)
	}
}

func TestSettingsStaleDialogResult(t *testing.T) {
	h, acc := withAccount(t)
	p := h.openSettings(accountRoute(acc.ID))
	b := &blockingUpdate{svc: h.env.Svc, release: make(chan struct{})}
	p.update = b.update
	h.saveServers("localhost", "127.0.0.1")
	h.until("the first save blocks", func() bool { return len(b.Log()) == 1 })
	h.click("取消")
	h.click("编辑")
	h.fill("IMAP 服务器", "typing.example")
	close(b.release)
	h.until("the first save ends", func() bool { return len(b.Log()) == 2 })
	h.until("the reload after it", func() bool { h.drain(); return h.app.writes.chains[accountRoute(acc.ID)] == nil })
	h.tt.Frame()
	if p.dialog != "servers" || !p.open || p.busy || p.servers.imapHost != "typing.example" {
		t.Fatalf("the old save touched the new dialog: %q open %v busy %v host %q", p.dialog, p.open, p.busy, p.servers.imapHost)
	}
}

func TestSettingsSavedFieldSurvivesStaleLoad(t *testing.T) {
	h, acc := withAccount(t)
	p := h.openSettings(accountRoute(acc.ID))
	stale := slices.Clone(h.app.accounts)
	release := make(chan struct{})
	var calls atomic.Int32
	fetch := h.app.fetch
	h.app.fetch = func(ctx context.Context) (core.Settings, []core.Account, error) {
		st, accs, err := fetch(ctx)
		if calls.Add(1) == 1 { // read before the save: hand back the old accounts
			<-release
			return st, stale, err
		}
		return st, accs, err
	}
	h.app.load()
	staleGen := h.app.loadGen
	h.until("the stale load runs", func() bool { return calls.Load() == 1 })
	h.fillRow("发件人名称", "Alice Liddell")
	h.advance(accountSaveDelay)
	h.until("saved", func() bool { return p.forms[acc.ID].ok.Name == "Alice Liddell" })
	if h.app.accounts[0].Name != "Alice Liddell" {
		t.Fatalf("the saved account was not merged: %+v", h.app.accounts[0])
	}
	h.until("the reload after the save", func() bool { return h.app.accountsGen > staleGen })
	h.app.loadGen = staleGen // let the stale load apply, as if it were the newest
	close(release)
	h.until("the stale load applies", func() bool { return h.app.accountsGen == staleGen && h.app.accounts[0].Name == "alice" })
	h.tt.Frame()
	if f := p.forms[acc.ID]; f.name != "Alice Liddell" {
		t.Fatalf("a stale load reverted the field to %q", f.name)
	}
}

func TestSettingsStaleLoadKeepsSavedSettings(t *testing.T) {
	h, _ := withAccount(t)
	release := make(chan struct{})
	var calls atomic.Int32
	fetch := h.app.fetch
	h.app.fetch = func(ctx context.Context) (core.Settings, []core.Account, error) {
		st, accs, err := fetch(ctx) // reads the settings before the save
		if calls.Add(1) == 1 {
			<-release
		}
		return st, accs, err
	}
	h.app.load()
	h.until("the load read", func() bool { return calls.Load() == 1 })
	h.openSettings("general")
	h.click("开机时自动启动")
	h.until("saved", func() bool { return h.app.writes.keys["settings"] == nil && h.app.settingsOK.LaunchAtLogin })
	gen := h.app.loadGen
	close(release)
	h.until("the old load finishes", func() bool { return h.app.accountsGen == gen })
	if !h.app.settings.LaunchAtLogin {
		t.Fatal("a load begun before the save reverted the setting")
	}
}

func TestSettingsLanguageAtStart(t *testing.T) {
	t.Cleanup(func() { i18n.SetLanguage("zh-CN") })
	h := newHarness(t, func(e *coretest.Env) {
		e.AddAccount(t, "alice@example.com")
		st, err := e.Svc.GetSettings(context.Background())
		if err == nil {
			st.UILanguage = "en"
			_, err = e.Svc.SetSettings(context.Background(), st)
		}
		if err != nil {
			t.Fatal(err)
		}
	})
	h.see("Mail")
	h.see("Settings")
}

// gatedSettings blocks the App's first settings save until releaseSave, and
// (once armed) a load after it read the settings until releaseLoad.
type gatedSettings struct {
	releaseSave, releaseLoad chan struct{}
	saves                    atomic.Int32
	armed, read              atomic.Bool
}

func gateSettings(h *harness) *gatedSettings {
	g := &gatedSettings{releaseSave: make(chan struct{}), releaseLoad: make(chan struct{})}
	save, fetch := h.app.saveSettings, h.app.fetch
	h.app.saveSettings = func(ctx context.Context, st core.Settings) (core.Settings, error) {
		if g.saves.Add(1) == 1 {
			select {
			case <-g.releaseSave:
			case <-ctx.Done():
			}
		}
		return save(ctx, st)
	}
	h.app.fetch = func(ctx context.Context) (core.Settings, []core.Account, error) {
		st, accs, err := fetch(ctx)
		if g.armed.CompareAndSwap(true, false) {
			g.read.Store(true)
			select {
			case <-g.releaseLoad:
			case <-ctx.Done():
			}
		}
		return st, accs, err
	}
	return g
}

// staleLoad starts a load that reads the stored settings now and is held;
// it returns the load's gen.
func (g *gatedSettings) staleLoad(h *harness) uint64 {
	g.armed.Store(true)
	h.app.load()
	gen := h.app.loadGen
	h.until("the load read", func() bool { return g.read.Load() })
	return gen
}

func TestSettingsLoadDuringSaveKeepsEdit(t *testing.T) {
	h, _ := withAccount(t)
	g := gateSettings(h)
	h.openSettings("general")
	h.click("开机时自动启动")
	h.until("the save blocks", func() bool { return g.saves.Load() == 1 })
	gen := g.staleLoad(h)
	close(g.releaseSave)
	h.until("saved", func() bool { return h.app.writes.keys["settings"] == nil && h.app.settingsOK.LaunchAtLogin })
	close(g.releaseLoad)
	h.until("the load applies", func() bool { return h.app.accountsGen == gen })
	if !h.app.settings.LaunchAtLogin {
		t.Fatal("a load that read before the save reverted the edit")
	}
}

func TestSettingsLoadBeforeSaveKeepsBothEdits(t *testing.T) {
	h, _ := withAccount(t)
	g := gateSettings(h)
	h.openSettings("general")
	h.click("开机时自动启动")
	h.until("the save blocks", func() bool { return g.saves.Load() == 1 })
	gen := g.staleLoad(h)
	close(g.releaseLoad)
	h.until("the load applies", func() bool { return h.app.accountsGen == gen })
	if !h.app.settings.LaunchAtLogin {
		t.Fatal("a load during the save reverted the edit")
	}
	h.click("在菜单栏显示未读数")
	close(g.releaseSave)
	h.until("saves done", func() bool { return h.app.writes.keys["settings"] == nil })
	st, err := h.env.Svc.GetSettings(context.Background())
	if err != nil || !st.LaunchAtLogin || !st.UnreadInMenuBar {
		t.Fatalf("stored %+v, %v; want both edits", st, err)
	}
}

func TestCloseSavesSettings(t *testing.T) {
	h, acc := withAccount(t)
	h.openSettings(accountRoute(acc.ID))
	h.fill("签名内容", "Bye")
	closed := false
	h.app.BeforeClose(func() {
		closed = true
		if got := h.account(acc.ID).Signature; got != "Bye" {
			t.Errorf("closed before the signature saved: %q", got)
		}
	})
	h.until("closed", func() bool { return closed })

	// A settings snapshot still saving holds the close until it is done.
	release := make(chan struct{})
	save := h.app.saveSettings
	h.app.saveSettings = func(ctx context.Context, st core.Settings) (core.Settings, error) {
		<-release
		return save(ctx, st)
	}
	st := h.app.settings
	st.Theme = "dark"
	h.app.changeSettings(st)
	closed = false
	h.app.BeforeClose(func() { closed = true })
	h.drain()
	if closed {
		t.Fatal("closed while the settings were saving")
	}
	close(release)
	h.until("closed after the settings saved", func() bool { return closed })
}

// localAPIHarness is withAccount with the stored settings set by edit
// before the App starts, and the fake platform answering SetLocalAPI with
// path or err.
func localAPIHarness(t *testing.T, path string, err error, edit func(*core.Settings)) *harness {
	h := newHarness(t, func(e *coretest.Env) {
		e.AddAccount(t, "alice@example.com")
		if edit != nil {
			st, err := e.Svc.GetSettings(context.Background())
			if err == nil {
				edit(&st)
				_, err = e.Svc.SetSettings(context.Background(), st)
			}
			if err != nil {
				t.Fatal(err)
			}
		}
	})
	h.plat.localAPIPath, h.plat.localAPIErr = path, err
	h.until("initial load", func() bool { return h.app.loaded })
	return h
}

func TestLocalAPIDefaultOff(t *testing.T) {
	h := localAPIHarness(t, "/tmp/gw/godwit.sock", nil, nil)
	if h.app.settings.LocalAPI {
		t.Fatal("the Local API is on by default")
	}
	h.openSettings("privacy")
	h.see("启用本机 API")
	for _, c := range h.plat.Calls() {
		if strings.HasPrefix(c, "SetLocalAPI") {
			t.Fatalf("called %s with the switch off", c)
		}
	}
	if h.tt.HasText("/tmp/gw/godwit.sock") || h.tt.HasText(`"mcp"`) {
		t.Fatal("the socket shows while the Local API is off")
	}
}

func TestLocalAPISwitch(t *testing.T) {
	h := localAPIHarness(t, "/tmp/gw/godwit.sock", nil, nil)
	h.openSettings("privacy")
	h.click("启用本机 API")
	h.called("SetLocalAPI true")
	h.see("/tmp/gw/godwit.sock")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	q, _ := json.Marshal(exe)
	config := `{"command": ` + string(q) + `, "args": ["mcp"]}`
	h.see(config)
	h.click("复制配置")
	if got := h.tt.Clipboard(); got != config {
		t.Fatalf("clipboard %q, want %q", got, config)
	}
	h.until("saved on", func() bool {
		st, err := h.env.Svc.GetSettings(context.Background())
		return err == nil && st.LocalAPI
	})
	h.click("启用本机 API")
	h.called("SetLocalAPI false")
	h.until("the socket hides", func() bool { h.tt.Frame(); return !h.tt.HasText("/tmp/gw/godwit.sock") })
	h.until("saved off", func() bool {
		st, err := h.env.Svc.GetSettings(context.Background())
		return err == nil && !st.LocalAPI
	})
}

// A stored LocalAPI starts the server once the settings first load.
func TestLocalAPIStartsOnLoad(t *testing.T) {
	h := localAPIHarness(t, "/tmp/gw/godwit.sock", nil, func(st *core.Settings) { st.LocalAPI = true })
	h.called("SetLocalAPI true")
	h.openSettings("privacy")
	h.see("/tmp/gw/godwit.sock")
}

func TestLocalAPIFailureReverts(t *testing.T) {
	h := localAPIHarness(t, "", errors.New("socket path too long"), nil)
	h.openSettings("privacy")
	h.click("启用本机 API")
	h.called("SetLocalAPI true")
	h.see("socket path too long")
	h.until("reverted and saved off", func() bool {
		st, err := h.env.Svc.GetSettings(context.Background())
		return err == nil && !st.LocalAPI && !h.app.settings.LocalAPI && h.app.writes.keys["settings"] == nil
	})
	if slices.Contains(h.plat.Calls(), "SetLocalAPI false") {
		t.Fatal("stopped a server that never started")
	}
}

func TestLocalAPICopyWarnsFullAccess(t *testing.T) {
	t.Cleanup(func() { i18n.SetLanguage("zh-CN") })
	h := localAPIHarness(t, "", nil, nil)
	h.openSettings("privacy")
	h.see("完整的本机 API 能读取邮件、发送邮件、修改账户和设置")
	h.see("只能读取邮件和创建草稿，不能发送")
	i18n.SetLanguage("en")
	h.tt.Frame()
	h.see("The full Local API can read mail, send mail, and change accounts and settings")
	h.see("can only read mail and create drafts, never send")
}

// localAPIRace clicks the switch on, off and on while the first "on" blocks,
// then lets it fail; last says how the third request ends. It returns
// whether the fake server is on at the end.
func localAPIRace(t *testing.T, last error) (*harness, func() bool) {
	release := make(chan struct{})
	var mu sync.Mutex
	n, server := 0, false
	h := localAPIHarness(t, "", nil, nil)
	h.plat.localAPIFn = func(on bool) (string, error) {
		mu.Lock()
		n++
		k := n
		mu.Unlock()
		if k == 1 {
			<-release
		}
		mu.Lock()
		defer mu.Unlock()
		switch {
		case !on:
			server = false
			return "", nil
		case k == 1:
			server = false
			return "", errors.New("first start failed")
		case last != nil:
			server = false
			return "", last
		}
		server = true
		return "/tmp/gw/godwit.sock", nil
	}
	h.openSettings("privacy")
	h.click("启用本机 API")
	h.called("SetLocalAPI true")
	h.click("启用本机 API")
	h.click("启用本机 API")
	close(release)
	h.until("every request done", func() bool {
		return h.app.writes.chains["local-api"] == nil && h.app.writes.keys["settings"] == nil
	})
	got := slices.DeleteFunc(h.plat.Calls(), func(c string) bool { return !strings.HasPrefix(c, "SetLocalAPI") })
	if !slices.Equal(got, []string{"SetLocalAPI true", "SetLocalAPI false", "SetLocalAPI true"}) {
		t.Fatalf("calls %v", got)
	}
	return h, func() bool { mu.Lock(); defer mu.Unlock(); return server }
}

// A stale failure of an older "on" neither reverts nor stops the newest one.
func TestLocalAPIStaleFailureIgnored(t *testing.T) {
	h, serverOn := localAPIRace(t, nil)
	st, err := h.env.Svc.GetSettings(context.Background())
	if err != nil || !st.LocalAPI || !h.app.settings.LocalAPI || !serverOn() {
		t.Fatalf("saved %v (%v), switch %v, server %v; want all on", st.LocalAPI, err, h.app.settings.LocalAPI, serverOn())
	}
	h.see("/tmp/gw/godwit.sock")
	if h.tt.HasText("first start failed") {
		t.Fatal("a stale failure shows")
	}
}

func TestLocalAPINewestFailureReverts(t *testing.T) {
	h, serverOn := localAPIRace(t, errors.New("newest start failed"))
	h.see("newest start failed")
	h.until("saved off", func() bool {
		st, err := h.env.Svc.GetSettings(context.Background())
		return err == nil && !st.LocalAPI && !h.app.settings.LocalAPI
	})
	if serverOn() {
		t.Fatal("the server is on with the switch off")
	}
}

// The server stopping by itself turns the switch off with the reason.
func TestLocalAPIServerStopped(t *testing.T) {
	h := localAPIHarness(t, "/tmp/gw/godwit.sock", nil, func(st *core.Settings) { st.LocalAPI = true })
	h.openSettings("privacy")
	h.see("/tmp/gw/godwit.sock")
	h.app.LocalAPIStopped(h.app.localAPIGen, errors.New("accept: too many open files"))
	h.see("accept: too many open files")
	h.until("saved off", func() bool {
		st, err := h.env.Svc.GetSettings(context.Background())
		return err == nil && !st.LocalAPI && !h.app.settings.LocalAPI
	})
	h.until("the socket hides", func() bool { h.tt.Frame(); return !h.tt.HasText("/tmp/gw/godwit.sock") })
}

// A data directory other than the default goes into the MCP config.
func TestLocalAPIMCPConfigDataDir(t *testing.T) {
	h := localAPIHarness(t, "/tmp/gw/godwit.sock", nil, func(st *core.Settings) { st.LocalAPI = true })
	h.plat.mu.Lock()
	h.plat.dataDir, h.plat.customDir = `/tmp/gw "x"`, true
	h.plat.mu.Unlock()
	h.openSettings("privacy")
	exe, _ := os.Executable()
	q, _ := json.Marshal(exe)
	config := `{"command": ` + string(q) + `, "args": ["mcp","--data-dir","/tmp/gw \"x\""]}`
	h.see(config)
	h.click("复制配置")
	if got := h.tt.Clipboard(); got != config {
		t.Fatalf("clipboard %q, want %q", got, config)
	}
}

// A stop notice for a server the user has since replaced changes nothing.
func TestLocalAPIStaleStopIgnored(t *testing.T) {
	h := localAPIHarness(t, "/tmp/gw/godwit.sock", nil, func(st *core.Settings) { st.LocalAPI = true })
	h.called("SetLocalAPI true")
	h.until("on", func() bool { return h.app.writes.chains["local-api"] == nil })
	old := h.app.localAPIGen
	h.openSettings("privacy")
	h.click("启用本机 API")
	h.click("启用本机 API")
	h.until("off and on again", func() bool {
		return h.app.writes.chains["local-api"] == nil && h.app.writes.keys["settings"] == nil && h.app.localAPIGen == old+2
	})
	h.app.LocalAPIStopped(old, errors.New("the old server died"))
	h.drain()
	h.tt.Frame()
	h.until("settled", func() bool { return h.app.writes.chains["local-api"] == nil && h.app.writes.keys["settings"] == nil })
	st, err := h.env.Svc.GetSettings(context.Background())
	if err != nil || !st.LocalAPI || !h.app.settings.LocalAPI {
		t.Fatalf("saved %v (%v), switch %v; want on", st.LocalAPI, err, h.app.settings.LocalAPI)
	}
	if h.app.localAPIGen != old+2 {
		t.Fatalf("a stale notice made a request (gen %d, want %d)", h.app.localAPIGen, old+2)
	}
	h.see("/tmp/gw/godwit.sock")
	if h.tt.HasText("the old server died") {
		t.Fatal("a stale notice shows")
	}
}

// fillRow fills the input at the right of the settings row labelled label; the
// row's text sits in the label's name too, so a click lands beside it.
func (h *harness) fillRow(label, value string) {
	h.t.Helper()
	r, ok := h.tt.Find(label)
	if !ok {
		h.t.Fatalf("no row %q", label)
	}
	h.tt.ClickAt(r.X+r.W+theme.SettingsInputWidth/2, r.Y+r.H/2)
	h.tt.Command("selectAll")
	h.typeText(value)
}

// clickRemove presses the 删除账户 button, found by its accessible label (the visible text is also the section title).
func (h *harness) clickRemove() { h.t.Helper(); h.click("删除账户 alice@example.com") }

func TestAccountHeaderMeta(t *testing.T) {
	h, acc := withAccount(t)
	h.app.syncedAtBy[acc.ID] = h.clock.Now().Add(-3 * time.Hour)
	st := h.app.settings
	st.Theme = "dark"
	h.app.changeSettings(st)
	h.openSettings(accountRoute(acc.ID))
	h.see("已同步 · 3 小时前")
	chip, _ := h.tt.Find("已同步 · 3 小时前")
	title, _ := h.tt.Find("alice@example.com")
	badge, ok := h.tt.Find("默认发件")
	if !ok || badge.Y < title.Y+title.H || chip.Y < title.Y+title.H {
		t.Fatalf("badge %v and chip %v belong under the title %v", badge, chip, title)
	}
	got, want := h.tt.Image().RGBAAt(int(chip.X-4), int(chip.Y+chip.H/2)), theme.Dark().SuccessBg.SRGB()
	if d := max(diff(got.R, want.R), diff(got.G, want.G), diff(got.B, want.B)); d > 8 {
		t.Fatalf("chip %v, want dark SuccessBg %v", got, want)
	}
	delete(h.app.syncedAtBy, acc.ID)
	h.advance(time.Minute)
	h.tt.Frame()
	if _, ok := h.tt.Find("已同步"); !ok {
		t.Fatalf("no bare chip: %q", h.tt.Texts())
	}
}

func TestReauthBannerElapsed(t *testing.T) {
	h, acc := withAccount(t)
	h.env.IMAP(acc.ID).SetPassword("x")
	_ = h.env.Svc.SyncNow(context.Background(), acc.ID)
	h.app.syncedAtBy[acc.ID] = h.clock.Now().Add(-3 * time.Hour)
	h.openSettings(accountRoute(acc.ID))
	h.until("elapsed banner", func() bool {
		h.advance(refreshEvery)
		return h.tt.HasText("登录已过期，3 小时前起停止同步。")
	})
	h.see("需要重新登录")
	delete(h.app.syncedAtBy, acc.ID)
	h.advance(time.Minute)
	h.tt.Frame()
	if !h.tt.HasText("登录已过期，已停止同步。") {
		t.Fatalf("no unknown-time banner: %q", h.tt.Texts())
	}
}

func TestDeleteAccountOutline(t *testing.T) {
	h, acc := withAccount(t)
	h.openSettings(accountRoute(acc.ID))
	h.see("清除本机的邮件缓存、草稿和钥匙串里的凭据。服务器上的邮件不受影响")
	if h.tt.HasText("清除本机的邮件缓存、草稿和钥匙串里的凭据。服务器上的邮件不受影响。") {
		t.Fatal("row desc keeps the trailing 。")
	}
	h.clickRemove()
	h.see("删除 alice@example.com？") // the dialog keeps the email
}

// TestSyncRangeLabels: onboarding recommends 2000, settings lists it plain.
func TestSyncRangeLabels(t *testing.T) {
	for _, lang := range []string{"zh-CN", "en"} {
		i18n.SetLanguage(lang)
		onb, set := i18n.T("onboarding.sync.2000"), limitOptions(2000)[0].Label
		if !strings.Contains(onb, "（推荐）") && !strings.Contains(onb, "recommended") || strings.Contains(set, "推荐") || strings.Contains(set, "recommended") || set == "" {
			t.Errorf("%s: onboarding %q, settings %q", lang, onb, set)
		}
	}
	i18n.SetLanguage("zh-CN")
}
