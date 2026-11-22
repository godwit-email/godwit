package main

import (
	"fmt"
	"strings"

	"github.com/godwit-email/godwit/app"
)

// mygoLoginNeedsApproval ends the error mygo's SetOpenAtLogin returns on
// macOS 13 and later when SMAppService registered the login item but keeps
// it waiting for the user's approval, as it does with one the user turned
// off in System Settings > General > Login Items (#875). mygo has no error
// value for it to match.
const mygoLoginNeedsApproval = "to open at login in System Settings > General > Login Items"

// loginItemErr makes the error of a login item waiting for the user's
// approval app.ErrLoginItemOff, which the settings say in words rather than
// offer to report.
func loginItemErr(err error) error {
	if err != nil && strings.HasSuffix(err.Error(), mygoLoginNeedsApproval) {
		return fmt.Errorf("%w: %w", app.ErrLoginItemOff, err)
	}
	return err
}
