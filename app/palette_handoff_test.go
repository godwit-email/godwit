package app

import (
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
)

// paletteOver opens the palette over settings and types q.
func paletteOver(h *harness, q string) {
	h.app.navigate(Route{Page: PageSettings})
	h.tt.Frame()
	h.tt.Key(ui.Cmd, ui.KeyK)
	h.until("the palette", func() bool { return paletteShown(h) })
	h.typeText(q)
}

// Tab turns the box into a question to Wit, the text kept, and back; ↵
// asks it. The footer's 问 Wit turns it too.
func TestPaletteTabAsksWit(t *testing.T) {
	h, _, _ := askHarness(t, true)
	var asks []string
	h.app.SetWitAsk(func(q string, _ ...core.MessageSummary) { asks = append(asks, q) })
	paletteOver(h, "去年的发票")
	h.tt.Key(0, ui.KeyTab)
	h.until("the question box", func() bool { return !paletteShown(h) && h.tt.HasText("发送") && h.app.palette.open })
	h.tt.Key(0, ui.KeyTab)
	h.until("the search again", func() bool { return paletteShown(h) })
	if q := h.app.palette.query; q != "去年的发票" {
		t.Fatalf("query %q", q)
	}
	h.tt.Key(0, ui.KeyTab)
	h.until("the question box", func() bool { return !paletteShown(h) })
	h.tt.Key(ui.Shift, ui.KeyEnter)
	h.typeText("要报销的")
	h.tt.Key(0, ui.KeyEnter)
	h.until("the Wit page", func() bool { return h.app.route.Page == PageWit && !h.app.palette.open })
	if !slices.Equal(asks, []string{"去年的发票\n要报销的"}) {
		t.Fatalf("asks %q", asks)
	}

	paletteOver(h, "合同")
	h.click("问 Wit")
	h.until("the question box", func() bool { return !paletteShown(h) })
	h.tt.Key(0, ui.KeyEnter)
	h.until("the Wit page", func() bool { return !h.app.palette.open })
	if !slices.Equal(asks, []string{"去年的发票\n要报销的", "合同"}) {
		t.Fatalf("asks %q", asks)
	}
}

// Without a model asking only shows the Wit page, which says how to add one.
func TestPaletteTabNoModelOnlyNavigates(t *testing.T) {
	h, _, _ := askHarness(t, false)
	var asks []string
	h.app.SetWitAsk(func(q string, _ ...core.MessageSummary) { asks = append(asks, q) })
	paletteOver(h, "发票")
	h.tt.Key(0, ui.KeyTab)
	h.until("the question box", func() bool { return !paletteShown(h) })
	h.tt.Key(0, ui.KeyEnter)
	h.until("the Wit page", func() bool { return h.app.route.Page == PageWit && !h.app.palette.open })
	if len(asks) != 0 {
		t.Fatalf("asks %q", asks)
	}
	h.see("添加模型服务")
}

// ⌘↵ shows every hit in the list's search, with the query and the filters;
// a filter carried over shows as a chip there and comes off.
func TestPaletteViewAll(t *testing.T) {
	h, _ := invoices(t)
	h.typeText("/")
	h.until("the filter list", func() bool { return filtersShown(h) })
	h.tt.Key(0, ui.KeyDown)
	h.tt.Key(0, ui.KeyEnter) // 未读, the second
	h.typeText("invoice")
	h.see("邮件 · 4")
	h.see("在邮件列表中查看全部 4 封")
	h.tt.Key(ui.Cmd, ui.KeyEnter)
	p := h.app.mail
	h.until("the list's search", func() bool {
		return !paletteShown(h) && h.app.route.Page == PageMail && p.searched && len(p.results) == 4
	})
	if p.query != "invoice" || !p.sf.unread {
		t.Fatalf("query %q, filters %+v", p.query, p.sf)
	}
	h.click("移除「未读」")
	h.until("the chip off", func() bool { return !p.sf.unread })
}

// The row 在邮件列表中查看全部 shows only when hits are left out, and a
// click on it is ⌘↵.
// View-all lists at least what the palette's mail tab could: core's most.
func TestPaletteViewAllKeepsEveryPaletteHit(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.putMany(accs[0], 120)
	h.waitMail("the threads", func() bool { return len(h.items()) >= 50 })
	h.tt.Key(ui.Cmd, ui.KeyK)
	h.until("the palette", func() bool { return paletteShown(h) })
	h.typeText("msg")
	h.see("邮件 · 120")
	h.tt.Key(ui.Cmd, ui.KeyEnter)
	p := h.app.mail
	h.until("all 120 in the list", func() bool { return !paletteShown(h) && p.searched && len(p.results) == 120 })
}

// Left on Drafts or the Outbox, which draw no search, view-all and a contact
// move to a list that shows one.
func TestPaletteHandoffLeavesDraftsAndOutbox(t *testing.T) {
	h, _ := invoices(t)
	h.tt.Key(0, ui.KeyEscape)
	p := h.app.mail
	for _, v := range []core.View{p.firstDrafts(), {Unified: "outbox"}} {
		h.post(func() { h.app.navigate(Route{Page: PageMail, View: v}) })
		h.until("the view", func() bool { return h.app.mail.view == v })
		h.tt.Key(ui.Cmd, ui.KeyK)
		h.until("the palette", func() bool { return paletteShown(h) })
		h.typeText("invoice")
		h.see("邮件 · 4")
		h.tt.Key(ui.Cmd, ui.KeyEnter)
		h.until("the results in a list", func() bool {
			_, drafts := draftsOf(p.folders, p.view)
			return !paletteShown(h) && p.searched && len(p.results) == 4 && !drafts && p.view.Unified != "outbox"
		})
		h.see("找到 4 封 · 全部账户 · 只搜索已同步到本机的邮件")
	}
}

func TestPaletteViewAllRow(t *testing.T) {
	h, _ := invoices(t)
	h.tt.Key(0, ui.KeyRight) // 邮件
	h.typeText("invoice")
	h.see("邮件 · 4")
	if h.tt.HasText("在邮件列表中查看全部 4 封") {
		t.Fatal("the row with every hit shown")
	}
	h.tt.Key(ui.Ctrl|ui.Shift, ui.KeyTab) // 全部
	h.click("在邮件列表中查看全部 4 封")
	p := h.app.mail
	h.until("the list's search", func() bool { return !paletteShown(h) && p.searched && len(p.results) == 4 })
}

// A / in a question to Wit stays text: no filter list takes it.
func TestPaletteAskKeepsSlash(t *testing.T) {
	h, _, _ := askHarness(t, true)
	var asks []string
	h.app.SetWitAsk(func(q string, _ ...core.MessageSummary) { asks = append(asks, q) })
	paletteOver(h, "总结")
	h.tt.Key(0, ui.KeyTab)
	h.until("the question box", func() bool { return !paletteShown(h) })
	for _, r := range " /tmp/report" { // a key at a time: / after the space is the one the search takes
		h.typeText(string(r))
	}
	h.tt.Key(0, ui.KeyEnter)
	h.until("asked", func() bool { return len(asks) == 1 })
	if asks[0] != "总结 /tmp/report" {
		t.Fatalf("asked %q", asks[0])
	}
}
