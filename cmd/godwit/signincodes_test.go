package main

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/theme"
)

func TestSignInCodeGlassDoesNotCoverBackdrop(t *testing.T) {
	for _, dark := range []bool{false, true} {
		p := &platform{}
		b := &signInBanner{code: core.SignInCode{Code: "aB0012", From: "Godwit Test"}}
		tt := ui.NewTester(func(c *ui.Context) {
			tokens := theme.Light()
			if dark {
				tokens = theme.Dark()
			}
			theme.Apply(c, tokens)
			p.signInCodeView(c, b)
		}, codeBannerWidth, codeBannerHeight)
		if a := tt.Image().RGBAAt(0, 0).A; a != 0 {
			t.Errorf("dark=%v: rounded corner covered the desktop, alpha=%d", dark, a)
		}
		if a := tt.Image().RGBAAt(codeBannerWidth/2, codeBannerHeight-4).A; a == 0 || a > 96 {
			t.Errorf("dark=%v: background should be lightly translucent, alpha=%d", dark, a)
		}
		pixel := tt.Image().RGBAAt(codeBannerWidth/2, codeBannerHeight-4)
		if white := int(pixel.R) + 255 - int(pixel.A); !dark && white < 240 {
			t.Errorf("white backdrop darkened to %d/255", white)
		}
		tt.SetVibrancy(true)
		if a := tt.Image().RGBAAt(codeBannerWidth/2, codeBannerHeight-4).A; a != 0 {
			t.Errorf("dark=%v: card tinted the native blur a second time, alpha=%d", dark, a)
		}
	}
}

func TestSignInCodePosition(t *testing.T) {
	primary := mygo.Rectangle{X: 0, Y: 25, Width: 1440, Height: 850}
	left := mygo.Rectangle{X: -1920, Y: 0, Width: 1920, Height: 1080}
	displays := []mygo.Display{{WorkArea: primary}, {WorkArea: left}}
	for _, tc := range []struct {
		name  string
		saved *mygo.Point
		want  mygo.Point
	}{
		{"default bottom center", nil, mygo.Point{X: 560, Y: 803}},
		{"user position", &mygo.Point{X: 200, Y: 300}, mygo.Point{X: 200, Y: 300}},
		{"other display", &mygo.Point{X: -1500, Y: 400}, mygo.Point{X: -1500, Y: 400}},
		{"clamp at screen edge", &mygo.Point{X: 1430, Y: 850}, mygo.Point{X: 1120, Y: 827}},
		{"removed display", &mygo.Point{X: 4000, Y: 0}, mygo.Point{X: 560, Y: 803}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := signInCodePosition(primary, displays, tc.saved); got != tc.want {
				t.Fatalf("position %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestSignInCodePositionPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "code-position.json")
	if got := loadSignInCodePosition(path); got != nil {
		t.Fatalf("missing position: %+v", got)
	}
	want := mygo.Point{X: -1500, Y: 400}
	if err := saveSignInCodePosition(path, want); err != nil {
		t.Fatal(err)
	}
	if got := loadSignInCodePosition(path); got == nil || *got != want {
		t.Fatalf("reloaded position: %+v", got)
	}
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := loadSignInCodePosition(path); got != nil {
		t.Fatalf("corrupt position: %+v", got)
	}
}

func TestRecentCodeMenuCopiesSelectedCode(t *testing.T) {
	now := time.Now()
	var copied core.SignInCode
	items := recentCodeMenu([]core.SignInCode{
		{MessageID: 1, Code: "aB0012", From: "Login <login@example.com>", ReceivedAt: now},
		{MessageID: 2, Code: "123456", From: "second@example.com", ReceivedAt: now},
		{MessageID: 3, Code: "999999", From: "old@example.com", ReceivedAt: now.Add(-15 * time.Minute)},
		{MessageID: 4, Link: "https://www.acme.example/l/1", From: "Acme <id@acme.example>", ReceivedAt: now},
	}, now, func(code core.SignInCode) { copied = code })
	menu := items[1].Submenu
	if len(menu) != 3 || menu[0].Label != "aB0012 · Login" || menu[2].Label != i18n.Tf("codes.link_item", "acme.example", "Acme") {
		t.Fatalf("menu %+v", menu)
	}
	menu[1].Click(menu[1], nil)
	if copied.MessageID != 2 || copied.Code != "123456" {
		t.Fatalf("copied %+v", copied)
	}
	menu[2].Click(menu[2], nil)
	if copied.MessageID != 4 {
		t.Fatalf("used %+v", copied)
	}
	empty := recentCodeMenu(nil, now, nil)[1].Submenu
	if len(empty) != 1 || !empty[0].Disabled {
		t.Fatalf("empty menu %+v", empty)
	}
}

func TestSignInLinkBannerRenders(t *testing.T) {
	p := &platform{}
	b := &signInBanner{code: core.SignInCode{Link: "https://linear.app/auth/x", From: "Linear <no-reply@linear.app>"}}
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, theme.Light())
		p.signInCodeView(c, b)
	}, codeBannerWidth, codeBannerHeight)
	if a := tt.Image().RGBAAt(0, 0).A; a != 0 {
		t.Errorf("rounded corner covered the desktop, alpha=%d", a)
	}
}

func TestSignInBannerShowsLoadedAvatar(t *testing.T) {
	var buf bytes.Buffer
	red := image.NewRGBA(image.Rect(0, 0, 32, 32))
	draw.Draw(red, red.Bounds(), &image.Uniform{color.RGBA{R: 255, A: 255}}, image.Point{}, draw.Src)
	if err := png.Encode(&buf, red); err != nil {
		t.Fatal(err)
	}
	img, err := ui.DecodeBitmap(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	b := &signInBanner{code: core.SignInCode{Code: "482913", From: "Acme <id@acme.example>"}, avatar: img}
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, theme.Light())
		(&platform{}).signInCodeView(c, b)
	}, codeBannerWidth, codeBannerHeight)
	// The avatar is the first 32px square after the 8px padding; the initial would be pale.
	if px := tt.Image().RGBAAt(24, 24); px.R < 200 || px.G > 120 || px.B > 120 {
		t.Fatalf("avatar center %v, want the sender's image", px)
	}
}

func TestSnapSignInCode(t *testing.T) {
	area := mygo.Rectangle{X: 0, Y: 25, Width: 1440, Height: 850} // home is {560, 803}
	for _, tc := range []struct {
		name    string
		in, out mygo.Point
	}{
		{"near the centre line", mygo.Point{X: 572, Y: 400}, mygo.Point{X: 560, Y: 400}},
		{"near home", mygo.Point{X: 550, Y: 790}, mygo.Point{X: 560, Y: 803}},
		{"near home's height only", mygo.Point{X: 700, Y: 800}, mygo.Point{X: 700, Y: 800}},
		{"free", mygo.Point{X: 200, Y: 300}, mygo.Point{X: 200, Y: 300}},
		{"just outside", mygo.Point{X: 560 + codeBannerSnap + 1, Y: 300}, mygo.Point{X: 577, Y: 300}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := snapSignInCode(tc.in, area); got != tc.out {
				t.Fatalf("snap %+v = %+v, want %+v", tc.in, got, tc.out)
			}
		})
	}
}
