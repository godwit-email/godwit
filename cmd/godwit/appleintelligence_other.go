//go:build !darwin

package main

import "github.com/godwit-email/godwit/core"

// appleIntelligence: only macOS offers on-device Apple Intelligence.
func appleIntelligence() core.AppleIntelligence { return nil }
