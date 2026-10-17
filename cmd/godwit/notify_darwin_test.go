package main

import (
	"testing"

	"github.com/godwit-email/godwit/app"
)

func TestNotificationAvatarWithoutEntitlement(t *testing.T) {
	// A plain test executable has no communication entitlement. A nil service
	// proves the gate returns before reading an avatar or contacting its source.
	if communicationAuthorized() {
		t.Fatal("a plain Go test executable has no communication entitlement")
	}
	if got := new(platform).notificationAvatar(app.Notification{Sender: "ming@example.com", MessageID: 7, ThreadID: 3}); got != nil {
		t.Fatal("an unentitled process must not read an avatar")
	}
}
