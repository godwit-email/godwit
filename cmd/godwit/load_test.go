package main

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/app"
	"github.com/godwit-email/godwit/webui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An embedded page is set up (locked, registered, bound) before it loads,
// so its first call finds it; a load that fails undoes the setup.
func TestEmbedSetsUpBeforeLoading(t *testing.T) {
	var steps []string
	step := func(s string) func() { return func() { steps = append(steps, s) } }
	load := func(err error) func() error {
		return func() error { steps = append(steps, "load"); return err }
	}
	if err := loadAfterSetup(step("setup"), load(nil), step("undo")); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(steps, []string{"setup", "load"}) {
		t.Fatalf("steps %q", steps)
	}
	steps = nil
	bad := errors.New("no such page")
	if err := loadAfterSetup(step("setup"), load(bad), step("undo")); !errors.Is(err, bad) {
		t.Fatalf("err %v", err)
	}
	if !slices.Equal(steps, []string{"setup", "load", "undo"}) {
		t.Fatalf("steps %q", steps)
	}
}

// Only the native WebView is substituted: registration, binding, dispatch and
// the logical view handed to the app are the same as in the main window.
type testReaderShell struct {
	ui.NativeWebView
	page             mygo.Page
	loads, destroyed int
	onLoad, capture  func()
	loadErr, sendErr error
	events           []webui.ReaderBinding
}

func (s *testReaderShell) Page() *mygo.Page { return &s.page }
func (s *testReaderShell) Destroy()         { s.destroyed++ }
func (s *testReaderShell) load() error {
	s.loads++
	s.onLoad()
	return s.loadErr
}
func (s *testReaderShell) signal(b webui.ReaderBinding) error {
	s.events = append(s.events, b)
	return s.sendErr
}
func (s *testReaderShell) CapturePage() ([]byte, error) {
	if s.capture != nil {
		s.capture()
	}
	return []byte("page"), nil
}

type shellContextKey struct{}

func shellContext(s readerShell) context.Context {
	return context.WithValue(context.Background(), shellContextKey{}, s.Page())
}

func readerPlatform(t *testing.T) (*platform, *webui.ReaderPage, *[]*testReaderShell) {
	t.Helper()
	p := &platform{roles: webui.NewPages(func(ctx context.Context) any { return ctx.Value(shellContextKey{}) })}
	p.reader = webui.NewReader(p.roles, webui.ReaderDeps{})
	page := webui.NewReaderPage(p.reader)
	var shells []*testReaderShell
	p.makeBody = func() (readerShell, error) {
		s := &testReaderShell{}
		s.onLoad = func() {
			b, err := page.Binding(shellContext(s))
			require.NoError(t, err)
			require.True(t, b.Active, "a cold document's first call must find its assignment")
			_, ok := p.reader.Bound(b.ID)
			require.True(t, ok)
		}
		shells = append(shells, s)
		return s, nil
	}
	return p, page, &shells
}

func TestReaderShellPoolAcrossSwitches(t *testing.T) {
	p, page, shells := readerPlatform(t)
	var displayed *bodyView
	var release func(bool)
	for msg := int64(1); msg <= 21; msg++ {
		v, nextRelease, err := p.NewBodyView(msg, nil) // previous page still displayed
		require.NoError(t, err)
		next := v.(*bodyView)
		b, err := page.Binding(shellContext(next.shell))
		require.NoError(t, err)
		require.Equal(t, webui.ReaderBinding{ID: next.id, Active: true}, b)
		if displayed != nil {
			require.NotSame(t, displayed.shell.Page(), next.shell.Page())
			require.Greater(t, next.id, displayed.id)
			release(true)
			b, err := page.Binding(shellContext(displayed.shell))
			require.NoError(t, err)
			require.Equal(t, webui.ReaderBinding{ID: displayed.id}, b)
			_, err = p.roles.Caller(shellContext(displayed.shell), "reader")
			require.ErrorIs(t, err, webui.ErrWrongPage, "idle shell has no reader role")
		}
		displayed, release = next, nextRelease
	}
	require.Len(t, *shells, 2, "twenty handoffs require only two physical shells")
	p.closeBodyViews() // one still displayed and one idle
	for _, s := range *shells {
		require.Equal(t, 1, s.loads)
		require.Equal(t, 1, s.destroyed)
		require.Zero(t, len(s.events)%2, "every assignment was activated and released")
		for i := 0; i < len(s.events); i += 2 {
			assert.True(t, s.events[i].Active)
			assert.Equal(t, webui.ReaderBinding{ID: s.events[i].ID}, s.events[i+1])
		}
		_, err := page.Binding(shellContext(s))
		assert.ErrorIs(t, err, webui.ErrWrongPage)
	}
	release(false)
	_, _, err := p.NewBodyView(1, nil)
	require.Error(t, err, "a shutdown cannot recreate the idle shell")
}

func TestReaderQueuedCallbacksAndSnapshotsDoNotFollowReusedShell(t *testing.T) {
	p, _, shells := readerPlatform(t)
	v, release, err := p.NewBodyView(1, nil)
	require.NoError(t, err)
	old := v.(*bodyView)
	var queue []func(*app.App)
	var lookedUp []int
	d := readerDeps(p.roles, func(f func(*app.App)) { queue = append(queue, f) }, func(id int) *bodyView {
		lookedUp = append(lookedUp, id)
		return p.bodies[id]
	})
	d.OnHeight(old.id, 1, 10)
	d.OnPainted(old.id, 1)
	d.OnLink(old.id, "https://example.com/")
	d.OnNavigate(old.id, 1)
	d.OnSearch(old.id)
	d.OnSave(old.id)
	d.OnPrint(old.id)
	d.OnBuilt(old.id, webui.ReaderBody{Binding: old.id, Message: 1})
	d.OnFailed(old.id, 1, errors.New("retry"))
	d.OnImagesFailed(old.id, 1, 1)
	require.Empty(t, lookedUp, "do not resolve the shell on the IPC goroutine")
	require.Len(t, queue, 10)

	(*shells)[0].capture = func() {
		release(true)
		_, stop, err := p.NewBodyView(2, nil)
		require.NoError(t, err)
		stop(true)
		again, _, err := p.NewBodyView(1, nil)
		require.NoError(t, err)
		require.Same(t, old.shell.Page(), again.(*bodyView).shell.Page())
		require.NotSame(t, old, again.(*bodyView), "modal screenshots are keyed by logical assignment")
	}
	shot, err := old.CapturePage()
	assert.ErrorIs(t, err, webui.ErrWrongPage)
	assert.Empty(t, shot)
	assert.Nil(t, old.SurfaceWebView(nil), "an old node cannot place the recycled shell")
	release(false) // duplicate old release cannot destroy current A
	for _, f := range queue {
		f(nil) // invoking any old action on the app would panic
	}
	assert.Len(t, lookedUp, len(queue))
	for _, id := range lookedUp {
		assert.Equal(t, old.id, id)
	}
	assert.Zero(t, (*shells)[0].destroyed)
	p.closeBodyViews()
}

func TestReaderShellDropAndFailureCleanup(t *testing.T) {
	t.Run("surplus and forced discard", func(t *testing.T) {
		p, _, shells := readerPlatform(t)
		_, release, err := p.NewBodyView(1, nil)
		require.NoError(t, err)
		_, releaseOther, err := p.NewBodyView(2, nil)
		require.NoError(t, err)
		release(true)
		releaseOther(true)
		require.Equal(t, 1, (*shells)[1].destroyed, "only one shell may remain idle")
		_, release, err = p.NewBodyView(3, nil)
		require.NoError(t, err)
		release(false)
		require.Equal(t, 1, (*shells)[0].destroyed)
		_, _, err = p.NewBodyView(4, nil)
		require.NoError(t, err)
		require.Len(t, *shells, 3, "a failed assignment is never reused")
		p.closeBodyViews()
	})
	for _, at := range []string{"load", "activate", "deactivate", "shutdown during load"} {
		t.Run(at, func(t *testing.T) {
			p, page, shells := readerPlatform(t)
			newShell := p.makeBody
			failure := errors.New("renderer unavailable")
			p.makeBody = func() (readerShell, error) {
				made, _ := newShell()
				s := made.(*testReaderShell)
				switch at {
				case "load":
					s.loadErr = failure
				case "activate":
					s.sendErr = failure
				case "shutdown during load":
					s.onLoad = p.closeBodyViews
				}
				return s, nil
			}
			v, release, err := p.NewBodyView(5, nil)
			s := (*shells)[0]
			if at == "deactivate" {
				require.NoError(t, err)
				s.sendErr = failure
				release(true)
			} else {
				require.Error(t, err)
				require.Nil(t, v)
				require.Nil(t, release)
			}
			p.closeBodyViews()
			assert.Equal(t, 1, s.destroyed)
			_, err = page.Binding(shellContext(s))
			assert.ErrorIs(t, err, webui.ErrWrongPage)
			_, registered := p.roles.Role(s.Page())
			assert.False(t, registered)
		})
	}
}
