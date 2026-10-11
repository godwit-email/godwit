package app

import (
	"cmp"
	"maps"
	"math"
	"runtime"
	"slices"
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// pageView is a top-level page in the window's frame. refresh re-reads what
// the page shows; the events are hints only (their IDs never tell everything
// that changed), and nil when the page was just navigated to.
type pageView interface {
	Sidebar(c *ui.Context)
	Content(c *ui.Context)
	Title() (title, sub string)
	refresh(hints []events.Event)
}

// goos is a var so a test can pose as another platform.
var goos = runtime.GOOS

// chromeMetrics is the top bar's height and the panel's corner radius. Windows
// draws the design's 40 px strip with 8 px corners; elsewhere the bar is at
// least TopBarHeight (the window controls only take room beside our bar).
func chromeMetrics(titleH float32, t theme.Tokens) (barH, radius float32) {
	if goos == "windows" && titleH > 0 {
		return titleH, t.Radius.Lg
	}
	return max(theme.TopBarHeight, titleH), t.Radius.Xl
}

const (
	refreshEvery = 250 * time.Millisecond
)

func (a *App) page() pageView { return a.pages[a.route.Page] }

// navigate goes to r, after the current route in the history; the routes
// ahead are dropped.
func (a *App) navigate(r Route) {
	if r == a.route {
		return
	}
	a.history, a.ahead = append(a.history, a.route), nil
	a.setRoute(r)
}

func (a *App) back() {
	if n := len(a.history); n > 0 {
		a.ahead = append(a.ahead, a.route)
		r := a.history[n-1]
		a.history = a.history[:n-1]
		a.setRoute(r)
	}
}

func (a *App) forward() {
	if n := len(a.ahead); n > 0 {
		a.history = append(a.history, a.route)
		r := a.ahead[n-1]
		a.ahead = a.ahead[:n-1]
		a.setRoute(r)
	}
}

// setRoute shows r: results of queries for the old route are dropped, and
// the page reads what it shows.
func (a *App) setRoute(r Route) {
	a.route = r
	a.gen++
	a.page().refresh(nil)
}

// onEvent notes ev for the next refresh, at most one per refreshEvery; sync
// events also update the sync status at once.
func (a *App) onEvent(ev events.Event) {
	a.events++
	switch ev.Type {
	case events.SyncStarted:
		a.syncing[ev.AccountID] = true
	case events.SyncFinished:
		delete(a.syncing, ev.AccountID)
		delete(a.syncErr, ev.AccountID)
		a.syncedAt = a.clock.Now()
		a.tick()
	case events.SyncFailed:
		delete(a.syncing, ev.AccountID)
		a.syncErr[ev.AccountID] = ev.Error
	case events.MessageReceived:
		a.mailArrived(ev)
	}
	a.dirty = append(a.dirty, ev)
	if a.refreshStop == nil {
		a.refreshStop = a.after(refreshEvery, func() {
			dirty := a.dirty
			a.dirty, a.refreshStop = nil, nil
			a.refresh(dirty)
		})
	}
}

// refresh re-reads the accounts, settings and unread counts, and what the
// page shows.
func (a *App) refresh(dirty []events.Event) {
	a.refreshes++
	a.load()
	a.page().refresh(dirty)
}

// tick redraws once a minute, so the sync age stays current.
func (a *App) tick() {
	if a.ticking {
		return
	}
	a.ticking = true
	a.after(time.Minute, func() {
		a.ticking = false
		a.tick()
	})
}

func (a *App) toast(msg string) { a.toastAs("", msg) }

// toastAs shows msg as a toast of a kind: kit.ToastSuccess, ToastWarning,
// ToastError, or "" for a plain one.
func (a *App) toastAs(kind, msg string) {
	a.toasts = append(a.toasts, ui.Toast{ID: msg, Title: msg, Type: kind})
}

// toastErr shows err in words; a cancellation shows nothing.
func (a *App) toastErr(err error) {
	if msg := humanize(err); msg != "" {
		a.toastAs(kit.ToastError, msg)
	}
}

// View builds the window: the title bar, the rail, and the page's sidebar
// and content as one panel. First run shows only the onboarding page.
func (a *App) View(c *ui.Context) {
	theme.Apply(c, theme.FromSettings(a.settings, c.Theme().Dark))
	t, th := theme.Of(c), c.Theme()
	root := t.Window
	if a.vibrancy && a.loaded && a.route.Page != PageOnboarding {
		root = ui.Transparent // the material shows where the window's own color would
	}
	for _, ts := range a.toasts {
		c.AddToast(ts)
	}
	a.toasts = nil

	if c.Shortcut(ui.Cmd, ui.KeyBracketLeft) {
		a.back()
	}
	if c.Shortcut(ui.Cmd, ui.KeyBracketRight) {
		a.forward()
	}
	barH, _ := chromeMetrics(c.TitleBar().Height, theme.Of(c))
	switch {
	case !a.loaded:
		ui.Box(c).Height(barH).FillWidth().DragWindow()
		if a.loadErr != nil {
			ui.Column(c).Grow(1).FillWidth().Center().Gap(th.Space(3)).Children(func() {
				ui.Text(c, humanize(a.loadErr)).TextColor(t.Muted)
				kit.Button(c, i18n.T("common.retry"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(a.load)
			})
		}
	case a.route.Page == PageOnboarding:
		ui.Box(c).Height(barH).FillWidth().DragWindow()
		ui.Column(c).Grow(1).Children(func() { a.page().Content(c) })
	default:
		a.titleBar(c, barH)
		ui.Row(c).Grow(1).AlignItems(ui.Stretch).Children(func() {
			a.rail(c)
			a.panel(c)
		})
	}
	if a.addAccount != nil {
		a.addAccount.sheetView(c)
	}
	a.linkDialog(c)
	a.closeDialog(c)
	a.mail.reader.egressDialog(c)
	kit.ToastHost(c, i18n.T("toast.close"))
	// Last: c.SetTheme, which kit.Split calls mid-frame, resets the root's
	// background to the theme's, which would cover the window's material.
	c.Root().Background(root)
}

// titleBar leaves the window controls their room (c.TitleBar) and drags the
// window. The page's title starts where the panel does.
func (a *App) titleBar(c *ui.Context, h float32) {
	t, th := theme.Of(c), c.Theme()
	bar := c.TitleBar()
	padL := bar.Left + th.Space(2)
	ui.Row(c).Height(h).FillWidth().Padding(0, bar.Right+th.Space(4), 0, padL).DragWindow().Children(func() {
		lead := ui.Row(c).Gap(th.Space(1))
		if !a.sidebarHidden {
			lead.MinWidth(max(0, theme.RailWidth+a.sidebarWidth()-padL))
		}
		lead.Children(func() {
			ghost := kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}
			label := i18n.T("shell.sidebar.collapse")
			if a.sidebarHidden {
				label = i18n.T("shell.sidebar.expand")
			}
			kit.IconButton(c, "panel-left", label, ghost).OnClick(func() { a.sidebarHidden = !a.sidebarHidden })
			ghost.Disabled = len(a.history) == 0
			kit.IconButton(c, "arrow-left", i18n.T("shell.back"), ghost).OnClick(a.back)
			ghost.Disabled = len(a.ahead) == 0
			kit.IconButton(c, "arrow-right", i18n.T("shell.forward"), ghost).OnClick(a.forward)
		})
		if title, sub := a.page().Title(); title != "" {
			ui.Row(c).Gap(th.Space(2)).Children(func() {
				kit.Separator(c, true).Margin(0, th.Space(2), 0, 0)
				ui.Text(c, title).FontWeight(600).SingleLine()
				if sub != "" {
					ui.Text(c, sub).FontSize(t.Text.Sm).TextColor(t.Muted).SingleLine()
				}
			})
		}
		ui.Spacer(c)
		a.syncStatus(c)
	})
}

// syncStatus tells, most urgent first: an account needs signing in again
// (a click opens its settings), a sync failed (hover for the error), a sync
// is under way, or how long ago the last one finished.
func (a *App) syncStatus(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	small := t.Text.Sm
	for _, acc := range a.accounts {
		if acc.Status == "reauth_required" {
			r := Route{Page: PageSettings, Settings: "account:" + strconv.FormatInt(acc.ID, 10)}
			kit.ButtonFrame(c, kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).FontSize(small).TextColor(t.WarningText).Children(func() {
				kit.StatusDot(c, kit.Attention)
				ui.Text(c, i18n.T("shell.sync.reauth"))
			}).OnClick(func() { a.navigate(r) })
			return
		}
	}
	row := ui.Row(c).Gap(th.Space(1.5)).FontSize(small).TextColor(t.Muted)
	switch {
	case len(a.syncing) > 0:
		row.Children(func() { ui.Text(c, i18n.T("shell.sync.syncing")) })
	case len(a.syncErr) > 0:
		id := slices.Min(slices.Collect(maps.Keys(a.syncErr)))
		kit.Tooltip(c, row, a.syncErr[id])
		row.Children(func() {
			kit.StatusDot(c, kit.Error)
			ui.Text(c, i18n.T("shell.sync.failed"))
		})
	case !a.syncedAt.IsZero():
		row.Children(func() {
			kit.StatusDot(c, kit.OK)
			ui.Text(c, a.syncedAgo())
		})
	}
}

func (a *App) syncedAgo() string {
	switch d := a.clock.Now().Sub(a.syncedAt); {
	case d < time.Minute:
		return i18n.T("shell.sync.done")
	case d < time.Hour:
		return i18n.Tf("shell.sync.synced", i18n.Plural("shell.sync.minutes_ago", int(d.Minutes())))
	default:
		return i18n.Tf("shell.sync.synced", i18n.Plural("shell.sync.hours_ago", int(d.Hours())))
	}
}

// rail is the icon bar: Mail and Wit at the top, Settings at the bottom.
func (a *App) rail(c *ui.Context) {
	th := c.Theme()
	ui.Column(c).Width(theme.RailWidth).AlignItems(ui.Center).Gap(th.Space(1.5)).Padding(th.Space(1), 0, th.Space(3), 0).Children(func() {
		a.railItem(c, "mail", i18n.T("shell.rail.mail"), Route{Page: PageMail, View: core.View{Unified: "inbox"}})
		a.railItem(c, "sparkles", i18n.T("shell.rail.wit"), Route{Page: PageWit})
		ui.Spacer(c)
		if a.dev {
			a.railItem(c, "component", "Kit", Route{Page: PageKit})
		}
		a.railItem(c, "sliders-horizontal", i18n.T("shell.rail.settings"), Route{Page: PageSettings})
	})
}

func (a *App) railItem(c *ui.Context, icon, label string, r Route) {
	kit.RailButton(c, icon, label, a.route.Page == r.Page, func() { a.navigate(r) })
}

// panel is the page's sidebar and content as one rounded block, inset from
// the window's right and bottom edges as onboarding's panel is.
func (a *App) panel(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	p := a.page()
	_, rad := chromeMetrics(c.TitleBar().Height, t)
	panel := ui.Row(c).Grow(1).AlignItems(ui.Stretch).Margin(0, th.Space(2), th.Space(2), 0).Radius(rad).Clip().Background(t.Background).
		Border(theme.Hairline, t.Outline)
	kit.Elevate(c, panel, theme.ShadowXs).Children(func() {
		content := func() { ui.Column(c).Fill().Children(func() { p.Content(c) }) }
		if a.sidebarHidden {
			content()
			return
		}
		a.sidebarW = a.sidebarWidth()
		split := kit.Split(c, &a.sidebarW, func() {
			// Rounded itself, inside the panel's outline: the GPU renderer does not
			// round the panel's clip, and the material shows past a square corner.
			r := rad - theme.Hairline
			ui.Column(c).Fill().Padding(th.Space(3)).Gap(th.Space(0.5)).Radius(r, 0, 0, r).Background(t.Sidebar).Children(func() { p.Sidebar(c) })
		}, content).Grow(1)
		a.sidebarW = min(max(a.sidebarW, theme.SidebarMinWidth), theme.SidebarMaxWidth)
		if split.Changed() {
			a.saveSidebarWidth()
		}
	})
}

// sidebarWidth is the page sidebars' width: as dragged, else as saved, else
// the default. Every page's sidebar takes it, so a drag on one moves them all.
func (a *App) sidebarWidth() float32 {
	if a.sidebarW != 0 {
		return a.sidebarW
	}
	return float32(cmp.Or(a.settings.SidebarWidth, theme.SidebarWidth))
}

// sidebarSaveDelay is how long a drag rests before its width is saved.
const sidebarSaveDelay = 400 * time.Millisecond

// saveSidebarWidth saves the dragged width once the drag rests, not every frame.
func (a *App) saveSidebarWidth() {
	if a.sidebarSave != nil {
		a.sidebarSave()
	}
	a.sidebarSave = a.after(sidebarSaveDelay, func() {
		a.sidebarSave = nil
		if w := int(math.Round(float64(a.sidebarW))); w != a.settings.SidebarWidth {
			st := a.settings
			st.SidebarWidth = w
			a.changeSettings(st)
		}
	})
}

// closeDialog asks, when a draft could not be saved before closing, whether
// to close anyway and lose its last edits.
func (a *App) closeDialog(c *ui.Context) {
	t := theme.Of(c)
	kit.Dialog(c, &a.closeAsk, i18n.T("close.unsaved.title"), func() {
		ui.Text(c, humanize(a.closeErr)).TextColor(t.Muted)
	}, func() {
		kit.Button(c, i18n.T("close.unsaved.back"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(func() { a.closeAsk = false })
		kit.Button(c, i18n.T("close.unsaved.quit"), kit.ButtonOpts{Variant: kit.Destructive}).OnClick(func() {
			a.closeAsk = false
			a.closeAnyway()
		})
	})
}
