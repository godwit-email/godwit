package ownerlock_test

import (
	"bufio"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/godwit-email/godwit/ownerlock"
)

const childEnv = "GODWIT_OWNERLOCK_CHILD"

// TestMain turns the test binary into a lock holder when started by
// TestOwnerLockAcrossProcesses: it takes the lock, says so, and holds it
// until killed.
func TestMain(m *testing.M) {
	if dir := os.Getenv(childEnv); dir != "" {
		if _, err := ownerlock.Acquire(dir); err != nil {
			os.Stdout.WriteString("error: " + err.Error() + "\n")
			os.Exit(1)
		}
		os.Stdout.WriteString("locked\n")
		io.Copy(io.Discard, os.Stdin) // the parent never closes it: hold until killed
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestOwnerLockExclusive(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	release, err := ownerlock.Acquire(a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ownerlock.Acquire(a); !errors.Is(err, ownerlock.ErrAnotherOwner) {
		t.Fatalf("second Acquire: %v, want ErrAnotherOwner", err)
	}
	relB, err := ownerlock.Acquire(b) // other directories are independent
	if err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	again, err := ownerlock.Acquire(a)
	if err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
	again()
	relB()
}

func TestOwnerLockCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")
	release, err := ownerlock.Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := os.Stat(filepath.Join(dir, "godwit.lock")); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerLockAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), childEnv+"="+dir)
	if _, err := cmd.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || line != "locked\n" {
		t.Fatalf("child: %q, %v", line, err)
	}
	if _, err := ownerlock.Acquire(dir); !errors.Is(err, ownerlock.ErrAnotherOwner) {
		t.Fatalf("Acquire while the child holds it: %v, want ErrAnotherOwner", err)
	}
	// The child never releases: its exit alone must free the lock.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	cmd.Wait()
	deadline := time.Now().Add(5 * time.Second) // Windows may release a dead process's locks a moment later
	for {
		release, err := ownerlock.Acquire(dir)
		if err == nil {
			release()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Acquire after the child exited: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestOwnerLockTightensPerms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows relies on the user directory's ACL")
	}
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil { // past the umask
		t.Fatal(err)
	}
	release, err := ownerlock.Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("mode %v, want 0700", fi.Mode().Perm())
	}
}
