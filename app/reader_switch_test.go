package app

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
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

// Switching threads keeps what showed until the new thread is read and its
// newest body's page has reported its height, building unseen meanwhile,
// then replaces it at once: no blank body between (#667).
func TestReaderSwitchKeepsShownUntilSized(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	a := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "First thread", "first body"))
	b := h.deliverOne(accs[0], coretest.Msg("carol@example.com", accs[0].Email, "Second thread", "second body"))
	r := h.app.mail.reader
	h.openThread(a.ThreadID)
	va := h.seeBody(a.ID)
	h.post(func() { h.app.BodyHeight(va, a.ID, 400) })
	h.until("first body sized", func() bool { return r.bodies[a.ID] != nil && r.bodies[a.ID].height == 400 })

	release, _ := gateThread(h, b.ThreadID)
	switchTo(h, b.ThreadID)
	for range 5 {
		h.drain()
		h.tt.Frame()
	}
	if r.key[0] != a.ThreadID || !r.loaded {
		t.Fatalf("switching blanked what showed: key %v loaded %v", r.key, r.loaded)
	}
	h.see("First thread")
	var was ui.Rect
	h.until("the shown body at rest", func() bool { box, _ := h.viewBox(va); settled := box == was; was = box; return settled })
	if was.H == 0 || va.closed {
		t.Fatal("the shown body's page went before the next thread was read")
	}

	release()
	h.until("second thread read", func() bool { return r.key[0] == b.ThreadID && r.loaded })
	vb := unseenBody(h, b.ID)
	for range 3 { // the thread shown keeps its elements: none goes and comes again with its motion
		if box, _ := h.viewBox(va); box != was {
			t.Fatalf("the shown body moved from %v to %v as the switch began", was, box)
		}
		h.tt.Advance(time.Second / 60)
		h.tt.Frame()
	}
	if r.prev == nil || va.closed || !h.tt.HasText("First thread") || h.tt.HasText("Second thread") {
		t.Fatalf("the first thread went before the second's body was sized: prev %v, closed %v", r.prev != nil, va.closed)
	}

	h.post(func() { h.app.BodyHeight(vb, b.ID, 120) })
	h.drain()
	h.tt.Frame() // no time passes: the new thread shows whole at once, not entering part by part
	if !h.tt.HasText("Second thread") || r.prev != nil || !va.closed {
		t.Fatal("the replaced thread stayed after the new body was sized")
	}
	if box, _ := h.viewBox(vb); box.H < 100 {
		t.Fatalf("new body shown %v high as the thread came, want its reported 120", box.H)
	}
}

// A page that never reports its height lets the thread before go after a
// while all the same.
func TestReaderSwitchGivesUpWaiting(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	a := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "First thread", "first body"))
	b := h.deliverOne(accs[0], coretest.Msg("carol@example.com", accs[0].Email, "Second thread", "second body"))
	r := h.app.mail.reader
	h.openThread(a.ThreadID)
	va := h.seeBody(a.ID)
	switchTo(h, b.ThreadID)
	unseenBody(h, b.ID)
	h.advance(prevWait)
	h.see("Second thread")
	if r.prev != nil || !va.closed {
		t.Fatal("the thread before stayed past prevWait")
	}
}

// Back to the thread still shown while the switch's body builds: its pages
// go before the message gets a page again, one page a message.
func TestReaderSwitchBackWhileUnseen(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	a := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "First thread", "first body"))
	b := h.deliverOne(accs[0], coretest.Msg("carol@example.com", accs[0].Email, "Second thread", "second body"))
	r := h.app.mail.reader
	h.openThread(a.ThreadID)
	va := h.seeBody(a.ID)
	switchTo(h, b.ThreadID)
	vb := unseenBody(h, b.ID)
	h.openThread(a.ThreadID) // the fake panics on a second page of a message
	h.until("the first thread's page made again", func() bool { v := h.plat.view(a.ID); return v != nil && v != va })
	if !va.closed || !vb.closed || r.prev != nil {
		t.Fatalf("closed %v %v, prev %v: want both earlier pages closed", va.closed, vb.closed, r.prev != nil)
	}
}

// unseenBody waits for message's page made under the thread shown before
// the switch, and returns it. Placed under all else, it is at no point the
// tester looks.
func unseenBody(h *harness, message int64) *fakeBodyView {
	h.t.Helper()
	var v *fakeBodyView
	h.until(fmt.Sprintf("message %d's page made unseen", message), func() bool {
		v = h.plat.view(message)
		return v != nil && h.app.mail.reader.prev != nil
	})
	return v
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
	switchTo(h, b.ThreadID)
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

// switchTo opens thread id as a click on its row does, leaving the switch
// under way (openThread waits it out).
func switchTo(h *harness, id int64) {
	h.app.mail.open([]mailItem{{ID: id, Thread: true}}, 0)
	h.tt.Frame()
}

// A read the kept thread had under way when it goes is dropped: no page is
// made for a reader no one sees, out of reach of later switches (#682).
func TestReaderSwitchDropsKeptThreadsReads(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	msgs := h.thread2(accs[0])
	b := h.deliverOne(accs[0], coretest.Msg("dave@example.com", accs[0].Email, "Second thread", "second body"))
	r := h.app.mail.reader
	gate, read := make(chan struct{}), &atomic.Bool{}
	real := r.getMessage
	r.getMessage = func(ctx context.Context, id int64, remote bool) (core.Message, error) {
		if id == msgs[0].ID {
			<-gate
			defer read.Store(true)
		}
		return real(ctx, id, remote)
	}
	h.openThread(msgs[0].ThreadID)
	h.seeBody(msgs[1].ID)

	switchTo(h, b.ThreadID)
	vb := unseenBody(h, b.ID)
	h.post(func() { r.prev.expand(msgs[0].ID) }) // as a click on its folded row while it is kept
	h.drain()
	h.post(func() { h.app.BodyHeight(vb, b.ID, 120) })
	h.until("the kept thread gone", func() bool { return r.prev == nil })
	close(gate)
	h.until("the held read back", read.Load)
	for range 5 { // its callback, had it not been dropped
		h.drain()
		h.tt.Frame()
	}
	if v := h.plat.view(msgs[0].ID); v != nil {
		t.Fatal("a page was made for the kept thread after it went")
	}
}
