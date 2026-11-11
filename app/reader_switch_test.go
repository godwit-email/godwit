package app

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
)

// gateThread holds the reader's read of thread id until the returned
// release runs (#667's switch, caught between the two threads).
func gateThread(h *harness, id int64) (release func(), read *atomic.Bool) {
	r := h.app.mail.reader
	gate, read := make(chan struct{}), &atomic.Bool{}
	real := r.getThread
	r.getThread = func(ctx context.Context, tid int64) (core.Thread, error) {
		if tid == id {
			select {
			case <-gate:
			case <-ctx.Done():
			}
			defer read.Store(true)
		}
		return real(ctx, tid)
	}
	return func() { close(gate) }, read
}

// Switching threads keeps what showed until the new thread is read, then
// replaces it at once: no blank pane between (#667). Its body keeps the
// height the replaced one had until its own page reports.
func TestReaderSwitchKeepsShownUntilRead(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	a := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "First thread", "first body"))
	b := h.deliverOne(accs[0], coretest.Msg("carol@example.com", accs[0].Email, "Second thread", "second body"))
	r := h.app.mail.reader
	h.openThread(a.ThreadID)
	va := h.seeBody(a.ID)
	h.post(func() { h.app.BodyHeight(va, a.ID, 400) })
	h.until("first body sized", func() bool { return r.bodies[a.ID] != nil && r.bodies[a.ID].height == 400 })

	release, _ := gateThread(h, b.ThreadID)
	h.openThread(b.ThreadID)
	for range 5 {
		h.drain()
		h.tt.Frame()
	}
	if r.key[0] != a.ThreadID || !r.loaded {
		t.Fatalf("switching blanked what showed: key %v loaded %v", r.key, r.loaded)
	}
	h.see("First thread")
	if _, ok := h.viewBox(va); !ok || va.closed {
		t.Fatal("the shown body's page went before the next thread was read")
	}

	release()
	h.until("second thread shown", func() bool { return r.key[0] == b.ThreadID && r.loaded })
	if !va.closed {
		t.Fatal("the replaced body's page stayed open")
	}
	if got := r.reserveFor(b.ID); got != 400 {
		t.Fatalf("reserve %v, want the replaced body's 400", got)
	}
	vb := h.seeBody(b.ID)
	if box, _ := h.viewBox(vb); box.H <= 60 { // the window cuts it; a line it is not
		t.Fatalf("new body drawn %v high before its page reported, want the kept height", box.H)
	}
	h.post(func() { h.app.BodyHeight(vb, b.ID, 120) })
	h.until("second body sized", func() bool { return r.bodies[b.ID].height == 120 })
	if r.reserve != 0 {
		t.Fatalf("reserve %v kept after the page reported", r.reserve)
	}
}

// Going back to the shown thread before the switch's read returns drops
// that read: the thread shown stays.
func TestReaderSwitchBackDropsPendingRead(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	a := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "First thread", "first body"))
	b := h.deliverOne(accs[0], coretest.Msg("carol@example.com", accs[0].Email, "Second thread", "second body"))
	r := h.app.mail.reader
	h.openThread(a.ThreadID)
	va := h.seeBody(a.ID)

	release, read := gateThread(h, b.ThreadID)
	h.openThread(b.ThreadID)
	h.openThread(a.ThreadID)
	release()
	h.until("the dropped read back", read.Load)
	for range 5 { // its callback, had it not been dropped
		h.drain()
		h.tt.Frame()
	}
	if r.key[0] != a.ThreadID || r.next != [2]int64{} {
		t.Fatalf("key %v next %v, want the first thread alone", r.key, r.next)
	}
	if va.closed {
		t.Fatal("the shown body's page closed")
	}
}
