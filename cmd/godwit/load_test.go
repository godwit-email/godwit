package main

import (
	"errors"
	"slices"
	"testing"
)

// An embedded page is set up (locked, registered, bound) before it loads,
// so its first call finds it; a load that fails undoes the setup.
func TestEmbedSetsUpBeforeLoading(t *testing.T) {
	var steps []string
	step := func(s string) func() { return func() { steps = append(steps, s) } }
	load := func(err error) func() error {
		return func() error { steps = append(steps, "load"); return err }
	}
	if err := loadAfterSetup(step("setup"), load(nil), step("undo")); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(steps, []string{"setup", "load"}) {
		t.Fatalf("steps %q", steps)
	}
	steps = nil
	bad := errors.New("no such page")
	if err := loadAfterSetup(step("setup"), load(bad), step("undo")); !errors.Is(err, bad) {
		t.Fatalf("err %v", err)
	}
	if !slices.Equal(steps, []string{"setup", "load", "undo"}) {
		t.Fatalf("steps %q", steps)
	}
}
