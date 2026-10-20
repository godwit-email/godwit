//go:build darwin

package main

import (
	"sync"

	"github.com/godwit-email/godwit/core"
)

// appleIntelligence is the Mac's on-device intelligence.
var appleIntelligence = sync.OnceValue(func() core.AppleIntelligence {
	return core.NewDefaultAppleIntelligence()
})
