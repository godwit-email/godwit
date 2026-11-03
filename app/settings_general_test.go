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
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/theme"
)

// TestThemeThumbTooltip: the theme name shows only as a tooltip on hover.
func TestThemeThumbTooltip(t *testing.T) {
	h, _ := withAccount(t)
	h.openSettings("general")
	count := func() (n int) { // the label counts once; the tip is a second text
		for _, x := range h.tt.Texts() {
			if x == "深色" {
				n++
			}
		}
		return
	}
	if count() > 1 {
		t.Fatal("the theme name shows without hover")
	}
	r, ok := h.tt.Find("深色")
	if !ok {
		t.Fatal("no dark thumbnail")
	}
	if math.Abs(float64(r.W-76)) > 0.01 || math.Abs(float64(r.H-48)) > 0.01 { // float layout: the card's title line is fractional
		t.Errorf("thumbnail %vx%v, want 76x48", r.W, r.H)
	}
	h.tt.Move(r.X+r.W/2, r.Y+r.H/2)
	h.tt.Advance(900 * time.Millisecond)
	if count() < 2 {
		t.Errorf("no tooltip after hover: %q", h.tt.Texts())
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
	for _, s := range []string{"主题", "跟随系统时随 macOS 的外观切换", "登录后在后台启动 Godwit"} {
		h.see(s)
	}
	if _, ok := h.tt.Find("总结、翻译和 Wit 的回答用这种语言"); ok {
		t.Fatal("阅读语言 is on General; it belongs to the Intelligence page's 功能 tab")
	}
	h.openSettings("models")
	h.click("功能")
	h.see("总结、翻译和 Wit 的回答用这种语言")
	h.see("为收到的邮件准备几条简短的回复")
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
	h.resize(1280, 1600) // the group sits at the page's end; ClickAt needs it in the window
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
	h.see("不使用代理")
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
	h.until("credentials saved", func() bool { return proxyUser() == "alice:pr0xy-pass" })
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
