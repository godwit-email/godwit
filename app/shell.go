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

// chromeMetrics is the top bar's height and the panels' corner radius. Windows
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
	if r.Page != PageMail {
		a.mail.stopOlder() // older mail is fetched only while the list shows
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
	case events.SettingsChanged:
		a.mail.reader.invalidateAvatars(false)
	case events.SyncStarted:
		a.syncing[ev.AccountID] = true
	case events.SyncFinished:
		delete(a.syncing, ev.AccountID)
		delete(a.syncErr, ev.AccountID)
		a.syncedAt = a.clock.Now()
		a.syncedAtBy[ev.AccountID] = a.syncedAt
		a.mail.older.notYet = 0
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

// View builds the window: the title bar, then the page's sidebar on the
// window material beside its content. First run shows only the onboarding page.
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
	kit.DialogShown() // forget another window's

	if c.Shortcut(ui.Cmd, ui.KeyBracketLeft) {
		a.back()
	}
	if c.Shortcut(ui.Cmd, ui.KeyBracketRight) {
		a.forward()
	}
	barH, _ := chromeMetrics(c.TitleBar().Height, theme.Of(c))
	switch {
	case !a.loaded:
		bareBar(c, barH)
		if a.loadErr != nil {
			ui.Column(c).Grow(1).FillWidth().Center().Gap(th.Space(3)).Children(func() {
				ui.Text(c, humanize(a.loadErr)).TextColor(t.Muted)
				kit.Button(c, i18n.T("common.retry"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(a.load)
			})
		}
	case a.route.Page == PageOnboarding:
		bareBar(c, barH)
		ui.Column(c).Grow(1).Children(func() { a.page().Content(c) })
	default:
		a.titleBar(c, barH)
		a.body(c)
	}
	if a.addAccount != nil {
		a.addAccount.sheetView(c)
	}
	a.linkDialog(c)
	a.closeDialog(c)
	a.mail.reader.egressDialog(c)
	kit.ToastHost(c, i18n.T("toast.close"))
	// Last, on the root, which overlays (menus, dialogs, Select lists, toasts)
	// attach to: a c.SetTheme mid-frame resets its background to the theme's,
	// which would cover the window's material.
	if kit.DialogShown() {
		root = t.Window
	}
	c.Root().Background(root).LineHeight(t.Text.Line(t.Text.Body))
}

// titleBar leaves the window controls their room (c.TitleBar) and drags the
// window. The page's title lines up with the content's text; settings shows
// neither a title nor the sync status (its sidebar names the page).
func (a *App) titleBar(c *ui.Context, h float32) {
	t, th := theme.Of(c), c.Theme()
	bar := c.TitleBar()
	padL := bar.Left + th.Space(2)
	padR := bar.Right + th.Space(4)
	if goos == "windows" {
		padR = bar.Right + th.Space(3) // the design's 150 = 3 caption buttons of 46 + 12
	}
	ui.Row(c).Height(h).FillWidth().Padding(0, padR, 0, padL).DragWindow().Children(func() {
		lead := ui.Row(c).Gap(th.Space(1))
		if !a.sidebarHidden {
			lead.MinWidth(max(0, a.contentLeft(c)-padL))
		}
		lead.Children(func() {
			winCaption(c)
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
		if a.route.Page == PageSettings {
			return
		}
		if title, sub := a.page().Title(); title != "" {
			ui.Row(c).Gap(th.Space(2)).Padding(0, 0, 0, th.Space(4)).Children(func() {
				ui.Text(c, title).FontWeight(600).SingleLine()
				if sub != "" {
					kit.Text(c, sub, t.Text.Sm).TextColor(t.Muted).SingleLine()
				}
			})
		}
		ui.Spacer(c)
		a.syncStatus(c)
	})
}

// bareBar is the title bar of a window with no page chrome: it drags the
// window and, on Windows, names it.
func bareBar(c *ui.Context, h float32) {
	ui.Row(c).Height(h).FillWidth().Padding(0, 0, 0, c.TitleBar().Left+c.Theme().Space(2)).DragWindow().Children(func() { winCaption(c) })
}

// winCaption is Windows' app icon and name at the title bar's start.
func winCaption(c *ui.Context) {
	if goos != "windows" {
		return
	}
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).Gap(th.Space(2)).Padding(0, th.Space(2), 0, th.Space(1)).Children(func() {
		ui.Icon(c, logo).Size(theme.Icon, theme.Icon).TextColor(t.Foreground)
		kit.Text(c, "Godwit", t.Text.Sm).SingleLine()
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

func (a *App) syncedAgo() string { return i18n.Tf("shell.sync.synced", a.ago(a.syncedAt)) }

// ago is how long before now t was: 刚刚, 5 分钟前, 3 小时前.
func (a *App) ago(t time.Time) string {
	switch d := a.clock.Now().Sub(t); {
	case d < time.Minute:
		return i18n.T("shell.sync.just_now")
	case d < time.Hour:
		return i18n.Plural("shell.sync.minutes_ago", int(d.Minutes()))
	default:
		return i18n.Plural("shell.sync.hours_ago", int(d.Hours()))
	}
}

// body is the page's sidebar, on the window material, beside its content,
// inset from the window's edges; the content draws its own panels.
func (a *App) body(c *ui.Context) {
	th := c.Theme()
	p := a.page()
	padL := th.Space(2)
	if a.sidebarHidden {
		padL = 0 // the edge grip takes the inset
	}
	ui.Row(c).Grow(1).AlignItems(ui.Stretch).Padding(0, th.Space(2), th.Space(2), padL).Children(func() {
		content := func() { ui.Column(c).Fill().Children(func() { p.Content(c) }) }
		if a.sidebarHidden {
			a.edgeGrip(c, content)
			return
		}
		a.sidebarW = a.sidebarWidth()
		before := a.sidebarW
		changed, dragging := kit.Resizer(c, &a.sidebarW, theme.GripWidth, true, i18n.T("shell.resize"), nil, func() {
			top := th.Space(3)
			if a.route.Page == PageMail || a.route.Page == PageWit {
				top = 0 // the page switch sits right under the title bar
			}
			ui.Column(c).Fill().Padding(top, 0, th.Space(3), th.Space(3)).Gap(th.Space(0.5)).Children(func() {
				ui.Column(c).Grow(1).MinHeight(0).Gap(th.Space(0.5)).Children(func() { p.Sidebar(c) })
				ui.Column(c).Gap(th.Space(0.5)).Padding(0, sidebarInset(c), 0, 0).Children(func() { a.sidebarFoot(c) })
			})
		}, content)
		a.collapsePull(before, dragging)
		a.sidebarW = min(max(a.sidebarW, theme.SidebarMinWidth), theme.SidebarMaxWidth)
		if changed {
			a.saveWidths()
		}
	})
}

// edgeGrip is the folded sidebar's way back: the same grip in the window's
// left inset, beside content; dragging it SidebarCollapsePull to the right
// (or a click, or, focused, the right arrow) brings the sidebar back at its width.
func (a *App) edgeGrip(c *ui.Context, content func()) {
	var pulled float32 // the grip's first pane stays empty: the drag only counts
	open := func() { a.sidebarHidden = false }
	changed, dragging := kit.Resizer(c, &pulled, c.Theme().Space(2), false, i18n.T("shell.resize"), open, func() {}, content)
	if changed && !dragging && pulled > 0 {
		a.sidebarHidden = false
		return
	}
	if !dragging {
		a.sidebarPull = 0
		return
	}
	if a.sidebarPull = max(0, a.sidebarPull+pulled); a.sidebarPull >= theme.SidebarCollapsePull {
		a.sidebarHidden, a.sidebarPull = false, 0
	}
}

// collapsePull folds the sidebar away when a drag goes on past its least
// width by SidebarCollapsePull, back at the width the drag started from
// (before is the width this frame started with).
func (a *App) collapsePull(before float32, dragging bool) {
	if !dragging {
		a.sidebarPull, a.sidebarDragFrom = 0, 0
		return
	}
	if a.sidebarDragFrom == 0 {
		a.sidebarDragFrom = before
	}
	// Past the least width the sidebar stays at it while the pull grows; a
	// move back right takes the pull back first.
	if a.sidebarPull > 0 || a.sidebarW < theme.SidebarMinWidth {
		if pull := a.sidebarPull + theme.SidebarMinWidth - a.sidebarW; pull >= 0 {
			a.sidebarPull, a.sidebarW = pull, theme.SidebarMinWidth
		} else {
			a.sidebarPull, a.sidebarW = 0, theme.SidebarMinWidth-pull
		}
	}
	if a.sidebarPull >= theme.SidebarCollapsePull {
		a.sidebarHidden = true
		a.sidebarW, a.sidebarPull, a.sidebarDragFrom = a.sidebarDragFrom, 0, 0
	}
}

// contentLeft is where the content's panels start, from the window's left edge.
func (a *App) contentLeft(c *ui.Context) float32 {
	th := c.Theme()
	return th.Space(2) + a.sidebarWidth() + th.Space(2)
}

// panelRadius is the corner radius of the content's panels (kit.Panel).
func panelRadius(c *ui.Context) float32 {
	_, r := chromeMetrics(c.TitleBar().Height, theme.Of(c))
	return r
}

// pageSwitch is the Mail / Wit switch at the top of their sidebars.
func (a *App) pageSwitch(c *ui.Context) ui.Element {
	return kit.NavSwitch(c,
		kit.NavItem{Icon: "mail", Label: i18n.T("shell.nav.mail"), Selected: a.route.Page == PageMail, OnClick: func() { a.navigate(Route{Page: PageMail, View: core.View{Unified: "inbox"}}) }},
		kit.NavItem{Icon: "sparkles", Label: i18n.T("shell.nav.wit"), Selected: a.route.Page == PageWit, OnClick: func() { a.navigate(Route{Page: PageWit}) }})
}

// sidebarInset is the sidebar's right inset. The sidebar itself has none:
// each of its parts keeps this much, and sidebarScroll's list keeps it inside
// the scroll area, which so reaches the sidebar's edge, where its scroll bar
// runs, clear of the items' counts.
func sidebarInset(c *ui.Context) float32 { return c.Theme().Space(3) }

// sidebarScroll scrolls a page's sidebar list (sidebarInset).
func sidebarScroll(c *ui.Context, children func()) {
	ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
		ui.Column(c).Gap(c.Theme().Space(0.5)).Padding(0, sidebarInset(c), 0, 0).Children(children)
	})
}

// sidebarFoot is pinned under the page's sidebar: Settings (and, in dev
// builds, the Kit); in settings, the way back to the page it was opened from,
// in the same spot.
func (a *App) sidebarFoot(c *ui.Context) {
	th := c.Theme()
	item := func(icon, label string, r Route) {
		b := kit.SidebarItem(c, kit.SidebarItemOpts{OnClick: func() { a.navigate(r) }}).Gap(th.Space(2.5))
		b.Children(func() {
			kit.SidebarIcon(c, b, icon, false)
			ui.Text(c, label)
		})
	}
	if a.route.Page == PageSettings {
		r := a.leaveSettings()
		name := i18n.T("shell.nav.mail")
		if r.Page == PageWit {
			name = i18n.T("shell.nav.wit")
		}
		item("arrow-left", i18n.Tf("shell.nav.back_to", name), r)
		return
	}
	if a.dev {
		item("component", "Kit", Route{Page: PageKit})
	}
	item("sliders-horizontal", i18n.T("shell.nav.settings"), Route{Page: PageSettings})
}

// leaveSettings is where settings' way back goes: the last page before
// settings in the history, else the inbox.
func (a *App) leaveSettings() Route {
	for i := len(a.history) - 1; i >= 0; i-- {
		if r := a.history[i]; r.Page == PageMail || r.Page == PageWit {
			return r
		}
	}
	return Route{Page: PageMail, View: core.View{Unified: "inbox"}}
}

// sidebarWidth is the page sidebars' width: as dragged, else as saved, else
// the default. Every page's sidebar takes it, so a drag on one moves them all.
func (a *App) sidebarWidth() float32 {
	if a.sidebarW != 0 {
		return a.sidebarW
	}
	return float32(cmp.Or(a.settings.SidebarWidth, theme.SidebarWidth))
}

// listWidth is the mail list's width: as dragged, else as saved, else the default.
func (a *App) listWidth() float32 {
	if a.listW != 0 {
		return a.listW
	}
	return float32(cmp.Or(a.settings.ListWidth, theme.ListWidth))
}

// sidebarSaveDelay is how long a drag rests before its width is saved.
const sidebarSaveDelay = 400 * time.Millisecond

// saveWidths saves the dragged widths (the sidebars', the mail list's) once
// the drag rests, not every frame.
func (a *App) saveWidths() {
	if a.sidebarSave != nil {
		a.sidebarSave()
	}
	a.sidebarSave = a.after(sidebarSaveDelay, func() {
		a.sidebarSave = nil
		st := a.settings
		round := func(w float32, saved int) int {
			if w == 0 {
				return saved // not dragged yet
			}
			return int(math.Round(float64(w)))
		}
		st.SidebarWidth, st.ListWidth = round(a.sidebarW, st.SidebarWidth), round(a.listW, st.ListWidth)
		if st.SidebarWidth != a.settings.SidebarWidth || st.ListWidth != a.settings.ListWidth {
			a.changeSettings(st)
		}
	})
}

// closeDialog asks, when a draft could not be saved before closing, whether
// to close anyway and lose its last edits.
func (a *App) closeDialog(c *ui.Context) {
	kit.Dialog(c, &a.closeAsk, i18n.T("close.unsaved.title"), kit.DialogOpts{Size: kit.DialogSm, Desc: humanize(a.closeErr), Close: i18n.T("common.close")}, nil, func() {
		kit.Button(c, i18n.T("close.unsaved.back"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(func() { a.closeAsk = false })
		kit.Button(c, i18n.T("close.unsaved.quit"), kit.ButtonOpts{Variant: kit.Destructive}).OnClick(func() {
			a.closeAsk = false
			a.closeAnyway()
		})
	})
}
