package app

import (
	"context"
	"strings"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/internal/richtext"
)

// A reply a program drafted for the open thread is named in the reply bar,
// and 查看 opens it in the compose window; the user's own reply is not named.
func TestThreadProgramReplyOpens(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	acc := accs[0]
	h.env.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", acc.Email, "Question", "can you come on Friday?"))
	h.waitMail("the delivered mail", func() bool { return h.tt.HasText("Question") })
	page, err := h.env.Svc.ListMessages(context.Background(), core.View{Unified: "inbox"}, "", 10)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("inbox %+v, %v", page.Items, err)
	}
	if _, err := h.env.Svc.NewDraft(context.Background(), acc.ID, "reply", page.Items[0].ID); err != nil {
		t.Fatal(err)
	}
	h.click("Question")
	h.see("can you come on Friday?")
	h.waitMail("the thread read again", func() bool { return true })
	if h.tt.HasText("查看") {
		t.Fatal("the user's own draft is named as a program's")
	}

	mcp := core.WithExternalSource(context.Background(), core.AuditMCP)
	d, err := h.env.Svc.NewDraft(mcp, acc.ID, "reply", page.Items[0].ID)
	if err == nil {
		d.HTML = "<p>Yes, Friday works.</p>"
		d, err = h.env.Svc.SaveDraft(mcp, d)
	}
	if err != nil {
		t.Fatal(err)
	}
	h.waitMail("the program's reply", func() bool { return h.tt.HasText("程序写好了一封回复：Yes, Friday works.") })
	h.click("查看")
	h.until("its compose window", func() bool { return h.opens(d.ID) == 1 })
}

// programDraftsHarness opens the drafts folder on one draft godwit mcp wrote,
// to bob with Cc and Bcc, its body html.
func programDraftsHarness(t *testing.T, html string) (*harness, core.Draft) {
	t.Helper()
	h, accs := mailHarness(t, nil, "alice@example.com")
	if err := h.env.IMAP(accs[0].ID).User.Create("Drafts", nil); err != nil {
		t.Fatal(err)
	}
	h.syncNow(accs[0])
	mcp := core.WithExternalSource(context.Background(), core.AuditMCP)
	d, err := h.env.Svc.NewDraft(mcp, accs[0].ID, "new", 0)
	if err == nil {
		d.Subject, d.To, d.Cc, d.Bcc, d.HTML = "Friday", []string{"bob@example.com"}, []string{"carol@example.com"}, []string{"hidden@example.net"}, html
		d, err = h.env.Svc.SaveDraft(mcp, d)
	}
	if err != nil {
		t.Fatal(err)
	}
	h.waitMail("the drafts folder", func() bool { return h.tt.HasText("草稿") })
	h.click("草稿")
	h.see("Friday")
	h.click("Friday")
	return h, d
}

// The drafts pane shows a program's draft with its label and every address,
// and 批准并发送 queues it.
func TestDraftPaneProgramDraftApproves(t *testing.T) {
	h, d := programDraftsHarness(t, "<p>Yes, Friday works.</p>")
	h.see("这封草稿由程序（AI 工具、脚本、插件或 Wit）写入或修改过。发送前请核对收件人和内容。")
	h.see("carol@example.com")
	h.see("hidden@example.net")
	h.click("批准并发送")
	h.draftQueued(d.ID)
}

// A program's change after the pane showed the draft voids the click.
func TestDraftPaneProgramDraftChangedRefused(t *testing.T) {
	h, d := programDraftsHarness(t, "<p>Yes, Friday works.</p>")
	h.see("Yes, Friday works.")
	d.HTML = "<p>Swapped behind the user's back.</p>"
	if _, err := h.env.Svc.SaveDraft(core.WithExternalSource(context.Background(), core.AuditMCP), d); err != nil {
		t.Fatal(err)
	}
	h.click("批准并发送") // still the text shown: the reload has not run
	h.see("Swapped behind the user's back.")
	if items, err := h.env.Svc.ListOutbox(context.Background(), d.AccountID); err != nil || len(items) != 0 {
		t.Fatalf("queued %+v, %v", items, err)
	}
}

// A draft too long to show whole is not approved from the pane: the click
// would send text the user never saw.
func TestDraftPaneProgramDraftTooLong(t *testing.T) {
	h, d := programDraftsHarness(t, "<p>Yes, Friday works."+strings.Repeat("</p><p>more", richtext.DefaultLimits.MaxBlocks)+"</p>")
	h.see("这封草稿太长，这里显示不全。请点「继续编辑」核对全文，再从那里发送。")
	h.click("这封草稿太长，这里显示不全。请点「继续编辑」核对全文，再从那里发送。") // the send button's tip
	h.waitMail("the click to settle", func() bool { return true })
	if items, err := h.env.Svc.ListOutbox(context.Background(), d.AccountID); err != nil || len(items) != 0 {
		t.Fatalf("queued %+v, %v", items, err)
	}
}
