package app

import (
	"fmt"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
)

// paletteShown is whether the palette's kind tabs show.
func paletteShown(h *harness) bool { return h.tt.HasText("联系人") && h.tt.HasText("附件") }

// inPalette is whether the palette, built over the page, shows text s.
func inPalette(h *harness, s string) bool {
	texts := h.tt.Texts()
	i := slices.Index(texts, "联系人")
	return i >= 0 && slices.Contains(texts[i:], s)
}

// ⌘K on any page and ⌘F open the palette; Escape closes it on the page it
// was opened over.
func TestPaletteOpensAndCloses(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.put(accs[0], "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Invoice March", "pay"))
	h.syncNow(accs[0])
	h.waitMail("a thread", func() bool { return len(h.items()) == 1 })

	before := h.app.route
	h.tt.Key(ui.Cmd, ui.KeyK)
	h.until("the palette", func() bool { return paletteShown(h) })
	h.tt.Key(0, ui.KeyEscape)
	h.until("the palette gone", func() bool { return !paletteShown(h) })
	if h.app.route != before || h.app.mail.searchOpen {
		t.Fatalf("route %+v, list search %v", h.app.route, h.app.mail.searchOpen)
	}

	h.tt.Key(ui.Cmd, ui.KeyF)
	h.until("the palette from ⌘F", func() bool { return paletteShown(h) })
	if h.app.mail.searchOpen {
		t.Fatal("⌘F opened the list's search")
	}
	h.click("关闭")
	h.until("the palette closed", func() bool { return !paletteShown(h) })

	h.app.navigate(Route{Page: PageSettings})
	h.tt.Frame()
	h.tt.Key(ui.Cmd, ui.KeyK)
	h.until("the palette over settings", func() bool { return paletteShown(h) })
	h.tt.Key(0, ui.KeyEscape)
	h.until("closed", func() bool { return !paletteShown(h) })
	if h.app.route.Page != PageSettings {
		t.Fatalf("route %+v", h.app.route)
	}
}

// invoices puts four mails "Invoice 1" (newest) to "Invoice 4" from Bob,
// the third with invoice-march.pdf, and opens the palette on them.
func invoices(t *testing.T) (*harness, []core.Account) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	base := h.clock.Now()
	for i := 1; i <= 4; i++ {
		opts := []coretest.MsgOption{at(base, i)}
		if i == 3 {
			opts = append(opts, coretest.Attachment("invoice-march.pdf", "application/pdf", []byte("%PDF")))
		}
		h.put(accs[0], "INBOX", coretest.Msg("Bob <bob@example.com>", "alice@example.com", fmt.Sprintf("Invoice %d", i), "pay", opts...))
	}
	h.syncNow(accs[0])
	h.waitMail("four threads", func() bool { return len(h.items()) == 4 })
	h.tt.Key(ui.Cmd, ui.KeyK)
	h.until("the palette", func() bool { return paletteShown(h) })
	return h, accs
}

// routeTo waits for the palette to close on a mail page showing subject.
func routeTo(h *harness, subject string) {
	h.t.Helper()
	ms, err := h.env.Svc.SearchWith(h.t.Context(), core.SearchQuery{Text: subject}, 1)
	if err != nil || len(ms) != 1 {
		h.t.Fatalf("%q: %v %v", subject, ms, err)
	}
	h.until("the reader on "+subject, func() bool {
		r := h.app.route
		return !paletteShown(h) && r.Page == PageMail && r.MessageID == ms[0].ID && r.ThreadID == ms[0].ThreadID
	})
}

// With nothing typed 全部 has the recent hits and the commands; typing
// groups the hits by kind, a few each with how many there are.
func TestPaletteGroups(t *testing.T) {
	h, _ := invoices(t)
	h.see("最近")
	h.see("操作")
	h.see("写邮件")
	h.see("invoice-march.pdf")

	h.typeText("invoice")
	h.see("邮件 · 4")
	h.see("附件 · 1")
	h.until("three of the four mails", func() bool {
		return inPalette(h, "Invoice 3") && !inPalette(h, "Invoice 4") && !inPalette(h, "写邮件")
	})

	h.typeText(" zzz")
	h.see("没有找到「invoice zzz」")
	h.see("只搜索了已同步到本机的邮件，按 Tab 让 Wit 帮你找")

	for range len(" zzz") {
		h.tt.Key(0, ui.KeyBackspace)
	}
	h.typeText("")
	h.see("邮件 · 4")
	h.tt.Key(ui.Cmd, ui.KeyA)
	h.typeText("设置") // a command
	h.see("打开设置")
}

// ↑↓ go through every group and wrap, hovering a row chooses it too; ↵
// opens the row chosen. Rows: Invoice 1, 2, 3, 查看全部, then the attachment.
func TestPaletteKeysOpen(t *testing.T) {
	h, _ := invoices(t)
	again := func() {
		h.tt.Move(1, 1) // off the rows, which it would choose
		h.tt.Key(ui.Cmd, ui.KeyK)
		h.until("the palette", func() bool { return paletteShown(h) })
		h.typeText("invoice")
		h.see("附件 · 1")
	}
	h.typeText("invoice")
	h.see("附件 · 1")
	r, _ := h.tt.Find("Bob · Invoice 3") // the attachment's line: only the palette has it
	h.tt.Move(r.X+r.W/2, r.Y+r.H/2)
	h.tt.Frame()
	h.tt.Key(0, ui.KeyEnter)
	routeTo(h, "Invoice 3")

	again()
	h.tt.Key(0, ui.KeyUp) // wraps to the attachment, then up past 查看全部 to Invoice 2
	h.tt.Key(0, ui.KeyUp)
	h.tt.Key(0, ui.KeyUp)
	h.tt.Key(0, ui.KeyUp)
	h.tt.Key(0, ui.KeyEnter)
	routeTo(h, "Invoice 2")

	again()
	for range 4 { // from the mail into the attachments
		h.tt.Key(0, ui.KeyDown)
	}
	h.tt.Key(0, ui.KeyEnter)
	routeTo(h, "Invoice 3")
}

// ←→ change the kind only while the box is empty; ⌃Tab and ⌃⇧Tab always.
func TestPaletteKinds(t *testing.T) {
	h, _ := invoices(t)
	h.see("写邮件")
	h.tt.Key(0, ui.KeyRight) // 邮件
	h.until("the mail tab: no commands", func() bool { return !inPalette(h, "写邮件") && inPalette(h, "Invoice 4") })

	h.typeText("inv")
	h.see("邮件 · 4")
	h.tt.Key(0, ui.KeyRight) // moves the caret
	h.tt.Key(ui.Ctrl, ui.KeyTab)
	h.see("没有找到「inv」") // 对话
	h.tt.Key(ui.Ctrl|ui.Shift, ui.KeyTab)
	h.see("邮件 · 4")
	h.tt.Key(ui.Ctrl|ui.Shift, ui.KeyTab)
	h.see("附件 · 1") // 全部
	h.see("邮件 · 4")

	h.click("对话") // a click on a tab
	h.see("没有找到「inv」")
}

// A conversation opens on the Wit page, a contact as their mail, a command
// runs.
func TestPaletteOpensEachKind(t *testing.T) {
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
	h.tt.Key(0, ui.KeyEnter)
	h.until("the conversation", func() bool {
		return h.app.route.Page == PageWit && h.app.route.ConversationID == cv.ID && !paletteShown(h)
	})

	h.tt.Key(ui.Cmd, ui.KeyK)
	h.until("the palette", func() bool { return paletteShown(h) })
	h.typeText("打开设置")
	h.see("操作 · 1")
	h.tt.Key(0, ui.KeyEnter)
	h.until("settings", func() bool { return h.app.route.Page == PageSettings && !paletteShown(h) })
}

func TestPaletteContactShowsTheirMail(t *testing.T) {
	h, _ := invoices(t)
	h.click("联系人")
	h.typeText("bob")
	h.see("联系人 · 1")
	h.tt.Key(0, ui.KeyEnter)
	h.until("Bob's mail in the list", func() bool {
		p := h.app.mail
		return !paletteShown(h) && p.searched && p.sf.from == "bob@example.com" && len(p.results) == 4
	})
}
