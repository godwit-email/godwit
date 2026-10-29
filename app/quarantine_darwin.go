package app

import (
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// markFromInternet quarantines path, so Gatekeeper checks it before it runs.
func markFromInternet(path string) error {
	return unix.Setxattr(path, "com.apple.quarantine", fmt.Appendf(nil, "0081;%x;Godwit;", time.Now().Unix()), 0)
}
