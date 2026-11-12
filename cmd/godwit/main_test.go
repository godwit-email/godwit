package main

import (
	"bytes"
	"errors"
	"github.com/godwit-email/godwit/internal/theme"
	"image/png"
	"testing"

	"github.com/egoist/mygo"
	"github.com/godwit-email/godwit/app"
	"github.com/godwit-email/godwit/core"
)

func TestBackendOnlyAtLogin(t *testing.T) {
	always := core.Settings{BackgroundReceive: core.BackgroundReceiveAlways}
	both := core.Settings{LaunchAtLogin: true, BackgroundReceive: core.BackgroundReceiveAlways}
	for _, tc := range []struct {
		name    string
		atLogin bool
		st      core.Settings
		err     error
		want    bool
	}{
		{"一直继续 at login: the backend alone", true, always, nil, true},
		{"both at login: the window, one backend", true, both, nil, false},
		{"开机时自动启动 at login", true, core.Settings{LaunchAtLogin: true}, nil, false},
		{"a login item nothing wants", true, core.Settings{}, nil, true},
		{"unreadable settings keep the window", true, always, errors.New("x"), false},
		{"opened by the user", false, always, nil, false},
	} {
		if got := backendOnlyAtLogin(tc.atLogin, tc.st, tc.err); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestWindowOptionsPerOS(t *testing.T) {
	for _, tc := range []struct {
		goos     string
		vibrancy mygo.Vibrancy
		style    mygo.TitleBarStyle
		barH     int
	}{
		{"darwin", mygo.VibrancySidebar, mygo.TitleBarHidden, 0},
		{"windows", mygo.VibrancyMica, mygo.TitleBarHiddenInset, 40},
		{"linux", mygo.VibrancyNone, mygo.TitleBarHiddenInset, 0},
	} {
		o := windowOptions(tc.goos)
		if o.Width != 1280 || o.Height != 820 || o.MinWidth != 960 || o.MinHeight != 600 || o.StateKey != "main" || o.Title != "Godwit" {
			t.Errorf("%s: size %+v", tc.goos, o)
		}
		if o.TitleBarStyle != tc.style || o.Vibrancy != tc.vibrancy || o.TitleBarHeight != tc.barH {
			t.Errorf("%s: title bar %v %v %d", tc.goos, o.TitleBarStyle, o.Vibrancy, o.TitleBarHeight)
		}
		if p := o.TrafficLightPosition; (tc.goos == "darwin") != (p != nil) || p != nil && 2*p.Y+trafficLightSize != theme.TopBarHeight {
			t.Errorf("%s: traffic lights %+v", tc.goos, p)
		}
	}
}

func TestUILocale(t *testing.T) {
	for _, tc := range []struct{ preference, system, want string }{
		{"", "zh-TW", ""},
		{"", "zh-HK", ""},
		{"", "zh-Hant", ""},
		{"", "zh-Latn", "zh-Hans"},
		{"", "zh-CN", ""},
		{"", "zh-Hans", ""},
		{"", "en-US", ""},
		{"", "fr-FR", ""},
		{"en", "zh-TW", "en"},
		{"zh-CN", "en-US", "zh-CN"},
		{"en-XA", "zh-TW", "en-XA"},
	} {
		if got := uiLocale(tc.preference, tc.system); got != tc.want {
			t.Errorf("uiLocale(%q, %q) = %q, want %q", tc.preference, tc.system, got, tc.want)
		}
	}
}

func TestCloseMinimizes(t *testing.T) {
	for _, tc := range []struct {
		goos     string
		hasTray  bool
		minimize bool
	}{
		{"darwin", true, false},
		{"darwin", false, false}, // the Dock reopens it
		{"windows", true, false},
		{"windows", false, true},
		{"linux", false, true},
	} {
		if got := closeMinimizes(tc.goos, tc.hasTray); got != tc.minimize {
			t.Errorf("closeMinimizes(%q, %v) = %v", tc.goos, tc.hasTray, got)
		}
	}
}

// Closing the main window while Godwit keeps running in the background exits
// the interface, but not while a compose window is open; with 后台收信 at
// 关窗后停止 it quits, stopping the backend. Without closeExitsUI (#738) it hides as before.
func TestMainCloseAction(t *testing.T) {
	for _, tc := range []struct {
		keep, exits bool
		compose     int
		goos        string
		hasTray     bool
		want        closeAction
	}{
		{false, true, 0, "darwin", true, closeQuit},
		{false, true, 2, "windows", true, closeQuit}, // the quit closes the compose windows, each saving
		{true, true, 0, "darwin", true, closeExit},
		{true, true, 0, "windows", false, closeExit}, // relaunching it is the way back, as with the tray
		{true, true, 1, "darwin", true, closeHide},   // decided again once the last one closes
		{true, false, 0, "darwin", true, closeHide},
		{true, false, 0, "windows", false, closeMinimize},
		{false, false, 0, "darwin", true, closeQuit},
	} {
		if got := mainCloseAction(tc.keep, tc.exits, tc.compose, tc.goos, tc.hasTray); got != tc.want {
			t.Errorf("mainCloseAction(%v, %v, %d, %q, %v) = %d, want %d", tc.keep, tc.exits, tc.compose, tc.goos, tc.hasTray, got, tc.want)
		}
	}
	if !closeExitsUI {
		t.Error("closing the window hides the interface; #733 exits it")
	}
}

// A second launch's arguments, handed to the running interface, say what it opens.
func TestOpenArg(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, app.OpenWindow}, // the user launching it again
		{[]string{"-psn_0_123"}, app.OpenWindow},
		{[]string{"--data-dir", "/d", "--open", "message:3"}, "message:3"},
		{[]string{"--open=compose"}, app.OpenCompose},
		{[]string{"-open", "account:2"}, "account:2"},
		{[]string{"--open"}, app.OpenWindow},
	} {
		if got := openArg(tc.args); got != tc.want {
			t.Errorf("openArg(%q) = %q, want %q", tc.args, got, tc.want)
		}
	}
}

func TestTrayIcon(t *testing.T) {
	for _, goos := range []string{"darwin", "windows"} {
		t.Run(goos, func(t *testing.T) { checkTrayIcon(t, trayBoxFor(goos)) })
	}
}

func checkTrayIcon(t *testing.T, box trayBox) {
	b, err := trayIcon(box)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil || img.Bounds().Dx() != int(box.w) || img.Bounds().Dy() != int(box.h) {
		t.Fatalf("tray icon %v, %v", img.Bounds(), err)
	}
	if _, _, _, a := img.At(0, 0).RGBA(); a != 0 {
		t.Fatal("the corner is not transparent")
	}
	trayW, trayH, trayInkW := int(box.w), int(box.h), int(box.inkW)
	ink := false // the mark is black: a template image
	x0, y0, x1, y1 := trayW, trayH, 0, 0
	for y := range trayH {
		for x := range trayW {
			r, g, bl, a := img.At(x, y).RGBA()
			if a == 0xffff && r|g|bl == 0 {
				ink = true
			}
			if a > 0x1000 {
				x0, y0, x1, y1 = min(x0, x), min(y0, y), max(x1, x), max(y1, y)
			}
		}
	}
	if !ink {
		t.Fatal("no black mark")
	}
	// The bird fills the width, box.inkW of it, centred both ways: within 2 px
	// (1 point), as the renderer snaps the picture's fractional edge.
	if w := x1 - x0 + 1; w < trayInkW-2 || w > trayInkW+1 {
		t.Errorf("mark %d px wide, want %d", w, trayInkW)
	}
	if l, r := x0, trayW-1-x1; l-r < -2 || l-r > 2 {
		t.Errorf("mark off centre across: %d px left, %d right", l, r)
	}
	if top, bot := y0, trayH-1-y1; top-bot < -2 || top-bot > 2 {
		t.Errorf("mark off centre down: %d px above, %d below", top, bot)
	}
}
