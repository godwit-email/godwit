//go:build !windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// fakeGH puts a gh that runs script in place of the GitHub CLI.
func fakeGH(t *testing.T, script string) {
	p := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := ghPath
	ghPath = func() string { return p }
	t.Cleanup(func() { ghPath = old })
}

func TestRepoStarred(t *testing.T) {
	for _, c := range []struct {
		name, script string
		starred, ok  bool
	}{
		{"starred", "exit 0", true, true},
		{"not starred", `echo "gh: Not Found (HTTP 404)" >&2; exit 1`, false, true},
		{"signed out", `echo "To get started with GitHub CLI, please run:  gh auth login" >&2; exit 4`, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			fakeGH(t, c.script)
			if starred, ok := (*platform)(nil).RepoStarred(context.Background()); starred != c.starred || ok != c.ok {
				t.Errorf("RepoStarred = %v, %v; want %v, %v", starred, ok, c.starred, c.ok)
			}
		})
	}
	t.Run("no gh", func(t *testing.T) {
		old := ghPath
		ghPath = func() string { return "" }
		t.Cleanup(func() { ghPath = old })
		if _, ok := (*platform)(nil).RepoStarred(context.Background()); ok {
			t.Error("ok without gh")
		}
	})
}

func TestStarRepo(t *testing.T) {
	out := filepath.Join(t.TempDir(), "args")
	fakeGH(t, `echo "$@" > `+out)
	if err := (*platform)(nil).StarRepo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(out); string(b) != "api -X PUT user/starred/godwit-email/godwit\n" {
		t.Errorf("gh got %q", b)
	}
}
