//go:build !darwin

package main

import (
	"github.com/godwit-email/godwit/app"
	"github.com/godwit-email/godwit/core"
)

func (*platform) notificationAvatar(app.Notification) []byte    { return nil }
func notificationAvatar(*core.Service, app.Notification) []byte { return nil }
