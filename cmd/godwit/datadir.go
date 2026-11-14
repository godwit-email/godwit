package main

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/egoist/mygo"
)

// productionDataDir is Godwit's data directory: the system's per-user config
// directory joined with "Godwit", except on Windows, where it is .godwit in the
// home directory: an MSIX redirects what it creates under %APPDATA% into its
// private store, where the CLI and the Local API socket cannot reach it.
// It needs neither a running App nor a display.
func productionDataDir() (string, error) {
	mygo.App.SetName("Godwit")
	if runtime.GOOS == "windows" {
		home, err := os.UserHomeDir()
		return filepath.Join(home, ".godwit"), err
	}
	return mygo.App.Path(mygo.PathUserData)
}

func defaultDataDir() (string, error) {
	dir, err := productionDataDir()
	if mygo.IsDev() {
		dir += "-dev"
	}
	return dir, err
}

// singleInstance: a GUI on dir takes mygo's single-instance lock, which is
// per user, not per directory. Only the production directory does; a GUI on
// another one runs beside it, guarded by that directory's owner lock.
func singleInstance(dir string) bool {
	def, err := productionDataDir()
	return err == nil && filepath.Clean(def) == filepath.Clean(dir)
}

// externalDir is where an agent gateway such as magpie writes what Godwit
// mirrors (ADR 0041): ~/.godwit/external, one path on every system because the
// gateway's plugin cannot tell them apart. Only the default data directory
// reads it: another one is a test's or a second profile's.
func externalDir(dataDir string) string {
	def, err := defaultDataDir()
	home, herr := os.UserHomeDir()
	if err != nil || herr != nil || filepath.Clean(def) != filepath.Clean(dataDir) {
		return ""
	}
	return filepath.Join(home, ".godwit", "external")
}
