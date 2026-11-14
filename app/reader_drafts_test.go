package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
)

// programReplyHarness opens a thread with Bob's question, under which the
// user's own reply draft waits (no label, no button), and returns the
// question's account and ID.
func programReplyHarness(t *testing.T) (*harness, core.Account, int64) {
	t.Helper()
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
	if h.tt.HasText("批准并发送") || h.tt.HasText("来自程序的草稿") {
		t.Fatal("the user's own draft is offered for approval")
	}
	return h, acc, page.Items[0].ID
}

// programReply is a reply godwit mcp writes to message src.
func programReply(t *testing.T, h *harness, acc core.Account, src int64, text string) core.Draft {
	t.Helper()
	mcp := core.WithExternalSource(context.Background(), core.AuditMCP)
	d, err := h.env.Svc.NewDraft(mcp, acc.ID, "reply", src)
	if err == nil {
		d.HTML, d.Cc, d.Bcc = "<p>"+text+"</p>", []string{"carol@example.com"}, []string{"hidden@example.net"}
		d, err = h.env.Svc.SaveDraft(mcp, d)
	}
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestThreadProgramDraftApproveSends(t *testing.T) {
	h, acc, src := programReplyHarness(t)
	d := programReply(t, h, acc, src, "Yes, Friday works.")
	h.waitMail("the program's draft", func() bool { return h.tt.HasText("Yes, Friday works.") })
	h.see("来自程序的草稿")
	h.see("carol@example.com") // every address it goes to shows before the approval
	h.see("hidden@example.net")
	h.click("批准并发送")
	h.draftQueued(d.ID)
}

// A program's attachment change while the thread is open shows at once: the
// card is the revision an approval sends.
func TestThreadProgramDraftAttachmentShows(t *testing.T) {
	h, acc, src := programReplyHarness(t)
	d := programReply(t, h, acc, src, "Yes, Friday works.")
	h.waitMail("the program's draft", func() bool { return h.tt.HasText("Yes, Friday works.") })
	path := filepath.Join(t.TempDir(), "agenda.txt")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.env.Svc.AddAttachmentAt(core.WithExternalSource(context.Background(), core.AuditMCP), d.ID, d.Revision, path); err != nil {
		t.Fatal(err)
	}
	h.waitMail("the attachment on the card", func() bool { return h.tt.HasText("agenda.txt") })
	h.click("批准并发送")
	h.draftQueued(d.ID)
}

// A program's change after the draft was shown voids the click: nothing is
// queued until the user approves the new text.
func TestThreadProgramDraftChangedRefused(t *testing.T) {
	h, acc, src := programReplyHarness(t)
	d := programReply(t, h, acc, src, "Yes, Friday works.")
	h.waitMail("the program's draft", func() bool { return h.tt.HasText("Yes, Friday works.") })
	d.HTML = "<p>Swapped behind the user's back.</p>"
	if _, err := h.env.Svc.SaveDraft(core.WithExternalSource(context.Background(), core.AuditMCP), d); err != nil {
		t.Fatal(err)
	}
	h.click("批准并发送") // still the text shown: the refresh has not run
	h.see("草稿在别处被修改。")
	if items, err := h.env.Svc.ListOutbox(context.Background(), acc.ID); err != nil || len(items) != 0 {
		t.Fatalf("queued %+v, %v", items, err)
	}
	h.waitMail("the new text", func() bool { return h.tt.HasText("Swapped behind the user's back.") })
	h.click("批准并发送")
	h.draftQueued(d.ID)
}
