package main

import (
	"net/mail"
	"strconv"
	"strings"

	"github.com/egoist/mygo"
	"github.com/godwit-email/godwit/app"
)

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
