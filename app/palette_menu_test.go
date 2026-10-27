package app

import (
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// paletteHas is whether a text of the palette holds s.
func paletteHas(h *harness, s string) bool {
	texts := h.tt.Texts()
	i := slices.Index(texts, "联系人")
	return i >= 0 && slices.ContainsFunc(texts[i:], func(t string) bool { return strings.Contains(t, s) })
}

// ⌥↵ opens the chosen mail's menu; Esc closes it and not the palette; ↑↓
// and ↵ choose in it. Marking read and archiving leave the palette open on
// the refreshed hit.
func TestPaletteMenuMail(t *testing.T) {
	h, accs := invoices(t)
	h.typeText("Invoice 2")
	h.see("邮件 · 1")

	h.tt.Key(ui.Alt, ui.KeyEnter)
	h.see("全部回复")
	h.tt.Key(0, ui.KeyEscape)
	h.until("the menu closed over the palette", func() bool { return !h.tt.HasText("全部回复") && paletteShown(h) })

	// 回复, 全部回复, 归档, 标为已读, 添加标签
	h.tt.Key(ui.Alt, ui.KeyEnter)
	h.see("标为已读")
	for range 3 {
		h.tt.Key(0, ui.KeyDown)
	}
	h.tt.Key(0, ui.KeyEnter)
	h.until("the menu closed", func() bool { return !h.tt.HasText("全部回复") })
	h.until("read, the menu offers unread", func() bool {
		h.tt.Key(ui.Alt, ui.KeyEnter)
		h.drain()
		h.tt.Frame()
		ok := h.tt.HasText("标为未读")
		h.tt.Key(0, ui.KeyEscape)
		return ok && paletteShown(h)
	})

	h.tt.Key(ui.Alt, ui.KeyEnter)
	h.see("全部回复")
	h.tt.Key(0, ui.KeyDown)
	h.tt.Key(0, ui.KeyDown)
	h.tt.Key(0, ui.KeyEnter)
	h.until("archived, the palette still open", func() bool {
		return h.countIn(h.folderOf(accs[0], "archive")) == 1 && paletteShown(h) && paletteHas(h, "归档")
	})

	h.tt.Key(ui.Alt, ui.KeyEnter)
	h.click("回复")
	h.until("a reply in a compose window", func() bool {
		return slices.ContainsFunc(h.plat.Calls(), func(c string) bool { return strings.HasPrefix(c, "OpenCompose ") })
	})
}

// A conversation's menu opens it or deletes it, the delete once confirmed;
// the palette stays open on what is left.
func TestPaletteMenuConversation(t *testing.T) {
	h, _, _ := askHarness(t, true)
	ctx := t.Context()
	cv, err := h.env.Svc.NewConversation(ctx, "ask", 0)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := h.env.Svc.Ask(ctx, cv.ID, "找一下去年的发票")
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	h.app.navigate(Route{Page: PageSettings})
	h.tt.Frame()
	h.tt.Key(ui.Cmd, ui.KeyK)
	h.until("the palette", func() bool { return paletteShown(h) })
	h.typeText("发票")
	h.see("对话 · 1")

	h.tt.Key(ui.Alt, ui.KeyEnter)
	h.see("删除")
	if h.tt.HasText("重命名") {
		t.Fatal("rename offered")
	}
	h.tt.Key(0, ui.KeyDown)
	h.tt.Key(0, ui.KeyEnter)
	h.see("删除这个对话？")
	h.click("取消")
	h.until("cancelled", func() bool { return !h.tt.HasText("删除这个对话？") && paletteShown(h) })
	if cs, _ := h.env.Svc.ListConversations(ctx, "ask"); len(cs) != 1 {
		t.Fatalf("conversations %v", cs)
	}

	h.tt.Key(ui.Alt, ui.KeyEnter)
	h.see("删除")
	h.tt.Key(0, ui.KeyUp) // wraps to 删除
	h.tt.Key(0, ui.KeyEnter)
	h.see("删除这个对话？")
	h.click("删除")
	h.see("没有找到「发票」")
	if cs, _ := h.env.Svc.ListConversations(ctx, "ask"); len(cs) != 0 || !paletteShown(h) {
		t.Fatalf("conversations %v", cs)
	}
}

// A contact's menu writes to them and copies the address; an attachment's
// saves it and opens its mail.
func TestPaletteMenuContactAttachment(t *testing.T) {
	h, _ := invoices(t)
	h.click("联系人")
	h.typeText("bob")
	h.see("联系人 · 1")
	h.tt.Key(ui.Alt, ui.KeyEnter)
	h.see("复制地址")
	h.click("复制地址")
	h.until("the address copied", func() bool { return h.tt.Clipboard() == "bob@example.com" && paletteShown(h) })
	h.tt.Key(ui.Alt, ui.KeyEnter)
	h.tt.Key(0, ui.KeyEnter) // 写邮件
	h.until("a draft to Bob", func() bool {
		return !paletteShown(h) && slices.ContainsFunc(h.plat.Calls(), func(c string) bool { return strings.HasPrefix(c, "OpenCompose ") })
	})

	h.tt.Key(ui.Cmd, ui.KeyK)
	h.until("the palette", func() bool { return paletteShown(h) })
	h.typeText("march")
	h.see("附件 · 1")
	h.tt.Key(ui.Alt, ui.KeyEnter)
	h.click("另存为…")
	h.until("asked where to save", func() bool { return slices.Contains(h.plat.Calls(), "SaveFile invoice-march.pdf") })
	h.tt.Key(ui.Alt, ui.KeyEnter)
	h.click("打开所在邮件")
	routeTo(h, "Invoice 3")
}
