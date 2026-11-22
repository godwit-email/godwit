package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/godwit-email/godwit/app"
)

// TestLoginItemNeedsApproval: macOS keeping the login item for the user's
// approval is app.ErrLoginItemOff; mygo's other errors stay as they are (#875).
func TestLoginItemNeedsApproval(t *testing.T) {
	err := loginItemErr(errors.New("mygo: allow Godwit to open at login in System Settings > General > Login Items"))
	if !errors.Is(err, app.ErrLoginItemOff) {
		t.Fatalf("%v: want app.ErrLoginItemOff", err)
	}
	other := errors.New("mygo: starting at login needs an app bundle; run the app with mygo dev or build it with mygo build")
	if err := loginItemErr(other); err != other {
		t.Fatalf("%v: want the error unchanged", err)
	}
	if loginItemErr(nil) != nil {
		t.Fatal("nil became an error")
	}
}

// TestLoginItemNeedsApprovalMatchesMygo pins the text loginItemErr matches to
// the mygo in go.mod, so an update that changes it fails here.
func TestLoginItemNeedsApprovalMatchesMygo(t *testing.T) {
	dir := mygoDir(t)
	src, err := os.ReadFile(filepath.Join(dir, "internal", "darwin", "login.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `"mygo: allow %s `+mygoLoginNeedsApproval+`"`) {
		t.Fatalf("mygo's login item error no longer ends %q", mygoLoginNeedsApproval)
	}
}

// mygoDir is the source of the mygo module go.mod requires.
func mygoDir(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/egoist/mygo").Output()
	if err != nil {
		t.Skipf("go list: %v", err)
	}
	return strings.TrimSpace(string(out))
}
