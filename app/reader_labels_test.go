package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/store"
)

// labelState is a message_labels row as (source, active), or ("", -1) for none.
func (h *harness) labelState(msg int64, slug string) (string, int) {
	h.t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(h.env.DataDir, "godwit.db"))
	if err != nil {
		h.t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(context.Background(), "SELECT source, active FROM message_labels WHERE message_id = ? AND label_id = ?", msg, h.labelID(slug))
	if err != nil {
		h.t.Fatal(err)
	}
	if len(rows.Values) == 0 {
		return "", -1
	}
	return store.Str(rows.Values[0][0]), int(store.Int(rows.Values[0][1]))
}

// labeledThread is a two-message thread; the newer message has otp from the model.
func labeledThread(t *testing.T) (*harness, int64, []core.MessageSummary) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	acc, base := accs[0], h.clock.Now()
	h.put(acc, "INBOX", coretest.Msg("bob@example.com", acc.Email, "Plan", "one", coretest.MessageID("a@x"), at(base, 2)))
	h.put(acc, "INBOX", coretest.Msg("bob@example.com", acc.Email, "Re: Plan", "two", coretest.MessageID("b@x"), coretest.InReplyTo("a@x"), at(base, 1)))
	h.syncNow(acc)
	page, err := h.env.Svc.ListThreads(context.Background(), core.View{Unified: "inbox"}, "", 10)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("threads %v %v", page.Items, err)
	}
	thread := page.Items[0].ID
	msgs := h.messages(thread)
	h.autoLabel(msgs[1].ID, "otp")
	return h, thread, msgs
}

func (h *harness) wantOff(msgs []core.MessageSummary, slug string) {
	h.t.Helper()
	for _, m := range msgs {
		h.until("the label off by hand on every message", func() bool {
			src, on := h.labelState(m.ID, slug)
			return src == "manual" && on == 0
		})
	}
}

func TestReaderLabelRow(t *testing.T) {
	h, thread, _ := labeledThread(t)
	h.openThread(thread)
	h.until("the chip and the add button", func() bool {
		h.tt.Frame()
		return h.tt.HasText("去掉标签「验证码」") && h.tt.HasText("添加标签")
	})
}

func TestReaderLabelRemove(t *testing.T) {
	h, thread, msgs := labeledThread(t)
	h.openThread(thread)
	h.until("the chip", func() bool { h.tt.Frame(); return h.tt.HasText("去掉标签「验证码」") })
	h.click("去掉标签「验证码」")
	h.wantOff(msgs, "otp")
	h.until("the chip gone", func() bool { h.tt.Frame(); return !h.tt.HasText("去掉标签「验证码」") })
}

func TestReaderLabelFromSearch(t *testing.T) {
	h, thread, msgs := labeledThread(t)
	h.app.navigate(Route{Page: PageMail, View: core.View{Unified: "inbox"}, ThreadID: thread, MessageID: msgs[0].ID})
	h.until("the thread's chip on its other message", func() bool { h.tt.Frame(); return h.tt.HasText("去掉标签「验证码」") })
	h.click("去掉标签「验证码」")
	h.wantOff(msgs, "otp")
}

func TestReaderChipOpensMenu(t *testing.T) {
	h, thread, _ := labeledThread(t)
	h.openThread(thread)
	h.until("the chip", func() bool { h.tt.Frame(); return h.tt.HasText("去掉标签「验证码」") })
	r, _ := h.tt.Find("去掉标签「验证码」")
	h.tt.ClickAt(r.X-20, r.Y+r.H/2) // on the chip's name, left of its ×
	h.drain()
	h.tt.Frame()
	h.see("管理标签…")
}

func TestReaderLabelMenu(t *testing.T) {
	h, thread, msgs := labeledThread(t)
	h.openThread(thread)
	h.until("the add button", func() bool { h.tt.Frame(); return h.tt.HasText("添加标签") })
	h.click("添加标签")
	for _, s := range []string{"订单与物流", "订阅与资讯", "模型标记", "管理标签…"} {
		h.see(s)
	}
	h.click("账单")
	for _, m := range msgs {
		h.until("bill on by hand", func() bool { src, on := h.labelState(m.ID, "bill"); return src == "manual" && on == 1 })
	}
	h.click("管理标签…")
	h.until("the labels settings", func() bool { return h.app.route == Route{Page: PageSettings, Settings: "labels"} })
}

func TestReaderLabelCreate(t *testing.T) {
	h, thread, msgs := labeledThread(t)
	h.openThread(thread)
	h.until("the add button", func() bool { h.tt.Frame(); return h.tt.HasText("添加标签") })
	h.click("添加标签")
	h.click("搜索或新建标签")
	h.typeText("账单")
	if h.tt.HasText("新建「账单」") {
		t.Fatal("offers to create a label that exists")
	}
	h.tt.Key(0, ui.KeyBackspace)
	h.tt.Key(0, ui.KeyBackspace)
	h.drain()
	h.typeText("家人")
	h.click("新建「家人」")
	h.until("the new label on the thread", func() bool {
		ls, err := h.env.Svc.ListLabels(context.Background())
		if err != nil || ls[len(ls)-1].Name != "家人" {
			return false
		}
		l := ls[len(ls)-1]
		if l.Color != "gray" || l.Icon != "folder-3" {
			t.Fatalf("new label %+v", l)
		}
		got, err := h.env.Svc.MessageLabels(context.Background(), []int64{msgs[0].ID, msgs[1].ID})
		return err == nil && len(got[msgs[0].ID]) == 1 && got[msgs[0].ID][0].LabelID == l.ID
	})
}
