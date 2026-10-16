package main

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/godwit-email/godwit/app"
	"github.com/godwit-email/godwit/core"
)

func TestNotificationOptions(t *testing.T) {
	n := app.Notification{ID: "message:7", Title: "小明", Subtitle: "午饭", Body: "十二点见",
		Sender: "小明 <ming@EXAMPLE.com>", MessageID: 7, ThreadID: 3}
	avatar := []byte{1, 2, 3}
	got := notificationOptions(n, avatar)
	if got.ID != "message:7" || got.Title != "小明" || got.Subtitle != "午饭" || got.Body != "十二点见" || got.Group != "thread:3" {
		t.Fatalf("notification lost its content or conversation: %+v", got)
	}
	if got.Sender == nil || got.Sender.ID != "ming@example.com" || got.Sender.Name != "小明" || !bytes.Equal(got.Sender.Avatar, avatar) {
		t.Fatalf("incorrect sender: %+v", got.Sender)
	}
	for _, tc := range []struct {
		name string
		edit func(*app.Notification)
	}{
		{"preview disabled or summary", func(n *app.Notification) { n.Sender = "" }},
		{"invalid sender", func(n *app.Notification) { n.Sender = "invalid address" }},
		{"missing message", func(n *app.Notification) { n.MessageID = 0 }},
		{"missing conversation", func(n *app.Notification) { n.ThreadID = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plain := n
			tc.edit(&plain)
			got := notificationOptions(plain, avatar)
			if got.Sender != nil || got.Group != "" || got.Body != "十二点见" || got.Subtitle != "午饭" {
				t.Fatalf("incomplete notification did not stay ordinary: %+v", got)
			}
		})
	}
	if got := notificationOptions(n, nil); got.Sender != nil || got.Group != "" || got.ID != "message:7" || got.Body != "十二点见" {
		t.Fatalf("missing avatar did not keep the ordinary notification: %+v", got)
	}
}

func TestNotificationSVGAvatar(t *testing.T) {
	avatar := core.Avatar{Format: "svg", Data: []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="200000" height="100000" viewBox="0 0 200000 100000"><rect width="100000" height="100000" fill="#ff0000"/><rect x="100000" width="100000" height="100000" fill="#0000ff"/></svg>`)}
	n := app.Notification{Sender: "ming@example.com", MessageID: 7, ThreadID: 3}
	opts := notificationOptions(n, notificationAvatarData(avatar))
	if opts.Sender == nil {
		t.Fatal("valid SVG must produce a sender avatar")
	}
	img, err := png.Decode(bytes.NewReader(opts.Sender.Avatar))
	if err != nil {
		t.Fatalf("notification avatar must be PNG: %v", err)
	}
	if img.Bounds() != image.Rect(0, 0, 128, 128) {
		t.Fatalf("SVG dimensions must not control output allocation: %v", img.Bounds())
	}
	for _, tc := range []struct {
		x, y int
		want color.NRGBA
	}{
		{32, 64, color.NRGBA{R: 255, A: 255}},
		{96, 64, color.NRGBA{B: 255, A: 255}},
		{64, 16, color.NRGBA{}},
		{64, 112, color.NRGBA{}},
	} {
		if got := color.NRGBAModel.Convert(img.At(tc.x, tc.y)).(color.NRGBA); got != tc.want {
			t.Errorf("pixel (%d,%d) = %v, want %v; preserve colors, transparency and aspect ratio", tc.x, tc.y, got, tc.want)
		}
	}
}

func TestNotificationBitmapAvatar(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for _, format := range []string{"png", "jpeg"} {
		t.Run(format, func(t *testing.T) {
			var data bytes.Buffer
			var err error
			if format == "png" {
				err = png.Encode(&data, img)
			} else {
				err = jpeg.Encode(&data, img, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := notificationAvatarData(core.Avatar{Format: format, Data: data.Bytes()}); !bytes.Equal(got, data.Bytes()) {
				t.Fatal("bitmap avatars must pass through without re-encoding")
			}
		})
	}
}

func TestNotificationInvalidSVGAvatar(t *testing.T) {
	avatar := notificationAvatarData(core.Avatar{Format: "svg", Data: []byte(`<svg><path`)})
	if avatar != nil {
		t.Fatal("invalid SVG must not be passed to MyGo")
	}
	n := app.Notification{ID: "message:7", Body: "hello", Sender: "ming@example.com", MessageID: 7, ThreadID: 3}
	if opts := notificationOptions(n, avatar); opts.Sender != nil || opts.Group != "" || opts.ID != n.ID || opts.Body != n.Body {
		t.Fatalf("invalid SVG must preserve the ordinary notification: %+v", opts)
	}
}
