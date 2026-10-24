package app

import (
	"context"
	"slices"
	"testing"

	"github.com/emersion/go-smtp"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
)

// sendNew saves a draft to rcpt with subject and sends it, as a compose
// window or the Local API would: the App hears only the outbox change.
func (h *harness) sendNew(acc core.Account, rcpt, subject string) core.Draft {
	h.t.Helper()
	ctx := context.Background()
	d, err := h.env.Svc.NewDraft(ctx, acc.ID, "new", 0)
	if err != nil {
		h.t.Fatal(err)
	}
	d.To, d.Subject, d.HTML = []string{rcpt}, subject, "<p>hi</p>"
	if d, err = h.env.Svc.SaveDraft(ctx, d); err != nil {
		h.t.Fatal(err)
	}
	if err := h.env.Svc.Send(ctx, d.ID); err != nil {
		h.t.Fatal(err)
	}
	return d
}

func (h *harness) outboxItem(acc core.Account, draftID int64) (core.OutboxItem, bool) {
	h.t.Helper()
	items, err := h.env.Svc.ListOutbox(context.Background(), acc.ID)
	if err != nil {
		h.t.Fatal(err)
	}
	i := slices.IndexFunc(items, func(it core.OutboxItem) bool { return it.DraftID == draftID })
	if i < 0 {
		return core.OutboxItem{}, false
	}
	return items[i], true
}

func (h *harness) count(text string) int {
	n := 0
	for _, s := range h.tt.Texts() {
		if s == text {
			n++
		}
	}
	return n
}

// A message sent from anywhere offers 撤销 with its subject and account;
// taking it back opens its draft again, and nothing reaches the server.
func TestUndoSendToast(t *testing.T) {
	h, accs := mailHarness(t, []coretest.Option{coretest.WithoutAutoSync()}, "alice@example.com")
	acc := accs[0]
	d := h.sendNew(acc, "bob@example.com", "change of plans")
	h.see("正在发送")
	h.see("change of plans · alice@example.com")
	h.click("撤销")
	if id := h.composeOpened(0); id != d.ID {
		t.Fatalf("opened draft %d, want %d", id, d.ID)
	}
	if n := h.outbox(acc); n != 0 {
		t.Fatalf("%d items still in the outbox", n)
	}
	h.env.AdvanceClock(core.UndoSendWindow)
	h.syncNow(acc)
	if n := len(h.env.Sent(acc.ID)); n != 0 {
		t.Fatalf("%d messages reached the server", n)
	}
	got, err := h.env.Svc.GetDraft(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.env.Svc.SaveDraft(context.Background(), got); err != nil {
		t.Fatalf("the draft is still read-only: %v", err)
	}
}

// Each message gets one toast, however often the outbox changes, and it
// closes once the message is on its way.
func TestUndoSendToastOncePerItem(t *testing.T) {
	h, accs := mailHarness(t, []coretest.Option{coretest.WithoutAutoSync()}, "alice@example.com")
	acc := accs[0]
	h.sendNew(acc, "bob@example.com", "first")
	h.see("first · alice@example.com")
	h.sendNew(acc, "bob@example.com", "second") // the outbox changes again
	h.see("second · alice@example.com")
	h.waitMail("both listed", func() bool { return len(h.app.mail.outbox) == 2 })
	if n := h.count("撤销"); n != 2 {
		t.Fatalf("%d undo buttons, want 2: %q", n, h.tt.Texts())
	}
	h.env.AdvanceClock(core.UndoSendWindow)
	h.syncNow(acc)
	h.waitMail("the toasts closed", func() bool { return !h.tt.HasText("正在发送") && h.count("撤销") == 0 })
	if n := len(h.env.Sent(acc.ID)); n != 2 {
		t.Fatalf("%d messages sent, want 2", n)
	}
}

// Undo after sending began says it cannot be taken back.
func TestUndoSendTooLate(t *testing.T) {
	h, accs := mailHarness(t, []coretest.Option{coretest.WithoutAutoSync()}, "alice@example.com")
	acc := accs[0]
	d := h.sendNew(acc, "bob@example.com", "on its way")
	it, _ := h.outboxItem(acc, d.ID)
	h.see("on its way · alice@example.com")
	h.env.AdvanceClock(core.UndoSendWindow)
	h.syncNow(acc)
	h.post(func() { h.app.undoSend(outboxEntry{it, "alice@example.com"}) }) // the click, before the toast closed
	h.see("已开始发送，无法撤销")
	h.noCompose()
}

// The outbox shows per state what can be done: 等待撤销 and retrying items
// can be taken back, a retrying one retried now; a failed one says why in
// words, with the server's own, and 修改 opens its draft.
func TestOutboxViewStates(t *testing.T) {
	h, accs := mailHarness(t, []coretest.Option{coretest.WithoutAutoSync()}, "alice@example.com")
	acc := accs[0]
	ctx := context.Background()
	h.env.SMTP(acc.ID).Reply("carol@example.com", coretest.AtRcpt, &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 7, 1}, Message: "try again later"}, true)
	h.env.SMTP(acc.ID).Reply("dave@example.com", coretest.AtRcpt, &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "no such user here"}, false)
	retry := h.sendNew(acc, "carol@example.com", "to carol")
	failed := h.sendNew(acc, "dave@example.com", "to dave")
	h.env.AdvanceClock(core.UndoSendWindow)
	h.syncNow(acc)
	waiting := h.sendNew(acc, "bob@example.com", "to bob")
	h.waitMail("three in the outbox", func() bool { return len(h.app.mail.outbox) == 3 })
	h.click("发件箱")
	h.see("to bob")
	h.see("等待撤销")
	h.see("重试中")
	h.see("收件人 dave@example.com 被服务器拒绝（550 5.1.1）")
	h.see("no such user here")
	if n := h.count("取消发送"); n != 2*2 { // a button shows as its label and its text
		t.Fatalf("%d 取消发送, want 2 (waiting and retrying): %q", n, h.tt.Texts())
	}
	h.see("重新发送")
	h.see("丢弃")

	h.click("立即重试")
	h.until("retried now", func() bool { it, ok := h.outboxItem(acc, retry.ID); return ok && it.NextAttempt.IsZero() })

	h.click("修改")
	if id := h.composeOpened(0); id != failed.ID {
		t.Fatalf("opened draft %d, want %d", id, failed.ID)
	}
	if _, ok := h.outboxItem(acc, failed.ID); ok {
		t.Fatal("the failed item stays in the outbox")
	}

	h.waitMail("two left", func() bool { return len(h.app.mail.outbox) == 2 })
	h.click("取消发送") // the oldest: carol's
	h.until("carol's taken back", func() bool { _, ok := h.outboxItem(acc, retry.ID); return !ok })
	drafts, err := h.env.Svc.ListDrafts(ctx, acc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(drafts, func(d core.Draft) bool { return d.ID == retry.ID }) {
		t.Fatalf("carol's draft is not listed again: %+v", drafts)
	}
	if _, ok := h.outboxItem(acc, waiting.ID); !ok {
		t.Fatal("bob's item went too")
	}
}

func TestFailureText(t *testing.T) {
	for _, c := range []struct {
		it          core.OutboxItem
		why, server string
	}{
		{core.OutboxItem{Failure: &core.OutboxFailure{Reason: "rejected", Stage: "RCPT", Recipient: "x@example.com", Code: 550, Enhanced: "5.1.1", Message: "unknown user"}},
			"收件人 x@example.com 被服务器拒绝（550 5.1.1）", "unknown user"},
		{core.OutboxItem{Failure: &core.OutboxFailure{Reason: "rejected", Stage: "DATA", Code: 552, Enhanced: "5.3.4", Message: "too big"}},
			"邮件过大（552 5.3.4）", "too big"},
		{core.OutboxItem{Failure: &core.OutboxFailure{Reason: "rejected", Stage: "MAIL", Code: 553, Message: "not yours"}},
			"服务器拒绝了发件地址（553）", "not yours"},
		{core.OutboxItem{Failure: &core.OutboxFailure{Reason: "rejected", Stage: "DATA", Code: 554, Enhanced: "5.7.1", Message: "spam"}},
			"服务器拒绝了这封邮件（554 5.7.1）", "spam"},
		{core.OutboxItem{Failure: &core.OutboxFailure{Reason: "expired"}, LastError: "send: queued for over 3 days"},
			"排队时间过长，没有发出", ""},
		{core.OutboxItem{LastError: "open outbox/1.eml: no such file"}, "open outbox/1.eml: no such file", ""},
	} {
		why, server := failureText(c.it)
		if why != c.why || server != c.server {
			t.Errorf("failureText(%+v) = %q, %q; want %q, %q", c.it.Failure, why, server, c.why, c.server)
		}
	}
}

// A draft in the outbox does not open: it is read-only there.
func TestOpenDraftInOutbox(t *testing.T) {
	h, ds := draftsHarness(t, "Draft hello")
	if err := h.env.Svc.Send(context.Background(), ds[0].ID); err != nil {
		t.Fatal(err)
	}
	h.post(func() { h.app.OpenDraft(ds[0].ID) }) // from a row drawn before the send, or from Wit
	h.see("这封邮件在发件箱里，取消发送后才能修改。")
	h.noCompose()
}
