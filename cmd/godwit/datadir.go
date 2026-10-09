package main

import "github.com/egoist/mygo"

// defaultDataDir is Godwit's data directory: the system's per-user config
// directory joined with "Godwit". It needs neither a running App nor a display.
func defaultDataDir() (string, error) {
	mygo.App.SetName("Godwit")
	return mygo.App.Path(mygo.PathUserData)
}
