package app

import (
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/theme"
)

// filtersShown is whether the palette lists its filters.
func filtersShown(h *harness) bool { return inPalette(h, "有附件") && inPalette(h, "已标星") }

// / opens the filter list in an empty box or after a space, and is a
// character after anything else; Escape closes the list before the palette.
func TestPaletteSlashOpensFilters(t *testing.T) {
	h, _ := invoices(t)
	h.typeText("/")
	h.until("the filter list", func() bool { return filtersShown(h) })
	h.tt.Key(0, ui.KeyEscape)
	h.until("the list closed, the palette open", func() bool { return !filtersShown(h) && paletteShown(h) })

	h.typeText("10")
	h.typeText("/")
	h.typeText("02")
	h.see("没有找到「10/02」")
	if filtersShown(h) {
		t.Fatal("/ after a digit opened the filters")
	}

	h.typeText(" ")
	h.typeText("/")
	h.until("the filter list after a space", func() bool { return filtersShown(h) })
}

// A filter chosen shows as a chip and filters the mail and attachments, with
// or without text; Backspace in an empty box takes the last chip off, and a
// chip's button removes it.
func TestPaletteFilterChips(t *testing.T) {
	h, _ := invoices(t)
	h.typeText("/")
	h.until("the filter list", func() bool { return filtersShown(h) })
	h.tt.Key(0, ui.KeyEnter) // 有附件, the first
	h.see("邮件 · 1")
	h.see("附件 · 1")
	h.until("only the mail with an attachment, no commands", func() bool {
		return inPalette(h, "Invoice 3") && !inPalette(h, "Invoice 4") && !inPalette(h, "写邮件") && !inPalette(h, "最近")
	})

	h.typeText("/")
	h.until("the filter list", func() bool { return filtersShown(h) })
	h.tt.Key(0, ui.KeyDown)
	h.tt.Key(0, ui.KeyDown)
	h.tt.Key(0, ui.KeyEnter) // 已标星, the third
	h.see("没有符合筛选的邮件")

	h.tt.Key(0, ui.KeyBackspace) // takes 已标星 off
	h.see("邮件 · 1")

	h.typeText("invoice")
	h.see("邮件 · 1")
	h.tt.Key(ui.Cmd, ui.KeyA)
	h.tt.Key(0, ui.KeyBackspace) // clears the text, not a chip
	h.see("邮件 · 1")

	h.click("移除「有附件」")
	h.see("最近")
	h.see("写邮件")
}

// The / that opens the filters is not left in the box.
func TestPaletteSlashLeavesNoSlash(t *testing.T) {
	h, _ := invoices(t)
	h.typeText("a ")
	h.tt.Frame()
	before, _ := h.tt.TextCaret()
	h.typeText("/")
	h.until("the filter list", func() bool { return filtersShown(h) })
	h.tt.Frame()
	after, _ := h.tt.TextCaret() // the box's text is not among Texts: its caret says where the text ends
	if after.X != before.X || h.app.palette.query != "a " {
		t.Fatalf("caret %v, was %v; query %q", after.X, before.X, h.app.palette.query)
	}
}

// Each filter option has an icon, and label/time options have no "Label:" or "Time:" prefixes.
func TestPaletteFilterOptionIconsAndLabels(t *testing.T) {
	h, _ := invoices(t)
	p := h.app.palette
	opts := p.filterOpts()
	if len(opts) == 0 {
		t.Fatal("no filter options")
	}
	for _, opt := range opts {
		if opt.icon == "" {
			t.Errorf("filter %q (kind %s) has no icon", opt.label, opt.kind)
		}
		if opt.kind == "account" {
			if opt.icon != "at" {
				t.Errorf("account filter %q icon = %q, want at", opt.label, opt.icon)
			}
			if strings.HasPrefix(opt.label, "Account:") || strings.HasPrefix(opt.label, "Konto:") || strings.HasPrefix(opt.label, "账户：") {
				t.Errorf("account filter %q has redundant prefix", opt.label)
			}
		}
		if opt.kind == "label" && (strings.HasPrefix(opt.label, "Label:") || strings.HasPrefix(opt.label, "标签：")) {
			t.Errorf("label filter %q has redundant prefix", opt.label)
		}
		if opt.kind == "span" && (strings.HasPrefix(opt.label, "Time:") || strings.HasPrefix(opt.label, "时间：")) {
			t.Errorf("span filter %q has redundant prefix", opt.label)
		}
	}
}

// Labels preserve their color when selected and use fill icons; account icons support custom icons gracefully.
func TestPaletteFilterLabelAndAccountIconSelected(t *testing.T) {
	h, _ := invoices(t)
	p := h.app.palette
	opts := p.filterOpts()

	var labelOpt, accountOpt palFilter
	for _, opt := range opts {
		if opt.kind == "label" && labelOpt.kind == "" {
			labelOpt = opt
		}
		if opt.kind == "account" && accountOpt.kind == "" {
			accountOpt = opt
		}
	}
	if labelOpt.kind == "" || accountOpt.kind == "" {
		t.Fatalf("missing label or account options: label=%+v account=%+v", labelOpt, accountOpt)
	}

	// Label icon color is preserved
	th := theme.Dark()
	if labelOpt.iconColor == nil {
		t.Fatal("label option has no iconColor func")
	}
	clr := labelOpt.iconColor(th)
	if clr == th.Foreground {
		t.Errorf("label color should not be foreground, got %v", clr)
	}

	// Account renderIcon doesn't panic and renders for various icon types
	tt := ui.NewTester(func(c *ui.Context) {
		p.renderAccountIcon(c, core.Account{ID: 1, Icon: "image:1"}, false)
		p.renderAccountIcon(c, core.Account{ID: 1, Icon: "image:1"}, true)
		p.renderAccountIcon(c, core.Account{ID: 2, Icon: "brand:google"}, false)
		p.renderAccountIcon(c, core.Account{ID: 2, Icon: "brand:google"}, true)
		p.renderAccountIcon(c, core.Account{ID: 3, Icon: "label:star"}, false)
		p.renderAccountIcon(c, core.Account{ID: 3, Icon: "label:star"}, true)
		p.renderAccountIcon(c, core.Account{ID: 4, Icon: ""}, false)
		p.renderAccountIcon(c, core.Account{ID: 4, Icon: ""}, true)
	}, 200, 200)
	tt.Frame()
}

// The filters list under a heading per group, which ↑↓ pass over: three
// steps from 有附件 land on the account, the first after 状态's three.
func TestPaletteFilterGroups(t *testing.T) {
	h, _ := invoices(t)
	h.typeText("/")
	h.until("the filter list", func() bool { return filtersShown(h) })
	texts := h.tt.Texts()
	texts = texts[slices.Index(texts, "联系人"):] // the palette's, not the sidebar's
	at := func(s string) int { return slices.Index(texts, s) }
	if !(at("状态") >= 0 && at("状态") < at("有附件") && at("已标星") < at("账户") && at("账户") < at("alice@example.com") && at("alice@example.com") < at("时间")) {
		t.Fatalf("groups out of order: %q", texts)
	}
	for range 3 {
		h.tt.Key(0, ui.KeyDown)
	}
	h.tt.Key(0, ui.KeyEnter)
	h.see("移除「alice@example.com」")
}

// ←→ and ⌃Tab leave the kind tabs alone while the filter list shows, and
// switch them once it is closed.
func TestPaletteFilterKeepsKind(t *testing.T) {
	h, _ := invoices(t)
	h.typeText("/")
	h.until("the filter list", func() bool { return filtersShown(h) })
	h.tt.Key(0, ui.KeyRight)
	h.tt.Key(ui.Ctrl, ui.KeyTab)
	h.drain()
	if k := h.app.palette.kind; k != 0 || !filtersShown(h) {
		t.Fatalf("kind %d, filters shown %v", k, filtersShown(h))
	}
	h.tt.Key(0, ui.KeyEscape)
	h.tt.Key(0, ui.KeyRight)
	h.drain()
	if k := h.app.palette.kind; k != 1 {
		t.Fatalf("kind %d after the list closed, want 1", k)
	}
}

// Over the filter list the hints are the list's keys, not the palette's.
func TestPaletteFilterHints(t *testing.T) {
	h, _ := invoices(t)
	h.see("切换类型")
	h.typeText("/")
	h.until("the filter list", func() bool { return filtersShown(h) })
	h.see("添加筛选")
	if h.tt.HasText("切换类型") || h.tt.HasText("问 Wit") {
		t.Fatal("the palette's hints over the filter list")
	}
	h.tt.Key(0, ui.KeyEscape)
	h.see("切换类型")
}
