package main

import (
	"path/filepath"

	"github.com/egoist/mygo"
)

// guardCoreML is the CoreML bridge 识别模型 reads with
// (core.Options.GuardCoreML), which native/guardcoreml builds into the
// app's resources (scripts/build-guard-coreml.sh). A build that skipped
// it reads on the CPU, and the settings say why.
func guardCoreML() string {
	dir, err := mygo.App.Path(mygo.PathResources)
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "libgodwit-guard-coreml.dylib")
}
