package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

func TestComposeInWindowOpensWindow(t *testing.T) {
	h, accs := mailHarness(t, nil, "me@example.com")
	h.app.ComposeInWindow()
	var call string
	h.until("OpenCompose", func() bool {
		for _, c := range h.plat.Calls() {
			if strings.HasPrefix(c, "OpenCompose ") {
				call = c
				return true
			}
		}
		return false
	})
	id, err := strconv.ParseInt(strings.TrimPrefix(call, "OpenCompose "), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if d := h.draft(id); d.AccountID != accs[0].ID {
		t.Fatalf("draft %+v", d)
	}
	if h.app.route.Page == PageCompose {
		t.Fatal("the old compose page opened too")
	}
}

func TestSettingsChangedReachesPlatform(t *testing.T) {
	h, _ := withAccount(t)
	h.openSettings("general")
	h.click("深色")
	h.called("SettingsChanged dark")
}

// A draft deleted, sent or discarded here closes its compose window: the
// platform hears DraftDeleted once the draft is gone.
func TestDeleteDraftNotifiesPlatform(t *testing.T) {
	gone := func(h *harness, id int64) {
		t.Helper()
		h.called(fmt.Sprint("DraftDeleted ", id))
		if _, err := h.env.Svc.GetDraft(context.Background(), id); err == nil {
			t.Fatalf("DraftDeleted %d told while the draft is still there", id)
		}
	}
	t.Run("drafts list", func(t *testing.T) {
		h, accs := mailHarness(t, nil, "alice@example.com")
		if err := h.env.IMAP(accs[0].ID).User.Create("Drafts", nil); err != nil {
			t.Fatal(err)
		}
		h.syncNow(accs[0])
		d, err := h.env.Svc.NewDraft(context.Background(), accs[0].ID, "new", 0)
		if err != nil {
			t.Fatal(err)
		}
		d.Subject = "Draft bye"
		if _, err := h.env.Svc.SaveDraft(context.Background(), d); err != nil {
			t.Fatal(err)
		}
		h.waitMail("the drafts folder", func() bool { return h.tt.HasText("草稿") })
		h.click("草稿")
		h.see("Draft bye")
		if err := h.tt.RightClick("Draft bye"); err != nil {
			t.Fatal(err)
		}
		if err := h.tt.ChooseMenuItem("删除草稿"); err != nil {
			t.Fatal(err)
		}
		h.tt.Frame()
		gone(h, d.ID)
	})
	t.Run("native discard", func(t *testing.T) {
		h, _ := mailHarness(t, nil, "me@example.com")
		p := h.openCompose()
		h.click("丢弃草稿")
		h.click("丢弃")
		gone(h, p.id)
	})
	t.Run("native send", func(t *testing.T) {
		h, accs := mailHarness(t, nil, "me@example.com")
		p := h.openCompose()
		h.addAddress("添加收件人", "lena@example.com")
		h.writeBody("hi")
		h.click("发送")
		h.sent(accs[0], 1)
		gone(h, p.id)
	})
}
