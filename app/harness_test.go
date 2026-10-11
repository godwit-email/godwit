package app

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
)

// harness runs an App on a real coretest Service in a headless Tester. Posted
// callbacks queue up and run only on the test goroutine (until, advance, click,
// typeText), as they would on the UI thread.
type harness struct {
	t     *testing.T
	env   *coretest.Env
	app   *App
	tt    *ui.Tester
	plat  *fakePlatform
	clock *fakeClock
	stop  context.CancelFunc // cancels the ctx the App was started with
	size  [2]int             // the window's, once resize set it; else 1280×820

	mu    sync.Mutex
	queue []func()
	logs  []string // what the App logged
}

func newHarness(t *testing.T, setup ...func(*coretest.Env)) *harness {
	t.Helper()
	return newHarnessWith(t, nil, setup...)
}

// newHarnessWith is newHarness on an Env started with opts, as
// coretest.WithoutAutoSync for tests that need every sync to be their own.
func newHarnessWith(t *testing.T, opts []coretest.Option, setup ...func(*coretest.Env)) *harness {
	t.Helper()
	return newHarnessDev(t, false, opts, setup...)
}

// newDevHarness is newHarness on a development build (Deps.Dev).
func newDevHarness(t *testing.T, setup ...func(*coretest.Env)) *harness {
	t.Helper()
	return newHarnessDev(t, true, nil, setup...)
}

func newHarnessDev(t *testing.T, dev bool, opts []coretest.Option, setup ...func(*coretest.Env)) *harness {
	t.Helper()
	h := &harness{t: t, env: coretest.New(t, opts...), plat: &fakePlatform{}, clock: newFakeClock()}
	for _, s := range setup {
		s(h.env)
	}
	h.app = New(Deps{Svc: h.env.Svc, Platform: h.plat, Post: h.post, Clock: h.clock, SystemLanguage: "zh-CN", Logf: h.logf, Dev: dev})
	h.tt = ui.NewTester(h.app.View, 1280, 820)
	ctx, stop := context.WithCancel(context.Background())
	h.stop = stop
	t.Cleanup(stop)
	h.app.Start(ctx)
	t.Cleanup(h.app.Close) // before the Env's cleanup closes the Service
	return h
}

func (h *harness) logf(format string, args ...any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.logs = append(h.logs, fmt.Sprintf(format, args...))
}

func (h *harness) logged() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.logs)
}

func (h *harness) post(f func()) {
	h.mu.Lock()
	h.queue = append(h.queue, f)
	h.mu.Unlock()
}

// drain runs posted callbacks, including ones they post, and reports whether any ran.
func (h *harness) drain() bool {
	ran := false
	for {
		h.mu.Lock()
		q := h.queue
		h.queue = nil
		h.mu.Unlock()
		if len(q) == 0 {
			return ran
		}
		for _, f := range q {
			f()
		}
		ran = true
	}
}

// until drains the queue and renders frames until cond holds, failing after 10 real seconds.
func (h *harness) until(desc string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		h.drain()
		h.tt.Frame()
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %s; texts %q", desc, h.tt.Texts())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// queued waits until at least n callbacks are posted and not yet drained,
// failing after 10 real seconds.
func (h *harness) queued(desc string, n int) {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		h.mu.Lock()
		got := len(h.queue)
		h.mu.Unlock()
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %s to be posted", desc)
		}
		time.Sleep(time.Millisecond)
	}
}

// advance moves the fake clock, fires due timers, then drains the queue.
func (h *harness) advance(d time.Duration) {
	h.clock.advance(d)
	h.drain()
	h.tt.Frame()
}

// resize makes the window w×ht DIPs.
func (h *harness) resize(w, ht int) {
	h.size = [2]int{w, ht}
	h.tt.SetSize(w, ht)
	h.tt.Frame()
}

func (h *harness) see(text string) {
	h.t.Helper()
	h.until(fmt.Sprintf("text %q", text), func() bool { return h.tt.HasText(text) })
}

// click waits for an element whose text or label is exactly label (see
// matches a part of a text, as "m1" in "test / m1"), scrolls it into the
// window, then clicks it.
func (h *harness) click(label string) {
	h.t.Helper()
	h.until(fmt.Sprintf("an element %q", label), func() bool { _, ok := h.tt.Find(label); return ok })
	if r, _ := h.tt.Find(label); r.Y < 0 || r.Y+r.H > 820-8 { // out of the panel (inset 8 from the window's bottom): scroll it to the middle
		h.tt.Scroll(900, 400, 0, r.Y-400)
		h.tt.Frame()
	}
	if err := h.tt.Click(label); err != nil {
		h.t.Fatal(err)
	}
	h.drain()
	h.tt.Frame()
}

func (h *harness) typeText(s string) {
	h.tt.Type(s)
	h.drain()
	h.tt.Frame()
}

// fakeClock fires AfterFunc callbacks only from advance, in due order.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	at      time.Time
	f       func()
	stopped bool
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 9, 9, 0, 0, 0, time.Local)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	tm := &fakeTimer{at: c.now.Add(d), f: f}
	c.timers = append(c.timers, tm)
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		was := !tm.stopped && slices.Contains(c.timers, tm)
		tm.stopped = true
		return was
	}
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []*fakeTimer
	c.timers = slices.DeleteFunc(c.timers, func(tm *fakeTimer) bool {
		if tm.stopped || !tm.at.After(c.now) {
			if !tm.stopped {
				due = append(due, tm)
			}
			return true
		}
		return false
	})
	c.mu.Unlock()
	slices.SortStableFunc(due, func(a, b *fakeTimer) int { return a.at.Compare(b.at) })
	for _, tm := range due {
		tm.f()
	}
}

// fakePlatform records every call; dialogs return the preset answers.
type fakePlatform struct {
	mu        sync.Mutex
	calls     []string
	notes     []Notification
	files     []string // OpenFiles
	dir       string   // OpenDirectory
	savePath  string   // SaveFile
	supported bool     // NotificationsSupported
	notifyErr error    // Notify
	tempDir   string

	localAPIPath string // SetLocalAPI(true)'s answer
	localAPIErr  error
	localAPIFn   func(on bool) (string, error) // answers SetLocalAPI in their place when set
	dataDir      string                        // DataDir
	customDir    bool

	views     map[int64]*fakeBodyView // the body pages open, by message
	onNewView func(message int64)     // runs inside NewBodyView, as the event loop may run there
	viewErr   error                   // NewBodyView's error
}

// fakeBodyView stands for a reader page's *mygo.WebView, bound to message.
type fakeBodyView struct {
	ui.NativeWebView
	message int64
	closed  bool
}

// NewBodyView records "NewBodyView <message>" and makes a fake page;
// closing it records "CloseBodyView <message>". A second close fails the
// test, as would a second page for one message.
func (p *fakePlatform) NewBodyView(message int64) (ui.NativeWebView, func(), error) {
	p.record("NewBodyView", message)
	if f := p.onNewView; f != nil {
		f(message)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.viewErr != nil {
		return nil, nil, p.viewErr
	}
	if p.views == nil {
		p.views = map[int64]*fakeBodyView{}
	}
	if p.views[message] != nil {
		panic(fmt.Sprintf("a second body page for message %d", message))
	}
	v := &fakeBodyView{message: message}
	p.views[message] = v
	return v, func() {
		p.record("CloseBodyView", message)
		p.mu.Lock()
		defer p.mu.Unlock()
		if v.closed {
			panic(fmt.Sprintf("message %d's body page closed twice", message))
		}
		v.closed = true
		delete(p.views, message)
	}, nil
}

func (p *fakePlatform) AllowImages(message int64) { p.record("AllowImages", message) }

// view is message's open body page, or nil.
func (p *fakePlatform) view(message int64) *fakeBodyView {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.views[message]
}

// openViews is how many body pages are open.
func (p *fakePlatform) openViews() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.views)
}

func (p *fakePlatform) record(name string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, strings.TrimSpace(fmt.Sprintln(append([]any{name}, args...)...)))
}

// Calls returns the recorded calls, e.g. "OpenURL https://x", "SetOpenAtLogin true".
func (p *fakePlatform) Calls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.calls)
}

func (p *fakePlatform) OpenURL(url string) error   { p.record("OpenURL", url); return nil }
func (p *fakePlatform) OpenPath(path string) error { p.record("OpenPath", path); return nil }
func (p *fakePlatform) OpenFiles(multiple bool) ([]string, error) {
	p.record("OpenFiles", multiple)
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.files), nil
}
func (p *fakePlatform) OpenDirectory() (string, error) {
	p.record("OpenDirectory")
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dir, nil
}
func (p *fakePlatform) SaveFile(defaultName string) (string, error) {
	p.record("SaveFile", defaultName)
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.savePath, nil
}
func (p *fakePlatform) Notify(n Notification) error {
	p.record("Notify", n.ID)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.notes = append(p.notes, n)
	return p.notifyErr
}
func (p *fakePlatform) Notes() []Notification {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.notes)
}
func (p *fakePlatform) NotificationsSupported() bool {
	p.record("NotificationsSupported")
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.supported
}
func (p *fakePlatform) SetOpenAtLogin(on bool) error { p.record("SetOpenAtLogin", on); return nil }
func (p *fakePlatform) SetLocalAPI(_ uint64, on bool) (string, error) {
	p.record("SetLocalAPI", on)
	p.mu.Lock()
	defer p.mu.Unlock()
	if fn := p.localAPIFn; fn != nil {
		p.mu.Unlock()
		defer p.mu.Lock()
		return fn(on)
	}
	if !on {
		return "", nil
	}
	return p.localAPIPath, p.localAPIErr
}
func (p *fakePlatform) DataDir() (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dataDir, p.customDir
}
func (p *fakePlatform) SetTrayUnread(n int)         { p.record("SetTrayUnread", n) }
func (p *fakePlatform) ShowWindow()                 { p.record("ShowWindow") }
func (p *fakePlatform) SetThemeSource(theme string) { p.record("SetThemeSource", theme) }
func (p *fakePlatform) TempDir() string {
	p.record("TempDir")
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.tempDir
}
func (p *fakePlatform) OpenCompose(draftID int64)        { p.record("OpenCompose", draftID) }
func (p *fakePlatform) SettingsChanged(st core.Settings) { p.record("SettingsChanged", st.Theme) }
func (p *fakePlatform) DraftDeleted(id int64)            { p.record("DraftDeleted", id) }
