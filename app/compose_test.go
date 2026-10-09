package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
)

// openCompose starts a new draft as the sidebar's button does and waits
// for the composer to show it.
func (h *harness) openCompose() *composer {
	h.t.Helper()
	h.click("写邮件")
	return h.composerLoaded()
}

func (h *harness) composerLoaded() *composer {
	h.t.Helper()
	h.until("the composer", func() bool {
		p := h.app.mail.draft
		return h.app.route.Page == PageCompose && p != nil && p.loaded
	})
	return h.app.mail.draft
}

// addAddress types addr into the address field labelled label and adds it.
func (h *harness) addAddress(label, addr string) {
	h.t.Helper()
	h.click(label)
	h.typeText(addr)
	h.tt.Key(0, ui.KeyEnter)
	h.drain()
	h.tt.Frame()
}

// writeBody types s at the end of the body.
func (h *harness) writeBody(s string) {
	h.t.Helper()
	h.click("正文")
	h.tt.Key(ui.Cmd, ui.KeyDown)
	h.typeText(s)
}

func (h *harness) draft(id int64) core.Draft {
	h.t.Helper()
	d, err := h.env.Svc.GetDraft(context.Background(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	return d
}

// sent waits until the account's SMTP server has n messages and returns them.
func (h *harness) sent(acc core.Account, n int) []string {
	h.t.Helper()
	h.until("sent mail", func() bool { return len(h.env.Sent(acc.ID)) >= n })
	var out []string
	for _, m := range h.env.Sent(acc.ID) {
		out = append(out, string(m))
	}
	return out
}

// fillNew fills a recipient and a subject and waits until they are saved.
func (h *harness) fillNew(subject string) *composer {
	h.t.Helper()
	p := h.openCompose()
	h.addAddress("添加收件人", "lena@example.com")
	h.click("填写主题")
	h.typeText(subject)
	h.advance(time.Second)
	h.until("saved", func() bool { return h.draft(p.id).Subject == subject && !p.s.busy })
	return p
}

func TestComposeNewAndSend(t *testing.T) {
	h, accs := mailHarness(t, nil, "me@example.com")
	p := h.openCompose()
	if title, _ := h.app.page().Title(); title == "写邮件" || title == "邮件" {
		t.Fatalf("title %q collides with a navigation label", title)
	}
	h.addAddress("添加收件人", "lena@example.com")
	h.click("填写主题")
	h.typeText("Plans")
	h.writeBody("See you soon")
	h.click("发送")
	out := h.sent(accs[0], 1)
	if !strings.Contains(out[0], "multipart/alternative") || !strings.Contains(out[0], "See you soon") || !strings.Contains(out[0], "Subject: Plans") {
		t.Fatalf("sent %s", out[0])
	}
	h.see("已放入发件箱")
	if h.app.route.Page != PageMail {
		t.Fatalf("route %+v after sending, want back on the mail page", h.app.route)
	}
	if !p.s.stopped {
		t.Fatal("the saver still runs after the draft was sent")
	}
}

func TestSendFlushesLatest(t *testing.T) {
	h, accs := mailHarness(t, nil, "me@example.com")
	h.fillNew("Plans")
	h.writeBody("first")
	h.advance(time.Second)
	h.writeBody(" second")
	h.click("发送") // no time passes: the debounce never fired
	out := h.sent(accs[0], 1)
	if !strings.Contains(out[0], "first second") {
		t.Fatalf("sent %s, want the newest body", out[0])
	}
}

func TestSendWhileSaveBlocked(t *testing.T) {
	h, accs := mailHarness(t, nil, "me@example.com")
	p := h.fillNew("Plans")
	release, started := make(chan struct{}), make(chan struct{})
	var saves atomic.Int32
	save := p.s.save
	p.s.save = func(ctx context.Context, d core.Draft) (core.Draft, error) {
		if saves.Add(1) == 1 {
			close(started)
			<-release
		}
		return save(ctx, d)
	}
	h.writeBody("alpha")
	h.advance(time.Second)
	<-started
	h.writeBody(" beta")
	h.click("发送")
	h.drain()
	if len(h.env.Sent(accs[0].ID)) != 0 || h.app.route.Page != PageCompose {
		t.Fatal("sent while a save was blocked")
	}
	close(release)
	out := h.sent(accs[0], 1)
	if !strings.Contains(out[0], "alpha beta") {
		t.Fatalf("sent %s, want the last input", out[0])
	}
	h.until("back on the mail page", func() bool { return h.app.route.Page == PageMail })
	h.advance(2 * time.Second)
	if n := len(h.env.Sent(accs[0].ID)); n != 1 {
		t.Fatalf("%d messages sent, want 1", n)
	}
}

func TestComposeAutosave(t *testing.T) {
	h, _ := mailHarness(t, nil, "me@example.com")
	p := h.openCompose()
	h.writeBody("hello there")
	h.see("未保存")
	if strings.Contains(h.draft(p.id).HTML, "hello there") {
		t.Fatal("saved before the pause")
	}
	h.advance(time.Second)
	h.until("saved", func() bool { return strings.Contains(h.draft(p.id).HTML, "hello there") })
	h.see("草稿已保存 · 09:00")
}

func TestNoSaveWhileComposing(t *testing.T) {
	h, _ := mailHarness(t, nil, "me@example.com")
	p := h.openCompose()
	h.writeBody("a")
	h.advance(time.Second)
	h.until("saved", func() bool { return strings.Contains(h.draft(p.id).HTML, "a") && !p.s.busy })
	rev := h.draft(p.id).Revision
	h.tt.Compose("ni", 2)
	h.tt.Frame()
	h.advance(2 * time.Second)
	h.drain()
	if d := h.draft(p.id); d.Revision != rev || strings.Contains(d.HTML, "ni") {
		t.Fatalf("saved while composing: revision %d→%d, %q", rev, d.Revision, d.HTML)
	}
	h.typeText("你")
	h.advance(time.Second)
	h.until("the committed text saved", func() bool { return strings.Contains(h.draft(p.id).HTML, "a你") })
}

func TestComposeReplyQuoteReadOnly(t *testing.T) {
	h, accs := mailHarness(t, nil, "me@example.com")
	m := h.deliverOne(accs[0], coretest.Msg("lena@example.com", accs[0].Email, "Q4", "the original words"))
	d, err := h.env.Svc.NewDraft(context.Background(), accs[0].ID, "reply", m.ID)
	if err != nil {
		t.Fatal(err)
	}
	h.app.navigate(Route{Page: PageCompose, DraftID: d.ID})
	p := h.composerLoaded()
	h.see("显示引用的原邮件（助手只读）")
	if h.tt.HasText("the original words") {
		t.Fatal("the quote shows before it is opened")
	}
	h.click("显示引用的原邮件（助手只读）")
	h.see("the original words")
	h.writeBody("Thanks")
	h.advance(time.Second)
	h.until("saved", func() bool { return strings.Contains(h.draft(p.id).HTML, "Thanks") })
	got := h.draft(p.id).HTML
	if i, j := strings.Index(got, "Thanks"), strings.Index(got, `data-godwit="quote"`); j < 0 || i > j || !strings.Contains(got, "the original words") {
		t.Fatalf("draft %q: the reply goes before the kept quote", got)
	}
}

func TestComposeAttachments(t *testing.T) {
	h, _ := mailHarness(t, nil, "me@example.com")
	dir := t.TempDir()
	small, big := filepath.Join(dir, "notes.txt"), filepath.Join(dir, "huge.bin")
	if err := os.WriteFile(small, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(core.MaxMessageBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()

	p := h.openCompose()
	h.writeBody("unsaved words")
	h.plat.files = []string{small}
	h.click("添加附件")
	h.see("notes.txt")
	d := h.draft(p.id)
	if len(d.Attachments) != 1 || !strings.Contains(d.HTML, "unsaved words") {
		t.Fatalf("draft %+v: want the attachment, saved after the body", d)
	}
	h.see("5 B")
	h.writeBody(" more")
	h.advance(time.Second)
	h.until("saved after the attachment", func() bool { return strings.Contains(h.draft(p.id).HTML, "more") })

	h.plat.files = []string{big}
	h.click("添加附件")
	h.see("附件超过了 25.0 MB 的上限")

	h.click("移除 notes.txt")
	h.until("removed", func() bool { return len(h.draft(p.id).Attachments) == 0 && !h.tt.HasText("notes.txt") })
}

func TestComposeBeforeSendWarning(t *testing.T) {
	h, accs := mailHarness(t, nil, "me@example.com")
	h.env.InstallPlugin(t, "send-warn")
	h.fillNew("Report")
	h.click("发送")
	h.see("check before sending: Report")
	h.see("来自插件 send-warn（message.beforeSend）")
	if len(h.env.Sent(accs[0].ID)) != 0 || h.app.route.Page != PageCompose {
		t.Fatal("sent despite the warning")
	}
	h.click("仍然发送")
	h.sent(accs[0], 1)
	h.until("back on the mail page", func() bool { return h.app.route.Page == PageMail })
}

func TestComposeBlocked(t *testing.T) {
	h, accs := mailHarness(t, nil, "me@example.com")
	h.env.InstallPlugin(t, "send-block")
	p := h.fillNew("Report")
	h.click("发送")
	h.see("blocked: Report")
	if h.tt.HasText("仍然发送") {
		t.Fatal("a block offers to send anyway")
	}
	h.advance(time.Second)
	if len(h.env.Sent(accs[0].ID)) != 0 || h.app.route.Page != PageCompose {
		t.Fatal("sent despite the block")
	}
	h.draft(p.id) // still there
}

func TestStaleDraftConflict(t *testing.T) {
	h, _ := mailHarness(t, nil, "me@example.com")
	p := h.openCompose()
	h.writeBody("mine")
	h.advance(time.Second)
	h.until("saved", func() bool { return strings.Contains(h.draft(p.id).HTML, "mine") && !p.s.busy })

	elsewhere := func(body string) {
		d := h.draft(p.id)
		d.HTML = "<p>" + body + "</p>"
		if _, err := h.env.Svc.SaveDraft(context.Background(), d); err != nil {
			t.Fatal(err)
		}
	}
	elsewhere("theirs")
	h.writeBody(" more")
	h.advance(time.Second)
	h.see("草稿在别处被修改")
	if strings.Contains(h.draft(p.id).HTML, "more") {
		t.Fatal("a stale save overwrote the draft")
	}
	h.advance(2 * time.Second) // no retry
	if !strings.Contains(h.draft(p.id).HTML, "theirs") {
		t.Fatal("the conflict was overwritten without asking")
	}
	h.click("保留我的版本")
	h.until("mine kept", func() bool { return strings.Contains(h.draft(p.id).HTML, "mine more") })
	h.until("the notice gone", func() bool { return !h.tt.HasText("草稿在别处被修改") })

	elsewhere("newer")
	h.writeBody(" again")
	h.advance(time.Second)
	h.see("草稿在别处被修改")
	h.click("使用最新版本")
	h.until("the latest loaded", func() bool {
		return strings.Contains(p.ed.HTML(), "newer") && !h.tt.HasText("草稿在别处被修改")
	})
	h.advance(2 * time.Second)
	if got := h.draft(p.id).HTML; !strings.Contains(got, "newer") || strings.Contains(got, "again") {
		t.Fatalf("draft %q, want the latest version", got)
	}
	h.see("草稿已保存")
}

func TestSwitchSenderAccount(t *testing.T) {
	h, accs := mailHarness(t, nil, "a@example.com", "b@example.com")
	for i, sig := range []string{"Sig A", "Sig B"} {
		s := sig
		if _, err := h.env.Svc.UpdateAccount(context.Background(), accs[i].ID, core.AccountPatch{Signature: &s}); err != nil {
			t.Fatal(err)
		}
	}
	p := h.fillNew("Hi")
	h.see("Sig A")
	h.writeBody("typed before the switch")
	h.click("收起侧栏") // its account headers show the same addresses
	h.click("发件账户")
	h.click("b@example.com")
	h.until("moved to b", func() bool { return h.draft(p.id).AccountID == accs[1].ID })
	h.see("Sig B")
	if h.tt.HasText("Sig A") || !strings.Contains(h.draft(p.id).HTML, "typed before the switch") {
		t.Fatalf("after the switch: texts %q, draft %q", h.tt.Texts(), h.draft(p.id).HTML)
	}
	h.click("发送")
	out := h.sent(accs[1], 1)
	if m := h.env.SMTP(accs[1].ID).Messages()[0]; m.From != "b@example.com" || !strings.Contains(fromLine(out[0]), "b@example.com") || !strings.Contains(out[0], "Sig B") {
		t.Fatalf("envelope from %q, message %s", m.From, out[0])
	}
	if len(h.env.Sent(accs[0].ID)) != 0 {
		t.Fatal("the first account sent it")
	}
}

func TestDiscardDraft(t *testing.T) {
	h, _ := mailHarness(t, nil, "me@example.com")
	p := h.openCompose()
	h.writeBody("never mind")
	h.click("丢弃草稿")
	h.click("丢弃")
	h.until("back on the mail page", func() bool { return h.app.route.Page == PageMail })
	if _, err := h.env.Svc.GetDraft(context.Background(), p.id); err == nil {
		t.Fatal("the draft is still there")
	}
	h.advance(2 * time.Second) // the stopped saver never brings it back
	if _, err := h.env.Svc.GetDraft(context.Background(), p.id); err == nil {
		t.Fatal("the draft came back")
	}
}

func TestLeavingComposeSaves(t *testing.T) {
	h, _ := mailHarness(t, nil, "me@example.com")
	p := h.openCompose()
	h.writeBody("left behind")
	h.click("后退")
	h.until("saved on leaving", func() bool { return strings.Contains(h.draft(p.id).HTML, "left behind") })

	h.click("前进")
	p = h.composerLoaded()
	h.writeBody(" and closed")
	done := false
	h.app.FlushDrafts(func() { done = true })
	h.until("flushed for closing", func() bool { return done })
	if !strings.Contains(h.draft(p.id).HTML, "left behind and closed") {
		t.Fatalf("draft %q", h.draft(p.id).HTML)
	}
}

// fromLine is the message's From header line.
func fromLine(raw string) string {
	for _, l := range strings.Split(raw, "\r\n") {
		if strings.HasPrefix(l, "From:") {
			return l
		}
	}
	return ""
}
