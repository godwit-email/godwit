//go:build darwin

package main

import (
	"sync"

	"github.com/godwit-email/godwit/core"
)

// appleIntelligence is the Mac's on-device intelligence.
var appleIntelligence = sync.OnceValue(func() core.AppleIntelligence {
	ai := core.NewDefaultAppleIntelligence()
	if !ai.Available() {
		return nil
	}
	return ai
})
