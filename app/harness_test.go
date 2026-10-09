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

	mu    sync.Mutex
	queue []func()
}

func newHarness(t *testing.T, setup ...func(*coretest.Env)) *harness {
	t.Helper()
	return newHarnessWith(t, nil, setup...)
}

// newHarnessWith is newHarness on an Env started with opts, as
// coretest.WithoutAutoSync for tests that need every sync to be their own.
func newHarnessWith(t *testing.T, opts []coretest.Option, setup ...func(*coretest.Env)) *harness {
	t.Helper()
	h := &harness{t: t, env: coretest.New(t, opts...), plat: &fakePlatform{}, clock: newFakeClock()}
	for _, s := range setup {
		s(h.env)
	}
	h.app = New(Deps{Svc: h.env.Svc, Platform: h.plat, Post: h.post, Clock: h.clock})
	h.tt = ui.NewTester(h.app.View, 1280, 820)
	ctx, stop := context.WithCancel(context.Background())
	h.stop = stop
	t.Cleanup(stop)
	h.app.Start(ctx)
	t.Cleanup(h.app.Close) // before the Env's cleanup closes the Service
	return h
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

func (h *harness) see(text string) {
	h.t.Helper()
	h.until(fmt.Sprintf("text %q", text), func() bool { return h.tt.HasText(text) })
}

func (h *harness) click(label string) {
	h.t.Helper()
	h.see(label)
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
	clipHTML  string
	tempDir   string
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
	return nil
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
func (p *fakePlatform) SetTrayUnread(n int)          { p.record("SetTrayUnread", n) }
func (p *fakePlatform) SetThemeSource(theme string)  { p.record("SetThemeSource", theme) }
func (p *fakePlatform) ClipboardHTML() string {
	p.record("ClipboardHTML")
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.clipHTML
}
func (p *fakePlatform) TempDir() string {
	p.record("TempDir")
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.tempDir
}
