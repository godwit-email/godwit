package webui

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeWin is a Window that counts what was done to it. Its Destroy reports
// Closed synchronously, as mygo does, unless async: then the test does.
type fakeWin struct {
	id                         int
	ws                         *Windows
	async                      bool
	focused, emitted, destroys atomic.Int32
	gen                        atomic.Int32 // the last Closing's
}

func (w *fakeWin) ID() int             { return w.id }
func (w *fakeWin) Focus()              { w.focused.Add(1) }
func (w *fakeWin) EmitClosing(gen int) { w.emitted.Add(1); w.gen.Store(int32(gen)) }
func (w *fakeWin) Destroy() {
	w.destroys.Add(1)
	if !w.async {
		w.ws.Closed(w.id)
	}
}

// closing is the attempt the page answers: the last Closing's.
func (w *fakeWin) closing() int    { return int(w.gen.Load()) }
func (w *fakeWin) destroyed() bool { return w.destroys.Load() > 0 }

// winFixture is Windows on a real Compose, with fake windows, a scripted
// ask and a clock that fires only when told.
type winFixture struct {
	*fixture
	ws     *Windows
	made   []*fakeWin
	answer atomic.Bool // what ask says: true = close anyway
	asked  atomic.Int32
	asking chan bool   // when set, ask waits on it for its answer instead
	block  func(n int) // when set, runs before window n (from 1) is made
	mu     sync.Mutex
	inNew  int // windows being made
	timers []func()
}

func newWinFixture(t *testing.T) *winFixture {
	t.Helper()
	f := &winFixture{fixture: newFixture(t)}
	f.ws = NewWindows(f.c, func(int64) Window {
		f.mu.Lock()
		n := len(f.made) + 1 + f.inNew
		f.inNew++
		block := f.block
		f.mu.Unlock()
		if block != nil {
			block(n)
		}
		f.mu.Lock()
		f.inNew--
		defer f.mu.Unlock()
		w := &fakeWin{id: len(f.made) + 1, ws: f.ws}
		f.made = append(f.made, w)
		return w
	}, func(Window) bool {
		f.asked.Add(1)
		if f.asking != nil {
			return <-f.asking
		}
		return f.answer.Load()
	})
	f.ws.after = func(d time.Duration, fn func()) func() bool {
		assert.Equal(t, 5*time.Second, d)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.timers = append(f.timers, fn)
		return func() bool { return true }
	}
	f.ws.spawn = func(fn func()) { fn() } // the release in line: these tests do not race a call
	return f
}

// elapse fires every timer set so far, as if 5 s went by.
func (f *winFixture) elapse() {
	f.mu.Lock()
	ts := f.timers
	f.timers = nil
	f.mu.Unlock()
	for _, fn := range ts {
		fn()
	}
}

func TestWindowsOpenFocusesExisting(t *testing.T) {
	f := newWinFixture(t)
	f.ws.Open(7)
	f.ws.Open(7)
	require.Len(t, f.made, 1)
	assert.EqualValues(t, 1, f.made[0].focused.Load())
	assert.Equal(t, 1, f.ws.Count())
}

func TestWindowsCloseWaitsForSave(t *testing.T) {
	f := newWinFixture(t)
	d := f.draft(t, "new", 0)
	f.ws.Open(d.ID)
	w := f.made[0]
	st, err := f.page.Load(f.ctx(w.id))
	require.NoError(t, err)
	f.ws.RequestClose(w.id)
	assert.EqualValues(t, 1, w.emitted.Load())
	assert.False(t, w.destroyed())
	// The page flushes, then reports.
	_, err = f.page.Save(f.ctx(w.id), ComposeEdit{Body: "<p>最后一句</p>", Revision: st.Revision})
	require.NoError(t, err)
	require.NoError(t, f.page.Closed(f.ctx(w.id), w.closing(), true))
	assert.True(t, w.destroyed())
	assert.Zero(t, f.asked.Load())
	assert.Zero(t, f.ws.Count())
	stored, err := f.e.Svc.GetDraft(t.Context(), d.ID)
	require.NoError(t, err)
	ed, _, _ := splitOf(stored.HTML)
	assert.Equal(t, "<p>最后一句</p>", ed)
	f.elapse() // the stopped timer, if a clock ran it anyway
	assert.EqualValues(t, 1, w.destroys.Load())
	assert.Zero(t, f.asked.Load())
}

func TestWindowsCloseFailedSaveAsks(t *testing.T) {
	f := newWinFixture(t)
	f.ws.Open(f.draft(t, "new", 0).ID)
	w := f.made[0]
	f.ws.RequestClose(w.id)
	require.NoError(t, f.page.Closed(f.ctx(w.id), w.closing(), false))
	assert.EqualValues(t, 1, f.asked.Load())
	assert.False(t, w.destroyed(), "返回 keeps the window")
	assert.Equal(t, 1, f.ws.Count())

	f.answer.Store(true)
	f.ws.RequestClose(w.id)
	assert.EqualValues(t, 2, w.emitted.Load())
	require.NoError(t, f.page.Closed(f.ctx(w.id), w.closing(), false))
	assert.EqualValues(t, 2, f.asked.Load())
	assert.True(t, w.destroyed(), "关闭 closes it")
}

func TestWindowsSlowPageAsksNotCloses(t *testing.T) {
	f := newWinFixture(t)
	f.ws.Open(f.draft(t, "new", 0).ID)
	w := f.made[0]
	f.ws.RequestClose(w.id)
	f.ws.RequestClose(w.id) // a second click while waiting asks the page nothing new
	assert.EqualValues(t, 1, w.emitted.Load())
	f.elapse()
	assert.EqualValues(t, 1, f.asked.Load())
	assert.False(t, w.destroyed())
	require.NoError(t, f.page.Closed(f.ctx(w.id), w.closing(), true)) // too late: the user chose 返回
	assert.False(t, w.destroyed())

	f.answer.Store(true)
	f.ws.RequestClose(w.id)
	f.elapse()
	assert.EqualValues(t, 2, f.asked.Load())
	assert.True(t, w.destroyed())
}

func TestWindowsClosedReleases(t *testing.T) {
	f := newWinFixture(t)
	d := f.draft(t, "new", 0)
	f.ws.Open(d.ID)
	w := f.made[0]
	f.ws.Closed(w.id)
	_, err := f.page.Load(f.ctx(w.id))
	assert.ErrorIs(t, err, ErrNoDraft)
	assert.True(t, f.gone(t, d.ID), "the empty draft is deleted")
	assert.Zero(t, f.ws.Count())
}

// Closed runs on mygo's main thread: it never waits for the release, which
// waits for the window's call in progress. A quit still waits for it, so
// the empty draft is deleted before the process exits.
func TestWindowsClosedReleasesOffCaller(t *testing.T) {
	f := newWinFixture(t)
	f.ws.spawn = func(fn func()) { go fn() }
	d := f.draft(t, "new", 0)
	f.ws.Open(d.ID)
	w := f.made[0]
	w.async = true
	inSave, resume := make(chan struct{}), make(chan struct{})
	saveHook = func() { close(inSave); <-resume }
	t.Cleanup(func() { saveHook = nil })
	saved := make(chan error)
	go func() {
		_, err := f.page.Save(f.ctx(w.id), ComposeEdit{Body: "<p></p>", Revision: d.Revision})
		saved <- err
	}()
	<-inSave
	results := make(chan bool, 1)
	f.ws.CloseAll(func(ok bool) { results <- ok })
	require.NoError(t, f.page.Closed(f.ctx(w.id), w.closing(), true))
	require.True(t, w.destroyed())
	f.ws.Closed(w.id) // returns at once: the release waits for the save
	assert.Equal(t, 1, f.ws.Count(), "counted until released")
	select {
	case <-results:
		t.Fatal("the quit went on before the release")
	case <-time.After(50 * time.Millisecond):
	}
	close(resume)
	require.NoError(t, <-saved)
	assert.True(t, <-results)
	assert.True(t, f.gone(t, d.ID), "released before the quit went on")
	assert.Zero(t, f.ws.Count())
}

// windows is how many windows were made so far.
func (f *winFixture) windows() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.made)
}

// Opening a draft whose closed window is still being released waits for
// the release: a draft kept opens then; one deleted as empty does not.
func TestWindowsOpenWaitsForRelease(t *testing.T) {
	for _, tc := range []struct {
		body string
		kept bool
	}{{"<p>kept</p>", true}, {"<p></p>", false}} {
		t.Run(tc.body, func(t *testing.T) {
			f := newWinFixture(t)
			f.ws.spawn = func(fn func()) { go fn() }
			d := f.draft(t, "new", 0)
			f.ws.Open(d.ID)
			w := f.made[0]
			inSave, resume := make(chan struct{}), make(chan struct{})
			saveHook = func() { close(inSave); <-resume }
			t.Cleanup(func() { saveHook = nil })
			saved := make(chan error)
			go func() {
				_, err := f.page.Save(f.ctx(w.id), ComposeEdit{Body: tc.body, Revision: d.Revision})
				saved <- err
			}()
			<-inSave
			f.ws.Closed(w.id) // the release waits for the save
			f.ws.Open(d.ID)
			assert.Equal(t, 1, f.windows(), "no second window while the first is released")
			close(resume)
			require.NoError(t, <-saved)
			if tc.kept {
				require.Eventually(t, func() bool { return f.windows() == 2 }, 5*time.Second, time.Millisecond)
				require.Eventually(t, func() bool { return f.ws.Count() == 1 }, 5*time.Second, time.Millisecond)
				_, err := f.page.Load(f.ctx(f.made[1].id))
				assert.NoError(t, err)
				return
			}
			require.Eventually(t, func() bool { return f.ws.Count() == 0 }, 5*time.Second, time.Millisecond)
			assert.True(t, f.gone(t, d.ID))
			assert.Equal(t, 1, f.windows(), "no window for a deleted draft")
		})
	}
}

func TestWindowsDoneDestroysWithoutAsking(t *testing.T) {
	f := newWinFixture(t)
	f.ws.Open(f.draft(t, "new", 0).ID)
	f.ws.Open(f.draft(t, "new", 0).ID)
	w1, w2 := f.made[0], f.made[1]
	f.ws.Done(w1.id)
	assert.True(t, w1.destroyed())
	assert.Zero(t, w1.emitted.Load())
	assert.Zero(t, f.asked.Load())
	assert.Equal(t, 1, f.ws.Count())

	// A close in progress, then the page sends: the close settles.
	var results []bool
	f.ws.CloseAll(func(ok bool) { results = append(results, ok) })
	f.ws.Done(w2.id)
	f.elapse() // its timer, had it not stopped, finds nothing to ask
	assert.True(t, w2.destroyed())
	assert.Zero(t, f.asked.Load())
	assert.Equal(t, []bool{true}, results)
	f.ws.Done(w2.id) // gone: nothing
	assert.EqualValues(t, 1, w2.destroys.Load())
}

// The page sends while the close question shows: the window goes, and the
// answer to the question does nothing.
func TestWindowsDoneWhileAsking(t *testing.T) {
	f := newWinFixture(t)
	f.asking = make(chan bool)
	f.ws.Open(f.draft(t, "new", 0).ID)
	w := f.made[0]
	w.async = true
	var results []bool
	f.ws.CloseAll(func(ok bool) { results = append(results, ok) })
	answered := make(chan error)
	go func() { answered <- f.page.Closed(f.ctx(w.id), w.closing(), false) }()
	require.Eventually(t, func() bool { return f.asked.Load() == 1 }, 5*time.Second, time.Millisecond)
	f.ws.Done(w.id)
	assert.True(t, w.destroyed())
	f.asking <- false // 返回, too late
	require.NoError(t, <-answered)
	assert.Empty(t, results, "not kept: it is going")
	f.ws.Closed(w.id)
	assert.Equal(t, []bool{true}, results)
	assert.EqualValues(t, 1, w.destroys.Load())
}

func TestWindowsDraftDeletedCloses(t *testing.T) {
	f := newWinFixture(t)
	d7 := f.draft(t, "new", 0)
	f.ws.Open(d7.ID)
	f.ws.Open(f.draft(t, "new", 0).ID)
	w1, w2 := f.made[0], f.made[1]
	require.NoError(t, f.e.Svc.DeleteDraft(t.Context(), d7.ID))
	f.ws.CloseDraft(d7.ID)
	assert.True(t, w1.destroyed())
	assert.Zero(t, w1.emitted.Load())
	assert.Zero(t, f.asked.Load())
	assert.False(t, w2.destroyed())
	f.ws.CloseDraft(d7.ID + 100) // no window
	assert.False(t, w2.destroyed())
	assert.Equal(t, 1, f.ws.Count())
}

func TestWindowsCloseAllForQuit(t *testing.T) {
	f := newWinFixture(t)
	f.ws.Open(f.draft(t, "new", 0).ID)
	f.ws.Open(f.draft(t, "new", 0).ID)
	w1, w2 := f.made[0], f.made[1]
	var results []bool
	f.ws.CloseAll(func(ok bool) { results = append(results, ok) })
	assert.EqualValues(t, 1, w1.emitted.Load())
	assert.EqualValues(t, 1, w2.emitted.Load())
	require.NoError(t, f.page.Closed(f.ctx(w1.id), w1.closing(), true))
	assert.Empty(t, results, "w2 has not answered")
	require.NoError(t, f.page.Closed(f.ctx(w2.id), w2.closing(), false)) // the user keeps it
	assert.Equal(t, []bool{false}, results)
	assert.True(t, w1.destroyed())
	assert.False(t, w2.destroyed())
	assert.Equal(t, 1, f.ws.Count())

	f.ws.Open(f.draft(t, "new", 0).ID)
	w3 := f.made[2]
	results = nil
	f.ws.CloseAll(func(ok bool) { results = append(results, ok) })
	require.NoError(t, f.page.Closed(f.ctx(w2.id), w2.closing(), true))
	require.NoError(t, f.page.Closed(f.ctx(w3.id), w3.closing(), true))
	assert.Equal(t, []bool{true}, results)
	assert.Zero(t, f.ws.Count())

	results = nil
	f.ws.CloseAll(func(ok bool) { results = append(results, ok) })
	assert.Equal(t, []bool{true}, results, "nothing to close")
}

func TestWindowsCloseWhileAskingJoins(t *testing.T) {
	f := newWinFixture(t)
	f.asking = make(chan bool)
	f.ws.Open(f.draft(t, "new", 0).ID)
	w := f.made[0]
	f.ws.RequestClose(w.id)
	answered := make(chan error)
	go func() { answered <- f.page.Closed(f.ctx(w.id), w.closing(), false) }()
	require.Eventually(t, func() bool { return f.asked.Load() == 1 }, 5*time.Second, time.Millisecond)
	// While the question shows: another close click, a quit, a late answer.
	f.ws.RequestClose(w.id)
	var results []bool
	f.ws.CloseAll(func(ok bool) { results = append(results, ok) })
	f.elapse()
	assert.NoError(t, f.page.Closed(f.ctx(w.id), w.closing(), true))
	assert.EqualValues(t, 1, w.emitted.Load(), "one flush")
	assert.False(t, w.destroyed())
	f.asking <- false // 返回
	require.NoError(t, <-answered)
	assert.EqualValues(t, 1, f.asked.Load(), "one question")
	assert.False(t, w.destroyed())
	assert.Equal(t, []bool{false}, results)
	assert.Equal(t, 1, f.ws.Count())
}

func TestWindowsStaleTimerIgnored(t *testing.T) {
	f := newWinFixture(t)
	f.ws.Open(f.draft(t, "new", 0).ID)
	w := f.made[0]
	f.ws.RequestClose(w.id)
	require.NoError(t, f.page.Closed(f.ctx(w.id), w.closing(), false)) // 返回
	f.ws.RequestClose(w.id)                                            // a second attempt; the page has not answered yet
	f.mu.Lock()
	first := f.timers[0]
	f.mu.Unlock()
	first() // the first attempt's timer, which a real clock could still fire
	assert.EqualValues(t, 1, f.asked.Load())
	f.answer.Store(true)
	require.NoError(t, f.page.Closed(f.ctx(w.id), w.closing(), true))
	assert.True(t, w.destroyed())
}

func TestWindowsCloseAllTwiceOneDone(t *testing.T) {
	f := newWinFixture(t)
	f.ws.Open(f.draft(t, "new", 0).ID)
	w := f.made[0]
	var results []bool
	f.ws.CloseAll(func(ok bool) { results = append(results, ok) })
	f.ws.CloseAll(func(ok bool) { results = append(results, ok) })
	assert.EqualValues(t, 1, w.emitted.Load())
	require.NoError(t, f.page.Closed(f.ctx(w.id), w.closing(), true))
	assert.Equal(t, []bool{true}, results)
}

func TestWindowsCloseAllWaitsForClosed(t *testing.T) {
	f := newWinFixture(t)
	f.ws.Open(f.draft(t, "new", 0).ID)
	w := f.made[0]
	w.async = true
	var results []bool
	f.ws.CloseAll(func(ok bool) { results = append(results, ok) })
	require.NoError(t, f.page.Closed(f.ctx(w.id), w.closing(), true))
	assert.True(t, w.destroyed())
	assert.Empty(t, results, "the window has not gone yet")
	assert.Equal(t, 1, f.ws.Count())
	f.ws.RequestClose(w.id) // a close click while it goes does nothing
	assert.EqualValues(t, 1, w.emitted.Load())
	f.ws.Closed(w.id)
	assert.Equal(t, []bool{true}, results)
	assert.Zero(t, f.ws.Count())
}

// A page's late answer to a close attempt the user turned back from must not
// close the window in the middle of the next attempt, before that one's flush.
func TestWindowsLateReplyOfEarlierAttemptIgnored(t *testing.T) {
	f := newWinFixture(t)
	f.ws.Open(f.draft(t, "new", 0).ID)
	w := f.made[0]
	f.ws.RequestClose(w.id)
	round1 := w.closing()
	f.elapse() // the page is slow: ask, and the user picks 返回
	assert.EqualValues(t, 1, f.asked.Load())
	// The user types on, then closes again.
	f.ws.RequestClose(w.id)
	round2 := w.closing()
	require.NotEqual(t, round1, round2)
	require.NoError(t, f.page.Closed(f.ctx(w.id), round1, true)) // round 1's flush finally answers
	assert.False(t, w.destroyed(), "closed before round 2's flush saved the newer input")
	require.NoError(t, f.page.Closed(f.ctx(w.id), round2, true))
	assert.True(t, w.destroyed())
	assert.EqualValues(t, 1, f.asked.Load())
}

// gate blocks the making of window n until the returned release: entered
// receives once it is being made.
func (f *winFixture) gate(n int) (entered chan struct{}, release func()) {
	entered, rel := make(chan struct{}, 1), make(chan struct{})
	f.block = func(i int) {
		if i == n {
			entered <- struct{}{}
			<-rel
		}
	}
	return entered, func() { close(rel) }
}

// An Open while the draft's window is being made joins it: one window,
// focused once it exists.
func TestWindowsConcurrentOpensMakeOne(t *testing.T) {
	f := newWinFixture(t)
	entered, release := f.gate(1)
	opened := make(chan struct{})
	go func() { f.ws.Open(7); close(opened) }()
	<-entered
	f.ws.Open(7)
	release()
	<-opened
	require.Equal(t, 1, f.windows())
	assert.EqualValues(t, 1, f.made[0].focused.Load())
	assert.Equal(t, 1, f.ws.Count())
}

// The reopen after a release and a fresh Open racing it make one window.
func TestWindowsReopenAfterReleaseRacesOpen(t *testing.T) {
	f := newWinFixture(t)
	var rel func()
	f.ws.spawn = func(fn func()) { rel = fn }
	d := f.draft(t, "new", 0)
	f.ws.Open(d.ID)
	w := f.made[0]
	_, err := f.page.Save(f.ctx(w.id), ComposeEdit{Body: "<p>kept</p>", Revision: d.Revision})
	require.NoError(t, err)
	f.ws.Closed(w.id)
	f.ws.Open(d.ID) // waits for the release
	entered, release := f.gate(2)
	released := make(chan struct{})
	go func() { rel(); close(released) }()
	<-entered // the release reopens it
	f.ws.Open(d.ID)
	release()
	<-released
	require.Equal(t, 2, f.windows())
	assert.Equal(t, 1, f.ws.Count())
	assert.EqualValues(t, 1, f.made[1].focused.Load())
}
