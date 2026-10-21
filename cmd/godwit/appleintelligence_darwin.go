//go:build darwin

package main

import (
	"os"
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
// helper (a build that skipped it). Whether the system model is ready is
// probed when used, so turning Apple Intelligence on needs no restart.
var appleIntelligence = sync.OnceValue(func() core.AppleIntelligence {
	dir, err := mygo.App.Path(mygo.PathResources)
	if err != nil {
		return nil
	}
	build, _ := syscall.Sysctl("kern.osversion") // the build: an update may change the system model
	helper := filepath.Join(dir, appleIntelligenceHelper)
	if _, err := os.Stat(helper); err != nil {
		return nil
	}
	return core.NewDefaultAppleIntelligence(helper, build+" "+app.Version)
})
