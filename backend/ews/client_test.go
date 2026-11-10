package ews

import (
	"context"
	"testing"
)

// An account's slot is kept only while a call holds it or waits for it, so
// accounts seen once do not pile up.
func TestAccountSlotReleased(t *testing.T) {
	release, err := acquire(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := acquire(ctx, "a"); err == nil {
		t.Fatal("took a held slot")
	}
	release()
	accounts.Lock()
	n := len(accounts.m)
	accounts.Unlock()
	if n != 0 {
		t.Errorf("%d slots kept after the calls ended, want 0", n)
	}
}
