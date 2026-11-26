package app

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"weak"

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
// then replaces it at once: no blank body between (#667, #887).
func TestReaderSwitchKeepsShownUntilPainted(t *testing.T) {
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
	h.tt.Frame()
	if va.closed || !h.tt.HasText("First thread") {
		t.Fatal("receiving a height exposed a page that hadn't painted")
	}
	h.app.BodyHeight(vb, b.ID, 180) // an image changed the height of the already-staged page
	h.app.BodyPainted(vb, b.ID)
	if r.waiting() {
		t.Fatal("a resize restarted the wait for an already-staged, painted body")
	}
	h.tt.Frame() // the new thread shows whole, not entering part by part
	if !h.tt.HasText("Second thread") || r.prev != nil || !va.closed {
		t.Fatal("the new body was not shown after its first drawable frame")
	}
	if box, _ := h.viewBox(vb); box.H < 160 {
		t.Fatalf("new body shown %v high as the thread came, want its reported 180", box.H)
	}
}

// Building a measured slot is not presenting it: one input event can rebuild
// the tree again before MyGo commits placements. The first build must wait.
func TestReaderStagesACommittedPlacement(t *testing.T) {
	b := &msgBody{view: &fakeBodyView{}, height: -1}
	s := &session{msgs: []core.MessageSummary{{ID: 1}}, bodies: map[int64]*msgBody{1: b}}
	r := &reader{readerPane: &readerPane{p: &mailPage{a: &App{}}}, session: s}
	var staged []bool
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Children(func() {
			r.unseenBody(c, s)
			if b.height >= 0 {
				staged = append(staged, b.staged)
			}
		})
	}, 600, 600)
	b.height = 500
	tt.Frame()
	if len(staged) < 2 || staged[0] || !b.staged {
		t.Fatalf("sized builds %v: want first uncommitted, then placed", staged)
	}
}

// A slow page must not replace a readable body with an empty WebView.
func TestReaderSwitchKeepsShownForSlowBody(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	a := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "First thread", "first body"))
	b := h.deliverOne(accs[0], coretest.Msg("carol@example.com", accs[0].Email, "Second thread", "second body"))
	r := h.app.mail.reader
	h.openThread(a.ThreadID)
	va := h.seeBody(a.ID)
	switchTo(h, b.ThreadID)
	vb := unseenBody(h, b.ID)
	h.advance(2 * time.Second)
	if va.closed || r.prev == nil || !h.tt.HasText("First thread") {
		t.Fatal("the visible body was replaced before the new body had a frame")
	}
	h.app.BodyHeight(vb, b.ID, 0) // an empty body is a valid frame, too
	h.app.BodyPainted(vb, b.ID)
	h.see("Second thread")
}

// A page that never responds gives the selected thread a native retry instead
// of revealing the empty surface or leaving the old message on screen forever.
func TestReaderSwitchUnresponsiveBodyRetries(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	a := h.deliverOne(accs[0], coretest.Msg("carol@example.com", accs[0].Email, "First thread", "first body"))
	b := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "Slow thread", "slow body"))
	h.openThread(a.ThreadID)
	h.seeBody(a.ID)
	h.app.SetForeground(true)
	paint := h.plat.paintBody
	switchTo(h, b.ThreadID)
	stuck := unseenBody(h, b.ID)
	h.app.SetForeground(false)
	h.advance(bodyPaintTimeout - time.Millisecond)
	h.app.SetForeground(true)
	h.tt.Frame()
	h.advance(time.Millisecond)
	if stuck.closed {
		t.Fatal("time spent in the background was charged to this page")
	}
	h.advance(bodyPaintTimeout)
	h.see("重试")
	if !stuck.discarded || h.plat.view(b.ID) != nil {
		t.Fatal("unresponsive renderer was kept")
	}
	h.plat.paintBody = paint
	h.click("重试")
	h.seeBody(b.ID)
}

func TestReaderSwitchStalledBodyReadCanRetry(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	a := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "First thread", "first body"))
	b := h.deliverOne(accs[0], coretest.Msg("carol@example.com", accs[0].Email, "Second thread", "second body"))
	h.openThread(a.ThreadID)
	h.seeBody(a.ID)
	h.app.SetForeground(true)
	reading, release := gateBody(h.app.mail.reader, b.ID)
	defer release()
	paint := h.plat.paintBody
	switchTo(h, b.ThreadID)
	h.until("body being read", func() bool { return len(reading) > 0 })
	h.advance(bodyPaintTimeout)
	h.see("重试")
	release()
	h.plat.paintBody = paint
	h.click("重试")
	h.seeBody(b.ID)
}

func TestReaderSwitchDoesNotRevealSupersededBody(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	a := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "First thread", "first body"))
	b := h.deliverOne(accs[0], coretest.Msg("carol@example.com", accs[0].Email, "Second thread", "second body"))
	c := h.deliverOne(accs[0], coretest.Msg("dave@example.com", accs[0].Email, "Third thread", "third body"))
	h.openThread(a.ThreadID)
	va := h.seeBody(a.ID)
	switchTo(h, b.ThreadID)
	vb := unseenBody(h, b.ID)
	release, _ := gateThread(h, c.ThreadID)
	switchTo(h, c.ThreadID)
	h.app.BodyHeight(vb, b.ID, 500)
	h.app.BodyPainted(vb, b.ID)
	h.tt.Frame()
	h.tt.Frame()
	release()
	if va.closed || !h.tt.HasText("First thread") {
		t.Fatal("the intermediate selection replaced the visible mail")
	}
	vc := unseenBody(h, c.ID)
	h.app.BodyPainted(vb, b.ID) // a queued callback on the retired binding
	h.app.BodyHeight(vc, c.ID, 320)
	h.tt.Frame()
	if !vb.closed || va.closed {
		t.Fatal("a superseded page revealed the new selection")
	}
	h.app.BodyPainted(vc, c.ID)
	h.see("Third thread")
}

func TestReaderSwitchFailedThreadCanRetry(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	a := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "First thread", "first body"))
	b := h.deliverOne(accs[0], coretest.Msg("carol@example.com", accs[0].Email, "Second thread", "second body"))
	h.openThread(a.ThreadID)
	va := h.seeBody(a.ID)
	r := h.app.mail.reader
	get := r.getThread
	r.getThread = func(ctx context.Context, id int64) (core.Thread, error) {
		if id == b.ThreadID {
			return core.Thread{}, context.DeadlineExceeded
		}
		return get(ctx, id)
	}
	h.openThread(b.ThreadID)
	h.see("重试")
	if !va.closed {
		t.Fatal("the failed selection left the previous message visible")
	}
	r.getThread = get
	h.click("重试")
	h.seeBody(b.ID)
}

func TestReaderSwitchToSameMessageKeepsItsPage(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	acc := accs[0]
	if err := h.env.IMAP(acc.ID).User.Create("Junk", nil); err != nil {
		t.Fatal(err)
	}
	h.syncNow(acc)
	msgs := h.thread2(acc)
	m := msgs[len(msgs)-1]
	r := h.app.mail.reader
	avatar := make(chan struct{})
	r.getAvatar = func(ctx context.Context, _ int64, _ bool) (core.Avatar, error) {
		select {
		case <-avatar:
		case <-ctx.Done():
		}
		return core.Avatar{}, ctx.Err()
	}
	h.openThread(m.ThreadID)
	v := h.seeBody(m.ID)
	b := r.bodies[m.ID]
	h.app.mail.open([]mailItem{messageItem(m)}, 0)
	h.until("message alone", func() bool { return r.key[1] == m.ID && len(r.msgs) == 1 })
	close(avatar)
	if h.seeBody(m.ID) != v || v.closed {
		t.Fatal("showing the same message alone recreated the visible page")
	}
	h.until("transferred body's avatar finished", func() bool { return b.avatarDone })
	if err := h.env.Svc.MoveToRole(t.Context(), []int64{m.ID}, "junk"); err != nil {
		t.Fatal(err)
	}
	h.openThread(m.ThreadID)
	if h.seeBody(m.ID) == v || !v.closed || !r.bodies[m.ID].msg.InJunk || r.bodies[m.ID].trusted {
		t.Fatal("the inbox binding or its automatic image grant followed the message to Junk")
	}
}

func TestReaderOffscreenBodyDoesNotTimeOut(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	msgs := h.thread2(accs[0])
	h.app.SetForeground(true)
	h.plat.paintBody = nil
	h.openThreadWith(msgs[0].ThreadID, msgs[0].ID)
	h.until("both views bound", func() bool { return h.plat.openViews() == 2 })
	upper := h.plat.view(msgs[0].ID)
	lower := h.plat.view(msgs[1].ID)
	h.app.BodyHeight(upper, msgs[0].ID, 3000)
	h.app.BodyPainted(upper, msgs[0].ID)
	h.until("lower body below viewport", func() bool {
		return h.app.mail.reader.bodies[msgs[1].ID].stopPaint == nil && h.app.mail.reader.scroll.MaxY > 2000
	})
	h.advance(bodyPaintTimeout)
	if lower.closed {
		t.Fatal("a page outside the viewport was treated as unresponsive")
	}
}

func TestReaderPrintWaitsForAssignedDocument(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	msgs := h.thread2(accs[0])
	h.openThread(msgs[0].ThreadID)
	h.seeBody(msgs[1].ID)
	r := h.app.mail.reader
	r.toggle(msgs[1].ID)
	h.plat.paintBody = nil
	id := msgs[0].ID
	r.toggle(id)
	h.until("new print target bound", func() bool { return h.plat.view(id) != nil })
	stale := h.plat.view(id)
	h.tt.Key(ui.Cmd, ui.KeyP)
	if h.countPrefix("Print") != 0 {
		t.Fatal("printing an unpainted binding could print the previous document")
	}
	r.toggle(id)
	r.toggle(id)
	h.app.BodyPainted(stale, id)
	v := h.plat.view(id)
	h.app.BodyHeight(v, id, 80)
	h.app.BodyPainted(v, id)
	if h.countPrefix("Print") != 0 {
		t.Fatal("the deferred print survived folding its binding")
	}
	r.toggle(id)
	r.toggle(id)
	v = h.plat.view(id)
	r.printMail()
	h.app.BodyHeight(v, id, 80)
	h.app.BodyPainted(v, id)
	if h.countCalls(fmt.Sprint("Print ", id)) != 1 {
		t.Fatal("the queued print did not print the newly painted document exactly once")
	}
}

// Switching back to the still-visible thread keeps the body already on screen.
func TestReaderSwitchBackWhileUnseen(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	a := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "First thread", "first body"))
	b := h.deliverOne(accs[0], coretest.Msg("carol@example.com", accs[0].Email, "Second thread", "second body"))
	r := h.app.mail.reader
	h.openThread(a.ThreadID)
	va := h.seeBody(a.ID)
	switchTo(h, b.ThreadID)
	vb := unseenBody(h, b.ID)
	h.openThread(a.ThreadID)
	if va.closed || !vb.closed || r.prev != nil || h.plat.view(a.ID) != va {
		t.Fatal("switching back discarded the body still on screen")
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
	h.plat.paintBody = nil // deliver height and painted independently
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
	h.post(func() { r.on(r.prev).expand(msgs[0].ID) }) // work the kept thread has under way as it goes
	h.drain()
	h.app.BodyHeight(vb, b.ID, 120)
	h.app.BodyPainted(vb, b.ID)
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

// The kept thread's pages are heard while they show: a link opens, a height
// is taken; once the thread goes, its closed pages are not (#682).
func TestReaderSwitchKeptPagesCallBack(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	a := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "First thread", "first body"))
	b := h.deliverOne(accs[0], coretest.Msg("carol@example.com", accs[0].Email, "Second thread", "second body"))
	r := h.app.mail.reader
	h.openThread(a.ThreadID)
	va := h.seeBody(a.ID)
	switchTo(h, b.ThreadID)
	vb := unseenBody(h, b.ID)
	kept := r.prev

	h.post(func() {
		h.app.BodyHeight(va, a.ID, 321)
		h.app.BodyLink(va, "https://example.com/kept")
	})
	h.drain()
	if kept.bodies[a.ID].height != 321 || !h.app.linkOpen || h.app.link != "https://example.com/kept" {
		t.Fatalf("kept page unheard: height %d, link %v %q", kept.bodies[a.ID].height, h.app.linkOpen, h.app.link)
	}
	h.click("取消")

	h.app.BodyHeight(vb, b.ID, 120)
	h.app.BodyPainted(vb, b.ID)
	h.until("the kept thread gone", func() bool { return r.prev == nil })
	h.post(func() { h.app.BodyLink(va, "https://example.com/gone") })
	h.drain()
	if h.app.linkOpen {
		t.Fatal("a closed page's link opened")
	}
}

// The kept thread takes no input: a dialog or draft it started would come
// from a pane about to go (#682). It shows the reply button all the same.
func TestReaderSwitchKeptThreadTakesNoInput(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	a := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "First thread", "first body"))
	b := h.deliverOne(accs[0], coretest.Msg("carol@example.com", accs[0].Email, "Second thread", "second body"))
	h.openThread(a.ThreadID)
	h.seeBody(a.ID)
	switchTo(h, b.ThreadID)
	unseenBody(h, b.ID)
	at, ok := h.tt.Find("回复")
	if !ok {
		t.Fatal("the kept thread's actions are not shown")
	}
	h.tt.ClickAt(at.X+at.W/2, at.Y+at.H/2)
	time.Sleep(50 * time.Millisecond) // a draft is made off the UI thread
	for range 5 {
		h.drain()
		h.tt.Frame()
	}
	if h.app.mail.reader.prev == nil {
		t.Fatal("the switch ended before the click was checked")
	}
	for _, call := range h.plat.Calls() {
		if strings.HasPrefix(call, "OpenCompose") {
			t.Fatalf("a click on the kept thread's reply opened a draft: %q", call)
		}
	}
}

// A thread switched away from is let go whole: nothing the pane keeps
// across switches (the AI's reads by message) holds its session, with the
// bodies, text and avatars it read.
func TestReaderSwitchLetsSessionGo(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	a := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "First thread", "first body"))
	b := h.deliverOne(accs[0], coretest.Msg("carol@example.com", accs[0].Email, "Second thread", "second body"))
	c := h.deliverOne(accs[0], coretest.Msg("dave@example.com", accs[0].Email, "Third thread", "third body"))
	r := h.app.mail.reader
	h.openThread(a.ThreadID)
	h.seeBody(a.ID)
	h.until("the first thread's cards tried", func() bool { st := r.ai[a.ID]; return st != nil && !st.cards.loading })
	first := weak.Make(r.session)
	h.openThread(b.ThreadID)
	h.openThread(c.ThreadID)
	h.until("the first thread's session let go", func() bool {
		runtime.GC()
		return first.Value() == nil
	})
}
