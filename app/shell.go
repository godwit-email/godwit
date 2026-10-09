package app

import (
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
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

// placeholder is an empty page; later tasks replace each with the real one.
type placeholder struct{}

func (placeholder) Sidebar(*ui.Context)          {}
func (placeholder) Content(*ui.Context)          {}
func (placeholder) Title() (string, string)      { return "", "" }
func (placeholder) refresh(hints []events.Event) {}

const (
	refreshEvery = 250 * time.Millisecond
)

// section is the page that shows the route: a draft belongs to Mail.
func (r Route) section() Page {
	if r.Page == PageCompose {
		return PageMail
	}
	return r.Page
}

func (a *App) page() pageView { return a.pages[a.route.section()] }

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
	if a.route.Page == PageCompose {
		a.mail.closeDraft()
	}
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

func (a *App) toast(msg string) { a.toasts = append(a.toasts, ui.Toast{ID: msg, Title: msg}) }

// toastErr shows err in words; a cancellation shows nothing.
func (a *App) toastErr(err error) {
	if msg := humanize(err); msg != "" {
		a.toasts = append(a.toasts, ui.Toast{ID: msg, Title: msg, Type: "error"})
	}
}

// View builds the window: the title bar, the rail, and the page's sidebar
// and content as one panel. First run shows only the onboarding page.
func (a *App) View(c *ui.Context) {
	theme.Apply(c, theme.FromSettings(a.settings, c.Theme().Dark))
	t, th := theme.Of(c), c.Theme()
	c.Root().Background(t.Window)
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
	barH := max(theme.TopBarHeight, c.TitleBar().Height) // the window controls only take room beside our bar
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
			lead.MinWidth(max(0, theme.RailWidth+theme.SidebarWidth-padL))
		}
		lead.Children(func() {
			ghost := kit.ButtonOpts{Variant: kit.Ghost, Small: true}
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
				ui.Box(c).Size(theme.Hairline, theme.TitleButtonHeight).Margin(0, th.Space(2), 0, 0).Background(t.Border)
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
			ui.ButtonBase(c).Gap(th.Space(1.5)).PaddingX(th.Space(1.5)).Height(theme.TitleButtonHeight).Radius(t.Radius.Sm).FontSize(small).TextColor(t.WarningText).Children(func() {
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
		row.Tooltip(a.syncErr[id]).Children(func() {
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
		a.railItem(c, "sliders-horizontal", i18n.T("shell.rail.settings"), Route{Page: PageSettings})
	})
}

func (a *App) railItem(c *ui.Context, icon, label string, r Route) {
	t := theme.Of(c)
	on := a.route.section() == r.Page
	b := ui.ButtonBase(c).Size(theme.RailButton, theme.RailButton).Radius(t.Radius.Md).Center().Label(label).Tooltip(label).TextColor(t.Muted)
	switch {
	case on:
		b.Background(t.RailSelected).TextColor(t.Foreground)
	case b.Hovered():
		b.Background(t.Hover).TextColor(t.Foreground)
	}
	b.Children(func() { ui.Icon(c, icons.Get(icon)).Size(theme.IconLg, theme.IconLg) })
	if !on {
		b.OnClick(func() { a.navigate(r) })
	}
}

// panel is the page's sidebar and content as one block, its top-left corner
// rounded.
func (a *App) panel(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	p := a.page()
	ui.Row(c).Grow(1).AlignItems(ui.Stretch).Radius(t.Radius.Xl, 0, 0, 0).Clip().Background(t.Background).
		BorderWidth(theme.Hairline, 0, 0, theme.Hairline).BorderColor(t.Border).Children(func() {
		if !a.sidebarHidden {
			ui.Column(c).Width(theme.SidebarWidth).Padding(th.Space(3)).Gap(th.Space(0.5)).Background(t.Sidebar).
				BorderWidth(0, theme.Hairline, 0, 0).BorderColor(t.Border).Children(func() { p.Sidebar(c) })
		}
		ui.Column(c).Grow(1).Children(func() { p.Content(c) })
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
