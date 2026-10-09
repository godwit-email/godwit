package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
)

func TestAppStartsAndRenders(t *testing.T) {
	h := newHarness(t)
	h.until("accounts and settings loaded", func() bool { return h.app.loaded })
	if len(h.app.accounts) != 0 || h.app.settings.Theme != "system" {
		t.Fatalf("accounts %v settings %+v", h.app.accounts, h.app.settings)
	}
}

// startedIdle is a harness whose initial load has applied, so the post queue is empty.
func startedIdle(t *testing.T) *harness {
	h := newHarness(t)
	h.until("initial load", func() bool { return h.app.loaded && h.app.taskModels != nil }) // and its task query
	return h
}

func TestDoRunsOffUIThread(t *testing.T) {
	h := startedIdle(t)
	ran := make(chan struct{})
	var done bool
	h.app.do(func(context.Context) error { close(ran); return errors.New("boom") }, func(err error) {
		if err == nil || err.Error() != "boom" {
			t.Errorf("done err %v", err)
		}
		done = true
	})
	<-ran
	h.queued("done", 1)
	if done {
		t.Fatal("done ran before the post queue was drained")
	}
	h.drain()
	if !done {
		t.Fatal("done did not run when the queue was drained")
	}
}

func TestCloseCancelsWork(t *testing.T) {
	h := startedIdle(t)
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
	h.queued("the cancelled work's done", 1)
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
	h := startedIdle(t)
	release := make(chan struct{})
	var result string
	var fresh string
	h.app.query(func(context.Context) error { <-release; result = "old"; return nil }, func(error) { fresh = result })
	h.app.gen++ // a navigation
	close(release)
	h.queued("the old result", 1)
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

func TestLoadError(t *testing.T) {
	h := newHarness(t, func(e *coretest.Env) { e.Svc.Close() })
	h.until("load error", func() bool { return h.app.loadErr != nil })
	if h.app.loaded { // the closed Service fails the read
		t.Fatalf("loadErr %v loaded %v", h.app.loadErr, h.app.loaded)
	}
	h.app.fetch = func(context.Context) (core.Settings, []core.Account, error) {
		return core.Settings{Theme: "dark"}, nil, nil
	}
	h.app.load()
	h.until("a later load succeeds", func() bool { return h.app.loaded })
	if h.app.loadErr != nil || h.app.settings.Theme != "dark" {
		t.Fatalf("loadErr %v settings %+v", h.app.loadErr, h.app.settings)
	}
}

func TestLoadNewestWins(t *testing.T) {
	h := startedIdle(t)
	gate := func(theme string, release chan struct{}) func(context.Context) (core.Settings, []core.Account, error) {
		return func(context.Context) (core.Settings, []core.Account, error) {
			<-release
			return core.Settings{Theme: theme}, nil, nil
		}
	}
	first, second := make(chan struct{}), make(chan struct{})
	h.app.fetch = gate("light", first)
	h.app.load()
	h.app.fetch = gate("dark", second)
	h.app.load()
	close(second)
	h.until("the newer load applied", func() bool { return h.app.settings.Theme == "dark" })
	close(first)
	h.queued("the older load's result", 1)
	h.drain()
	if h.app.settings.Theme != "dark" {
		t.Fatalf("an older load rolled settings back to %q", h.app.settings.Theme)
	}
}

func TestStartCtxStopsEventPump(t *testing.T) {
	h := startedIdle(t)
	h.stop()
	select {
	case <-h.app.pumpDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the event pump kept running after Start's ctx was cancelled")
	}
	st := h.app.settings
	st.Theme = "dark"
	if _, err := h.env.Svc.SetSettings(context.Background(), st); err != nil { // publishes settings.changed
		t.Fatal(err)
	}
	h.mu.Lock()
	n := len(h.queue)
	h.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d callbacks posted after the pump stopped", n)
	}
}
