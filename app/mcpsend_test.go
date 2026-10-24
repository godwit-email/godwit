package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
)

// mcpSendHarness has an account and a draft to bob, with plan.pdf attached,
// that godwit mcp asked to send under 每次确认.
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
	f := filepath.Join(t.TempDir(), "plan.pdf")
	if err := os.WriteFile(f, make([]byte, 2048), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.env.Svc.AddAttachment(ctx, d.ID, f); err != nil {
		t.Fatal(err)
	}
	if d, err = h.env.Svc.GetDraft(ctx, d.ID); err != nil {
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

// The draft's attachments, which 发送 sends too, show with their sizes.
func TestMCPSendConfirmAttachments(t *testing.T) {
	h, _ := mcpSendHarness(t)
	h.see("发送这封邮件？")
	h.see("plan.pdf")
	h.see("2 KB")
}

// A plugin's warnings on 发送 are shown as the draft list shows them; 仍然发送 sends it once.
func TestMCPSendConfirmWarns(t *testing.T) {
	h, acc := mcpSendHarness(t)
	h.env.InstallPlugin(t, "send-warn")
	h.see("发送这封邮件？")
	h.click("发送")
	h.see("发送前检查：check before sending: Quarterly plan")
	if h.outbox(acc) != 0 {
		t.Fatal("sent before the warning was acknowledged")
	}
	h.click("仍然发送")
	h.until("queued", func() bool { return h.outbox(acc) == 1 })
	h.until("the dialog gone", func() bool { return !h.tt.HasText("仍然发送") })
	h.until("its window told", func() bool { return h.hasCall("DraftDeleted ") })
	h.drain()
	if err := h.env.Svc.ResolveMCPSendAcknowledged(context.Background(), h.app.mcpSend.warnID, h.app.mcpSend.warnings.Digest, true); !errors.Is(err, core.ErrInvalidState) {
		t.Fatalf("the request was not ended: %v", err)
	}
}

// Cancelling the warnings drops the request: nothing is sent.
func TestMCPSendConfirmWarnsCancel(t *testing.T) {
	h, acc := mcpSendHarness(t)
	h.env.InstallPlugin(t, "send-warn")
	h.see("发送这封邮件？")
	h.click("发送")
	h.see("发送前检查：check before sending: Quarterly plan")
	h.click("取消")
	h.until("the dialog gone", func() bool { return !h.tt.HasText("仍然发送") })
	h.drain()
	if err := h.env.Svc.ResolveMCPSendAcknowledged(context.Background(), h.app.mcpSend.warnID, h.app.mcpSend.warnings.Digest, true); !errors.Is(err, core.ErrInvalidState) {
		t.Fatalf("the request was not ended: %v", err)
	}
	if h.outbox(acc) != 0 {
		t.Fatal("sent after cancel")
	}
}

// A newer revision's request replaces the one shown without bringing the
// window to the front again.
func TestMCPSendConfirmReplaced(t *testing.T) {
	h, acc := mcpSendHarness(t)
	h.see("Quarterly plan")
	h.until("the notification", func() bool { return h.hasCall("Notify mcp_send:") })
	ctx := context.Background()
	ds, err := h.env.Svc.ListDrafts(ctx, acc.ID)
	if err != nil || len(ds) != 1 {
		t.Fatalf("drafts %v %v", ds, err)
	}
	d := ds[0]
	d.Subject = "Revised plan"
	if d, err = h.env.Svc.SaveDraft(ctx, d); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(t.TempDir(), "plan.pdf")
	if err := os.WriteFile(f, make([]byte, 2048), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.env.Svc.AddAttachment(ctx, d.ID, f); err != nil {
		t.Fatal(err)
	}
	if d, err = h.env.Svc.GetDraft(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.env.Svc.RequestMCPSend(ctx, d.ID, d.Revision); err != nil {
		t.Fatal(err)
	}
	h.see("Revised plan")
	h.drain()
	shown, notified := 0, 0
	for _, c := range h.plat.Calls() {
		if c == "ShowWindow" {
			shown++
		}
		if strings.HasPrefix(c, "Notify mcp_send:") {
			notified++
		}
	}
	if shown != 1 || notified != 1 {
		t.Fatalf("brought to the front %d times, notified %d times", shown, notified)
	}
	// The user confirms the version sent: 发送 waits a moment, saying the draft changed.
	h.see("内容刚刚有变化，请重新核对后再发送。")
	h.click("发送")
	h.drain()
	if h.outbox(acc) != 0 {
		t.Fatal("the new revision was sent unseen")
	}
	h.advance(mcpSendHold)
	h.click("发送")
	h.until("queued", func() bool { return h.outbox(acc) == 1 })
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
