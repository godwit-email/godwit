package main

import (
	"github.com/godwit-email/godwit/internal/imagefetch"
	"github.com/godwit-email/godwit/internal/sysproxy"
)

// Remote images and one-click unsubscribes go through the user's proxy.
func init() { imagefetch.SystemProxy = sysproxy.For }
