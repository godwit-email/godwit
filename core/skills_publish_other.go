//go:build !darwin && !linux && !windows

package core

import (
	"errors"
	"os"
)

func publishSkillDir(_ *os.Root, _, _ string) error {
	return errors.New("atomic skill creation is not supported on this platform")
}
