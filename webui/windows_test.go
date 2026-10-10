package webui

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeWin is a Window that counts what was done to it.
type fakeWin struct {
	id                         int
	ws                         *Windows
	focused, emitted, destroys int
}

func (w *fakeWin) ID() int         { return w.id }
func (w *fakeWin) Focus()          { w.focused++ }
func (w *fakeWin) EmitClosing()    { w.emitted++ }
func (w *fakeWin) Destroy()        { w.destroys++; w.ws.Closed(w.id) } // mygo reports Closed synchronously
func (w *fakeWin) destroyed() bool { return w.destroys > 0 }

// winFixture is Windows on a real Compose, with fake windows, a scripted
// ask and a clock that fires only when told.
type winFixture struct {
	*fixture
	ws     *Windows
	made   []*fakeWin
	answer bool // what ask says: true = close anyway
	asked  int
	mu     sync.Mutex
	timers []func()
}

func newWinFixture(t *testing.T) *winFixture {
	t.Helper()
	f := &winFixture{fixture: newFixture(t)}
	f.ws = NewWindows(f.c, func(int64) Window {
		w := &fakeWin{id: len(f.made) + 1, ws: f.ws}
		f.made = append(f.made, w)
		return w
	}, func(Window) bool { f.asked++; return f.answer })
	f.ws.after = func(d time.Duration, fn func()) func() bool {
		assert.Equal(t, 5*time.Second, d)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.timers = append(f.timers, fn)
		return func() bool { return true }
	}
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
	assert.Equal(t, 1, f.made[0].focused)
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
	assert.Equal(t, 1, w.emitted)
	assert.False(t, w.destroyed())
	// The page flushes, then reports.
	_, err = f.page.Save(f.ctx(w.id), ComposeEdit{Body: "最后一句", Revision: st.Revision})
	require.NoError(t, err)
	require.NoError(t, f.page.Closed(f.ctx(w.id), true))
	assert.True(t, w.destroyed())
	assert.Zero(t, f.asked)
	assert.Zero(t, f.ws.Count())
	stored, err := f.e.Svc.GetDraft(t.Context(), d.ID)
	require.NoError(t, err)
	ed, _, _ := splitOf(stored.HTML)
	assert.Equal(t, "最后一句", bodyText(ed))
	f.elapse() // the stopped timer, if a clock ran it anyway
	assert.Equal(t, 1, w.destroys)
	assert.Zero(t, f.asked)
}

func TestWindowsCloseFailedSaveAsks(t *testing.T) {
	f := newWinFixture(t)
	f.ws.Open(f.draft(t, "new", 0).ID)
	w := f.made[0]
	f.ws.RequestClose(w.id)
	require.NoError(t, f.page.Closed(f.ctx(w.id), false))
	assert.Equal(t, 1, f.asked)
	assert.False(t, w.destroyed(), "返回 keeps the window")
	assert.Equal(t, 1, f.ws.Count())

	f.answer = true
	f.ws.RequestClose(w.id)
	assert.Equal(t, 2, w.emitted)
	require.NoError(t, f.page.Closed(f.ctx(w.id), false))
	assert.Equal(t, 2, f.asked)
	assert.True(t, w.destroyed(), "关闭 closes it")
}

func TestWindowsSlowPageAsksNotCloses(t *testing.T) {
	f := newWinFixture(t)
	f.ws.Open(f.draft(t, "new", 0).ID)
	w := f.made[0]
	f.ws.RequestClose(w.id)
	f.ws.RequestClose(w.id) // a second click while waiting asks the page nothing new
	assert.Equal(t, 1, w.emitted)
	f.elapse()
	assert.Equal(t, 1, f.asked)
	assert.False(t, w.destroyed())
	require.NoError(t, f.page.Closed(f.ctx(w.id), true)) // too late: the user chose 返回
	assert.False(t, w.destroyed())

	f.answer = true
	f.ws.RequestClose(w.id)
	f.elapse()
	assert.Equal(t, 2, f.asked)
	assert.True(t, w.destroyed())
}

func TestWindowsClosedDetaches(t *testing.T) {
	f := newWinFixture(t)
	d := f.draft(t, "new", 0)
	f.ws.Open(d.ID)
	w := f.made[0]
	f.ws.Closed(w.id)
	_, err := f.page.Load(f.ctx(w.id))
	assert.ErrorIs(t, err, ErrNoDraft)
	assert.Zero(t, f.ws.Count())
	f.ws.Open(d.ID)
	require.Len(t, f.made, 2)
	assert.Zero(t, w.focused)
	_, err = f.page.Load(f.ctx(f.made[1].id))
	assert.NoError(t, err)
}

func TestWindowsCloseAllForQuit(t *testing.T) {
	f := newWinFixture(t)
	f.ws.Open(f.draft(t, "new", 0).ID)
	f.ws.Open(f.draft(t, "new", 0).ID)
	w1, w2 := f.made[0], f.made[1]
	var results []bool
	f.ws.CloseAll(func(ok bool) { results = append(results, ok) })
	assert.Equal(t, 1, w1.emitted)
	assert.Equal(t, 1, w2.emitted)
	require.NoError(t, f.page.Closed(f.ctx(w1.id), true))
	assert.Empty(t, results, "w2 has not answered")
	require.NoError(t, f.page.Closed(f.ctx(w2.id), false)) // the user keeps it
	assert.Equal(t, []bool{false}, results)
	assert.True(t, w1.destroyed())
	assert.False(t, w2.destroyed())
	assert.Equal(t, 1, f.ws.Count())

	f.ws.Open(f.draft(t, "new", 0).ID)
	w3 := f.made[2]
	results = nil
	f.ws.CloseAll(func(ok bool) { results = append(results, ok) })
	require.NoError(t, f.page.Closed(f.ctx(w2.id), true))
	require.NoError(t, f.page.Closed(f.ctx(w3.id), true))
	assert.Equal(t, []bool{true}, results)
	assert.Zero(t, f.ws.Count())

	results = nil
	f.ws.CloseAll(func(ok bool) { results = append(results, ok) })
	assert.Equal(t, []bool{true}, results, "nothing to close")
}
