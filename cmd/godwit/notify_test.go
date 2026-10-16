package main

import (
	"bytes"
	"testing"

	"github.com/godwit-email/godwit/app"
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
