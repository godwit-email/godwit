package app

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
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

	h.click("全部归档 2 封")
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
	h.click("查看联系人")
	h.until("the contacts page", func() bool {
		return h.app.route.Page == PageContacts && h.app.route.Contact == "bob@example.com"
	})
	h.see("写邮件")
}
