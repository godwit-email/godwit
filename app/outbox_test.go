package app

import (
	"context"
	"testing"

	"github.com/godwit-email/godwit/core/coretest"
)

func TestOutboxResolve(t *testing.T) {
	h, accs := mailHarness(t, []coretest.Option{coretest.WithoutAutoSync()}, "alice@example.com")
	acc := accs[0]
	ctx := context.Background()
	h.env.SMTP(acc.ID).FailAfterData(1)
	d, err := h.env.Svc.NewDraft(ctx, acc.ID, "new", 0)
	if err != nil {
		t.Fatal(err)
	}
	d.To, d.Subject, d.HTML = []string{"bob@example.com"}, "lost in transit", "<p>hi</p>"
	if d, err = h.env.Svc.SaveDraft(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := h.env.Svc.Send(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	_ = h.env.Svc.SyncNow(ctx, acc.ID) // the SMTP connection drops after DATA: send_uncertain
	h.waitMail("the outbox entry", func() bool { return len(h.app.mail.outbox) == 1 && h.tt.HasText("发件箱") })
	h.see("1")
	h.click("发件箱")
	h.see("lost in transit")
	h.see("不确定是否已发出")
	h.see("重新发送")
	h.see("丢弃")
	h.click("确认已发出")
	h.until("confirmed", func() bool {
		items, err := h.env.Svc.ListOutbox(ctx, acc.ID)
		return err == nil && len(items) == 1 && items[0].State == "sent"
	})
	h.syncNow(acc) // files it into Sent: done
	h.waitMail("the outbox empty", func() bool { return len(h.app.mail.outbox) == 0 })
	if h.tt.HasText("确认已发出") || h.tt.HasText("lost in transit") {
		t.Fatalf("outbox still shows the item: %q", h.tt.Texts())
	}
}
