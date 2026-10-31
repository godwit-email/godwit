package app

import (
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// Writing to a contact drafts to their address as a header gives it: a
// quoted local part keeps its quotes, a comma in it stays one address, and
// #, ? and % are the address's own.
func TestPaletteComposeTo(t *testing.T) {
	h, _ := mailHarness(t, nil, "alice@example.com")
	for _, c := range []struct{ key, want string }{
		{"bob@example.com", "bob@example.com"},
		{"foo#bar?baz%41@example.com", "foo#bar?baz%41@example.com"},
		{"a b@example.com", `"a b"@example.com`},
		{"a,b@example.com", `"a,b"@example.com`},
	} {
		before, _ := h.lastCompose()
		h.post(func() { h.app.composeTo(c.key) })
		var id int64
		h.until("a compose window", func() bool { id, _ = h.lastCompose(); return id != before })
		d, err := h.env.Svc.GetDraft(t.Context(), id)
		if err != nil || len(d.To) != 1 || d.To[0] != c.want {
			t.Errorf("%s: draft to %q, %v; want [%s]", c.key, d.To, err, c.want)
		}
	}
}

// A menu opened on the old rows while a search runs closes when its results
// replace them: its row index would then name another hit.
func TestPaletteMenuClosesOnNewResults(t *testing.T) {
	h, _ := invoices(t)
	h.typeText("invoice")
	h.see("邮件 · 4")
	p := &h.app.palette
	h.tt.Key(0, ui.KeyBackspace) // a new search starts; its answer waits in the queue
	h.tt.Frame()
	h.tt.Key(ui.Alt, ui.KeyEnter)
	h.tt.Frame()
	if !p.m.open {
		t.Fatal("the menu did not open on the old rows")
	}
	h.until("the new results", func() bool { return p.res.Mail.Total == 4 && !p.m.open })
}

// paletteHas is whether a text of the palette holds s.
func paletteHas(h *harness, s string) bool {
	texts := h.tt.Texts()
	i := slices.Index(texts, "对话") // the palette's tabs; the page switch has 联系人 too
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
	// The search again after the write closes a menu opened before it lands: wait for it.
	h.until("the hit read", func() bool {
		r := h.app.palette.res.Mail.Items
		return len(r) == 1 && slices.Contains(r[0].Flags, `\Seen`)
	})
	h.tt.Key(ui.Alt, ui.KeyEnter)
	h.see("标为未读")
	h.tt.Key(0, ui.KeyEscape)
	h.until("the menu closed over the palette", func() bool { return !h.tt.HasText("标为未读") && paletteShown(h) })

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
	h.paletteContacts()
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

// The menu names its row and shows each item's key; a key runs its item
// while the menu shows and types nothing into the box.
func TestPaletteMenuKeys(t *testing.T) {
	h, accs := invoices(t)
	h.typeText("Invoice 2")
	h.see("邮件 · 1")
	h.tt.Key(ui.Alt, ui.KeyEnter)
	h.see("全部回复")
	before := len(slices.DeleteFunc(h.tt.Texts(), func(s string) bool { return s != "Invoice 2" }))
	h.tt.Key(0, ui.KeyEscape)
	h.until("the menu closed", func() bool { return !h.tt.HasText("全部回复") })
	if after := len(slices.DeleteFunc(h.tt.Texts(), func(s string) bool { return s != "Invoice 2" })); before != after+1 {
		t.Fatalf("the menu does not name its row: %d then %d", before, after)
	}
	h.tt.Key(ui.Alt, ui.KeyEnter)
	h.see("全部回复")
	h.tt.TypeKey(0, ui.KeyE, "e")
	h.until("archived", func() bool { return h.countIn(h.folderOf(accs[0], "archive")) == 1 })
	if q := h.app.palette.query; q != "Invoice 2" {
		t.Fatalf("query %q", q)
	}
}

// A key that ran an item which closed the palette, released elsewhere, does
// not hold the box of the palette opened next.
func TestPaletteMenuKeyReleasedElsewhere(t *testing.T) {
	h, _ := invoices(t)
	h.typeText("Invoice 2")
	h.see("邮件 · 1")
	h.tt.Key(ui.Alt, ui.KeyEnter)
	h.see("全部回复")
	h.tt.KeyDown(ui.KeyR) // 回复: the palette closes for the compose window, R still down
	h.until("the palette closed", func() bool { return !h.app.palette.open })
	h.tt.Frame()
	h.tt.Key(ui.Cmd, ui.KeyK)
	h.until("the palette", func() bool { return paletteShown(h) })
	h.typeText("bob")
	if q := h.app.palette.query; q != "bob" {
		t.Fatalf("query %q: the box is held", q)
	}
}
