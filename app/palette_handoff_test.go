package app

import (
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
)

// paletteOver opens the palette over settings and types q.
func paletteOver(h *harness, q string) {
	h.app.navigate(Route{Page: PageSettings})
	h.tt.Frame()
	h.tt.Key(ui.Cmd, ui.KeyK)
	h.until("the palette", func() bool { return paletteShown(h) })
	h.typeText(q)
}

// Tab hands the query to Wit, as does a click on the footer's 问 Wit.
func TestPaletteTabAsksWit(t *testing.T) {
	h, _, _ := askHarness(t, true)
	var asks []string
	h.app.SetWitAsk(func(q string) { asks = append(asks, q) })
	paletteOver(h, "去年的发票")
	h.tt.Key(0, ui.KeyTab)
	h.until("the Wit page", func() bool { return h.app.route.Page == PageWit && !paletteShown(h) })
	if !slices.Equal(asks, []string{"去年的发票"}) {
		t.Fatalf("asks %q", asks)
	}

	paletteOver(h, "合同")
	h.click("问 Wit")
	h.until("the Wit page", func() bool { return h.app.route.Page == PageWit && !paletteShown(h) })
	if !slices.Equal(asks, []string{"去年的发票", "合同"}) {
		t.Fatalf("asks %q", asks)
	}
}

// Without a model Tab only shows the Wit page, which says how to add one.
func TestPaletteTabNoModelOnlyNavigates(t *testing.T) {
	h, _, _ := askHarness(t, false)
	var asks []string
	h.app.SetWitAsk(func(q string) { asks = append(asks, q) })
	paletteOver(h, "发票")
	h.tt.Key(0, ui.KeyTab)
	h.until("the Wit page", func() bool { return h.app.route.Page == PageWit && !paletteShown(h) })
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
