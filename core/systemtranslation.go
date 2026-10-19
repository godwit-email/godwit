package core

import (
	"context"
	"errors"
)

// SystemTranslator is the operating system's on-device translation, which
// the built-in system-translation plugin offers (builtins.go); the apps set
// Options.SystemTranslator where the system has one.
type SystemTranslator interface {
	// Translate translates texts into target, one answer per text in order;
	// source "" detects it, and detected is the source used (BCP 47).
	Translate(ctx context.Context, texts []string, source, target string) (out []string, detected string, err error)
	// Version changes when the system's translations may, as with an OS update.
	Version() string
}

var (
	// ErrSystemLanguageNotInstalled: the system translates the pair once the
	// user downloads its languages in the system's settings.
	ErrSystemLanguageNotInstalled = errors.New("the languages are not downloaded for system translation")
	// ErrSystemLanguageUnsupported: the system does not translate the pair.
	ErrSystemLanguageUnsupported = errors.New("system translation does not support these languages")
)
