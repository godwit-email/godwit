package app

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
)

// draftsHarness has an account with saved drafts (subject, body "body of
// <subject>", to bob) and the drafts folder open.
func draftsHarness(t *testing.T, subjects ...string) (*harness, []core.Draft) {
	t.Helper()
	h, accs := mailHarness(t, nil, "alice@example.com")
	if err := h.env.IMAP(accs[0].ID).User.Create("Drafts", nil); err != nil {
		t.Fatal(err)
	}
	h.syncNow(accs[0])
	var out []core.Draft
	for _, subj := range subjects {
		d, err := h.env.Svc.NewDraft(context.Background(), accs[0].ID, "new", 0)
		if err != nil {
			t.Fatal(err)
		}
		d.Subject, d.To, d.HTML = subj, []string{"bob@example.com"}, "<p>body of "+subj+"</p>"
		if d, err = h.env.Svc.SaveDraft(context.Background(), d); err != nil {
			t.Fatal(err)
		}
		out = append(out, d)
	}
	h.waitMail("the drafts folder", func() bool { return h.tt.HasText("草稿") })
	h.click("草稿")
	h.see(subjects[0])
	return h, out
}

func (h *harness) noCompose() {
	h.t.Helper()
	if id, ok := h.lastCompose(); ok {
		h.t.Fatalf("a compose window opened for draft %d", id)
	}
}

func TestDraftSelectShowsPane(t *testing.T) {
	h, ds := draftsHarness(t, "Draft hello")
	h.see("草稿 · alice@example.com")
	if h.tt.HasText("继续编辑") {
		t.Fatal("a pane with nothing selected")
	}
	h.click("Draft hello")
	h.see("继续编辑")
	h.see("删除草稿")
	h.see("body of Draft hello")
	h.see("草稿已保存 · " + ds[0].UpdatedAt.In(h.clock.Now().Location()).Format("15:04"))
	h.see("bob@example.com")
	h.noCompose()
}

func TestDraftEditingChipFollowsWindow(t *testing.T) {
	h, ds := draftsHarness(t, "Draft hello")
	if h.tt.HasText("编辑中") {
		t.Fatal("chip without a window")
	}
	h.plat.setComposeOpen(ds[0].ID, true)
	h.advance(1)
	if h.tt.HasText("编辑中") {
		t.Fatal("chip before ComposeWindowsChanged")
	}
	h.app.ComposeWindowsChanged()
	h.see("编辑中")
	h.click("Draft hello")
	h.see("正在撰写窗口里编辑")
	h.plat.setComposeOpen(ds[0].ID, false)
	h.app.ComposeWindowsChanged()
	h.until("the chip gone", func() bool { return !h.tt.HasText("编辑中") && !h.tt.HasText("正在撰写窗口里编辑") })
}

// A draft deleted as empty when its window closed leaves the pane.
func TestEmptyDraftClosedLeavesPane(t *testing.T) {
	h, ds := draftsHarness(t, "Draft hello", "Draft bye")
	h.click("Draft bye")
	h.see("继续编辑")
	if err := h.env.Svc.DeleteDraft(context.Background(), ds[1].ID); err != nil {
		t.Fatal(err)
	}
	h.app.ComposeWindowsChanged()
	h.until("the pane gone", func() bool { return !h.tt.HasText("继续编辑") && !h.tt.HasText("Draft bye") })
	h.see("Draft hello")
}

func TestDraftPaneDeleteClosesWindow(t *testing.T) {
	h, ds := draftsHarness(t, "Draft hello")
	h.click("Draft hello")
	h.click("删除草稿")
	h.called(fmt.Sprint("DraftDeleted ", ds[0].ID))
	h.until("the draft gone", func() bool { return !h.tt.HasText("Draft hello") })
}

func TestDraftDoubleClickOpensCompose(t *testing.T) {
	h, ds := draftsHarness(t, "Draft hello")
	h.click("Draft hello")
	h.noCompose()
	h.click("Draft hello")
	if id := h.composeOpened(0); id != ds[0].ID {
		t.Fatalf("opened %d, want %d", id, ds[0].ID)
	}
}

func (h *harness) opens(id int64) int {
	return len(slices.DeleteFunc(h.plat.Calls(), func(c string) bool { return c != fmt.Sprint("OpenCompose ", id) }))
}

// Enter on the chosen row (not on a button) opens it.
func TestDraftEnterOpensCompose(t *testing.T) {
	h, ds := draftsHarness(t, "Draft hello")
	h.click("Draft hello")
	h.noCompose()
	h.tt.Key(0, ui.KeyEnter)
	h.until("Enter opens it", func() bool { return h.opens(ds[0].ID) == 1 })
}

func TestDraftContinueOpensCompose(t *testing.T) {
	h, ds := draftsHarness(t, "Draft hello")
	h.click("Draft hello")
	h.click("继续编辑")
	h.until("opened", func() bool { return h.opens(ds[0].ID) == 1 })
}

// The selection clears when its draft leaves the list.
func TestDraftSelectionClearsWhenGone(t *testing.T) {
	h, ds := draftsHarness(t, "Draft hello")
	h.click("Draft hello")
	h.see("继续编辑")
	if err := h.env.Svc.DeleteDraft(context.Background(), ds[0].ID); err != nil {
		t.Fatal(err)
	}
	h.app.ComposeWindowsChanged()
	h.until("cleared", func() bool { return h.app.mail.draftSel == 0 })
}

// TestDraftPaneLabelsNoClip: each label fits its column at font 16 in English, on one line and ends before its value.
func TestDraftPaneLabelsNoClip(t *testing.T) {
	h, _ := draftsHarness(t, "Draft hello")
	h.click("Draft hello")
	h.see("继续编辑")
	t.Cleanup(func() { i18n.SetLanguage("zh-CN") })
	i18n.SetLanguage("en")
	h.setSettings(func(s *core.Settings) { s.FontSize = 16 })
	for _, label := range []string{"To", "Subject", "From"} {
		h.see(label)
	}
	for _, label := range []string{"To", "Subject", "From"} {
		lab, ok1 := h.tt.Find(label)
		val, ok2 := h.tt.Find("draft field: " + label)
		if !ok1 || !ok2 {
			t.Fatalf("%s: boxes found %v %v", label, ok1, ok2)
		}
		if lab.X+lab.W > val.X {
			t.Errorf("%s label right %v past the value left %v", label, lab.X+lab.W, val.X)
		}
		if lab.H > val.H { // a label squeezed into a fixed column wraps
			t.Errorf("%s label wraps: height %v, its value's %v", label, lab.H, val.H)
		}
	}
}
