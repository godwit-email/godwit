package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// starPath is the GitHub API path of the user's star on Godwit's repository.
const starPath = "user/starred/godwit-email/godwit"

// ghPath finds the GitHub CLI: on PATH, else where its installers put it,
// as an app opened from the Finder gets only the system's PATH; "" without.
var ghPath = func() string {
	if p, err := exec.LookPath("gh"); err == nil {
		return p
	}
	dirs := []string{"/opt/homebrew/bin", "/usr/local/bin", "/home/linuxbrew/.linuxbrew/bin"}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".local", "bin"), filepath.Join(home, "bin"))
	}
	for _, d := range dirs {
		if p, err := exec.LookPath(filepath.Join(d, "gh")); err == nil {
			return p
		}
	}
	return ""
}

// gh runs the GitHub CLI's api command; err is set when it is missing.
func gh(ctx context.Context, args ...string) (stderr string, err error) {
	p := ghPath()
	if p == "" {
		return "", exec.ErrNotFound
	}
	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, p, append([]string{"api"}, args...)...)
	cmd.Stderr = &buf
	err = cmd.Run()
	return buf.String(), err
}

// RepoStarred asks the GitHub CLI whether the user starred Godwit: ok is
// false without gh, or with gh not signed in or offline.
func (*platform) RepoStarred(ctx context.Context) (starred, ok bool) {
	stderr, err := gh(ctx, starPath)
	switch {
	case err == nil:
		return true, true // 204
	case strings.Contains(stderr, "HTTP 404"):
		return false, true // signed in, not starred
	}
	return false, false
}

// StarRepo stars Godwit with the GitHub CLI.
func (*platform) StarRepo(ctx context.Context) error {
	_, err := gh(ctx, "-X", "PUT", starPath)
	return err
}
