package app

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
)

// The contacts page lists who the user exchanged mail with and chooses the
// one with the most; 退订 confirms first, 全部归档 says how much it moves
// and its toast's 撤销 puts it back, 屏蔽发件人 blocks; 订阅 narrows the
// list; a person's 写邮件 drafts to them.
func TestContactsPage(t *testing.T) {
	h, link, posts := unsubHarness(t, http.StatusOK)
	acc := h.app.accounts[0]
	ctx := context.Background()
	oneClick := []coretest.MsgOption{coretest.Header("List-Unsubscribe: <" + link + ">"), coretest.Header("List-Unsubscribe-Post: List-Unsubscribe=One-Click")}
	first := h.deliverOne(acc, coretest.Msg("Shop <news@shop.example>", acc.Email, "Deals", "Hello.", oneClick...))
	h.deliverOne(acc, coretest.Msg("Shop <news@shop.example>", acc.Email, "More deals", "Hello.", oneClick...))
	h.deliverOne(acc, coretest.Msg("Bob <bob@example.com>", acc.Email, "Lunch?", "Noon?"))
	folderOf := func(id int64) int64 {
		m, err := h.env.Svc.GetMessage(ctx, id, false)
		if err != nil {
			t.Fatal(err)
		}
		return m.FolderID
	}
	inbox, archive := h.folderOf(acc, "inbox").ID, h.folderOf(acc, "archive").ID

	h.click("联系人")
	h.see("news@shop.example")
	h.see("bob@example.com")
	h.until("the one with the most mail chosen", func() bool { return h.app.route.Contact == "news@shop.example" })

	h.click("退订")
	h.see("127.0.0.1")
	if posts.Load() != 0 {
		t.Fatal("posted before confirming")
	}
	h.tt.Key(0, ui.KeyEnter) // the dialog's 退订
	h.see("已向 127.0.0.1 发送退订请求")
	if n := posts.Load(); n != 1 {
		t.Fatalf("%d posts", n)
	}

	h.click("全部归档 2 封 · 之后可以撤销")
	h.see("已归档 2 封")
	h.until("archived", func() bool { return folderOf(first.ID) == archive })
	h.click("撤销")
	h.until("back in the inbox", func() bool { return folderOf(first.ID) == inbox })

	h.click("订阅")
	h.until("only the list", func() bool { return h.tt.HasText("news@shop.example") && !h.tt.HasText("bob@example.com") })
	h.click("屏蔽发件人")
	h.see("发件人已屏蔽")
	if !slices.Contains(h.app.blocked, "news@shop.example") {
		t.Fatalf("blocked %q", h.app.blocked)
	}

	h.click("全部联系人")
	h.app.contacts().choose("bob@example.com")
	h.click("写邮件")
	if d := h.draft(h.composeOpened(0)); len(d.To) != 1 || d.To[0] != "bob@example.com" {
		t.Fatalf("draft %+v", d)
	}
}

// The reader's sender menu opens that sender on the contacts page.
func TestReaderViewContact(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	acc := accs[0]
	m := h.deliverOne(acc, coretest.Msg("Bob <Bob@Example.com>", acc.Email, "Lunch?", "Noon?"))
	h.openThread(m.ThreadID)
	h.seeBody(m.ID)
	h.click("Bob@Example.com")
	if err := h.tt.ChooseMenuItem("查看联系人"); err != nil {
		t.Fatalf("%v; menu %q", err, h.tt.Menu())
	}
	h.drain()
	h.until("the contacts page", func() bool {
		return h.app.route.Page == PageContacts && h.app.route.Contact == "bob@example.com"
	})
	h.see("写邮件")
}

// Until a contact's detail is read, the one shown before does not stand in
// for it: none of its mail, nor its count on 全部归档.
func TestContactsDetailIsTheChosenOnes(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	acc := accs[0]
	h.deliverOne(acc, coretest.Msg("Shop <news@shop.example>", acc.Email, "Deals", "Hello."))
	h.deliverOne(acc, coretest.Msg("Shop <news@shop.example>", acc.Email, "More deals", "Hello."))
	h.deliverOne(acc, coretest.Msg("Bob <bob@example.com>", acc.Email, "Lunch?", "Noon?"))
	h.click("联系人")
	h.see("全部归档 2 封")
	h.see("More deals")

	h.post(func() { h.app.route.Contact = "bob@example.com" }) // chosen, its detail not read yet
	h.drain()
	h.tt.Frame()
	if h.tt.HasText("More deals") || h.tt.HasText("全部归档 2 封") {
		t.Fatalf("Shop's detail under Bob: %q", h.tt.Texts())
	}
	h.post(func() { h.app.contacts().load() })
	h.see("Lunch?")
	h.see("全部归档 1 封")
}

// The detail holds all the mail between the contact and the user, read a
// page at a time as it scrolls to its end.
func TestContactsMailReadsOn(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	acc := accs[0]
	n := contactMailPage + 5
	for i := range n {
		h.put(acc, "INBOX", coretest.Msg("Shop <news@shop.example>", acc.Email, fmt.Sprintf("Deal %02d", i), "Hello.", coretest.MessageID(fmt.Sprintf("d%d@x", i))))
	}
	h.syncNow(acc)
	h.click("联系人")
	h.see(fmt.Sprintf("往来邮件 · %d 封", n))
	p := h.app.contacts()
	h.until("the first page", func() bool { return len(p.mail) == contactMailPage && p.cursor != "" })
	h.until("the rest, read on at the end", func() bool {
		h.tt.Scroll(1100, 400, 0, 100000)
		h.drain()
		h.tt.Frame()
		return len(p.mail) == n && p.cursor == ""
	})
}

// Contacts show their avatars as the reader does, under the remote-image
// setting: none is fetched while remote images are asked for, the initial
// shows instead.
func TestContactsAvatarPolicy(t *testing.T) {
	for _, tc := range []struct {
		mode    string
		fetched bool
	}{{"ask", false}, {"auto", true}} {
		t.Run(tc.mode, func(t *testing.T) {
			var hits atomic.Int64
			h, acc := avatarHarness(t, tc.mode, func(w http.ResponseWriter, req *http.Request) { hits.Add(1); w.Write(readerAvatarSVG) })
			h.deliverOne(acc, coretest.Msg("Bob <bob@example.com>", acc.Email, "Avatar", "plain body"))
			h.click("联系人")
			h.see("bob@example.com")
			initial := func() bool { return slices.Contains(h.tt.Texts(), "B") }
			if tc.fetched {
				h.until("the avatar", func() bool { h.drain(); h.tt.Frame(); return hits.Load() > 0 && !initial() })
				return
			}
			h.until("the initial", initial)
			for range 5 {
				h.tt.Frame()
				h.drain()
			}
			if hits.Load() != 0 {
				t.Fatalf("fetched %d avatars", hits.Load())
			}
		})
	}
}

// An avatar read before the contact's first received mail (none then) does
// not stick: that mail is the newest, and its sender avatar loads.
func TestContactsAvatarFollowsNewestMessage(t *testing.T) {
	var hits atomic.Int64
	h, acc := avatarHarness(t, "auto", func(w http.ResponseWriter, req *http.Request) { hits.Add(1); w.Write(readerAvatarSVG) })
	h.post(func() { h.app.contacts().avatar(core.Contact{Address: "bob@example.com"}) })
	for range 5 {
		h.tt.Frame()
		h.drain()
	}
	h.deliverOne(acc, coretest.Msg("Bob <bob@example.com>", acc.Email, "Avatar", "plain body"))
	h.click("联系人")
	h.see("bob@example.com")
	h.until("the avatar", func() bool {
		h.drain()
		h.tt.Frame()
		return hits.Load() > 0 && !slices.Contains(h.tt.Texts(), "B")
	})
}

func TestContactsSidebarRefreshAndZeroMail(t *testing.T) {
	h, accs := mailHarness(t, []coretest.Option{coretest.WithoutAutoSync()}, "alice@example.com")
	acc := accs[0]
	h.deliverOne(acc, coretest.Msg("Bob <bob@example.com>", acc.Email, "Lunch?", "Noon?"))

	srv := h.env.StartCardDAV(t)
	photoB64 := base64.StdEncoding.EncodeToString(readerAvatarSVG)
	srv.PutCard("contacts", "zero", "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Zero Person\r\nEMAIL:zero@example.com\r\nPHOTO:data:image/svg+xml;base64,"+photoB64+"\r\nEND:VCARD\r\n")
	if _, err := h.env.Svc.AddAddressBook(context.Background(), core.AddressBookInput{
		Preset: core.PresetCustom, Name: "通讯录一", URL: srv.URL(), Username: "alice", Password: "secret",
	}); err != nil {
		t.Fatal(err)
	}

	h.click("联系人")
	h.see("全部联系人")
	h.see("往来过")
	h.see("通讯录")
	h.see("订阅")
	h.see("已屏蔽")
	h.see("bob@example.com")
	h.see("zero@example.com")

	// "往来过" includes received-only Bob (Messages > 0) and excludes zero-mail contacts.
	h.click("往来过")
	h.until("only contacts with mail", func() bool {
		return h.tt.HasText("bob@example.com") && !h.tt.HasText("zero@example.com")
	})

	// "通讯录" shows enabled address-book contacts and excludes mail-only Bob.
	h.click("通讯录")
	h.see("通讯录 · 1")
	h.click("zero@example.com")
	h.until("Zero Person chosen and Bob gone from list and detail", func() bool {
		return h.app.contacts().detailOf == "zero@example.com" && !h.app.contacts().loading && !h.tt.HasText("bob@example.com")
	})
	// Zero-mail contact omits row timestamp (never renders 1970/0001), shows "—" in last stat,
	// disables 全部归档, and loads its CardDAV PHOTO even with Latest == 0.
	h.see("—")
	h.see("0 封邮件")
	for _, s := range h.tt.Texts() {
		if strings.Contains(s, "1970") || strings.Contains(s, "0001") {
			t.Fatalf("unexpected zero timestamp %q in %q", s, h.tt.Texts())
		}
	}
	h.until("zero-mail CardDAV avatar loaded", func() bool {
		h.drain()
		h.tt.Frame()
		return !slices.Contains(h.tt.Texts(), "Z")
	})

	// Manual refresh button pulls newly added remote card, and updates removed avatar.
	srv.PutCard("contacts", "zero", "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Zero Person\r\nEMAIL:zero@example.com\r\nEND:VCARD\r\n")
	srv.PutCard("contacts", "carol", "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Carol New\r\nEMAIL:carol@example.com\r\nEND:VCARD\r\n")
	h.click("同步通讯录")
	h.see("Carol New")
	h.until("removed CardDAV avatar invalidated back to initial", func() bool {
		h.drain()
		h.tt.Frame()
		return slices.Contains(h.tt.Texts(), "Z")
	})

	// ⌘R shortcut also triggers manual refresh.
	srv.PutCard("contacts", "dan", "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Dan Shortcut\r\nEMAIL:dan@example.com\r\nEND:VCARD\r\n")
	h.tt.Key(ui.Cmd, ui.KeyR)
	h.see("Dan Shortcut")
}

func TestContactsMultiAddressAndActions(t *testing.T) {
	h, accs := mailHarness(t, []coretest.Option{coretest.WithoutAutoSync()}, "alice@example.com")
	acc := accs[0]

	srv := h.env.StartCardDAV(t)
	srv.PutCard("contacts", "bob", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Bob Chen\r\nORG:Acme Corp\r\nTITLE:CTO\r\nEMAIL;TYPE=PREF:bob@home.com\r\nEMAIL:bob@work.com\r\nEND:VCARD\r\n")
	if _, err := h.env.Svc.AddAddressBook(context.Background(), core.AddressBookInput{
		Preset: core.PresetCustom, Name: "公司通讯录", URL: srv.URL(), Username: "alice", Password: "secret",
	}); err != nil {
		t.Fatal(err)
	}

	m := h.deliverOne(acc, coretest.Msg("bob@work.com", acc.Email, "Secondary Mail", "From secondary"))
	h.deliverOne(acc, coretest.Msg("Other <other@example.com>", acc.Email, "Other Mail", "Hi"))

	// Opening "查看联系人" from a secondary address opens and normalizes to the merged contact.
	h.openThread(m.ThreadID)
	h.seeBody(m.ID)
	h.click("bob@work.com")
	if err := h.tt.ChooseMenuItem("查看联系人"); err != nil {
		t.Fatalf("%v; menu %q", err, h.tt.Menu())
	}
	h.drain()
	h.until("merged contact opened", func() bool {
		return h.app.route.Page == PageContacts && h.app.route.Contact == "bob@home.com" && h.app.contacts().detailOf == "bob@home.com"
	})
	h.see("Bob Chen")
	h.see("Acme Corp · CTO")
	// The head shows the preferred address and +1, the other one hidden till it opens.
	h.see("bob@home.com")
	h.see("+1")
	if h.tt.HasText("bob@work.com") {
		t.Fatal("the second address shows before +1 opens")
	}
	h.see("Secondary Mail")

	// Top 全部归档 covers mail received from secondary addresses too.
	inbox, archive := h.folderOf(acc, "inbox").ID, h.folderOf(acc, "archive").ID
	folderOf := func(id int64) int64 {
		got, err := h.env.Svc.GetMessage(context.Background(), id, false)
		if err != nil {
			t.Fatal(err)
		}
		return got.FolderID
	}
	h.click("全部归档 1 封 · 之后可以撤销")
	h.see("已归档 1 封")
	h.until("secondary mail archived", func() bool { return folderOf(m.ID) == archive })
	h.click("撤销")
	h.until("secondary mail restored to inbox", func() bool { return folderOf(m.ID) == inbox })

	// Filter matches subtitle (ORG / TITLE) and secondary email address.
	h.fill("按名字或地址筛选", "CTO")
	h.see("全部联系人 · 1")
	if h.tt.HasText("other@example.com") {
		t.Fatal("other@example.com matched CTO filter")
	}
	h.fill("按名字或地址筛选", "bob@work.com")
	h.see("全部联系人 · 1")
	h.fill("按名字或地址筛选", "")
	h.see("全部联系人 · 2")

	// Top 写邮件 opens popup menu with PREF address first.
	h.click("写邮件")
	menu := h.tt.Menu()
	if !slices.Equal(menu, []string{"bob@home.com", "bob@work.com"}) {
		t.Fatalf("compose menu %q", menu)
	}
	if err := h.tt.ChooseMenuItem("bob@work.com"); err != nil {
		t.Fatal(err)
	}
	h.drain()
	if d := h.draft(h.composeOpened(0)); !slices.Equal(d.To, []string{"bob@work.com"}) {
		t.Fatalf("draft 0 To %q", d.To)
	}

	// The preferred address's own menu writes to it.
	h.click("bob@home.com 的操作")
	if err := h.tt.ChooseMenuItem("写邮件"); err != nil {
		t.Fatalf("%v; menu %q", err, h.tt.Menu())
	}
	h.drain()
	if d := h.draft(h.composeOpened(1)); !slices.Equal(d.To, []string{"bob@home.com"}) {
		t.Fatalf("draft 1 To %q", d.To)
	}

	// +1 lists both, the preferred marked; a row's actions show on hover.
	h.click("另外 1 个地址")
	h.see("首选")
	h.click("bob@work.com")
	if err := h.tt.ChooseMenuItem("复制邮箱地址"); err != nil {
		t.Fatalf("%v; menu %q", err, h.tt.Menu())
	}
	h.drain()
	if got := h.tt.Clipboard(); got != "bob@work.com" {
		t.Fatalf("clipboard %q", got)
	}
	h.see("已复制邮箱地址")

	// Per-address block blocks only that address; top whole-card button still offers 屏蔽发件人.
	if !h.app.contacts().addrsOpen {
		h.click("另外 1 个地址")
	}
	h.click("bob@work.com")
	if err := h.tt.ChooseMenuItem("屏蔽发件人"); err != nil {
		t.Fatalf("%v; menu %q", err, h.tt.Menu())
	}
	h.drain()
	h.until("secondary blocked only", func() bool {
		return slices.Contains(h.app.blocked, "bob@work.com") && !slices.Contains(h.app.blocked, "bob@home.com")
	})
	h.see("已屏蔽 · 1") // the list's own group of blocked addresses
	h.tt.Key(0, ui.KeyEscape)
	h.until("the list closed", func() bool { return !h.app.contacts().addrsOpen })
	h.click("已屏蔽")
	h.see("已屏蔽 · 1")

	// Whole-card block blocks every remaining address; whole-card unblock clears all of them.
	h.click("屏蔽发件人")
	h.until("all addresses blocked", func() bool {
		return slices.Contains(h.app.blocked, "bob@home.com") && slices.Contains(h.app.blocked, "bob@work.com")
	})
	h.click("取消屏蔽发件人")
	h.until("all addresses unblocked", func() bool {
		return !slices.Contains(h.app.blocked, "bob@home.com") && !slices.Contains(h.app.blocked, "bob@work.com")
	})
	h.see("已屏蔽 · 0")
}

func TestContactsFilterHistoricalAndSecondaryMergedTerms(t *testing.T) {
	h, accs := mailHarness(t, []coretest.Option{coretest.WithoutAutoSync()}, "alice@example.com")
	acc := accs[0]

	srv1 := h.env.StartCardDAV(t)
	srv1.PutCard("contacts", "bob-work", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Bob Work\r\nORG:Acme Corp\r\nTITLE:CTO\r\nEMAIL;TYPE=PREF:bob.work@example.com\r\nEMAIL:bob.shared@example.com\r\nEND:VCARD\r\n")
	if _, err := h.env.Svc.AddAddressBook(context.Background(), core.AddressBookInput{
		Preset: core.PresetCustom, Name: "主通讯录", URL: srv1.URL(), Username: "alice", Password: "secret",
	}); err != nil {
		t.Fatal(err)
	}

	srv2 := h.env.StartCardDAV(t)
	srv2.PutCard("contacts", "bob-side", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Bobby Studio\r\nORG:Night Owl Labs\r\nTITLE:Founder\r\nEMAIL:bob.shared@example.com\r\nEMAIL:bob.home@example.com\r\nEND:VCARD\r\n")
	if _, err := h.env.Svc.AddAddressBook(context.Background(), core.AddressBookInput{
		Preset: core.PresetCustom, Name: "副通讯录", URL: srv2.URL(), Username: "alice", Password: "secret",
	}); err != nil {
		t.Fatal(err)
	}

	h.deliverOne(acc, coretest.Msg("Robert Legacy <bob.home@example.com>", acc.Email, "Old Thread", "Hi"))
	h.deliverOne(acc, coretest.Msg("Other <other@example.com>", acc.Email, "Other", "Hello"))

	h.click("联系人")
	h.see("全部联系人 · 2")

	for _, query := range []string{"Robert Legacy", "Bobby Studio", "Night Owl Labs", "Founder"} {
		h.fill("按名字或地址筛选", query)
		h.see("全部联系人 · 1")
		h.see("Bob Work")
		if h.tt.HasText("other@example.com") {
			t.Fatalf("unexpected match for query %q", query)
		}
	}
}
