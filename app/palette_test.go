package app

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/internal/theme"
)

// A contact row shows how much mail they sent (unselected: a selected row
// shows its keys instead).
func TestPaletteContactCount(t *testing.T) {
	h, _ := invoices(t)
	h.typeText("bob") // under 全部 a mail is first
	h.until("Bob's four mails", func() bool { return inPalette(h, "4 封邮件") })
}

// A kind's own tab lists past core's default page of 50.
func TestPaletteKindTabListsMore(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.putMany(accs[0], 60)
	h.waitMail("the threads", func() bool { return len(h.items()) >= 50 })
	h.tt.Key(ui.Cmd, ui.KeyK)
	h.until("the palette", func() bool { return paletteShown(h) })
	h.tt.Key(0, ui.KeyRight) // 邮件
	h.typeText("msg")
	h.until("the oldest of sixty", func() bool { return inPalette(h, "msg 059") })
}

// The query's hits are highlighted in the rows.
func TestPaletteHighlightsHits(t *testing.T) {
	h, _ := invoices(t)
	h.typeText("invoice")
	h.see("邮件 · 4")
	h.tt.Advance(time.Second) // past the palette's fade in
	h.tt.Frame()
	r, _ := h.tt.Find("邮件 · 4") // the heading over the mail rows: only the palette has it
	img, want := h.tt.Image(), theme.Light().Highlight.SRGB()
	for y := int(r.Y + r.H); y < int(r.Y+r.H)+36; y++ { // the first row
		for x := int(r.X); x < int(r.X)+640; x++ {
			if got := img.RGBAAt(x, y); got.R == want.R && got.G == want.G && got.B == want.B {
				return
			}
		}
	}
	t.Fatal("Invoice 1's hit is not highlighted")
}

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

// A contact's mail spans every account, as the palette's count does, even
// when the mail page was left on one account's folder.
func TestPaletteContactSearchesEveryAccount(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com", "carol@example.com")
	for i, acc := range accs {
		h.put(acc, "INBOX", coretest.Msg("Bob <bob@example.com>", acc.Email, fmt.Sprintf("Invoice %d", i), "pay"))
		h.syncNow(acc)
	}
	h.waitMail("two threads", func() bool { return len(h.items()) == 2 })
	f := h.folderOf(accs[0], "inbox")
	h.post(func() { h.app.navigate(Route{Page: PageMail, View: core.View{FolderID: f.ID}}) })
	h.waitMail("alice's inbox", func() bool { return len(h.items()) == 1 })
	h.tt.Key(ui.Cmd, ui.KeyK)
	h.until("the palette", func() bool { return paletteShown(h) })
	h.click("联系人")
	h.typeText("bob")
	h.see("联系人 · 1")
	h.tt.Key(0, ui.KeyEnter)
	h.until("Bob's mail from both accounts", func() bool {
		p := h.app.mail
		return !paletteShown(h) && p.searched && len(p.results) == 2
	})
}

// An account chip alone is no search for the list (core's Filtered leaves the
// account out), so the palette offers no view-all and ⌘↵ stays put.
func TestPaletteAccountOnlyNoViewAll(t *testing.T) {
	h, _ := invoices(t)
	h.tt.Key(0, ui.KeyRight) // 邮件: three hits would leave one out
	h.typeText("/")
	h.until("the filter list", func() bool { return filtersShown(h) })
	for range 3 {
		h.tt.Key(0, ui.KeyDown)
	}
	h.tt.Key(0, ui.KeyEnter)
	h.drain()
	h.see("移除「alice@example.com」")
	if h.tt.HasText("在邮件列表中查看全部 4 封") {
		t.Fatal("view-all offered for an account alone")
	}
	h.tt.Key(ui.Cmd, ui.KeyEnter)
	h.drain()
	h.tt.Frame()
	if !paletteShown(h) || h.app.mail.searchOpen {
		t.Fatalf("⌘↵ on an account alone: palette %v, list search %v", paletteShown(h), h.app.mail.searchOpen)
	}
}

// Reopening keeps counting searches, so an answer from before cannot pass
// as the newest one.
func TestPaletteReopenKeepsGeneration(t *testing.T) {
	h, _ := invoices(t)
	h.typeText("invoice")
	h.see("邮件 · 4")
	before := h.app.palette.gen
	h.post(func() { h.app.openPalette() })
	h.until("the reopened palette's first search counted past the old ones", func() bool {
		return h.app.palette.started && h.app.palette.gen > before
	})
}

// A contact's mail is from their address exactly: notbob@ is not bob@.
func TestPaletteContactExactAddress(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.put(accs[0], "INBOX", coretest.Msg("Bob <bob@example.com>", "alice@example.com", "From Bob", "x"))
	h.put(accs[0], "INBOX", coretest.Msg("notbob@example.com", "alice@example.com", "From notbob", "x"))
	h.syncNow(accs[0])
	h.waitMail("two threads", func() bool { return len(h.items()) == 2 })
	h.tt.Key(ui.Cmd, ui.KeyK)
	h.until("the palette", func() bool { return paletteShown(h) })
	h.click("联系人")
	h.typeText("bob@")
	h.see("联系人 · 2")
	h.post(func() { h.app.mail.mailFrom("bob@example.com") })
	h.until("Bob's mail alone", func() bool {
		p := h.app.mail
		return p.searched && len(p.results) == 1 && p.results[0].Subject == "From Bob"
	})
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
