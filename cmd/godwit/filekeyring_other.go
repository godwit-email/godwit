//go:build !unix

package main

import (
	"errors"
	"os"

	"github.com/godwit-email/godwit/core"
)

// keyringFromEnv refuses the file keyring here: Windows always has its
// credential manager, and a key file's Unix mode cannot be checked.
func keyringFromEnv() (core.Keyring, error) {
	if os.Getenv("GODWIT_KEYRING_FILE") != "" || os.Getenv("GODWIT_KEYRING_KEY_FILE") != "" {
		return nil, errors.New("GODWIT_KEYRING_FILE is only supported on Linux and macOS; this system's credential manager keeps credentials")
	}
	return nil, nil
}
