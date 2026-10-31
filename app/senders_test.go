package app

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
)

// The senders view counts a sender's mail; 全部归档 moves it out of the
// inbox and its toast's 撤销 puts it back; 屏蔽发件人 blocks the address;
// 退订 goes through the reader's own way (a mailto drafts).
func TestSendersView(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	acc := accs[0]
	ctx := context.Background()
	unsub := coretest.Header("List-Unsubscribe: <mailto:leave@shop.example?subject=unsubscribe>")
	first := h.deliverOne(acc, coretest.Msg("Shop <news@shop.example>", acc.Email, "Deals", "Hello.", unsub))
	h.deliverOne(acc, coretest.Msg("Shop <news@shop.example>", acc.Email, "More deals", "Hello.", unsub))
	folderOf := func(id int64) int64 {
		m, err := h.env.Svc.GetMessage(ctx, id, false)
		if err != nil {
			t.Fatal(err)
		}
		return m.FolderID
	}
	inbox, archive := h.folderOf(acc, "inbox").ID, h.folderOf(acc, "archive").ID

	h.click("发件人与订阅")
	h.see("news@shop.example")
	h.see("2 封，2 封未读")

	h.click("全部归档")
	h.see("已归档 2 封")
	h.until("archived", func() bool { return folderOf(first.ID) == archive })
	h.click("撤销")
	h.until("back in the inbox", func() bool { return folderOf(first.ID) == inbox })

	h.click("屏蔽发件人")
	h.see("发件人已屏蔽")
	if !slices.Contains(h.app.blocked, "news@shop.example") {
		t.Fatalf("blocked %q", h.app.blocked)
	}

	h.click("退订")
	d := h.draft(h.composeOpened(0))
	if len(d.To) != 1 || d.To[0] != "leave@shop.example" || d.Subject != "unsubscribe" {
		t.Fatalf("draft %+v", d)
	}
}

// 退订 again after its message failed to load loads it again, then unsubscribes.
func TestSendersUnsubscribeRetriesFailedLoad(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	acc := accs[0]
	m := h.deliverOne(acc, coretest.Msg("Shop <news@shop.example>", acc.Email, "Deals", "Hello.",
		coretest.Header("List-Unsubscribe: <mailto:leave@shop.example>")))
	r := h.app.mail.reader
	real, fail := r.getMessage, true
	r.getMessage = func(ctx context.Context, id int64, remote bool) (core.Message, error) {
		if fail {
			return core.Message{}, errors.New("offline")
		}
		return real(ctx, id, remote)
	}
	h.click("发件人与订阅")
	h.click("退订")
	h.until("the failed load", func() bool { b := r.bodies[m.ID]; return b != nil && b.err != nil && !b.loading })
	fail = false
	h.click("退订")
	if d := h.draft(h.composeOpened(0)); len(d.To) != 1 || d.To[0] != "leave@shop.example" {
		t.Fatalf("draft %+v", d)
	}
}
