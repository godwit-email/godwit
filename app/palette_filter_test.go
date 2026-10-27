package app

import (
	"testing"

	"github.com/egoist/mygo/ui"
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
