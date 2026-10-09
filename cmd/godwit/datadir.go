package main

import (
	"path/filepath"

	"github.com/egoist/mygo"
)

// defaultDataDir is Godwit's data directory: the system's per-user config
// directory joined with "Godwit". It needs neither a running App nor a display.
func defaultDataDir() (string, error) {
	mygo.App.SetName("Godwit")
	return mygo.App.Path(mygo.PathUserData)
}

// singleInstance: a GUI on dir takes mygo's single-instance lock, which is
// per user, not per directory. Only the default directory does; a GUI on
// another one runs beside it, guarded by that directory's owner lock.
func singleInstance(dir string) bool {
	def, err := defaultDataDir()
	return err == nil && filepath.Clean(def) == filepath.Clean(dir)
}
