package app

import (
	"context"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
)

// mcpSendHarness has an account and a draft to bob that godwit mcp asked to
// send under 每次确认.
func mcpSendHarness(t *testing.T) (*harness, core.Account) {
	t.Helper()
	var acc core.Account
	h := newHarnessWith(t, []coretest.Option{coretest.WithoutAutoSync(), coretest.WithConfirmWindow()},
		func(e *coretest.Env) { acc = e.AddAccount(t, "alice@example.com") })
	h.plat.supported = true
	h.until("initial load", func() bool { return h.app.loaded })
	ctx := context.Background()
	d, err := h.env.Svc.NewDraft(ctx, acc.ID, "new", 0)
	if err != nil {
		t.Fatal(err)
	}
	d.To, d.Subject, d.HTML = []string{"bob@example.com"}, "Quarterly plan", "<p>the plan body</p>"
	if d, err = h.env.Svc.SaveDraft(ctx, d); err != nil {
		t.Fatal(err)
	}
	if _, err := h.env.Svc.RequestMCPSend(ctx, d.ID, d.Revision); err != nil {
		t.Fatal(err)
	}
	return h, acc
}

func (h *harness) outbox(acc core.Account) int {
	h.t.Helper()
	out, err := h.env.Svc.ListOutbox(context.Background(), acc.ID)
	if err != nil {
		h.t.Fatal(err)
	}
	return len(out)
}

// A request shows the account, the recipients (bob marked first-time), the
// subject and the body in front, with a notification; 发送 sends it once.
func TestMCPSendConfirmWindow(t *testing.T) {
	h, acc := mcpSendHarness(t)
	h.see("发送这封邮件？")
	h.see("alice@example.com")
	h.see("bob@example.com")
	h.see("首次发送")
	h.see("Quarterly plan")
	h.see("the plan body")
	if !h.hasCall("ShowWindow") {
		t.Fatal("the window was not brought to the front")
	}
	h.until("the notification", func() bool { return h.hasCall("Notify mcp_send:") })
	if h.outbox(acc) != 0 {
		t.Fatal("sent before the user confirmed")
	}
	h.click("发送")
	h.until("queued", func() bool { return h.outbox(acc) == 1 })
	h.until("the dialog closed", func() bool { return !h.tt.HasText("发送这封邮件？") })
}

// Closing the window drops the request: nothing is sent, the draft stays.
func TestMCPSendConfirmWindowClose(t *testing.T) {
	h, acc := mcpSendHarness(t)
	h.see("发送这封邮件？")
	h.click("取消")
	h.until("rejected", func() bool {
		ps, err := h.env.Svc.PendingMCPSends(context.Background())
		return err == nil && len(ps) == 0
	})
	if h.outbox(acc) != 0 {
		t.Fatal("sent after cancel")
	}
	ds, err := h.env.Svc.ListDrafts(context.Background(), acc.ID)
	if err != nil || len(ds) != 1 {
		t.Fatalf("drafts %v %v", ds, err)
	}
}

// Picking 直接发送 shows its risk.
func TestMCPSendSettingRisk(t *testing.T) {
	h, _ := withAccount(t)
	h.openSettings("privacy")
	h.see("通过 MCP 发送")
	risk := "选择后，它可以不经你确认就以你的名义发信"
	if h.tt.HasText(risk) {
		t.Fatal("the risk shows under 每次确认")
	}
	h.click("每次确认")
	h.click("直接发送")
	h.see(risk)
	h.until("saved", func() bool {
		st, err := h.env.Svc.GetSettings(context.Background())
		return err == nil && st.MCPSend == core.MCPSendDirect
	})
}
