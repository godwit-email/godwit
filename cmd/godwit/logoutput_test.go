package main

import (
	"errors"
	"log"
	"strings"
	"testing"

	"github.com/godwit-email/godwit/app"
)

// noStderr fails every write, as stderr does in a Windows GUI build.
type noStderr struct{}

func (noStderr) Write([]byte) (int, error) { return 0, errors.New("The handle is invalid.") }

// A Windows release has no stderr; the log still reaches RecentLog, so an
// error report carries it (#536).
func TestLogOutputWithoutStderr(t *testing.T) {
	l := log.New(logOutput(noStderr{}), "", 0)
	l.Print("imap: traffic before sync, masked")
	if !strings.Contains(app.RecentLog.String(), "imap: traffic before sync, masked") {
		t.Fatalf("RecentLog = %q, want the line logged while stderr fails", app.RecentLog.String())
	}
}
