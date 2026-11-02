package main

import (
	"github.com/godwit-email/godwit/core/conn"
	"github.com/godwit-email/godwit/internal/imagefetch"
	"github.com/godwit-email/godwit/internal/sysproxy"
)

// Mail connections, remote images and one-click unsubscribes go through the user's proxy.
func init() {
	conn.SystemProxy = sysproxy.For
	imagefetch.SystemProxy = sysproxy.For
}
