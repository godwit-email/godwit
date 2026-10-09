package app

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func TestWriteQueueSerialAndLatest(t *testing.T) {
	h := newHarness(t)
	q := newWriteQueue(h.app)
	release := make(chan struct{})
	started := make(chan struct{})
	var ran, saved []uint64
	q.Submit("a", 1, func(context.Context) error { close(started); <-release; return nil },
		func(g uint64, err error) { ran = append(ran, g) })
	<-started
	q.Submit("a", 2, func(context.Context) error { t.Error("gen 2 ran; it was superseded"); return nil },
		func(g uint64, err error) { ran = append(ran, g) })
	q.Submit("a", 3, func(context.Context) error { return nil },
		func(g uint64, err error) { ran = append(ran, g) })

	// Another key runs while "a" is blocked.
	q.Submit("b", 7, func(context.Context) error { return nil }, func(g uint64, err error) { saved = append(saved, g) })
	h.until("key b saved while a is blocked", func() bool { return slices.Equal(saved, []uint64{7}) })
	if len(ran) != 0 {
		t.Fatalf("key a finished while blocked: %v", ran)
	}
	close(release)
	h.until("key a done", func() bool { return len(ran) == 2 })
	if !slices.Equal(ran, []uint64{1, 3}) {
		t.Fatalf("ran %v, want [1 3]", ran)
	}
}

func TestWriteQueueFlush(t *testing.T) {
	h := newHarness(t)
	q := newWriteQueue(h.app)

	var idle bool
	q.Flush("k", func(err error) { idle = err == nil })
	h.until("flush of an idle key", func() bool { return idle })

	release := make(chan struct{})
	started := make(chan struct{})
	var events []string
	q.Submit("k", 1, func(context.Context) error { close(started); <-release; return nil },
		func(g uint64, err error) { events = append(events, "saved1") })
	<-started
	q.Submit("k", 2, func(context.Context) error { return errors.New("disk full") },
		func(g uint64, err error) { events = append(events, "saved2") })
	var flushErr error
	q.Flush("k", func(err error) { flushErr = err; events = append(events, "flushed") })
	time.Sleep(20 * time.Millisecond)
	h.drain()
	if len(events) != 0 {
		t.Fatalf("flushed before the write finished: %v", events)
	}
	close(release)
	h.until("flushed", func() bool { return slices.Contains(events, "flushed") })
	if !slices.Equal(events, []string{"saved1", "saved2", "flushed"}) || flushErr == nil || flushErr.Error() != "disk full" {
		t.Fatalf("events %v flush err %v", events, flushErr)
	}
}
