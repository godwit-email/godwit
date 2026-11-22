package app

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/theme"
)

// hoverOn rests the pointer on the element labelled label.
func (h *harness) hoverOn(label string) {
	h.t.Helper()
	h.see(label)
	r, _ := h.tt.Find(label)
	h.tt.Move(r.X+r.W/2, r.Y+r.H/2)
	h.tt.Frame()
}

// settleToast plays the frames a toast needs to come in and find its place.
func (h *harness) settleToast() {
	h.tt.Frame()
	h.tt.Frame()
	h.tt.Advance(2 * theme.MotionBase)
	h.tt.Frame()
}

// windowSize is the window's size in DIPs.
func (h *harness) windowSize() (w, ht float32) {
	if h.size == [2]int{} {
		return 1280, 820
	}
	return float32(h.size[0]), float32(h.size[1])
}

// toastEnd is where the toast holding the element labelled label ends, as the
// toast's padding is 8 around it: its right and bottom edges.
func (h *harness) toastEnd(label string) (right, bottom float32) {
	h.t.Helper()
	r, ok := h.tt.Find(label)
	if !ok {
		h.t.Fatalf("no %q in %q", label, h.tt.Texts())
	}
	return r.X + r.W + 8, r.Y + r.H + 8
}

func near(got, want float32) bool { return got > want-1.5 && got < want+1.5 }

// archiveOnlyMail opens the only mail of a new inbox and archives it, which
// closes the reader and offers 撤销.
func archiveOnlyMail(t *testing.T) (*harness, core.Account) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.put(accs[0], "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "first", "x"))
	h.syncNow(accs[0])
	h.waitMail("the mail", func() bool { return len(h.items()) == 1 })
	h.click("first")
	h.waitMail("read by the reader", func() bool { return h.items()[0].Unread == 0 })
	return h, accs[0]
}

// TestArchiveToastUndo: archiving offers 撤销, which puts the mail back.
func TestArchiveToastUndo(t *testing.T) {
	h, acc := archiveOnlyMail(t)
	h.click("first")
	h.tt.Key(0, ui.KeyE)
	h.see(i18n.Tf("view.archived_n", 1))
	h.waitMail("archived", func() bool { return h.folderOf(acc, "archive").Total == 1 })
	h.click(i18n.T("compose.undo"))
	h.waitMail("back in the inbox", func() bool { return h.folderOf(acc, "inbox").Total == 1 && h.folderOf(acc, "archive").Total == 0 })
}

// TestArchiveToastUndoEndsWithItsWindow: held by the pointer past the undo
// window, the toast stays but 撤销 goes.
func TestArchiveToastUndoEndsWithItsWindow(t *testing.T) {
	h, _ := archiveOnlyMail(t)
	h.click("first")
	h.tt.Key(0, ui.KeyE)
	h.hoverOn(i18n.T("compose.undo"))
	h.clock.advance(core.UndoArchiveWindow + time.Second)
	h.tt.Advance(core.UndoArchiveWindow + time.Second)
	h.tt.Frame()
	if h.tt.HasText(i18n.T("compose.undo")) || !h.tt.HasText(i18n.Tf("view.archived_n", 1)) {
		t.Fatalf("after the window, held: %q, want the toast without 撤销", h.tt.Texts())
	}
}

// TestToastBeyondReplyBar: with the reader open the toast rises 8 above the
// reply bar and ends at its input's right edge, which the 720 column keeps
// off the window's; archiving the last mail takes the bar away, and the toast
// falls back to the content panel's corner.
func TestToastBeyondReplyBar(t *testing.T) {
	for _, size := range [][2]int{{1280, 820}, {1800, 900}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			h, _ := archiveOnlyMail(t)
			h.resize(size[0], size[1])
			h.click("first")
			h.post(func() {
				h.app.toasts = append(h.app.toasts, ui.Toast{ID: "probe", Title: "probe", Action: "go", Timeout: -1})
			})
			h.see("go")
			h.settleToast()
			r := h.app.mail.reader
			col := min(h.app.readerMaxWidth(), r.pane.W-2*28) // the bar pads its column 28 each side
			if size[0] > 1500 && col != theme.ReaderMaxWidth {
				t.Fatalf("the input is %v wide, want the 720 column", col)
			}
			right, bottom := h.toastEnd(i18n.T("toast.close"))
			wantRight := r.pane.X + r.pane.W/2 + col/2
			wantBottom := r.pane.Y + r.pane.H - r.barBottom - 8
			if !near(right, wantRight) || !near(bottom, wantBottom) {
				t.Errorf("toast ends at (%v, %v), want (%v, %v)", right, bottom, wantRight, wantBottom)
			}
		})
	}
	t.Run("bar gone", func(t *testing.T) {
		h, _ := archiveOnlyMail(t)
		h.click("first")
		h.tt.Key(0, ui.KeyE)
		h.see(i18n.T("compose.undo"))
		h.settleToast()
		w, ht := h.windowSize()
		right, bottom := h.toastEnd(i18n.T("compose.undo"))
		if !near(right, w-8-16) || !near(bottom, ht-8-16) {
			t.Errorf("without the bar the toast ends at (%v, %v), want the panel's corner (%v, %v)", right, bottom, w-24, ht-24)
		}
	})
}

// TestImportToastCancel: the progress toast offers 取消导入, which stops the
// import; the toast that follows an import keeps its text and offers nothing.
func TestImportToastCancel(t *testing.T) {
	h, _ := mailHarness(t, nil, "alice@example.com")
	acc := h.newArchive("Old mail")
	ctx, stop := context.WithCancel(t.Context())
	h.post(func() {
		h.app.mail.imports[acc.ID] = stop
		h.app.mail.importProgress(events.Event{AccountID: acc.ID, Done: 1, Total: 3})
	})
	h.see(i18n.Tf("mail.archive.importing", 1, 3))
	h.click(i18n.T("mail.archive.import.cancel"))
	if ctx.Err() == nil {
		t.Fatal("the import still runs")
	}
	h.until("the toast closed", func() bool { return !h.tt.HasText(i18n.T("mail.archive.import.cancel")) })
}

// TestUndoSendToastActionEndsAtSendAfter: held by the pointer past the undo
// window, the toast stays but its 撤销 goes with the window.
func TestUndoSendToastActionEndsAtSendAfter(t *testing.T) {
	h, accs := mailHarness(t, []coretest.Option{coretest.WithoutAutoSync()}, "alice@example.com")
	h.sendNew(accs[0], "bob@example.com", "change of plans")
	h.hoverOn(i18n.T("outbox.undo"))
	h.clock.advance(core.UndoSendWindow + time.Second)
	h.tt.Advance(core.UndoSendWindow + time.Second)
	h.tt.Frame()
	if h.tt.HasText(i18n.T("outbox.undo")) || !h.tt.HasText(i18n.T("outbox.undo.sending")) {
		t.Fatalf("after the window, held: %q, want the toast without 撤销", h.tt.Texts())
	}
}
