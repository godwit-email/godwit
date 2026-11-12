//go:build !windows

package main

import "github.com/egoist/mygo"

func setOpenAtLogin(on bool) error { return mygo.App.SetOpenAtLogin(on) }
func openAtLogin() bool            { return mygo.App.OpenAtLogin() }
func wasOpenedAtLogin() bool       { return mygo.App.WasOpenedAtLogin() }
