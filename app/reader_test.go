package app

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
)

// deliverOne delivers one message to the account's INBOX and returns its summary.
func (h *harness) deliverOne(acc core.Account, raw []byte) core.MessageSummary {
	h.t.Helper()
	h.env.Deliver(h.t, acc.ID, "INBOX", raw)
	page, err := h.env.Svc.ListMessages(context.Background(), core.View{Unified: "inbox"}, "", 200)
	if err != nil || len(page.Items) == 0 {
		h.t.Fatalf("no message: %v", err)
	}
	return page.Items[0] // newest first
}

// openThread opens a thread in the reader as a click on its row does.
func (h *harness) openThread(id int64) {
	h.app.mail.open([]mailItem{{ID: id, Thread: true}}, 0)
	h.tt.Frame()
}

func (h *harness) messages(threadID int64) []core.MessageSummary {
	h.t.Helper()
	th, err := h.env.Svc.GetThread(context.Background(), threadID)
	if err != nil {
		h.t.Fatal(err)
	}
	return th.Messages
}

func (h *harness) countTexts(sub string) int {
	n := 0
	for _, s := range h.tt.Texts() {
		if strings.Contains(s, sub) {
			n++
		}
	}
	return n
}

// called waits for a platform call starting with prefix and returns its argument.
func (h *harness) callArg(prefix string) string {
	h.t.Helper()
	var arg string
	h.until(prefix, func() bool {
		for _, c := range h.plat.Calls() {
			if a, ok := strings.CutPrefix(c, prefix+" "); ok {
				arg = a
				return true
			}
		}
		return false
	})
	return arg
}

func (h *harness) hasCall(prefix string) bool {
	return slices.ContainsFunc(h.plat.Calls(), func(c string) bool { return strings.HasPrefix(c, prefix) })
}

func seen(m core.MessageSummary) bool { return slices.Contains(m.Flags, `\Seen`) }

func TestReaderThreadCollapse(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	acc, base := accs[0], h.clock.Now()
	h.put(acc, "INBOX", coretest.Msg("bob@example.com", acc.Email, "Plan", "first words", coretest.MessageID("a@x"), at(base, 3)))
	h.put(acc, "INBOX", coretest.Msg("carol@example.com", acc.Email, "Re: Plan", "second words", coretest.MessageID("b@x"), coretest.InReplyTo("a@x"), at(base, 2)))
	h.put(acc, "INBOX", coretest.Msg("dave@example.com", acc.Email, "Re: Plan", "third words", coretest.MessageID("c@x"), coretest.InReplyTo("b@x"), at(base, 1)))
	h.syncNow(acc)
	page, _ := h.env.Svc.ListThreads(context.Background(), core.View{Unified: "inbox"}, "", 10)
	thread := page.Items[0].ID
	msgs := h.messages(thread)
	if len(msgs) != 3 {
		t.Fatalf("%d messages in the thread", len(msgs))
	}
	if err := h.env.Svc.SetFlags(context.Background(), []int64{msgs[0].ID, msgs[1].ID}, []string{`\Seen`}, nil); err != nil {
		t.Fatal(err)
	}
	h.openThread(thread)
	h.see("third words")
	r := h.app.mail.reader
	if !r.open[msgs[2].ID] || r.open[msgs[0].ID] || r.open[msgs[1].ID] {
		t.Fatalf("open %v; want only the newest", r.open)
	}
	h.see("bob@example.com") // the collapsed rows: sender, snippet
	h.see("second words")
	if n := h.countTexts("发给"); n != 1 {
		t.Fatalf("%d expanded headers, want 1: %q", n, h.tt.Texts())
	}
	h.click("bob@example.com")
	h.until("the oldest expanded", func() bool { return r.open[msgs[0].ID] && h.countTexts("发给") == 2 })
	h.click("bob@example.com")
	h.until("the oldest collapsed", func() bool { return !r.open[msgs[0].ID] && h.countTexts("发给") == 1 })
}

func TestReaderMarksRead(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	acc, base := accs[0], h.clock.Now()
	h.put(acc, "INBOX", coretest.Msg("bob@example.com", acc.Email, "Plan", "one", coretest.MessageID("a@x"), at(base, 2)))
	h.put(acc, "INBOX", coretest.Msg("bob@example.com", acc.Email, "Re: Plan", "two", coretest.MessageID("b@x"), coretest.InReplyTo("a@x"), at(base, 1)))
	h.syncNow(acc)
	thread := h.deliverOne(acc, coretest.Msg("bob@example.com", acc.Email, "Re: Plan", "three", coretest.MessageID("c@x"), coretest.InReplyTo("b@x"))).ThreadID
	h.openThread(thread)
	r := h.app.mail.reader
	h.until("all three expanded", func() bool { return len(r.open) == 3 })
	h.until("all read", func() bool {
		return !slices.ContainsFunc(h.messages(thread), func(m core.MessageSummary) bool { return !seen(m) })
	})
}

// pngBytes is a w×h PNG.
func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestReaderRemoteImages(t *testing.T) {
	var mu sync.Mutex
	var hdrs []http.Header
	pic := pngBytes(t, 2, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hdrs = append(hdrs, r.Header.Clone())
		mu.Unlock()
		w.Header().Set("Set-Cookie", "track=1")
		w.Write(pic)
	}))
	defer srv.Close()
	hits := func() int { mu.Lock(); defer mu.Unlock(); return len(hdrs) }

	h, accs := mailHarness(t, nil, "alice@example.com")
	src := srv.URL + "/pixel.png"
	m := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "News", "text",
		coretest.HTML(`<p>Hello reader</p><img src="`+src+`" width="2" height="2">`)))
	r := h.app.mail.reader
	r.allowLoopback = true
	h.openThread(m.ThreadID)
	h.see("Hello reader")
	h.see("已阻止 1 张远程图片，防止发件人追踪你是否打开了邮件")
	if n := hits(); n != 0 {
		t.Fatalf("%d requests before loading", n)
	}
	h.click("加载图片")
	h.until("the image loaded", func() bool {
		b := r.bodies[m.ID]
		if b == nil || b.images == nil {
			return false
		}
		e := b.images.imgs[src]
		return e != nil && e.bm != nil
	})
	h.until("the notice gone", func() bool { return !h.tt.HasText("已阻止") })
	if n := hits(); n != 1 {
		t.Fatalf("%d requests, want 1", n)
	}
	h.tt.Frame()
	h.tt.Frame()
	if n := hits(); n != 1 {
		t.Fatalf("%d requests after more frames, want 1", n)
	}
	for _, k := range []string{"Cookie", "Referer"} {
		if v := hdrs[0].Get(k); v != "" {
			t.Errorf("request sent %s: %q", k, v)
		}
	}
}

func TestReaderAttachments(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	data := []byte("%PDF-1.4 report body")
	m := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "Report", "see attached",
		coretest.Attachment("report.pdf", "application/pdf", data)))
	h.openThread(m.ThreadID)
	h.see("report.pdf")
	full, err := h.env.Svc.GetMessage(context.Background(), m.ID, false)
	if err != nil || len(full.Attachments) != 1 {
		t.Fatalf("attachments %+v, %v", full.Attachments, err)
	}
	h.see(byteSize(full.Attachments[0].Size))
	h.see("PDF")
	tmp := t.TempDir()
	h.plat.mu.Lock()
	h.plat.tempDir = tmp
	h.plat.mu.Unlock()
	h.click("report.pdf")
	path := h.callArg("OpenPath")
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("opened %q: %q, %v", path, got, err)
	}
	if filepath.Base(path) != "report.pdf" || !strings.HasPrefix(path, tmp+string(filepath.Separator)) {
		t.Fatalf("opened %q, not a report.pdf under %q", path, tmp)
	}
	defer func() {
		h.app.Close()
		if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
			t.Errorf("the opened copy's directory outlived Close: %v", err)
		}
	}()
	h.plat.mu.Lock()
	h.plat.savePath = filepath.Join(t.TempDir(), "saved.pdf")
	h.plat.mu.Unlock()
	h.click("另存为…")
	h.callArg("SaveFile")
	h.until("the copy written", func() bool {
		got, err := os.ReadFile(h.plat.savePath)
		return err == nil && bytes.Equal(got, data)
	})
}

// scrollTo scrolls the reader until label is in the window.
func (h *harness) scrollTo(label string) {
	h.t.Helper()
	for range 500 {
		if r, ok := h.tt.Find(label); ok && r.Y > 0 && r.Y+r.H < 820 {
			return
		}
		h.tt.Scroll(900, 400, 0, 100000)
		h.tt.Frame()
	}
	h.t.Fatalf("%q never came into view", label)
}

// more chooses an item of the reader's More menu.
func (h *harness) more(item string) {
	h.t.Helper()
	h.click("更多")
	if err := h.tt.ChooseMenuItem(item); err != nil {
		h.t.Fatalf("%v; menu %q", err, h.tt.Menu())
	}
	h.drain()
	h.tt.Frame()
}

func TestViewInBrowserHasCSP(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.plat.tempDir = t.TempDir()
	m := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "Page", "text",
		coretest.HTML(`<p>Shown in a browser</p><script>alert(1)</script><img src="https://tracker.example/p.gif">`)))
	h.openThread(m.ThreadID)
	h.see("Shown in a browser")
	h.more("在浏览器中查看原文")
	u, err := url.Parse(h.callArg("OpenURL"))
	if err != nil || u.Scheme != "file" {
		t.Fatalf("opened %v (%v)", u, err)
	}
	path := filepath.FromSlash(strings.TrimPrefix(u.Path, "/"))
	if !filepath.IsAbs(path) {
		path = filepath.FromSlash(u.Path) // not Windows: the path starts with /
	}
	if !strings.HasPrefix(path, h.plat.tempDir) {
		t.Fatalf("file %q not in the temp dir %q", path, h.plat.tempDir)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	csp := `<meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src data:; style-src 'unsafe-inline'">`
	if !strings.Contains(s, csp) || strings.Contains(strings.ToLower(s), "<script") || !strings.Contains(s, "Shown in a browser") {
		t.Fatalf("file:\n%s", s)
	}
}

func TestExportEML(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	m := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "Keep: this/that", "raw body"))
	h.plat.savePath = filepath.Join(t.TempDir(), "out.eml")
	h.openThread(m.ThreadID)
	h.see("raw body")
	h.more("导出原始邮件 (.eml)")
	if name := h.callArg("SaveFile"); name != "Keep_ this_that.eml" {
		t.Errorf("default name %q", name)
	}
	raw, err := h.env.Svc.GetRawMessage(context.Background(), m.ID)
	if err != nil {
		t.Fatal(err)
	}
	h.until("the file written", func() bool {
		got, err := os.ReadFile(h.plat.savePath)
		return err == nil && bytes.Equal(got, raw)
	})
	if h.hasCall("OpenURL") || h.hasCall("OpenPath") {
		t.Fatalf("export opened something: %q", h.plat.Calls())
	}
}

func TestReaderReplyCreatesDraft(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	m := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "Question", "can you?"))
	h.openThread(m.ThreadID)
	h.see("can you?")
	h.click("回复")
	d, err := h.env.Svc.GetDraft(context.Background(), h.composeOpened(0))
	if err != nil {
		t.Fatal(err)
	}
	if d.Mode != "reply" || d.SourceMessageID != m.ID || !slices.ContainsFunc(d.To, func(s string) bool { return strings.Contains(s, "bob@example.com") }) {
		t.Fatalf("draft %+v", d)
	}
}

func TestReaderArchive(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.app.sidebarHidden = true // its Archive folder is named as the button is
	m := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "Done", "file it"))
	h.waitMail("the folders", func() bool { return len(h.app.mail.folders[accs[0].ID]) > 0 }) // batch finds archives by them
	h.openThread(m.ThreadID)
	h.see("file it")
	h.click("归档")
	h.until("archived", func() bool { return h.countIn(h.folderOf(accs[0], "archive")) == 1 })
	if r := h.app.route; r.ThreadID != 0 || r.MessageID != 0 {
		t.Fatalf("the reader still shows %+v", r)
	}
}

func TestLinkConfirm(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	link := "https://shop.example.com/deal?id=7"
	m := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "Deal", "text",
		coretest.HTML(`<p>Go to <a href="`+link+`">the deal</a> now</p>`)))
	h.openThread(m.ThreadID)
	h.click("the deal")
	h.see("shop.example.com")
	h.see("/deal?id=7")
	if h.hasCall("OpenURL") {
		t.Fatal("opened before confirming")
	}
	h.click("打开")
	if got := h.callArg("OpenURL"); got != link {
		t.Fatalf("opened %q", got)
	}
	h.until("the dialog closed", func() bool { return !h.tt.HasText("复制链接") })

	for range 200 {
		if h.tt.Focused("the deal") {
			break
		}
		h.tt.Key(0, ui.KeyTab)
	}
	if !h.tt.Focused("the deal") {
		t.Fatal("Tab never reached the link")
	}
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	h.see("复制链接")
	h.click("复制链接")
	if got := h.tt.Clipboard(); got != link {
		t.Fatalf("clipboard %q", got)
	}
}

func TestReaderTruncatedNotice(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.plat.tempDir = t.TempDir()
	m := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "Long", "text",
		coretest.HTML(strings.Repeat("<p>line</p>", 5100))))
	h.openThread(m.ThreadID)
	h.see("正文过长，只显示了开头")
	h.scrollTo("在浏览器中查看")
	h.click("在浏览器中查看")
	if u := h.callArg("OpenURL"); !strings.HasPrefix(u, "file://") {
		t.Fatalf("opened %q", u)
	}
}

func TestReaderRefreshKeepsNewest(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	m := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "Star me", "body"))
	h.openThread(m.ThreadID)
	h.see("body")
	r := h.app.mail.reader
	fetched, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	real := r.getThread
	r.getThread = func(ctx context.Context, id int64) (core.Thread, error) {
		th, err := real(ctx, id)
		if calls.Add(1) == 1 {
			close(fetched)
			<-release
			defer close(returned)
		}
		return th, err
	}
	r.sync(true) // the older refresh: reads the thread unstarred, answers last
	<-fetched
	if err := h.env.Svc.SetFlags(context.Background(), []int64{m.ID}, []string{`\Flagged`}, nil); err != nil {
		t.Fatal(err)
	}
	r.sync(true) // the newer refresh: reads it starred
	flagged := func() bool { return len(r.msgs) == 1 && slices.Contains(r.msgs[0].Flags, `\Flagged`) }
	h.until("the newer refresh applied", flagged)
	close(release)
	<-returned
	frames := 0
	h.until("the older answer posted and run", func() bool { frames++; return frames > 50 })
	if !flagged() {
		t.Fatalf("an older refresh overwrote the newer: %+v", r.msgs)
	}
}

func TestReaderNewReplyExpandsAndReads(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	acc := accs[0]
	m := h.deliverOne(acc, coretest.Msg("bob@example.com", acc.Email, "Plan", "first", coretest.MessageID("p1@x"), at(h.clock.Now(), 2)))
	h.openThread(m.ThreadID)
	h.see("first")
	r := h.app.mail.reader
	h.env.Deliver(t, acc.ID, "INBOX", coretest.Msg("carol@example.com", acc.Email, "Re: Plan", "reply words",
		coretest.MessageID("p2@x"), coretest.InReplyTo("p1@x"), at(h.clock.Now(), 1)))
	msgs := h.messages(m.ThreadID)
	if len(msgs) != 2 {
		t.Fatalf("%d messages", len(msgs))
	}
	reply := msgs[1].ID
	h.waitMail("the reply expanded", func() bool { return r.open[reply] && h.tt.HasText("reply words") })
	h.until("the reply read", func() bool { return seen(h.messages(m.ThreadID)[1]) })
}

func TestReaderBodyRetry(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	m := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "Flaky", "eventually shown"))
	r := h.app.mail.reader
	var failed atomic.Bool
	real := r.getMessage
	r.getMessage = func(ctx context.Context, id int64, remote bool) (core.Message, error) {
		if failed.CompareAndSwap(false, true) {
			return core.Message{}, errors.New("server went away")
		}
		return real(ctx, id, remote)
	}
	h.openThread(m.ThreadID)
	h.see("server went away")
	h.click("重试")
	h.see("eventually shown")
}

func TestFileName(t *testing.T) {
	for in, want := range map[string]string{
		"report.pdf":   "report.pdf",
		"../../x":      "_.._x",
		"..":           "attachment",
		"":             "attachment",
		"  ":           "attachment",
		"a/b\\c.txt":   "a_b_c.txt",
		"tab\there":    "tab_here",
		"CON":          "_CON",
		"con.txt":      "_con.txt",
		"COM1.log":     "_COM1.log",
		"console.txt":  "console.txt",
		"trailing. . ": "trailing",
	} {
		if got := fileName(in, "attachment"); got != want {
			t.Errorf("fileName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSingleMessageHeadDoesNotCollapse(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	acc := accs[0]
	m := h.deliverOne(acc, coretest.Msg("bob@example.com", acc.Email, "Plan", "only words"))
	h.openThread(m.ThreadID)
	h.see("only words")
	var head string // the sender head's "发给 …" line, which only the reader shows
	for _, s := range h.tt.Texts() {
		if strings.HasPrefix(s, "发给") {
			head = s
		}
	}
	if head == "" {
		t.Fatalf("no sender head: %q", h.tt.Texts())
	}
	h.click(head)
	h.tt.Frame()
	if !h.app.mail.reader.open[m.ID] || !h.tt.HasText("only words") {
		t.Fatal("a click on the only message's head collapsed it")
	}
}
