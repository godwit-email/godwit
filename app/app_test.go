package app

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAppStartsAndRenders(t *testing.T) {
	h := newHarness(t)
	h.see("Godwit")
	h.until("accounts and settings loaded", func() bool { return h.app.loaded })
	if len(h.app.accounts) != 0 || h.app.settings.Theme != "system" {
		t.Fatalf("accounts %v settings %+v", h.app.accounts, h.app.settings)
	}
}

func TestDoRunsOffUIThread(t *testing.T) {
	h := newHarness(t)
	ran := make(chan struct{})
	var done bool
	h.app.do(func(context.Context) error { close(ran); return errors.New("boom") }, func(err error) {
		if err == nil || err.Error() != "boom" {
			t.Errorf("done err %v", err)
		}
		done = true
	})
	<-ran
	time.Sleep(20 * time.Millisecond) // give a misbehaving do time to call done itself
	if done {
		t.Fatal("done ran before the post queue was drained")
	}
	h.until("done", func() bool { return done })
}

func TestCloseCancelsWork(t *testing.T) {
	h := newHarness(t)
	started := make(chan struct{})
	ctxErr := make(chan error, 1)
	var done bool
	h.app.do(func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		ctxErr <- ctx.Err()
		return ctx.Err()
	}, func(error) { done = true })
	<-started
	h.app.Close()
	select {
	case err := <-ctxErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ctx err %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not cancel the work's context")
	}
	time.Sleep(20 * time.Millisecond)
	h.drain()
	if done {
		t.Fatal("done ran after Close")
	}
	// after also stops firing.
	var fired bool
	h.app.after(time.Second, func() { fired = true })
	h.advance(2 * time.Second)
	if fired {
		t.Fatal("after fired after Close")
	}
}

func TestStaleResultDropped(t *testing.T) {
	h := newHarness(t)
	release := make(chan struct{})
	var result string
	var fresh string
	finished := make(chan struct{})
	h.app.query(func(context.Context) error { <-release; result = "old"; close(finished); return nil }, func(error) { fresh = result })
	h.app.gen++ // a navigation
	close(release)
	<-finished
	time.Sleep(20 * time.Millisecond) // the result is posted
	h.drain()
	if fresh != "" {
		t.Fatalf("a stale result was applied: %q", fresh)
	}
	var cur bool
	h.app.query(func(context.Context) error { return nil }, func(error) { cur = true })
	h.until("current query applied", func() bool { return cur })
}

func TestFakeClockAfter(t *testing.T) {
	h := newHarness(t)
	var order []string
	h.app.after(2*time.Second, func() { order = append(order, "b") })
	h.app.after(time.Second, func() { order = append(order, "a") })
	stop := h.app.after(time.Second, func() { order = append(order, "stopped") })
	stop()
	h.advance(999 * time.Millisecond)
	if len(order) != 0 {
		t.Fatalf("fired early: %v", order)
	}
	h.advance(5 * time.Second)
	if len(order) != 2 || order[0] != "a" || order[1] != "b" {
		t.Fatalf("order %v", order)
	}
	// stop after the timer fired but before its callback ran on the UI thread.
	var late bool
	stop = h.app.after(time.Second, func() { late = true })
	h.clock.advance(time.Second) // fires, posts, does not drain
	stop()
	h.drain()
	if late {
		t.Fatal("a stopped after ran")
	}
	if !h.clock.Now().Equal(newFakeClock().Now().Add(6999 * time.Millisecond)) {
		t.Fatalf("clock now %v", h.clock.Now())
	}
}
