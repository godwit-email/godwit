package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/egoist/mygo/ui"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/theme"
)

func TestRemoteImagesAvatarSetting(t *testing.T) {
	defer i18n.SetLanguage("zh-CN")
	for _, tc := range []struct {
		lang  string
		words []string
	}{
		{"zh-CN", []string{"发件人头像", "发件人网站和 Gravatar", "本机 IP", "阅读时间", "邮箱哈希", "Cookie"}},
		{"en", []string{"sender avatars", "sender websites and Gravatar", "IP address", "reading time", "email hash", "cookies"}},
	} {
		t.Run(tc.lang, func(t *testing.T) {
			h, _ := mailHarness(t, nil, "alice@example.com")
			i18n.SetLanguage(tc.lang)
			p := h.app.pages[PageSettings].(*settingsPage)
			p.st = h.app.settings
			tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c, theme.Light()); p.remoteImagesView(c) }, 1100, 800)
			text := strings.Join(tt.Texts(), " ")
			for _, word := range tc.words {
				if !strings.Contains(text, word) {
					t.Errorf("%s missing %q in %q", tc.lang, word, text)
				}
			}
		})
	}
}

// openSettings shows the settings section sec ("general", "account:<id>"…).
func (h *harness) openSettings(sec string) *settingsPage {
	h.t.Helper()
	h.app.navigate(Route{Page: PageSettings, Settings: sec})
	h.drain()
	h.tt.Frame()
	return h.app.pages[PageSettings].(*settingsPage)
}

// openSignature makes a signature the account uses, opens it, and waits
// for the signature page to show it.
func (h *harness) openSignature(accountID int64, name string) core.Signature {
	h.t.Helper()
	sig, err := h.env.Svc.CreateSignature(context.Background(), name)
	if err != nil {
		h.t.Fatal(err)
	}
	if accountID != 0 {
		if _, err := h.env.Svc.UpdateAccount(context.Background(), accountID, core.AccountPatch{SignatureID: &sig.ID}); err != nil {
			h.t.Fatal(err)
		}
	}
	h.app.load()
	h.until("the signature loads", func() bool {
		return slices.ContainsFunc(h.app.signatures, func(s core.Signature) bool { return s.ID == sig.ID })
	})
	h.openSettings(signatureRoute(sig.ID))
	h.until("the signature page shows it", func() bool { return h.plat.sigShown() == sig.ID })
	return sig
}

func (h *harness) signatureHTML(id int64) string {
	h.t.Helper()
	sigs, err := h.env.Svc.ListSignatures(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	for _, s := range sigs {
		if s.ID == id {
			return s.HTML
		}
	}
	h.t.Fatalf("no signature %d", id)
	return ""
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
	h.advance(599 * time.Millisecond)
	h.drain()
	if got := h.account(acc.ID); got.Name != "alice" {
		t.Fatalf("saved before 600 ms of quiet: %+v", got)
	}
	h.advance(time.Millisecond)
	h.until("fields saved", func() bool {
		got := h.account(acc.ID)
		return got.Name == "Alice Liddell" && got.Alias == "个人"
	})
	h.until("the form settles", func() bool {
		f := h.app.pages[PageSettings].(*settingsPage).forms[acc.ID]
		return !f.edits[0].pending && !f.edits[1].pending
	})
	// The sidebar lists the account by its sidebar name (a field's value is not a text), and a search finds it by it.
	h.see("个人")
	p := h.app.pages[PageSettings].(*settingsPage)
	if got := p.accountByEmail("个人"); got == nil || got.ID != acc.ID {
		t.Fatalf("a search by the sidebar name misses the account: %+v", got)
	}

	// Sync range and Sync now.
	h.click("每个文件夹最近 2000 封")
	h.click("最近 3 个月")
	h.until("sync range saved", func() bool { return h.account(acc.ID).Sync == core.SyncRange{Months: 3} })
	h.click("最近 3 个月")
	h.click("全部")
	h.until("sync range saved", func() bool { return h.account(acc.ID).Sync == core.SyncRange{} })
	h.click("立即同步")
	h.see("已同步 · 刚刚")
}

// Instructions longer than core takes are not saved: they stay for the user
// to shorten, and shortened they save.
func TestSettingsAccountAIInstructions(t *testing.T) {
	h, acc := withAccount(t)
	p := h.openSettings(accountRoute(acc.ID))
	h.see("Wit 指令")
	f := p.forms[acc.ID]
	long := strings.Repeat("长", core.MaxAIInstructions+1)
	f.ai = long
	p.edited(acc.ID, f, fieldAI)
	h.advance(accountSaveDelay)
	h.drain()
	h.see("最多 2000 个字")
	if got := h.account(acc.ID).AIInstructions; got != "" {
		t.Fatalf("too long instructions saved: %d runes", len([]rune(got)))
	}
	if f.ai != long {
		t.Fatalf("too long instructions dropped")
	}

	f.ai = "电话：+86 10 12345678"
	p.edited(acc.ID, f, fieldAI)
	h.advance(accountSaveDelay)
	h.until("saved", func() bool { return h.account(acc.ID).AIInstructions == "电话：+86 10 12345678" })
	h.until("the form settles", func() bool { return !f.edits[fieldAI].pending })
}

func TestSettingsAccountIcon(t *testing.T) {
	h, acc := withAccount(t)
	p := h.openSettings(accountRoute(acc.ID))
	p.openDialog("icon", acc)
	if !p.open || p.dialog != "icon" {
		t.Fatalf("dialog not open: %v, %v", p.open, p.dialog)
	}

	p.iconSelected = "label:shield-keyhole"
	h.click("保存")
	// The save is written before its result closes the dialog, a frame on: wait for both.
	h.until("icon saved, the dialog closed", func() bool {
		return h.account(acc.ID).Icon == "label:shield-keyhole" && !p.open
	})

	p.openDialog("icon", h.account(acc.ID))
	p.iconSelected = ""
	h.click("保存")
	h.until("icon reset", func() bool {
		return h.account(acc.ID).Icon == ""
	})
}

func TestSettingsAccountCustomIconUpload(t *testing.T) {
	h, acc := withAccount(t)
	p := h.openSettings(accountRoute(acc.ID))
	p.openDialog("icon", acc)

	svgData := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><circle cx="12" cy="12" r="10" fill="red"/></svg>`
	tmpFile := filepath.Join(t.TempDir(), "icon.svg")
	if err := os.WriteFile(tmpFile, []byte(svgData), 0o600); err != nil {
		t.Fatal(err)
	}
	h.plat.files = []string{tmpFile}
	p.uploadAccountIcon()
	h.until("custom icon uploaded into selection", func() bool { return string(p.iconUpload) == svgData })
	h.click("保存")
	h.until("custom icon saved to account", func() bool {
		return strings.HasPrefix(h.account(acc.ID).Icon, "image:")
	})
	icon, err := h.env.Svc.AccountIcon(context.Background(), acc.ID)
	if err != nil || icon.ContentType != "image/svg+xml" || string(icon.Data) != svgData {
		t.Fatalf("stored icon = %q %q, %v", icon.ContentType, icon.Data, err)
	}
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

func TestSettingsReorderAccounts(t *testing.T) {
	var a, b, c core.Account
	h := newHarness(t, func(e *coretest.Env) {
		a = e.AddAccount(t, "alice@example.com")
		b = e.AddAccount(t, "bob@example.com")
		c = e.AddAccount(t, "carol@example.com")
	})
	h.until("initial load", func() bool { return h.app.loaded })
	h.openSettings("general")
	first, _ := h.tt.Find("alice@example.com")
	second, _ := h.tt.Find("bob@example.com")
	row := second.Y - first.Y
	x, y := first.X, first.Y+first.H/2
	h.tt.Move(x, y)
	h.tt.Press(x, y)
	h.tt.Move(x, y+row/2)
	h.tt.Move(x, y+row*2.5) // below carol: the end
	h.tt.Release(x, y+row*2.5)
	h.drain()
	ids := func(accs []core.Account) []int64 {
		var out []int64
		for _, acc := range accs {
			out = append(out, acc.ID)
		}
		return out
	}
	want := []int64{b.ID, c.ID, a.ID}
	h.until("the new order saved", func() bool {
		accs, err := h.env.Svc.ListAccounts(context.Background())
		return err == nil && slices.Equal(ids(accs), want)
	})
	if got := ids(h.app.accounts); !slices.Equal(got, want) {
		t.Fatalf("shown order %v, want %v", got, want)
	}
	h.click("carol@example.com")
	h.until("a row still opens its account", func() bool { return h.app.route.Settings == accountRoute(c.ID) })
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
		t.Fatalf("no red dot: %q", h.tt.Texts())
	}
	got, want := h.tt.Image().RGBAAt(int(r.X+r.W/2), int(r.Y+r.H/2)), theme.Light().Danger.SRGB() // signing in again is the user's to do
	if d := max(diff(got.R, want.R), diff(got.G, want.G), diff(got.B, want.B)); d > 8 {
		t.Fatalf("dot %v, want red %v", got, want)
	}

	h.env.IMAP(acc.ID).SetPassword("secret") // the user changed it back on the server
	h.click("更新密码")
	h.fill("密码", "secret")
	h.click("保存")
	h.until("signed in again", func() bool { return h.account(acc.ID).Status == "ok" })
	h.until("the banner goes", func() bool { return !h.tt.HasText(banner) })
}

func TestSettingsPasswordWhitespace(t *testing.T) {
	h, acc := withAccount(t)
	p := h.openSettings(accountRoute(acc.ID))
	p.openDialog("password", acc)
	const pasted = "\r\n abcd-efgh\tijkl\u00a0mnop \n"
	p.secret = pasted
	h.tt.Frame()
	h.see("检测到空格、换行或零宽字符，可能是复制时带入的。是否清除？")
	if p.secret != pasted {
		t.Fatal("password changed without confirmation")
	}
	h.click("清除空白字符")
	if p.secret != "abcd-efghijklmnop" {
		t.Fatalf("password after cleanup %q", p.secret)
	}
	if h.tt.HasText("检测到空格、换行或零宽字符") {
		t.Fatal("cleanup hint stayed after cleanup")
	}
}

func TestSettingsPasswordVisibility(t *testing.T) {
	h, acc := withAccount(t)
	p := h.openSettings(accountRoute(acc.ID))
	p.openDialog("password", acc)
	h.tt.Frame()
	h.fill("密码", "typed-secret")
	h.click("显示密码")
	h.see("隐藏密码")
	if p.secret != "typed-secret" {
		t.Fatal("showing the password changed its value")
	}
	h.click("隐藏密码")
	h.see("显示密码")
	if p.secret != "typed-secret" {
		t.Fatal("hiding the password changed its value")
	}
	h.click("显示密码")
	h.click("取消")
	p.openDialog("password", acc)
	h.tt.Frame()
	if p.secret != "" {
		t.Fatal("reopening the password dialog kept the password")
	}
	h.see("显示密码")
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
	px := h.tt.Image().RGBAAt(2, 400) // the window's left inset shows its color
	if int(px.R)+int(px.G)+int(px.B) > 3*96 {
		t.Fatalf("window pixel %v is not dark", px)
	}
	h.click("开机时自动启动")
	h.called("SetOpenAtLogin true")
	if got := langName("zh-TW"); got != "正體中文" {
		t.Fatalf("langName(zh-TW) = %q, want 正體中文", got)
	}
	if got := langName("zh-Hant-TW"); got != "正體中文" {
		t.Fatalf("langName(zh-Hant-TW) = %q, want 正體中文", got)
	}
	if got := langName("zh-HK"); got != "繁體中文" {
		t.Fatalf("langName(zh-HK) = %q, want 繁體中文", got)
	}
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
	if st.LaunchAtLogin || !st.UnreadInMenuBar || st.NotifyPreview { // preview starts on
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
	h.called("NewBirdView") // failed here: the logo stands in
	h.click("加星标")          // no gh: GitHub stars it
	h.called("OpenURL https://github.com/godwit-email/godwit")
	h.click("检查更新")
	h.called("CheckForUpdates")
	h.plat.mu.Lock()
	h.plat.latest = "9.0.0"
	h.plat.mu.Unlock()
	h.openSettings("privacy")
	h.openSettings("about")
	h.click("升级到 9.0.0")
	h.plat.mu.Lock()
	h.plat.gh = true
	h.plat.mu.Unlock()
	h.openSettings("privacy")
	h.openSettings("about")
	h.click("加星标")
	h.called("StarRepo")
	h.see("GitHub") // starred: a link like the others
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
	h.openSignature(acc.ID, "Work")
	h.plat.sigType("<p>Bye</p>")
	closed := false
	h.app.BeforeClose(func() {
		closed = true
		if got := h.account(acc.ID).Signature; got != core.SignatureHTMLPrefix+"<p>Bye</p>" {
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
	h.see("可以读取邮件和写草稿，能否发送见下方设置")
	i18n.SetLanguage("en")
	h.tt.Frame()
	h.see("The full Local API can read mail, send mail, and change accounts and settings")
	h.see("can read mail and write drafts, and the setting below decides whether they can send")
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

// quietAccount is an account on a harness with no background sync, so only the test's own events set a sync time.
func quietAccount(t *testing.T) (*harness, core.Account) {
	var acc core.Account
	h := newHarnessWith(t, []coretest.Option{coretest.WithoutAutoSync()}, func(e *coretest.Env) { acc = e.AddAccount(t, "alice@example.com") })
	h.until("initial load", func() bool { return h.app.loaded })
	return h, acc
}

// syncedHoursAgo delivers a SyncFinished through the app's event path and moves the clock on.
func (h *harness) syncedHoursAgo(id int64, hours int) {
	h.app.onEvent(events.Event{Type: events.SyncFinished, AccountID: id})
	h.advance(time.Duration(hours) * time.Hour)
	h.tt.Frame()
}

func TestAccountHeaderMeta(t *testing.T) {
	h, acc := quietAccount(t)
	st := h.app.settings
	st.Theme = "dark"
	h.app.changeSettings(st)
	h.openSettings(accountRoute(acc.ID))
	if _, ok := h.tt.Find("已同步"); !ok {
		t.Fatalf("no bare chip before a sync: %q", h.tt.Texts())
	}
	h.syncedHoursAgo(acc.ID, 3)
	h.see("已同步 · 3 小时前")
	// The sidebar footer shows the same text, so find the chip by its place: on the badge's row, right of it.
	badge, ok := h.tt.Find("默认发件")
	if !ok {
		t.Fatalf("no badge: %q", h.tt.Texts())
	}
	img, want, found := h.tt.Image(), theme.Dark().SuccessBg.SRGB(), false
	y := int(badge.Y + badge.H/2)
	for x := int(badge.X + badge.W); x < int(badge.X+badge.W)+400 && !found; x++ {
		got := img.RGBAAt(x, y)
		found = max(diff(got.R, want.R), diff(got.G, want.G), diff(got.B, want.B)) <= 8
	}
	if !found {
		t.Fatalf("no dark SuccessBg chip on the badge's row %d", y)
	}
}

func TestReauthBannerElapsed(t *testing.T) {
	h, acc := quietAccount(t)
	h.env.IMAP(acc.ID).SetPassword("x")
	_ = h.env.Svc.SyncNow(context.Background(), acc.ID) // fails: no SyncFinished
	h.openSettings(accountRoute(acc.ID))
	h.until("unknown-time banner", func() bool {
		h.advance(refreshEvery)
		return h.tt.HasText("登录已过期，已停止同步。")
	})
	h.syncedHoursAgo(acc.ID, 3)
	h.see("登录已过期，3 小时前起停止同步。")
	h.see("需要重新登录")
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
		onb, set := i18n.T("onboarding.sync.2000"), rangeOptions()[0].Label
		if !strings.Contains(onb, "（推荐）") && !strings.Contains(onb, "recommended") || strings.Contains(set, "推荐") || strings.Contains(set, "recommended") || set == "" {
			t.Errorf("%s: onboarding %q, settings %q", lang, onb, set)
		}
	}
	i18n.SetLanguage("zh-CN")
}

// The ranges are 2000, 3 months and all, nothing else; the header counts the
// folders once synced.
func TestSyncRangeOptionsAndFolders(t *testing.T) {
	opts := rangeOptions()
	var got []string
	for _, o := range opts {
		got = append(got, o.Label)
	}
	if want := "每个文件夹最近 2000 封|最近 3 个月|全部"; strings.Join(got, "|") != want {
		t.Fatalf("options %q", got)
	}
	// A limit set outside the app shows as it is, as an option of its own.
	if o := rangeOptions(core.SyncRange{Limit: 500}, core.SyncRange{Months: 3}); len(o) != 4 || o[3].Label != "每个文件夹最近 500 封" {
		t.Fatalf("options with 500: %+v", o)
	}
	h, acc := withAccount(t)
	h.syncNow(acc)
	h.openSettings(accountRoute(acc.ID))
	h.waitMail("the header counts the folders", func() bool { return h.tt.HasText("其他 IMAP 邮箱 · 4 个文件夹") })
}

// A signature page that never answers the flush holds the close for
// signatureFlushWait, then the window asks.
func TestCloseAsksWhenSignatureUnsaved(t *testing.T) {
	h, acc := withAccount(t)
	h.openSignature(acc.ID, "Work")
	h.plat.sigMute()
	closed := false
	h.app.BeforeClose(func() { closed = true })
	h.drain()
	if closed || h.app.closeAsk {
		t.Fatalf("settled before the flush: closed %v, ask %v", closed, h.app.closeAsk)
	}
	h.advance(signatureFlushWait)
	h.until("asks", func() bool { return h.app.closeAsk })
	if closed {
		t.Fatal("closed with the signature unsaved")
	}
	h.see("签名没有保存成功")
	// A save that lands late settles the close it held.
	h.plat.sigAnswer(true)
	h.until("closed after the late save", func() bool { return closed })
}

// A signature page that never ran holds no edit: closing neither waits
// nor asks.
func TestCloseIgnoresDeadSignaturePage(t *testing.T) {
	h, acc := withAccount(t)
	h.openSignature(acc.ID, "Work")
	h.plat.sigMute()
	h.plat.sigDead()
	closed := false
	h.app.BeforeClose(func() { closed = true })
	h.until("closed", func() bool { return closed })
	if h.app.closeAsk {
		t.Fatal("asked")
	}
}

// Closing the App closes the signature page.
func TestAppCloseClosesSignatureView(t *testing.T) {
	h, acc := withAccount(t)
	h.openSignature(acc.ID, "Work")
	h.app.Close()
	h.called("CloseSignatureView")
}

// The signature page saves a signature's edit as that one when native
// switches it to another; the other is untouched.
func TestSignatureSwitchKeepsSignatures(t *testing.T) {
	h, acc := withAccount(t)
	a := h.openSignature(acc.ID, "A")
	h.plat.sigType("<p>Alice</p>")
	b := h.openSignature(0, "B")
	if got := h.signatureHTML(a.ID); got != core.SignatureHTMLPrefix+"<p>Alice</p>" {
		t.Fatalf("a's signature %q", got)
	}
	if got := h.signatureHTML(b.ID); got != "" {
		t.Fatalf("b's signature %q", got)
	}
	if n := len(slices.DeleteFunc(h.plat.Calls(), func(c string) bool { return c != "NewSignatureView" })); n != 1 {
		t.Fatalf("%d signature views, want one", n)
	}
}

// The HTML mode edits the stored code in MyGo's editor in the page's
// place: saved clean as typing pauses, too many images said and not
// saved; the toggle goes back to the page, which reads it again.
func TestSignatureHTMLMode(t *testing.T) {
	h, acc := withAccount(t)
	sig := h.openSignature(acc.ID, "A")
	text := core.SignatureHTMLPrefix + "<p>Alice</p>"
	if _, err := h.env.Svc.UpdateSignature(context.Background(), sig.ID, core.SignaturePatch{HTML: &text}); err != nil {
		t.Fatal(err)
	}
	p := h.app.pages[PageSettings].(*settingsPage)
	h.app.SignatureHTML()
	h.until("the code loads", func() bool { return p.sigCode != nil && !p.sigCode.loading })
	if got := p.sigCode.st.Buffer.String(); got != "<p>Alice</p>" {
		t.Fatalf("code %q", got)
	}
	h.see("保存时会去掉脚本、事件属性和外链图片。图片在代码里写作 cid:…，要加新图片请切回富文本用「插入图片」上传。")

	p.sigCode.st.Buffer.Set(`<p onclick="x()">Bob</p><script>alert(1)</script>`)
	h.tt.Frame()
	h.advance(accountSaveDelay)
	h.until("saved clean", func() bool { return h.signatureHTML(sig.ID) == core.SignatureHTMLPrefix+"<p>Bob</p>" })

	five := "<p>Bob</p>"
	for i := range core.SignatureImagesMax + 1 {
		five += `<img src="data:image/png;base64,` + tinyPNG(t, uint8(i)) + `">`
	}
	p.sigCode.st.Buffer.Set(five)
	h.tt.Frame()
	h.advance(accountSaveDelay)
	h.see("最多 4 张图片，这次修改没有保存")
	if got := h.signatureHTML(sig.ID); got != core.SignatureHTMLPrefix+"<p>Bob</p>" {
		t.Fatalf("a refused save changed the signature: %q", got)
	}

	shows := len(slices.DeleteFunc(h.plat.Calls(), func(c string) bool { return !strings.HasPrefix(c, "ShowSignature") }))
	h.click("HTML 代码")
	// The close saves in the background; a click before it answers is ignored.
	h.until("the close's save answers", func() bool { return p.sigCode == nil || !p.sigCode.closing })
	if p.sigCode == nil {
		t.Fatal("left the HTML mode with its save refused: the edit would be lost")
	}
	p.sigCode.st.Buffer.Set("<p>Bobby</p>")
	h.tt.Frame()
	h.click("HTML 代码")
	h.until("back to the page, which reads it again", func() bool {
		return p.sigCode == nil && len(slices.DeleteFunc(h.plat.Calls(), func(c string) bool { return !strings.HasPrefix(c, "ShowSignature") })) > shows
	})
}

// Deleting a signature open in the HTML mode drops its code: the HTML
// mode works for the next one.
func TestSignatureDeletedInHTMLMode(t *testing.T) {
	h, acc := withAccount(t)
	h.openSignature(0, "Other")
	sig := h.openSignature(acc.ID, "A")
	p := h.app.pages[PageSettings].(*settingsPage)
	h.app.SignatureHTML()
	h.until("the code loads", func() bool { return p.sigCode != nil && !p.sigCode.loading })
	h.click("删除签名「A」")
	h.click("删除")
	h.until("deleted", func() bool { return h.app.route.Settings == "signatures" })
	h.until("its code dropped", func() bool { return p.sigCode == nil })
	_ = sig
	h.click("Other")
	h.until("the page shows Other", func() bool { return h.plat.sigShown() != 0 && h.app.route.Settings != "signatures" })
	h.app.SignatureHTML()
	h.until("the HTML mode opens again", func() bool { return p.sigCode != nil && !p.sigCode.loading })
}

// An HTML-mode save that failed is tried again when the App closes: the
// code shown is never dropped unsaved.
func TestSignatureHTMLFailedSaveRetriedOnClose(t *testing.T) {
	h, acc := withAccount(t)
	sig := h.openSignature(acc.ID, "A")
	p := h.app.pages[PageSettings].(*settingsPage)
	h.app.SignatureHTML()
	h.until("the code loads", func() bool { return p.sigCode != nil && !p.sigCode.loading })

	db, err := sqlite.Open(t.Context(), filepath.Join(h.env.DataDir, "godwit.db"), sqlite.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	exec := func(q string) {
		if _, err := db.Execute(t.Context(), q); err != nil {
			t.Fatal(err)
		}
	}
	exec("ALTER TABLE signatures RENAME TO signatures_away")
	p.sigCode.st.Buffer.Set("<p>Kept</p>")
	h.tt.Frame()
	h.advance(accountSaveDelay)
	h.until("the save failed", func() bool { return p.sigCode.stop == nil && len(h.app.writes.chains) == 0 })
	exec("ALTER TABLE signatures_away RENAME TO signatures")
	if got := h.signatureHTML(sig.ID); got == core.SignatureHTMLPrefix+"<p>Kept</p>" {
		t.Fatal("the first save did not fail")
	}

	closed := false
	h.app.BeforeClose(func() { closed = true })
	h.until("closed", func() bool { return closed })
	if got := h.signatureHTML(sig.ID); got != core.SignatureHTMLPrefix+"<p>Kept</p>" {
		t.Fatalf("stored %q: the close did not save the code again", got)
	}
}

// tinyPNG is a 1×1 PNG of a shade, base64.
func tinyPNG(t *testing.T, shade uint8) string {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.Gray{Y: shade})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b.Bytes())
}

// 签名: the empty state makes one and opens it; the list shows each with
// its users and text; a row opens it.
func TestSignaturesList(t *testing.T) {
	h, acc := withAccount(t)
	h.openSettings("signatures")
	h.see("还没有签名")
	if h.tt.HasText("在签名前加分隔线（-- ）") {
		t.Error("the options show with no signature")
	}
	h.click("新建签名")
	h.until("made and opened", func() bool { return strings.HasPrefix(h.app.route.Settings, "signature:") })
	h.see("未命名签名")
	h.see("没有账户在用")

	sig := h.openSignature(acc.ID, "Work")
	text := core.SignatureHTMLPrefix + "<p><b>Alice</b></p><p>CEO</p><p>Acme</p>"
	if _, err := h.env.Svc.UpdateSignature(context.Background(), sig.ID, core.SignaturePatch{HTML: &text}); err != nil {
		t.Fatal(err)
	}
	h.app.load()
	h.openSettings("signatures")
	h.see("1 个账户在用")
	h.see("CEO")
	if h.tt.HasText("Acme") {
		t.Error("the preview shows more than two lines")
	}
	h.see("方便 Mutt、Thunderbird 这类客户端识别和折叠签名")
	h.click("在签名前加分隔线（-- ）")
	h.until("the delimiter is on", func() bool {
		st, err := h.env.Svc.GetSettings(context.Background())
		return err == nil && st.SignatureDelimiter
	})
	h.click("Work")
	h.until("opened", func() bool { return h.app.route.Settings == signatureRoute(sig.ID) })
	// ‹ 签名 above the title goes back to the list (the sidebar's 签名 is selected and does not click).
	r, ok := h.tt.Find("Work")
	if !ok {
		t.Fatal("no title")
	}
	h.tt.ClickAt(r.X+8, r.Y-20) // the back link sits just above the title, its text aligned with it
	h.until("back to the list", func() bool { return h.app.route.Settings == "signatures" })
}

// A signature's page: its name saves once typing pauses; 默认用于 switches
// replace an account's signature; 删除签名 says who loses it, then deletes.
func TestSignatureDetail(t *testing.T) {
	var a, b core.Account
	h := newHarness(t, func(e *coretest.Env) {
		a = e.AddAccount(t, "alice@example.com")
		b = e.AddAccount(t, "bob@example.com")
	})
	h.until("initial load", func() bool { return h.app.loaded })
	other := h.openSignature(b.ID, "Other")
	sig := h.openSignature(a.ID, "Work")
	h.see("1 个账户在用")
	h.see("正在用这个签名")
	h.see("现在用「Other」")

	h.fillRow("名称", "Work 2")
	h.advance(accountSaveDelay)
	h.until("renamed", func() bool { return h.signatureHTML(sig.ID) == "" && h.app.signatures[1].Name == "Work 2" })

	h.click("bob@example.com 默认用这个签名")
	h.until("bob uses it", func() bool { return h.account(b.ID).SignatureID == sig.ID })
	h.see("2 个账户在用")
	h.see("删除后，alice@example.com、bob@example.com 写信时不再带签名")
	_ = other

	h.click("alice@example.com 默认用这个签名")
	h.until("alice has none", func() bool { return h.account(a.ID).SignatureID == 0 })
	h.see("现在不带签名")

	h.click("删除签名「Work 2」")
	h.see("删除「Work 2」？")
	h.click("删除")
	h.until("deleted", func() bool { return h.account(b.ID).SignatureID == 0 && h.app.route.Settings == "signatures" })
	h.see("Other")
}

// The account page picks its default signature and previews it.
func TestAccountDefaultSignature(t *testing.T) {
	h, acc := withAccount(t)
	sig := h.openSignature(0, "Work")
	text := core.SignatureHTMLPrefix + "<p>Alice at work</p>"
	if _, err := h.env.Svc.UpdateSignature(context.Background(), sig.ID, core.SignaturePatch{HTML: &text}); err != nil {
		t.Fatal(err)
	}
	h.app.load()
	h.openSettings(accountRoute(acc.ID))
	h.click("不使用签名")
	h.click("Work")
	h.until("picked", func() bool { return h.account(acc.ID).SignatureID == sig.ID })
	h.see("Alice at work")
	h.click("设置 › 签名")
	h.until("opened", func() bool { return h.app.route.Settings == "signatures" })
}

// 隐私与安全 lists the trusted senders; 移除 stops trusting one.
func TestSettingsTrustedSenders(t *testing.T) {
	h, _ := withAccount(t)
	h.openSettings("privacy")
	h.see("还没有信任的发件人")
	if err := h.env.Svc.TrustImageSender(context.Background(), "Bob <bob@example.com>"); err != nil {
		t.Fatal(err)
	}
	h.settle()
	h.advance(refreshEvery)
	h.see("bob@example.com")
	h.click("移除 bob@example.com") // the button names whom it removes
	h.until("untrusted", func() bool {
		l, err := h.env.Svc.TrustedImageSenders(context.Background())
		return err == nil && len(l) == 0
	})
	h.settle()
	h.advance(refreshEvery)
	h.see("还没有信任的发件人")
}

func TestMailboxRoleOptions(t *testing.T) {
	b := &mailbox{folders: []core.Folder{{ID: 1, Name: "INBOX", Role: "inbox"}, {ID: 2, Name: "Sent", Role: "sent"},
		{ID: 3, Name: "Trash", Role: "trash"}, {ID: 4, Name: "Archive", Role: "archive"}, {ID: 5, Name: "Bin"}},
		roles: [5]int64{0, 2, 0, 3, 4}}
	labels := func(i int) string {
		var out []string
		for _, o := range roleOptions(b, i) {
			out = append(out, o.Label)
		}
		return strings.Join(out, "|")
	}
	for i, want := range []string{
		"不使用|Bin",  // drafts: none yet; never another role's folder
		"Sent|Bin", // sent: no 不使用 once set
		"不使用|Bin",  // junk
		"Trash|Bin",
		"Archive|Bin|不使用", // archive keeps 不使用
	} {
		if got := labels(i); got != want {
			t.Errorf("%s: %q, want %q", mailboxRoles[i], got, want)
		}
	}
}

func TestSettingsMailboxPurge(t *testing.T) {
	h, acc := withAccount(t)
	h.syncNow(acc)
	h.openSettings(accountRoute(acc.ID))
	h.see("邮箱行为")
	h.see("超过这个时间的已删除邮件会被永久删除")
	h.click("永不") // junk's purge comes first
	h.click("一周后")
	h.until("the junk purge saved", func() bool {
		p, err := h.env.Svc.Purges(context.Background(), acc.ID)
		return err == nil && p["junk"].Days == 7 && p["trash"].Days == 0
	})
}

// 链接 on the signature page asks in a native dialog: a bad address is
// said, a good one answered; 移除链接 for a link already there; 取消 and
// leaving the page cancel.
func TestSignatureLinkDialog(t *testing.T) {
	h, acc := withAccount(t)
	h.openSignature(acc.ID, "A")
	type ans struct {
		url string
		ok  bool
	}
	var got []ans
	answer := func(url string, ok bool) { got = append(got, ans{url, ok}) }

	h.app.SignatureLink("", answer)
	h.see("链接地址")
	h.fill("链接地址", "example.com")
	h.click("添加链接")
	h.see("请输入以 http://、https:// 或 mailto: 开头的地址")
	r, _ := h.tt.Find("链接地址") // the field under its label: the error moved the focus off it
	h.tt.ClickAt(r.X+r.W/2, r.Y+r.H+16)
	h.tt.Command("selectAll")
	h.typeText("https://example.com/a")
	h.click("添加链接")
	h.until("answered", func() bool { return len(got) == 1 })
	if got[0] != (ans{"https://example.com/a", true}) {
		t.Fatalf("answer %+v", got[0])
	}

	h.app.SignatureLink("https://example.com/a", answer)
	h.click("移除链接")
	h.until("taken off", func() bool { return len(got) == 2 && got[1] == ans{"", true} })

	h.app.SignatureLink("", answer)
	h.click("取消")
	h.until("cancelled", func() bool { return len(got) == 3 && got[2] == ans{"", false} })

	h.app.SignatureLink("", answer)
	h.openSettings("general")
	h.until("leaving cancels", func() bool { return len(got) == 4 && got[3] == ans{"", false} })
}

func TestLinkHref(t *testing.T) {
	for in, want := range map[string]string{
		" https://example.com/a ": "https://example.com/a",
		"HTTP://Example.com":      "http://Example.com",
		"mailto:bob@example.com":  "mailto:bob@example.com",
		"example.com":             "",
		"https://":                "",
		"mailto:":                 "",
		"javascript:alert(1)":     "",
	} {
		got, ok := linkHref(in)
		if got != want || ok != (want != "") {
			t.Errorf("linkHref(%q) = %q, %v", in, got, ok)
		}
	}
}

func TestSettingsBlockedSenders(t *testing.T) {
	h, _ := withAccount(t)
	h.openSettings("privacy")
	h.see("没有已屏蔽的发件人")
	if err := h.env.Svc.BlockSender(context.Background(), "spammer@example.com"); err != nil {
		t.Fatal(err)
	}
	h.settle()
	h.advance(refreshEvery)
	h.see("spammer@example.com")
	h.click("取消屏蔽 spammer@example.com")
	h.until("unblocked", func() bool {
		l, err := h.env.Svc.BlockedSenders(context.Background())
		return err == nil && len(l) == 0
	})
	h.settle()
	h.advance(refreshEvery)
	h.see("没有已屏蔽的发件人")
}
