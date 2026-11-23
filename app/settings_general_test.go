package app

import (
	"context"
	"math"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/theme"
)

// TestThemeThumbLabel: the theme name is always shown under its thumbnail.
func TestThemeThumbLabel(t *testing.T) {
	h, _ := withAccount(t)
	h.openSettings("general")
	h.see("深色")
	r, ok := h.tt.Find("深色")
	if !ok {
		t.Fatal("no dark thumbnail")
	}
	if math.Abs(float64(r.W-76)) > 0.01 || math.Abs(float64(r.H-48)) > 0.01 { // float layout: the card's title line is fractional
		t.Errorf("thumbnail %vx%v, want 76x48", r.W, r.H)
	}
}

// TestThemeThumbsDark: each thumbnail is drawn in its own theme's tokens, whatever the app theme is.
func TestThemeThumbsDark(t *testing.T) {
	h, _ := withAccount(t)
	st := h.app.settings
	st.Theme = "dark"
	h.app.changeSettings(st)
	h.openSettings("general")
	for label, tok := range map[string]theme.Tokens{"浅色": theme.Light(), "深色": theme.Dark()} {
		r, ok := h.tt.Find(label)
		if !ok {
			t.Fatalf("no %s thumbnail", label)
		}
		// the rail, under the sidebar's top margin, shows the window colour
		got, want := h.tt.Image().RGBAAt(int(r.X)+4, int(r.Y+r.H)-6), tok.Window.SRGB()
		if d := max(diff(got.R, want.R), diff(got.G, want.G), diff(got.B, want.B)); d > 8 {
			t.Errorf("%s: rail pixel %v, want %v", label, got, want)
		}
	}
	// "system" is the light window on its left half, the dark one on its right.
	r, ok := h.tt.Find("跟随系统")
	if !ok {
		t.Fatal("no system thumbnail")
	}
	for _, px := range []struct {
		x    float32
		want ui.Color
	}{{r.X + 4, theme.Light().Window}, {r.X + r.W - 4, theme.Dark().Background}} {
		got, want := h.tt.Image().RGBAAt(int(px.x), int(r.Y+r.H)-6), px.want.SRGB()
		if d := max(diff(got.R, want.R), diff(got.G, want.G), diff(got.B, want.B)); d > 8 {
			t.Errorf("system at x %v: pixel %v, want %v", px.x-r.X, got, want)
		}
	}
}

// TestThemeThumbsFit: at font 16 the thumbnails stay inside the row and clear of the label.
func TestThemeThumbsFit(t *testing.T) {
	h, _ := withAccount(t)
	st := h.app.settings
	st.FontSize = 16
	h.app.changeSettings(st)
	h.openSettings("general")
	lab, ok1 := h.tt.Find(i18n.T("settings.theme"))
	first, ok2 := h.tt.Find("跟随系统")
	last, ok3 := h.tt.Find("深色")
	if !ok1 || !ok2 || !ok3 {
		t.Fatal("theme row not found")
	}
	if lab.X+lab.W > first.X {
		t.Errorf("label right %v past thumbnails %v", lab.X+lab.W, first.X)
	}
	if gap := last.X - (first.X + 2*(first.W+theme.ThemeTileGap)); gap != 0 {
		t.Errorf("tiles are not %v apart", theme.ThemeTileGap)
	}
	if last.X+last.W > 1280 {
		t.Errorf("thumbnails overflow: %v", last.X+last.W)
	}
}

func TestSettingsGeneralCopy(t *testing.T) {
	h, _ := withAccount(t)
	h.openSettings("general")
	for _, s := range []string{"主题", "跟随系统时随系统外观切换", "登录后在后台启动 Godwit"} {
		h.see(s)
	}
	if _, ok := h.tt.Find("总结、翻译和 Wit 的回答用这种语言"); ok {
		t.Fatal("阅读语言 is on General; it belongs to the AI 功能 page")
	}
	h.openSettings("features")
	h.see("总结、翻译和 Wit 的回答用这种语言")
	h.see("为收到的邮件准备几条简短的回复")
}

// TestSettingsRemoveAppDates: AI 功能 counts the reply dates other apps and
// Wit set that are in effect; 全部去掉 removes them, with those no longer in
// effect, its toast puts them back, and it is off at zero.
func TestSettingsRemoveAppDates(t *testing.T) {
	h, acc := withAccount(t)
	date := time.Now().AddDate(0, 0, 3).Format(time.DateOnly)
	ended := h.deliverOne(acc, coretest.Msg("Carol <carol@a.example>", acc.Email, "Plan", "Please confirm by Monday."))
	if _, err := h.env.Svc.SetThreadDeadline(context.Background(), ended.ThreadID, core.Deadline{Date: date, MessageID: ended.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.env.Svc.ArchiveThreads(context.Background(), []int64{ended.ThreadID}); err != nil {
		t.Fatal(err)
	}
	m := h.deliverOne(acc, coretest.Msg("Bob <bob@a.example>", acc.Email, "Quote", "Please confirm by Friday."))
	if _, err := h.env.Svc.SetThreadDeadline(context.Background(), m.ThreadID, core.Deadline{Date: date, MessageID: m.ID}); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		n, err := h.env.Svc.CountAppAndWitDeadlines(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	h.openSettings("features")
	h.see("其他应用和 Wit 设的回复日期")
	h.see("1")
	h.click("全部去掉")
	h.see("已去掉 1 个回复日期")
	h.until("removed", func() bool { return count() == 0 })
	h.until("the row reads 0", func() bool { return h.tt.HasText("0") })
	h.click("撤销")
	h.until("back", func() bool { return count() == 1 })

	_, err := h.env.Svc.RemoveAppAndWitDeadlines(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	h.openSettings("general")
	h.openSettings("features") // opening the page reads the count
	h.until("the row reads 0", func() bool { return h.tt.HasText("0") })
	h.click("全部去掉") // disabled: nothing happens
	if h.tt.HasText("已去掉 0 个回复日期") {
		t.Fatal("a removal at zero")
	}
}

// 邮件总结's levels: 收到就总结 only while core.SummaryOnArrival offers it.
func TestSummaryLevelsGated(t *testing.T) {
	old := core.SummaryOnArrival
	t.Cleanup(func() { core.SummaryOnArrival = old })
	for on, want := range map[bool][]string{false: {"关", "打开邮件时"}, true: {"关", "打开邮件时", "收到就总结"}} {
		core.SummaryOnArrival = on
		var got []string
		for _, o := range summaryLevels() {
			got = append(got, o.Label)
		}
		if !slices.Equal(got, want) {
			t.Errorf("SummaryOnArrival %v: %v, want %v", on, got, want)
		}
	}
}

// TestSettingsSummarySegments: 邮件总结's levels are segments, each level
// with its own help, two while 收到就总结 is not on offer; under 收到就总结,
// 更早的邮件 says the earlier mail waits for the user.
func TestSettingsSummarySegments(t *testing.T) {
	old := core.SummaryOnArrival
	core.SummaryOnArrival = false
	t.Cleanup(func() { core.SummaryOnArrival = old })
	h, _ := withAccount(t)
	h.env.LLM(t).Release()
	h.waitMail("the model in the settings", func() bool { return h.app.settings.DefaultModel != "" })
	h.openSettings("features")
	if _, ok := h.tt.AccessByRole(ui.RoleTab, "打开邮件时"); !ok {
		t.Fatal("no 打开邮件时 segment")
	}
	if _, ok := h.tt.AccessByRole(ui.RoleTab, "收到就总结"); ok {
		t.Fatal("收到就总结 while not on offer")
	}
	core.SummaryOnArrival = true
	h.tt.Frame()
	for _, c := range []struct{ level, mode, help string }{
		{"关", core.SummaryOff, "只在你点「总结」时调用模型。"},
		{"打开邮件时", core.SummaryOpen, "打开会话时自动总结它最新的那封。"},
		{"收到就总结", core.SummaryArrive, "需要你回复的新邮件（真人直接发给你、你还没回的）一到"},
	} {
		if _, ok := h.tt.AccessByRole(ui.RoleTab, c.level); !ok {
			t.Fatalf("no %s segment", c.level)
		}
		h.click(c.level)
		if c.mode == core.SummaryArrive { // it asks first
			h.click(i18n.T("settings.summary.arrive.confirm"))
		}
		h.until(c.level+" saved", func() bool {
			st, err := h.env.Svc.GetSettings(context.Background())
			return err == nil && st.SummaryMode == c.mode
		})
		h.see(c.help)
	}
	h.see("更早的邮件")
	h.see("选上之后到达的邮件会自动总结。更早的只在你点了之后做。")
}

// TestSettingsSummaryOnArrival: 收到就总结 names the model service new
// mail goes to, asks before it summarizes the last 30 days' unreplied mail,
// pauses when the service changes until the user confirms, and says so once
// another level is chosen, after that level's own help.
func TestSettingsSummaryOnArrival(t *testing.T) {
	old := core.SummaryOnArrival
	core.SummaryOnArrival = true
	t.Cleanup(func() { core.SummaryOnArrival = old })
	h, acc := withAccount(t)
	h.env.LLM(t).Release()
	h.deliverOne(acc, coretest.Msg("Bob <bob@a.example>", acc.Email, "Quote", "Please confirm by Friday."))
	ctx := context.Background()
	h.waitMail("the model in the settings", func() bool { return h.app.settings.DefaultModel != "" })
	p := h.openSettings("features")
	p.st.SummaryMode = core.SummaryArrive
	h.drain()
	h.tt.Frame()
	h.until("saved", func() bool {
		st, err := h.env.Svc.GetSettings(ctx)
		return err == nil && st.SummaryMode == core.SummaryArrive
	})
	h.until("the service and its host", func() bool { return strings.HasPrefix(h.app.arrival.Current, "test (127.0.0.1:") })
	h.see(i18n.Tf("settings.summary.arrive.help", h.app.arrival.Current))

	h.click("总结最近 30 天要回的邮件…")
	h.see(i18n.Tf("settings.summary.backfill.ask", 1, h.app.arrival.Current))
	h.click("开始总结")
	h.until("queued", func() bool {
		n, err := h.env.Svc.CountUnrepliedToSummarize(ctx)
		return err == nil && n == 0
	})

	h.env.LLMService(t, "other", "openai").Release()
	st, err := h.env.Svc.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	st.DefaultModel = "other/m1"
	if _, err := h.env.Svc.SetSettings(ctx, st); err != nil {
		t.Fatal(err)
	}
	paused, err := h.env.Svc.SummaryArrivalState(ctx)
	if err != nil || !strings.HasPrefix(paused.Current, "other (127.0.0.1:") {
		t.Fatalf("paused on %+v, %v: want the new service with its host", paused, err)
	}
	h.seeRefreshed(i18n.Tf("settings.summary.paused", paused.Current))
	h.click("继续")
	h.until("going on", func() bool {
		got, err := h.env.Svc.SummaryArrivalState(ctx)
		return err == nil && !got.Paused && got.Service == "other"
	})

	p.st.SummaryMode = core.SummaryOpen
	h.drain()
	h.tt.Frame()
	h.see("打开会话时自动总结它最新的那封。新邮件不再在收到时总结，已找到的回复日期保留。")
}

// TestThemeThumbFocusTip: Tab focus shows the tooltip too.
func TestThemeThumbFocusTip(t *testing.T) {
	h, _ := withAccount(t)
	h.openSettings("general")
	for i := 0; i < 40; i++ {
		h.tt.Key(0, ui.KeyTab)
		h.tt.Frame()
		n := 0
		for _, x := range h.tt.Texts() {
			if x == "跟随系统" {
				n++
			}
		}
		if n >= 2 {
			return
		}
	}
	t.Fatal("no tooltip after tabbing to the thumbnails")
}

// The languages are offered in the order of their tags.
func TestLanguagesByTag(t *testing.T) {
	if !slices.IsSortedFunc(languages, func(a, b struct{ tag, name string }) int { return strings.Compare(a.tag, b.tag) }) {
		t.Fatalf("languages out of tag order: %v", languages)
	}
}

// TestSettingsProxyMode: 网络代理 offers both modes, says where the system
// proxy comes from, saves the chosen mode, and a search for 代理 or proxy finds it.
func TestSettingsProxyMode(t *testing.T) {
	for _, v := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		t.Setenv(v, "")
	}
	h, _ := withAccount(t)
	p := h.openSettings("general")
	h.resize(1280, 2000) // the group sits at the page's end; ClickAt needs it in the window
	h.see("网络代理")
	h.see("使用系统网络设置中的代理")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:7890")
	h.tt.Frame()
	h.see("使用环境变量 HTTPS_PROXY、HTTP_PROXY 中的代理")
	// the select shows 跟随系统, which a theme tile shows first: click it by
	// place, at the row's middle and the tiles' right edge, where controls end
	lab, _ := h.tt.Find("代理方式")
	src, _ := h.tt.Find("使用环境变量 HTTPS_PROXY、HTTP_PROXY 中的代理")
	dark, _ := h.tt.Find("深色")
	h.tt.ClickAt(dark.X+dark.W-8, (lab.Y+src.Y+src.H)/2)
	h.drain()
	h.tt.Frame()
	h.click("不使用代理")
	h.until("proxy mode off", func() bool { return h.app.settings.ProxyMode == "off" })
	st, err := h.env.Svc.GetSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.ProxyMode != "off" {
		t.Errorf("stored ProxyMode %q, want off", st.ProxyMode)
	}
	if h.tt.HasText("中的代理") {
		t.Error("the system proxy's source shows with 不使用代理")
	}

	// 自定义: its address shows, and saves once it is usable
	pick := func(mode string) {
		lab, _ := h.tt.Find("代理方式")
		h.tt.ClickAt(dark.X+dark.W-8, lab.Y+lab.H/2)
		h.drain()
		h.tt.Frame()
		h.click(mode)
	}
	if h.tt.HasText("代理地址") {
		t.Error("the custom fields show with 不使用代理")
	}
	pick("自定义")
	h.see("代理地址")
	h.see("支持 SOCKS5 和 HTTP")
	h.fillRow("代理地址", "socks5://127.0.0.1:70000")
	h.see("代理地址无效")
	if h.app.settings.ProxyMode != "off" {
		t.Errorf("an invalid address saved mode %q", h.app.settings.ProxyMode)
	}
	h.fillRow("代理地址", "socks5://alice:x@127.0.0.1:7890")
	h.see("代理地址无效") // credentials go in their own fields
	h.fillRow("代理地址", "SOCKS5://127.0.0.1:7890/")
	h.until("custom proxy saved", func() bool {
		return h.app.settings.ProxyMode == "custom" && h.app.settings.ProxyURL == "socks5://127.0.0.1:7890"
	})
	if h.tt.HasText("代理地址无效") {
		t.Error("the address error stays once it is valid")
	}
	// the values outlast another mode
	pick("不使用代理")
	h.until("proxy mode off again", func() bool { return h.app.settings.ProxyMode == "off" })
	if h.tt.HasText("代理地址") {
		t.Error("the custom fields show with 不使用代理")
	}
	pick("自定义")
	h.until("custom again", func() bool { return h.app.settings.ProxyMode == "custom" })
	st, err = h.env.Svc.GetSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.ProxyMode != "custom" || st.ProxyURL != "socks5://127.0.0.1:7890" {
		t.Errorf("stored %q %q, want custom socks5://127.0.0.1:7890", st.ProxyMode, st.ProxyURL)
	}
	// 不使用代理的主机: comma-separated, saved as entries
	h.see("不使用代理的主机")
	h.fillRow("不使用代理的主机", "*.corp.example, 10.0.0.0/8 mail.lan")
	h.until("bypass saved", func() bool {
		return slices.Equal(h.app.settings.ProxyBypass, []string{"*.corp.example", "10.0.0.0/8", "mail.lan"})
	})
	pick("不使用代理")
	h.until("proxy mode off once more", func() bool { return h.app.settings.ProxyMode == "off" })
	if h.tt.HasText("不使用代理的主机") {
		t.Error("the bypass field shows with 不使用代理")
	}
	pick("自定义")
	h.until("custom for the credentials", func() bool { return h.app.settings.ProxyMode == "custom" })

	// the username and password save; the password goes to the keychain and never shows back
	proxyUser := func() string {
		if u := h.env.Svc.Proxy(&url.URL{Scheme: "https", Host: "example.com:993"}); u != nil && u.User != nil {
			return u.User.String()
		}
		return ""
	}
	h.fillRow("用户名", "alice")
	h.fillRow("密码", "pr0xy-pass")
	t.Cleanup(func() { // what the page saved, should the wait below time out
		if t.Failed() {
			t.Logf("settings user %q set %v; proxy user %q; draft pass %q sent %q", h.app.settings.ProxyUser, h.app.settings.ProxyPasswordSet, proxyUser(), p.proxy.pass, p.proxy.sent)
		}
	})
	h.until("credentials saved", func() bool {
		return proxyUser() == "alice:pr0xy-pass" && h.app.settings.ProxyPasswordSet && h.app.settings.ProxyPassword == ""
	})
	if h.app.settings.ProxyUser != "alice" || h.app.settings.ProxyPassword != "" {
		t.Errorf("settings %q %q, want alice and no password", h.app.settings.ProxyUser, h.app.settings.ProxyPassword)
	}
	p.proxy = proxyDraft{} // the page as shown afresh
	h.tt.Frame()
	if p.proxy.pass != "" || !h.app.settings.ProxyPasswordSet {
		t.Errorf("the password field shows %q, kept %v; want empty, kept", p.proxy.pass, h.app.settings.ProxyPasswordSet)
	}
	// a saved password, shown only as the placeholder, clears with 清除
	h.click("清除")
	h.until("saved password cleared", func() bool { return proxyUser() == "alice" && !h.app.settings.ProxyPasswordSet })
	if h.tt.HasText("清除") {
		t.Error("清除 shows with no password saved")
	}
	// erasing what was typed removes it
	h.fillRow("密码", "x")
	h.until("replaced", func() bool { return proxyUser() == "alice:x" })
	h.tt.Command("selectAll")
	h.tt.Key(0, ui.KeyBackspace)
	h.until("password removed", func() bool { return proxyUser() == "alice" })
	for _, q := range []string{"代理", "proxy"} {
		if m := p.matching(q); !slices.Contains(m.apps, "general") {
			t.Errorf("%s: %+v, want general", q, m)
		}
	}
}

// Choosing 收到就总结 asks first, naming the service and the host new mail
// would go to; only 开启 saves it, 取消 keeps the level as it was.
func TestSettingsSummaryOnArrivalAsksFirst(t *testing.T) {
	old := core.SummaryOnArrival
	core.SummaryOnArrival = true
	t.Cleanup(func() { core.SummaryOnArrival = old })
	h, _ := withAccount(t)
	h.env.LLM(t).Release()
	ctx := context.Background()
	h.waitMail("the model in the settings", func() bool { return h.app.settings.DefaultModel != "" })
	mode := func() string {
		st, err := h.env.Svc.GetSettings(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return st.SummaryMode
	}
	before := mode()
	h.openSettings("features")
	h.until("the destination", func() bool { return strings.Contains(h.app.arrival.Current, "(127.0.0.1:") })
	ask := i18n.Tf("settings.summary.arrive.help", h.app.arrival.Current)

	h.click(i18n.T("settings.summary.arrive"))
	h.see(i18n.T("settings.summary.arrive.title"))
	h.see(ask)
	h.drain()
	if got := mode(); got != before {
		t.Fatalf("saved %q before the user agreed", got)
	}
	h.click(i18n.T("common.cancel"))
	h.drain()
	if got := mode(); got != before {
		t.Fatalf("cancel saved %q", got)
	}

	h.click(i18n.T("settings.summary.arrive"))
	h.click(i18n.T("settings.summary.arrive.confirm"))
	h.until("saved", func() bool { return mode() == core.SummaryArrive })
}
