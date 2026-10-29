package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/internal/theme"
)

// viewBox is where the last frame shows v, as the pointer finds it (a
// scan on a 2-DIP grid; enough for the tests' sizes): ok is false when
// it does not show.
func (h *harness) viewBox(v ui.NativeWebView) (box ui.Rect, ok bool) {
	w, ht := float32(1280), float32(820)
	if h.size != [2]int{} {
		w, ht = float32(h.size[0]), float32(h.size[1])
	}
	x0, y0, x1, y1 := w, ht, float32(-1), float32(-1)
	for y := float32(0); y < ht; y += 2 {
		for x := float32(0); x < w; x += 8 {
			if h.tt.WebViewAt(x, y) == v {
				x0, y0, x1, y1 = min(x0, x), min(y0, y), max(x1, x), max(y1, y)
			}
		}
	}
	if x1 < 0 {
		return ui.Rect{}, false
	}
	return ui.Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}, true
}

// seeBody waits until message's body page shows and returns it.
func (h *harness) seeBody(message int64) *fakeBodyView {
	h.t.Helper()
	var v *fakeBodyView
	h.until(fmt.Sprintf("message %d's body page shown", message), func() bool {
		v = h.plat.view(message)
		if v == nil {
			return false
		}
		_, ok := h.viewBox(v)
		return ok
	})
	return v
}

func (h *harness) seeTranslation(message int64, text string) *fakeBodyView {
	h.t.Helper()
	v := h.seeBody(message)
	h.until("the body's translated text", func() bool {
		for _, translated := range v.translations {
			if strings.Contains(translated, text) {
				return true
			}
		}
		return false
	})
	return v
}

// countCalls counts the platform calls equal to call.
func (h *harness) countCalls(call string) int {
	n := 0
	for _, c := range h.plat.Calls() {
		if c == call {
			n++
		}
	}
	return n
}

// threeMessages is a thread of three messages, oldest first, the oldest
// read: the reader opens the other two.
func threeMessages(t *testing.T, h *harness, acc core.Account) (thread int64, msgs []core.MessageSummary) {
	t.Helper()
	base := h.clock.Now()
	h.put(acc, "INBOX", coretest.Msg("bob@example.com", acc.Email, "Plan", "first words", coretest.MessageID("a@x"), at(base, 3)))
	h.put(acc, "INBOX", coretest.Msg("carol@example.com", acc.Email, "Re: Plan", "second words", coretest.MessageID("b@x"), coretest.InReplyTo("a@x"), at(base, 2)))
	h.put(acc, "INBOX", coretest.Msg("dave@example.com", acc.Email, "Re: Plan", "third words", coretest.MessageID("c@x"), coretest.InReplyTo("b@x"), at(base, 1)))
	h.syncNow(acc)
	page, _ := h.env.Svc.ListThreads(context.Background(), core.View{Unified: "inbox"}, "", 10)
	thread = page.Items[0].ID
	msgs = h.messages(thread)
	if len(msgs) != 3 {
		t.Fatalf("%d messages in the thread", len(msgs))
	}
	if err := h.env.Svc.SetFlags(context.Background(), []int64{msgs[0].ID}, []string{`\Seen`}, nil); err != nil {
		t.Fatal(err)
	}
	return thread, msgs
}

// Review Focus 3: a page per expanded message, closed on fold and thread
// change, made again on re-expand; none is left over.
func TestReaderWebViewsLifecycle(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	thread, msgs := threeMessages(t, h, accs[0])
	other := h.deliverOne(accs[0], coretest.Msg("erin@example.com", accs[0].Email, "Elsewhere", "other body"))
	h.openThreadWith(thread, msgs[1].ID)
	v1, v2 := h.seeBody(msgs[1].ID), h.seeBody(msgs[2].ID)
	if h.plat.view(msgs[0].ID) != nil || h.plat.openViews() != 2 {
		t.Fatalf("%d pages, the read one's %v", h.plat.openViews(), h.plat.view(msgs[0].ID))
	}
	if b1, _ := h.viewBox(v1); func() bool { b2, _ := h.viewBox(v2); return b1.Y >= b2.Y }() {
		t.Fatal("the pages are not in the thread's order")
	}
	r := h.app.mail.reader
	r.toggle(msgs[1].ID)
	h.tt.Frame()
	if !v1.closed || h.plat.view(msgs[1].ID) != nil {
		t.Fatal("folding kept the page")
	}
	if r.bodies[msgs[1].ID].view != nil {
		t.Fatal("the folded body still holds its page")
	}
	r.toggle(msgs[1].ID)
	if again := h.seeBody(msgs[1].ID); again == v1 {
		t.Fatal("re-expanding reused the closed page")
	}
	h.openThread(other.ThreadID)
	h.seeBody(other.ID)
	if h.plat.openViews() != 1 || h.plat.view(msgs[1].ID) != nil || h.plat.view(msgs[2].ID) != nil || !v2.closed {
		t.Fatalf("pages left after the switch: %d", h.plat.openViews())
	}
	if made, closed := h.countPrefix("NewBodyView"), h.countPrefix("CloseBodyView"); made-closed != h.plat.openViews() {
		t.Fatalf("%d pages made, %d closed, %d open", made, closed, h.plat.openViews())
	}
	// Off the mail page the reader keeps its thread: its page hides.
	h.app.navigate(Route{Page: PageSettings, Settings: "general"})
	h.tt.Frame()
	if _, ok := h.viewBox(h.plat.view(other.ID)); ok {
		t.Fatal("the body's page shows on the settings page")
	}
}

func (h *harness) countPrefix(prefix string) int {
	n := 0
	for _, c := range h.plat.Calls() {
		if strings.HasPrefix(c, prefix+" ") {
			n++
		}
	}
	return n
}

// gateBody makes the reader's reads of message wait for the returned
// release.
func gateBody(r *reader, message int64) (reading <-chan struct{}, release func()) {
	in, gate := make(chan struct{}, 1), make(chan struct{})
	real := r.getMessage
	r.getMessage = func(ctx context.Context, id int64, remote bool) (core.Message, error) {
		if id == message {
			select {
			case in <- struct{}{}:
			default:
			}
			<-gate
		}
		return real(ctx, id, remote)
	}
	var once sync.Once
	return in, func() { once.Do(func() { close(gate) }) }
}

// A body that arrives after its message folded gets no page; expanding
// it again makes one.
func TestReaderFoldDuringLoad(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	thread, msgs := threeMessages(t, h, accs[0])
	r := h.app.mail.reader
	reading, release := gateBody(r, msgs[1].ID)
	defer release()
	h.openThreadWith(thread, msgs[1].ID)
	h.until("the body being read", func() bool {
		select {
		case <-reading:
			return true
		default:
			return false
		}
	})
	r.toggle(msgs[1].ID) // fold it while it loads
	release()
	h.until("the body read", func() bool { b := r.bodies[msgs[1].ID]; return b != nil && b.ready })
	h.tt.Frame()
	if h.plat.view(msgs[1].ID) != nil || h.hasCall(fmt.Sprint("NewBodyView ", msgs[1].ID)) {
		t.Fatal("a folded message got a page")
	}
	r.toggle(msgs[1].ID)
	h.seeBody(msgs[1].ID)
}

// A thread switch while a page is being made (NewBodyView may run the
// event loop) closes it: nothing of the old thread shows.
func TestReaderSwitchDuringCreate(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	m := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "First", "first body"))
	other := h.deliverOne(accs[0], coretest.Msg("carol@example.com", accs[0].Email, "Second", "second body"))
	switched := false
	h.plat.onNewView = func(id int64) {
		if id == m.ID && !switched {
			switched = true
			h.app.mail.open([]mailItem{{ID: other.ThreadID, Thread: true}}, 0)
		}
	}
	h.openThread(m.ThreadID)
	h.seeBody(other.ID)
	if !switched {
		t.Fatal("the first page was never made")
	}
	if h.plat.view(m.ID) != nil || h.countCalls(fmt.Sprint("CloseBodyView ", m.ID)) != 1 {
		t.Fatalf("the old thread's page was kept: %q", h.plat.Calls())
	}
	if b := h.app.mail.reader.bodies[m.ID]; b != nil {
		t.Fatal("the old thread's body is still held")
	}
}

// A page that could not be made shows as an error with Retry.
func TestReaderBodyViewError(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	m := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "Hi", "the body"))
	h.plat.mu.Lock()
	h.plat.viewErr = errors.New("no web view today")
	h.plat.mu.Unlock()
	h.openThread(m.ThreadID)
	h.see("no web view today")
	h.plat.mu.Lock()
	h.plat.viewErr = nil
	h.plat.mu.Unlock()
	h.click("重试")
	h.seeBody(m.ID)
}

// Translation and 显示原文 keep the same HTML body page visible.
func TestReaderTranslationKeepsView(t *testing.T) {
	h, acc, llm := aiHarness(t, true, noAuto)
	llm.Script(coretest.Turn{Text: []string{`{"texts":["Hallo Welt, das ist die Übersetzung des Textes."],"source":"en"}`}})
	m := h.deliverOne(acc, coretest.Msg("bob@example.com", acc.Email, "Hi", "the original words"))
	h.openThread(m.ThreadID)
	v := h.seeBody(m.ID)
	h.click("翻译")
	h.seeTranslation(m.ID, "Welt")
	if v.closed {
		t.Fatal("the translation closed the page")
	}
	h.click("显示原文")
	if h.seeBody(m.ID) != v {
		t.Fatal("the original is a new page")
	}
}

func TestReaderTranslationKeepsOriginalUntilSuccess(t *testing.T) {
	for _, answer := range []string{`{"texts":["你好"],"source":"en"}`, `{"texts":[]}`} {
		t.Run(answer, func(t *testing.T) {
			h, acc, llm := aiHarness(t, true, noAuto)
			llm.Script(coretest.Turn{Text: []string{answer}, Hang: true})
			m := h.deliverOne(acc, coretest.Msg("bob@example.com", acc.Email, "Hi", "Hello"))
			h.openThread(m.ThreadID)
			v := h.seeBody(m.ID)
			h.click("翻译")
			h.until("translation waiting", func() bool { return llm.Requests() == 1 })
			if h.seeBody(m.ID) != v || len(v.translations) != 0 {
				t.Fatal("translation hid or changed the original while waiting")
			}
			llm.Release()
			r := h.app.mail.reader
			h.until("translation finished", func() bool { return !r.ai[m.ID].translation.running })
			if r.ai[m.ID].translation.err == nil {
				h.seeTranslation(m.ID, "你好")
			} else if h.seeBody(m.ID) != v || len(v.translations) != 0 {
				t.Fatal("failed translation replaced the original")
			}
		})
	}
}

// The page's reported height is its element's.
func TestReaderHeightApplied(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.resize(1280, 1600)
	m := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "Tall", "body"))
	h.openThread(m.ThreadID)
	v := h.seeBody(m.ID)
	if box, _ := h.viewBox(v); box.H > 40 {
		t.Fatalf("height %v before any report, want about a line", box.H)
	}
	h.app.BodyHeight(v, m.ID, 640)
	h.tt.Advance(2 * theme.MotionBase) // the message grows to hold it
	if box, _ := h.viewBox(v); box.H < 636 || box.H > 640 {
		t.Fatalf("height %v, want 640", box.H)
	}
	h.app.BodyHeight(v, m.ID+1000, 10) // not its message: ignored
	h.tt.Frame()
	if box, _ := h.viewBox(v); box.H < 636 {
		t.Fatalf("another message's height applied: %v", box.H)
	}
}

// Review Focus 4: a wheel the body did not use scrolls the reader,
// within its content.
func TestReaderWheelMovesScroll(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	m := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "Tall", "body"))
	h.openThread(m.ThreadID)
	v := h.seeBody(m.ID)
	r := h.app.mail.reader
	h.app.BodyHeight(v, m.ID, 3000)
	h.tt.Frame()
	if r.scroll.MaxY < 1000 {
		t.Fatalf("MaxY %v: the reader does not scroll", r.scroll.MaxY)
	}
	h.app.BodyWheel(v, 120)
	h.tt.Frame()
	if r.scroll.Y != 120 {
		t.Fatalf("Y %v, want 120", r.scroll.Y)
	}
	h.app.BodyWheel(v, -1000)
	if r.scroll.Y != 0 {
		t.Fatalf("Y %v, want 0", r.scroll.Y)
	}
	h.app.BodyWheel(v, 1e9)
	if r.scroll.Y != r.scroll.MaxY {
		t.Fatalf("Y %v, want MaxY %v", r.scroll.Y, r.scroll.MaxY)
	}
	h.openThread(h.deliverOne(accs[0], coretest.Msg("carol@example.com", accs[0].Email, "Short", "other")).ThreadID)
	if r.scroll.Y != 0 {
		t.Fatalf("another thread starts at %v", r.scroll.Y)
	}
}

// 加载图片 lets the body's page load the remote images (Reader.Allow);
// the notice waits until the page's body has them (BodyBuilt), and offers
// a retry when reading it failed (BodyFailed). Re-expanding asks again for
// the new page. Native makes no request itself.
func TestReaderRemoteImages(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
	}))
	defer srv.Close()
	h, accs := mailHarness(t, nil, "alice@example.com")
	remoteImages(t, h, "ask")
	acc, base := accs[0], h.clock.Now()
	h.put(acc, "INBOX", coretest.Msg("bob@example.com", acc.Email, "News", "first", coretest.MessageID("n1@x"), at(base, 2)))
	h.put(acc, "INBOX", coretest.Msg("bob@example.com", acc.Email, "Re: News", "text", coretest.MessageID("n2@x"), coretest.InReplyTo("n1@x"), at(base, 1),
		coretest.HTML(`<p>Hello reader</p><img src="`+srv.URL+`/pixel.png" width="2" height="2">`)))
	h.syncNow(acc)
	page, _ := h.env.Svc.ListThreads(context.Background(), core.View{Unified: "inbox"}, "", 10)
	msgs := h.messages(page.Items[0].ID)
	m := msgs[1]
	h.openThread(m.ThreadID)
	v := h.seeBody(m.ID)
	h.see("1 张图片未加载")
	if h.hasCall("AllowImages") {
		t.Fatal("allowed before 加载图片")
	}
	h.click("加载")
	allow := fmt.Sprint("AllowImages ", m.ID)
	if h.countCalls(allow) != 1 {
		t.Fatalf("calls %q", h.plat.Calls())
	}
	h.tt.Frame()
	if _, link := h.tt.Find("加载"); !h.tt.HasText("张图片未加载") || link {
		t.Fatalf("the notice did not wait for the page: %q", h.tt.Texts())
	}
	h.app.BodyFailed(v, m.ID, errors.New("images went away"))
	h.see("images went away")
	h.click("重试")
	if h.countCalls(allow) != 2 {
		t.Fatalf("the retry did not allow again: %q", h.plat.Calls())
	}
	h.app.BodyBuilt(v, m.ID, false, false) // an older body, without them
	h.tt.Frame()
	if !h.tt.HasText("张图片未加载") {
		t.Fatal("the notice went before the page had the images")
	}
	h.app.BodyBuilt(v, m.ID, false, true)
	h.until("the notice gone", func() bool { return !h.tt.HasText("张图片未加载") && !h.tt.HasText("images went away") })
	r := h.app.mail.reader
	r.toggle(m.ID)
	r.toggle(m.ID)
	h.seeBody(m.ID)
	calls := h.plat.Calls()
	lastView := 0
	for i, c := range calls {
		if c == fmt.Sprint("NewBodyView ", m.ID) {
			lastView = i
		}
	}
	if h.countCalls(allow) != 3 || !slices.Contains(calls[lastView:], allow) {
		t.Fatalf("re-expanding did not allow the new page: %q", calls)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 0 {
		t.Fatalf("native made %d image requests", hits)
	}
}

// The truncated notice follows the body as the page's Reader built it
// (OnBuilt → BodyBuilt): the structural limits included.
func TestReaderTruncatedNotice(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.plat.tempDir = t.TempDir()
	m := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "Long", "text",
		coretest.HTML(strings.Repeat("<p>line</p>", 5100))))
	h.openThread(m.ThreadID)
	v := h.seeBody(m.ID)
	if h.tt.HasText("正文过长，只显示了开头") {
		t.Fatal("the notice shows before the body was built")
	}
	h.app.BodyBuilt(v, m.ID, true, false)
	h.see("正文过长，只显示了开头")
	h.scrollTo("在浏览器中查看")
	h.click("在浏览器中查看")
	if u := h.callArg("OpenURL"); !strings.HasPrefix(u, "file://") {
		t.Fatalf("opened %q", u)
	}
}

// A page's calls queued before it was closed or replaced are dropped:
// height, wheel, link, navigation and what its body was built as.
func TestReaderStaleCallbacksDropped(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	m := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "Hi", "body"))
	h.openThread(m.ThreadID)
	old := h.seeBody(m.ID)
	r := h.app.mail.reader
	r.toggle(m.ID)
	r.toggle(m.ID)
	v := h.seeBody(m.ID)
	h.app.BodyHeight(v, m.ID, 3000)
	h.tt.Frame()
	route := h.app.route
	h.app.BodyHeight(old, m.ID, 50)
	h.app.BodyWheel(old, 200)
	h.app.BodyLink(old, "https://shop.example.com/")
	h.app.BodyNavigate(old, -1)
	h.app.BodyBuilt(old, m.ID, true, true)
	h.tt.Frame()
	b := r.bodies[m.ID]
	if b.height != 3000 || r.scroll.Y != 0 || h.app.linkOpen || b.cut || b.remote || h.app.route != route {
		t.Fatalf("a closed page acted: height %d, Y %v, link %v, cut %v, remote %v, route %+v",
			b.height, r.scroll.Y, h.app.linkOpen, b.cut, b.remote, h.app.route)
	}
	h.app.BodyWheel(v, 200)
	h.app.BodyLink(v, "https://shop.example.com/")
	if r.scroll.Y != 200 || !h.app.linkOpen {
		t.Fatal("the current page's calls were dropped too")
	}
}

// ⌘F forwarded from a body's page opens the search palette; a closed
// page's is dropped.
func TestReaderBodySearch(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	m := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "Hi", "body"))
	h.openThread(m.ThreadID)
	old := h.seeBody(m.ID)
	r := h.app.mail.reader
	r.toggle(m.ID)
	r.toggle(m.ID)
	v := h.seeBody(m.ID)
	h.app.BodySearch(old)
	if h.app.palette.open {
		t.Fatal("a closed page opened the search")
	}
	h.app.BodySearch(v)
	if !h.app.palette.open {
		t.Fatal("the current page's ⌘F did not open the search")
	}
}

// Close closes every body page; one being made as the App closes is
// closed at once.
func TestReaderCloseClosesViews(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	thread, msgs := threeMessages(t, h, accs[0])
	h.openThreadWith(thread, msgs[1].ID)
	h.seeBody(msgs[1].ID)
	h.seeBody(msgs[2].ID)
	r := h.app.mail.reader
	r.toggle(msgs[0].ID) // read the oldest, then fold it
	h.seeBody(msgs[0].ID)
	r.toggle(msgs[0].ID)
	h.plat.onNewView = func(int64) { h.app.Close() }
	r.toggle(msgs[0].ID) // expand: its page is made as the App closes
	if n := h.plat.openViews(); n != 0 {
		t.Fatalf("%d pages open after Close", n)
	}
	if made, closed := h.countPrefix("NewBodyView"), h.countPrefix("CloseBodyView"); made != closed {
		t.Fatalf("%d pages made, %d closed", made, closed)
	}
}

// 始终信任此发件人 trusts the From address in core and loads the images;
// the sender's next message loads them at once, without the notice.
func TestReaderTrustSender(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	remoteImages(t, h, "trusted")
	acc := accs[0]
	img := coretest.HTML(`<p>Hi</p><img src="https://img.example/p.png" width="2" height="2">`)
	m := h.deliverOne(acc, coretest.Msg("Bob <Bob@Example.com>", acc.Email, "News", "text", img))
	h.openThread(m.ThreadID)
	h.seeBody(m.ID)
	h.see("1 张图片未加载")
	h.click("信任发件人")
	h.until("allowed", func() bool { return h.countCalls(fmt.Sprint("AllowImages ", m.ID)) == 1 })
	st, err := h.env.Svc.GetSettings(context.Background())
	if err != nil || !slices.Equal(st.TrustedImageSenders, []string{"bob@example.com"}) {
		t.Fatalf("trusted %q, %v", st.TrustedImageSenders, err)
	}
	n := h.deliverOne(acc, coretest.Msg("bob@example.com", acc.Email, "More news", "text", img))
	h.openThread(n.ThreadID)
	h.seeBody(n.ID)
	h.tt.Frame()
	if h.tt.HasText("张图片未加载") {
		t.Fatal("a trusted sender's message shows the notice")
	}
}

// Tracking pixels are noted apart from the remote images notice, and stay
// blocked for a trusted sender, whose other images load without a notice.
func TestReaderTrackersBlocked(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	remoteImages(t, h, "trusted")
	acc := accs[0]
	pixels := `<img src="https://t.example/o.gif" width="1" height="1"><img src="https://x.list-manage.com/track/open.php?u=1">`
	// two, so the body's count tells them from the sender's avatar, one image of its own
	photo := `<img src="https://img.example/p.png" width="2" height="2"><img src="https://img.example/q.png" width="2" height="2">`
	// the header's second line: "发给 我", then the trackers, then the images
	line := func(m core.MessageSummary, trackers, images bool) {
		t.Helper()
		h.openThread(m.ThreadID)
		h.seeBody(m.ID)
		h.see("发给 我")
		if got := h.tt.HasText("已阻止 2 个跟踪器"); got != trackers {
			t.Fatalf("%q: trackers shown %v, want %v: %q", m.Subject, got, trackers, h.tt.Texts())
		}
		if got := h.tt.HasText("2 张图片未加载"); got != images {
			t.Fatalf("%q: images shown %v, want %v: %q", m.Subject, got, images, h.tt.Texts())
		}
		if h.tt.HasText("张远程图片") {
			t.Fatalf("%q: a notice above the body: %q", m.Subject, h.tt.Texts())
		}
	}
	line(h.deliverOne(acc, coretest.Msg("carol@example.com", acc.Email, "Neither", "text", coretest.HTML(`<p>Hi</p>`))), false, false)
	line(h.deliverOne(acc, coretest.Msg("dave@example.com", acc.Email, "Trackers", "text", coretest.HTML(`<p>Hi</p>`+pixels))), true, false)
	line(h.deliverOne(acc, coretest.Msg("erin@example.com", acc.Email, "Images", "text", coretest.HTML(`<p>Hi</p>`+photo))), false, true)
	both := coretest.HTML(`<p>Hi</p>` + photo + pixels)
	m := h.deliverOne(acc, coretest.Msg("Bob <bob@example.com>", acc.Email, "Both", "text", both))
	line(m, true, true)
	h.click("信任发件人")
	h.until("allowed", func() bool { return h.countCalls(fmt.Sprint("AllowImages ", m.ID)) == 1 })
	// trusted: the next one shows its trackers alone
	line(h.deliverOne(acc, coretest.Msg("bob@example.com", acc.Email, "More", "text", both)), true, false)
}

// A quoted local part: core gets the From as sent, which parses.
func TestReaderTrustQuotedSender(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	remoteImages(t, h, "trusted")
	acc := accs[0]
	img := coretest.HTML(`<p>Hi</p><img src="https://img.example/p.png" width="2" height="2">`)
	m := h.deliverOne(acc, coretest.Msg(`Bob <"bob smith"@example.com>`, acc.Email, "News", "text", img))
	h.openThread(m.ThreadID)
	h.seeBody(m.ID)
	h.click("信任发件人")
	h.until("allowed", func() bool { return h.countCalls(fmt.Sprint("AllowImages ", m.ID)) == 1 })
}

// Trusting a sender loads its images in the thread, but not in a copy in Junk.
func TestReaderTrustSkipsJunk(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	remoteImages(t, h, "trusted")
	acc := accs[0]
	if err := h.env.IMAP(acc.ID).User.Create("Junk", nil); err != nil {
		t.Fatal(err)
	}
	img := coretest.HTML(`<p>Hi</p><img src="https://img.example/p.png" width="2" height="2">`)
	m := h.deliverOne(acc, coretest.Msg("Bob <bob@example.com>", acc.Email, "News", "text", img, coretest.MessageID("n1@x")))
	h.env.Deliver(t, acc.ID, "Junk", coretest.Msg("Bob <bob@example.com>", acc.Email, "Re: News", "text", img, coretest.InReplyTo("n1@x")))
	msgs := h.messages(m.ThreadID)
	if len(msgs) != 2 {
		t.Fatalf("thread of %d", len(msgs))
	}
	h.openThread(m.ThreadID)
	for _, x := range msgs {
		if r := h.app.mail.reader; !r.open[x.ID] {
			r.toggle(x.ID)
		}
		h.seeBody(x.ID)
	}
	h.click("信任发件人")
	h.until("allowed", func() bool { return h.countCalls(fmt.Sprint("AllowImages ", m.ID)) == 1 })
	for _, x := range msgs {
		if x.ID != m.ID && h.countCalls(fmt.Sprint("AllowImages ", x.ID)) != 0 {
			t.Fatal("the Junk copy's images loaded")
		}
	}
}

// remoteImages sets when remote images load (Settings.RemoteImages): the
// tests of the notice and 始终信任 need "ask" or "trusted", not the default.
func remoteImages(t *testing.T, h *harness, mode string) {
	t.Helper()
	st, err := h.env.Svc.GetSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	st.RemoteImages = mode
	if _, err := h.env.Svc.SetSettings(context.Background(), st); err != nil {
		t.Fatal(err)
	}
}

// A body's page is told how much of it the bars cover, as it scrolls under
// them: the reply bar its bottom at first, the toolbar its top once
// scrolled; again once it reports, as a page drops what came before it
// listened.
func TestReaderBodyGlass(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	m := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "Tall", "body"))
	h.openThread(m.ThreadID)
	v := h.seeBody(m.ID)
	r := h.app.mail.reader
	h.app.BodyHeight(v, m.ID, 3000)
	h.tt.Advance(2 * theme.MotionBase) // the message grows to hold it
	glass := func() [2]int {
		h.plat.mu.Lock()
		defer h.plat.mu.Unlock()
		return v.glass
	}
	// a band runs from its edge of the page to the bar's, out of sight too
	if g := glass(); g[0] != 0 || g[1] < int(r.barBottom) || g[1] >= 3000 {
		t.Fatalf("glass %v at the top (reply bar %v): want only the bottom covered", g, r.barBottom)
	}
	r.scroll.Y = 1000
	h.tt.Frame()
	h.tt.Frame()
	if g := glass(); g[0] < int(r.barTop) || g[1] < int(r.barBottom) {
		t.Fatalf("glass %v scrolled (bars %v, %v): want both covered", g, r.barTop, r.barBottom)
	}
	r.bodies[m.ID].glass = [2]int{}
	v.glass = [2]int{}
	h.app.BodyHeight(v, m.ID, 3000)
	h.tt.Frame()
	if g := glass(); g == ([2]int{}) {
		t.Fatal("not told again once the page reported")
	}
}
