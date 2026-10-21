//go:build darwin

package main

import (
	"path/filepath"
	"sync"
	"syscall"

	"github.com/egoist/mygo"
	"github.com/godwit-email/godwit/app"
	"github.com/godwit-email/godwit/core"
)

// appleIntelligenceHelper is the helper native/appleintelligence builds into
// the app's resources (scripts/build-apple-intelligence.sh).
const appleIntelligenceHelper = "apple-intelligence-helper"

// appleIntelligence is the Mac's on-device intelligence, nil without the
// helper (a build that skipped it) or where the system model is unavailable.
var appleIntelligence = sync.OnceValue(func() core.AppleIntelligence {
	dir, err := mygo.App.Path(mygo.PathResources)
	if err != nil {
		return nil
	}
	build, _ := syscall.Sysctl("kern.osversion") // the build: an update may change the system model
	ai := core.NewDefaultAppleIntelligence(filepath.Join(dir, appleIntelligenceHelper), build+" "+app.Version)
	if !ai.Available() {
		return nil
	}
	return ai
})
