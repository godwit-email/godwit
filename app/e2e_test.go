package app

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/webui"
)

// TestE2EOnboardToReply runs a first start through to a sent reply: add the
// account, see the delivered mail, read it, reply, send.
func TestE2EOnboardToReply(t *testing.T) {
	plaintext(t)
	h := newHarness(t)
	h.firstRun()
	h.click("其他 IMAP 邮箱")
	h.fill("邮箱地址", "new@example.com")
	h.manualServers()
	h.fill("密码", "secret")
	h.click("连接")
	h.see("连接成功")
	h.click("开始使用")
	h.until("the mail page", func() bool { return h.app.route.Page == PageMail && len(h.app.accounts) == 1 })
	acc := h.app.accounts[0]

	h.env.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", acc.Email, "Question", "can you come on Friday?"))
	h.waitMail("the delivered mail", func() bool { return h.tt.HasText("Question") })
	h.click("Question")
	h.see("can you come on Friday?")
	h.click("回复")
	id := h.composeOpened(0)
	if h.app.route.Page != PageMail {
		t.Fatalf("the main window left the mail page: %+v", h.app.route)
	}

	// The compose window's page, as the platform would bind it for id.
	const win = 7
	c := webui.NewCompose()
	c.SetService(h.env.Svc)
	c.Attach(win, id)
	page := webui.NewComposePage(c, webui.PageDeps{WinOf: func(context.Context) (int, bool) { return win, true }})
	ctx := context.Background()
	st, err := page.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := page.Save(ctx, webui.ComposeEdit{To: st.To, Cc: st.Cc, Bcc: st.Bcc, ReplyTo: st.ReplyTo, Subject: st.Subject,
		Body: "<p>Yes, see you then.</p>", Revision: st.Revision})
	if err != nil || saved.Conflict {
		t.Fatalf("save %+v, %v", saved, err)
	}
	if res, err := page.Send(ctx, saved.Revision, ""); err != nil || !res.Sent {
		t.Fatalf("send %+v, %v", res, err)
	}
	out := h.sent(acc, 1)
	if !strings.Contains(out[0], "Yes, see you then.") || !strings.Contains(out[0], "Subject: Re: Question") || !strings.Contains(out[0], "bob@example.com") {
		t.Fatalf("sent %s", out[0])
	}
	if n := len(h.env.Sent(acc.ID)); n != 1 {
		t.Fatalf("%d messages sent, want 1", n)
	}
	h.syncNow(acc) // files the sent reply
	h.waitMail("the outbox empty", func() bool {
		items, err := h.env.Svc.ListOutbox(context.Background(), acc.ID)
		return err == nil && len(items) == 0 && len(h.app.mail.outbox) == 0
	})
}

// TestE2EQuickReplyAndDraftsList takes a quick reply into a compose window, then
// finds the same draft again in the drafts list.
func TestE2EQuickReplyAndDraftsList(t *testing.T) {
	h, acc, llm := aiHarness(t, true, noSummary)
	llm.Script(coretest.Turn{Text: []string{`{"replies":[{"html":"<p>Sounds good</p>"},{"html":"<p>Not this week</p>"},{"html":"<p>Let me check</p>"}]}`}})
	h.env.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", acc.Email, "Lunch", "lunch on Friday?"))
	h.waitMail("the delivered mail", func() bool { return h.tt.HasText("Lunch") })
	h.click("Lunch")
	h.see("lunch on Friday?")
	h.click("Not this week")
	id := h.composeOpened(0)
	if d := h.draft(id); !strings.Contains(d.HTML, "Not this week") || strings.Contains(d.HTML, "Sounds good") || d.Subject != "Re: Lunch" {
		t.Fatalf("draft %+v", d)
	}

	h.advance(time.Second)
	h.click("草稿")
	h.see("Re: Lunch")
	h.click("Re: Lunch")
	h.click("继续编辑")
	call := fmt.Sprint("OpenCompose ", id)
	h.until("the same draft opened again", func() bool {
		return len(slices.DeleteFunc(h.plat.Calls(), func(c string) bool { return c != call })) == 2
	})
}
