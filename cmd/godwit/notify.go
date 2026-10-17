package main

import (
	"bytes"
	"image/png"
	"net/mail"
	"strconv"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/app"
	"github.com/godwit-email/godwit/core"
)

// TODO: Remove SVG conversion after upgrading to MyGo with https://github.com/egoist/mygo/pull/421.
func notificationAvatarData(avatar core.Avatar) []byte {
	if avatar.Format != "svg" {
		return avatar.Data
	}
	svg, err := ui.ParseSVG(avatar.Data)
	if err != nil {
		return nil
	}
	img := ui.Render(func(c *ui.Context) {
		c.Root().Background(ui.Transparent)
		ui.Image(c, svg).Size(128, 128)
	}, 128, 128, 1)
	var b bytes.Buffer
	if png.Encode(&b, img) != nil {
		return nil
	}
	return b.Bytes()
}

func notificationOptions(n app.Notification, avatar []byte) mygo.NotificationOptions {
	opts := mygo.NotificationOptions{ID: n.ID, Title: n.Title, Subtitle: n.Subtitle, Body: n.Body}
	if n.MessageID == 0 || n.ThreadID == 0 || len(avatar) == 0 {
		return opts
	}
	if addr, err := mail.ParseAddress(n.Sender); err == nil {
		opts.Group = "thread:" + strconv.FormatInt(n.ThreadID, 10)
		opts.Sender = &mygo.NotificationSender{ID: strings.ToLower(addr.Address), Name: n.Title, Avatar: avatar}
	}
	return opts
}
