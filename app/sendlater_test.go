package app

import (
	"context"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/coretest"
)

// A message sent later is listed under 已排定, with the note that only a
// running Godwit sends it, not in the outbox and with no undo toast; 修改
// takes it back and opens its draft.
func TestScheduledList(t *testing.T) {
	h, accs := mailHarness(t, []coretest.Option{coretest.WithoutAutoSync()}, "alice@example.com")
	acc := accs[0]
	ctx := context.Background()
	d, err := h.env.Svc.NewDraft(ctx, acc.ID, "new", 0)
	if err != nil {
		t.Fatal(err)
	}
	d.To, d.Subject, d.HTML = []string{"bob@example.com"}, "good morning", "<p>hi</p>"
	if d, err = h.env.Svc.SaveDraft(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := h.env.Svc.SendLater(ctx, d.ID, d.Revision, h.app.clock.Now().Add(time.Hour), ""); err != nil {
		t.Fatal(err)
	}
	h.waitMail("listed under 已排定", func() bool {
		return len(h.app.mail.scheduled) == 1 && len(h.app.mail.outbox) == 0 && h.tt.HasText("已排定")
	})
	if h.tt.HasText("正在发送") || h.tt.HasText("发件箱") {
		t.Fatalf("a scheduled message shows as sending: %q", h.tt.Texts())
	}
	h.click("已排定")
	h.see("good morning")
	h.see("排定的邮件只在 Godwit 运行时发出")
	h.see("取消发送")
	h.click("修改")
	if id := h.composeOpened(0); id != d.ID {
		t.Fatalf("opened draft %d, want %d", id, d.ID)
	}
	if n := h.outbox(acc); n != 0 {
		t.Fatalf("%d items still in the outbox", n)
	}
}
