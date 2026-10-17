//go:build !darwin

package main

import "github.com/egoist/mygo"

func signInCodeMaterial(*mygo.Window) {}

func snapFeedback() {} // no haptics outside macOS trackpads
