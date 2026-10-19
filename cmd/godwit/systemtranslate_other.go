//go:build !darwin

package main

import "github.com/godwit-email/godwit/core"

// systemTranslator: only macOS offers its own translation.
func systemTranslator() core.SystemTranslator { return nil }
