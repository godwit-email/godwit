//go:build !darwin

package main

import "github.com/godwit-email/godwit/app"

func (*platform) notificationAvatar(app.Notification) []byte     { return nil }
func notificationAvatar(backendService, app.Notification) []byte { return nil }
