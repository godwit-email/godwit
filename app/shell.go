package app

import (
	"cmp"
	"context"
	"math"
	"runtime"
	"slices"
	"sync"
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

// breadcrumber is a page whose title bar shows where it is (parent › current)
// in place of a title and the search: a settings sub-page. Clicking the
// parent calls BreadcrumbUp.
type breadcrumber interface {
	Breadcrumb() (parent, current string, ok bool)
	BreadcrumbUp()
}

// goos is a var so a test can pose as another platform.
var goos = runtime.GOOS

// splitChrome reports whether the platform splits the global title bar (logo
// menu, navigation, centered search, native window controls) from a Mica
// context bar aligned with the sidebar, list and reader below (Windows and
// Linux). macOS keeps its single-row unified title bar.
func splitChrome() bool { return goos != "darwin" }

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
	if a.mail.askToLeave(func() { a.navigate(r) }) {
		return
	}
	if r == a.route {
		a.mail.closeViewEditor() // the editor covers this route's list
		return
	}
	a.history, a.ahead = append(a.history, a.route), nil
	a.setRoute(r)
}

func (a *App) back() {
	if a.mail.askToLeave(a.back) {
		return
	}
	if n := len(a.history); n > 0 {
		a.ahead = append(a.ahead, a.route)
		r := a.history[n-1]
		a.history = a.history[:n-1]
		a.setRoute(r)
	} else if a.route.Page == PageSettings { // settings opened with no history still has a way out
		a.navigate(a.leaveSettings())
	}
}

func (a *App) forward() {
	if a.mail.askToLeave(a.forward) {
		return
	}
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
	if a.goneLabel(r) || a.goneView(r) {
		r = Route{Page: PageMail, View: core.View{Unified: "inbox"}}
	}
	a.mail.closeViewEditor()
	if r.Page != PageMail {
		a.mail.stopOlder() // older mail is fetched only while the list shows
		a.mail.reader.pausePaint()
	}
	if r.Page != PageSettings || r.Settings != "about" {
		a.pages[PageSettings].(*settingsPage).closeBird() // its WebGL runs only while About shows
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
	case events.RunsChanged: // only the Wit sidebar shows it: no refresh of the rest
		if p, ok := a.pages[PageWit].(*askPage); ok && a.route.Page == PageWit {
			p.load()
		}
		return
	case events.MCPSendsChanged: // only the confirmation dialog shows them
		a.loadMCPSends()
		return
	case events.ImportProgress: // only its toast shows it; MessagesChanged refreshes the rest
		a.mail.importProgress(ev)
		return
	case events.NotificationClicked: // the backend's notification
		a.NotificationClicked(ev.Notification)
		return
	case events.OpenRequested: // the backend's tray
		a.Open(OpenWindow)
		return
	case events.ComposeRequested: // the backend's tray
		a.Open(OpenCompose)
		return
	case events.QuitRequested: // the backend's tray
		if a.quit != nil {
			a.quit()
		}
		return
	case events.AddressBooksChanged:
		a.mail.reader.avatarsChanged(contactsChanged)
		a.contacts().avatars = nil
	case events.SyncStarted:
		a.syncing[ev.AccountID] = true
	case events.SyncFinished:
		delete(a.syncing, ev.AccountID)
		delete(a.syncErr, ev.AccountID)
		now := a.clock.Now()
		if a.syncBatch == nil { // a Sync All sets it once all its accounts finish
			a.syncedAt = now
		}
		a.syncedAtBy[ev.AccountID] = now
		a.mail.older.notYet = 0
		a.tick()
	case events.SyncFailed:
		delete(a.syncing, ev.AccountID)
		f := a.syncErr[ev.AccountID]
		f.err, f.kind = ev.Error, ev.Kind
		if f.since.IsZero() {
			f.since = a.clock.Now()
		}
		a.syncErr[ev.AccountID] = f
		a.tick() // a failure that lasts turns red
	case events.OutboxChanged:
		a.offerUndo()
	case events.PluginsChanged: // a card's plugin may have been switched
		a.mail.reader.reloadPluginParts()
	case events.SkillsChanged:
		r := a.mail.reader
		r.skills.restart()
		if r.loaded {
			r.loadSkills(true)
		}
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
	if len(dirty) > 0 && !slices.ContainsFunc(dirty, func(ev events.Event) bool { return ev.Type != events.SkillsChanged }) {
		if a.route.Page == PageSettings && a.pages[PageSettings].(*settingsPage).showsSkills() {
			a.page().refresh(dirty)
		}
		return // onEvent 已刷新阅读器的 Skill，隐藏状态也包含在内。
	}
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

// actionToastTime is how long a toast with an action shows unless it says,
// longer than a plain one so the user can reach the action.
const actionToastTime = 8 * time.Second

func (a *App) toast(msg string) { a.toastAs("", msg) }

// toastAs shows msg as a toast of a kind: kit.ToastSuccess, ToastWarning,
// ToastError, or "" for a plain one.
func (a *App) toastAs(kind, msg string) {
	a.toasts = append(a.toasts, ui.Toast{ID: msg, Title: msg, Type: kind})
}

// toastErr shows err in words; a cancellation shows nothing. An error
// humanize has no words for offers to report it as a GitHub issue, which
// the user reads and submits in the browser.
func (a *App) toastErr(err error) {
	msg := humanize(err)
	switch {
	case msg == "":
	case msg == i18n.Tf("error.unknown", err.Error()):
		errText := err.Error()
		a.toasts = append(a.toasts, ui.Toast{ID: msg, Title: msg, Type: kit.ToastError,
			Action: i18n.T("error.report"), OnAction: func() {
				a.do(func(context.Context) error { return a.plat.OpenURL(issueURL(errText, a.svc.RecentLog())) }, a.toastErr)
			}})
	default:
		a.toastAs(kit.ToastError, msg)
	}
}

// View builds the window: the title bar, then the page's sidebar on the
// window material beside its content. First run shows only the onboarding page.
func (a *App) View(c *ui.Context) {
	theme.Apply(c, theme.FromSettings(a.settings, c.Theme().Dark))
	t, th := theme.Of(c), c.Theme()
	root := t.Window
	if a.route.Page != PageOnboarding {
		root = a.windowFill(t)
	}
	kit.ToastNow(c, a.clock.Now())
	for _, ts := range a.toasts {
		if ts.Action != "" {
			if ts.Timeout == 0 {
				ts.Timeout = actionToastTime
			}
			if ts.OnAction != nil {
				ts.OnAction = sync.OnceFunc(ts.OnAction)
			}
		}
		c.AddToast(ts)
	}
	for _, id := range a.closeToasts {
		c.CloseToast(id)
	}
	a.toasts, a.closeToasts = nil, nil
	kit.DialogShown() // forget another window's
	kit.NewFrame(c)

	if c.Shortcut(ui.Cmd, ui.KeyBracketLeft) {
		a.back()
	}
	if c.Shortcut(ui.Cmd, ui.KeyBracketRight) {
		a.forward()
	}
	barH, _ := chromeMetrics(c.TitleBar().Height, theme.Of(c))
	switch {
	case !a.loaded:
		bareBar(c, barH, true)
		if a.loadErr != nil {
			ui.Column(c).Grow(1).FillWidth().Center().Gap(th.Space(3)).Children(func() {
				ui.Text(c, humanize(a.loadErr)).TextColor(t.Muted)
				kit.Button(c, i18n.T("common.retry"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(a.load)
			})
		}
	case a.route.Page == PageOnboarding:
		bareBar(c, barH, false) // onboarding draws the brand mark in its left column
		ui.Column(c).Grow(1).Children(func() { a.page().Content(c) })
	default:
		if c.Shortcut(ui.Cmd, ui.KeyK) {
			a.openPalette()
		}
		a.titleBar(c, barH)
		if splitChrome() {
			a.contextBar(c)
		}
		a.body(c)
		a.palette.view(c)
	}
	if a.addAccount != nil {
		a.addAccount.sheetView(c)
	}
	a.linkDialog(c)
	a.closeDialog(c)
	a.mail.emptyFolderDialog(c)
	a.mail.nameDialogView(c)
	a.mail.reader.egressDialog(c)
	a.mail.reader.unsubscribeDialog(c)
	a.mail.reader.attachmentConflictDialog(c)
	a.mcpSendDialog(c)
	a.replyDateDialog(c)
	a.pages[PageSettings].(*settingsPage).importDialog(c)
	kit.ToastHost(c, i18n.T("toast.close"))
	// Last, on the root, which overlays (menus, dialogs, Select lists, toasts)
	// attach to: a c.SetTheme mid-frame resets its background to the theme's,
	// which would cover the window's material.
	if kit.DialogShown() {
		root = t.Window
	}
	c.Root().Background(root)
}

// titleShrink makes the title give way first when the window is narrow: the
// search field and the actions keep their room until it is all gone.
const titleShrink = 100

// titleBar leaves the window controls their room (c.TitleBar) and drags the
// window: on macOS it is the single-row unified bar; on Windows and Linux it
// delegates to splitTitleBar, paired with contextBar below.
func (a *App) titleBar(c *ui.Context, h float32) {
	if splitChrome() {
		a.splitTitleBar(c, h)
		return
	}
	t, th := theme.Of(c), c.Theme()
	bar := c.TitleBar()
	padL := bar.Left + th.Space(3)
	padR := bar.Right + th.Space(2)
	ui.Row(c).Height(h).FillWidth().Padding(0, padR, 0, padL).DragWindow().Children(func() {
		lead := ui.Row(c).Gap(th.Space(1))
		width := max(0, a.contentLeft(c)-padL)
		if a.sidebarHidden {
			width = 0
		} else if a.sidebarDragFrom != 0 {
			lead.Jump("sidebar-width", width)
		}
		lead.MinWidth(lead.Animate("sidebar-width", width, theme.MotionBase))
		lead.Children(func() {
			a.navButtons(c)
		})
		if b, ok := a.page().(breadcrumber); ok {
			if parent, current, ok := b.Breadcrumb(); ok {
				a.breadcrumbRow(c, "", parent, current, b.BreadcrumbUp, th.Space(4))
				return
			}
		}
		if a.route.Page == PageSettings {
			return
		}
		title, sub := a.page().Title()
		searchable := a.route.Page == PageMail || a.route.Page == PageContacts || a.route.Page == PageWit
		if title != "" || searchable { // the search field keeps its place without a title
			// the list's width and the gap to the reader, as the panels below
			ui.Row(c).AlignItems(ui.Center).Gap(th.Space(2)).Width(a.listWidth()+th.Space(2)).MinWidth(0).Shrink(titleShrink).
				Padding(0, 0, 0, th.Space(4)).Children(func() {
				ui.Text(c, title).FontWeight(500).SingleLine().MaxWidth(theme.TitleNameMax).Shrink(1)
				if sub != "" {
					kit.Text(c, sub, t.Text.Xs).TextColor(t.Muted).SingleLine().MaxWidth(theme.TitleSubMax).Shrink(1)
				}
				a.mail.titleMenu(c)
			})
		}
		if searchable {
			a.titleSearch(c)
		}
		ui.Spacer(c)
		if a.route.Page == PageMail {
			a.mail.titleActions(c)
		}
		if p, ok := a.page().(*contactsPage); ok {
			p.titleActions(c)
		}
		a.titleSync(c)
	})
}

// splitTitleBar is the Windows/Linux global title bar (Fluent 2 A1): the
// clickable Godwit brand menu + sidebar/history navigation at left (flush with
// the sidebar's 20 px left edge), the centered global search field with wide
// drag space on both sides, and clear room for native window controls on the right.
func (a *App) splitTitleBar(c *ui.Context, h float32) {
	th := c.Theme()
	bar := c.TitleBar()
	padL := bar.Left + th.Space(5) // align with sidebar content (8 + 12 = 20)
	padR := bar.Right + th.Space(2)
	ui.Row(c).Height(h).FillWidth().AlignItems(ui.Center).Padding(0, padR, 0, padL).DragWindow().Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(th.Space(1)).Shrink(0).Children(func() {
			a.brandMenu(c)
			kit.GroupDivider(c).Margin(0, th.Space(1))
			a.navButtons(c)
		})
		ui.Spacer(c).MinWidth(th.Space(3))
		a.titleSearch(c)
		ui.Spacer(c).MinWidth(th.Space(3))
	})
}

// brandMenu is the Windows/Linux top-left Godwit mark: clicking it opens the
// compact application menu (Compose, Sync All, Settings, Check for Updates,
// About, Quit) without crowding the title bar with a permanent menu strip.
func (a *App) brandMenu(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	b := kit.Pressable(c, kit.PressableOpts{}).Label("Godwit").
		AlignItems(ui.Center).Gap(th.Space(2)).Padding(th.Space(1), th.Space(2), th.Space(1), th.Space(1)).
		Menu(func(m *ui.Menu) {
			if m.Item(i18n.T("mail.compose")).Icon(menuIcon("square-pen")).Chosen() {
				a.Compose()
			}
			if m.Item(i18n.T("shell.sync.all")).Icon(menuIcon("refresh-cw")).Chosen() {
				a.SyncAll()
			}
			m.Separator()
			if m.Item(i18n.T("shell.nav.settings")).Icon(menuIcon("settings")).Chosen() {
				a.OpenSettings()
			}
			if a.plat != nil {
				if m.Item(i18n.T("settings.about.updates.check")).Icon(menuIcon("download")).Chosen() {
					a.plat.CheckForUpdates()
				}
			}
			if m.Item(i18n.T("settings.about")).Icon(menuIcon("info")).Chosen() {
				a.OpenAbout()
			}
			if a.quit != nil {
				m.Separator()
				if m.Item(i18n.T("tray.quit")).Chosen() {
					a.quit()
				}
			}
		})
	b.Children(func() {
		ui.Icon(c, logo).Size(theme.Icon, theme.Icon).TextColor(t.Foreground)
		kit.Text(c, "Godwit", t.Text.Xs).FontWeight(500).SingleLine().Role(ui.RoleNone)
	})
}

func (a *App) navButtons(c *ui.Context) {
	ghost := kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}
	label := i18n.T("shell.sidebar.collapse")
	if a.sidebarHidden {
		label = i18n.T("shell.sidebar.expand")
	}
	kit.IconButton(c, "panel-left", label, ghost).OnClick(a.ToggleSidebar)
	ghost.Disabled = len(a.history) == 0 && a.route.Page != PageSettings
	kit.IconButton(c, "arrow-left", i18n.T("shell.back"), ghost).OnClick(a.back)
	ghost.Disabled = len(a.ahead) == 0
	kit.IconButton(c, "arrow-right", i18n.T("shell.forward"), ghost).OnClick(a.forward)
}

func (a *App) breadcrumbRow(c *ui.Context, prefix, parent, current string, onUp func(), padL float32) {
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).AlignItems(ui.Center).Gap(th.Space(1)).Padding(0, 0, 0, padL).Children(func() {
		if prefix != "" {
			kit.Text(c, prefix, t.Text.Sm).TextColor(t.Muted).SingleLine()
			ui.Icon(c, icons.Get("chevron-right")).Size(theme.IconSm, theme.IconSm).TextColor(t.Muted)
		}
		if onUp != nil {
			up := kit.Pressable(c, kit.PressableOpts{Bare: true, OnClick: onUp}).Label(parent)
			fg := t.Muted
			if up.Hovered() {
				fg = t.Foreground
			}
			up.Children(func() { kit.Text(c, parent, t.Text.Sm).TextColor(fg).SingleLine().Role(ui.RoleNone) })
		} else {
			kit.Text(c, parent, t.Text.Sm).TextColor(t.Muted).SingleLine()
		}
		ui.Icon(c, icons.Get("chevron-right")).Size(theme.IconSm, theme.IconSm).TextColor(t.Muted)
		kit.Text(c, current, t.Text.Sm).FontWeight(500).SingleLine()
	})
}

// contextBar is the Windows/Linux 40 px Mica strip directly under the global
// title bar, aligned across all three columns with the sidebar, list and reader
// panels below so the white cards stay clean without internal header dividers.
func (a *App) contextBar(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	gap := th.Space(2)
	width := a.sidebarWidth()
	target := width
	if a.sidebarHidden {
		target = 0
	}
	row := ui.Row(c).Height(theme.ContextBarHeight).FillWidth().AlignItems(ui.Center).Padding(0, gap, 0, gap)
	if !a.sidebarHidden && a.sidebarDragFrom != 0 {
		row.Jump("ctx-sidebar-width", width)
	}
	visible := row.Animate("ctx-sidebar-width", target, theme.MotionBase)
	row.Children(func() {
		if visible > 0 {
			colW := visible
			if !a.sidebarHidden {
				colW += gap
			}
			ui.Row(c).Width(colW).FillHeight().Shrink(0).Clip().AlignItems(ui.Center).Children(func() {
				ui.Row(c).Width(width+gap).Shrink(0).AlignItems(ui.Center).Translate(visible-width, 0).
					Padding(0, sidebarInset(c), 0, th.Space(3)).Disabled(a.sidebarHidden).Children(func() {
					switch a.route.Page {
					case PageMail, PageContacts, PageWit:
						a.pageSwitch(c)
					case PageSettings:
						if sp, ok := a.pages[PageSettings].(*settingsPage); ok {
							sp.searchField(c)
						}
					}
				})
			})
		}
		ui.Row(c).Grow(1).MinWidth(0).AlignItems(ui.Center).Children(func() {
			if a.route.Page == PageSettings {
				if b, ok := a.page().(breadcrumber); ok {
					if parent, current, ok := b.Breadcrumb(); ok {
						a.breadcrumbRow(c, i18n.T("settings.heading"), parent, current, b.BreadcrumbUp, th.Space(2))
						return
					}
				}
				title, sub := a.page().Title()
				if sub != "" {
					a.breadcrumbRow(c, "", title, sub, nil, th.Space(2))
				}
				return
			}
			title, sub := a.page().Title()
			if a.route.Page == PageMail {
				ui.Row(c).AlignItems(ui.Center).Gap(th.Space(2)).Width(a.listWidth()+th.Space(2)).MinWidth(0).Shrink(titleShrink).
					Padding(0, th.Space(2), 0, th.Space(1)).Children(func() {
					ui.Text(c, title).FontWeight(500).SingleLine().MaxWidth(theme.TitleNameMax).Shrink(1)
					if sub != "" {
						kit.Text(c, sub, t.Text.Xs).TextColor(t.Muted).SingleLine().MaxWidth(theme.TitleSubMax).Shrink(1)
					}
					a.mail.titleMenu(c)
					ui.Spacer(c)
					if a.mail.showsFilterTabs() {
						a.mail.filterTabs(c)
					}
				})
				ui.Spacer(c)
				a.mail.titleActions(c)
				return
			}
			if p, ok := a.page().(*contactsPage); ok {
				ui.Row(c).AlignItems(ui.Center).Gap(th.Space(2)).Width(a.listWidth()+th.Space(2)).MinWidth(0).Shrink(titleShrink).
					Padding(0, th.Space(2), 0, th.Space(1)).Children(func() {
					ui.Text(c, title).FontWeight(500).SingleLine().MaxWidth(theme.TitleNameMax).Shrink(1)
					if sub != "" {
						kit.Text(c, sub, t.Text.Xs).TextColor(t.Muted).SingleLine().MaxWidth(theme.TitleSubMax).Shrink(1)
					}
				})
				ui.Spacer(c)
				p.titleActions(c)
				return
			}
			if title != "" {
				ui.Row(c).AlignItems(ui.Center).Gap(th.Space(2)).Padding(0, 0, 0, th.Space(1)).Children(func() {
					ui.Text(c, title).FontWeight(500).SingleLine().MaxWidth(theme.TitleNameMax).Shrink(1)
					if sub != "" {
						kit.Text(c, sub, t.Text.Xs).TextColor(t.Muted).SingleLine().MaxWidth(theme.TitleSubMax).Shrink(1)
					}
				})
			}
		})
		a.titleSync(c)
	})
}

// ToggleSidebar folds or expands the left navigation sidebar.
func (a *App) ToggleSidebar() { a.sidebarHidden = !a.sidebarHidden }

// OpenAbout navigates to Settings › About.
func (a *App) OpenAbout() { a.navigate(Route{Page: PageSettings, Settings: "about"}) }

// titleSearch is the top bar's search field of the mail, contacts and Wit
// pages (and all pages in splitChrome): a small flat input that opens the
// palette (⌘K / Ctrl K) at a click.
func (a *App) titleSearch(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	w := float32(theme.TitleSearchWidth)
	if splitChrome() {
		w = theme.SplitTitleSearchWidth
	}
	box := kit.InputButton(c, kit.InputOpts{Size: kit.SizeSm, Flat: true, Tint: true}, a.openPalette).Label(i18n.T("mail.search")).
		Width(w).MinWidth(0).Gap(th.Space(2))
	if !splitChrome() {
		box.Margin(0, th.Space(3), 0, 0)
	}
	box.Children(func() {
		ui.Icon(c, icons.Get("search")).Size(theme.Icon, theme.Icon).TextColor(t.Muted)
		ui.Text(c, i18n.T("mail.search")).TextColor(t.Muted).SingleLine().Grow(1).Shrink(1).Role(ui.RoleNone)
		kit.Kbd(c, "⌘K").Shrink(0)
	})
}

// bareBar is the title bar of a window with no page chrome: it drags the
// window and, when caption is true on Windows, names it.
func bareBar(c *ui.Context, h float32, caption bool) {
	ui.Row(c).Height(h).FillWidth().Padding(0, 0, 0, c.TitleBar().Left+c.Theme().Space(2)).DragWindow().Children(func() {
		if caption {
			winCaption(c)
		}
	})
}

// winCaption is Windows' app icon and name at the title bar's start.
func winCaption(c *ui.Context) {
	if goos != "windows" {
		return
	}
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).Gap(th.Space(2)).Padding(0, th.Space(2), 0, th.Space(1)).Children(func() {
		ui.Icon(c, logo).Size(theme.Icon, theme.Icon).TextColor(t.Foreground)
		kit.Text(c, "Godwit", t.Text.Xs).SingleLine()
	})
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
	gap := th.Space(2)
	padL := gap
	if a.sidebarHidden {
		padL = 0 // the edge grip takes the inset
	}
	row := ui.Row(c).Grow(1).AlignItems(ui.Stretch).Padding(0, gap, gap, padL)
	width := a.sidebarWidth()
	target := width
	if a.sidebarHidden {
		target = 0
	} else if a.sidebarDragFrom != 0 {
		row.Jump("sidebar-width", width)
	}
	visible := row.Animate("sidebar-width", target, theme.MotionBase)
	sidebar := func() {
		if visible == 0 {
			return
		}
		top := th.Space(3)
		if a.route.Page == PageMail || a.route.Page == PageWit || a.route.Page == PageContacts || splitChrome() {
			top = 0 // the page switch (or splitChrome's context bar) sits right above
		}
		// Keep the sidebar's layout at its full width while it slides out of the clipped pane.
		// Include gap on the right so sidebarScroll reaches the content panel's border;
		// sidebarInset keeps every sidebar item clear of both the gap and the scroll bar.
		ui.Column(c).Width(width+gap).FillHeight().Shrink(0).Translate(visible-width, 0).Disabled(a.sidebarHidden).
			Padding(top, 0, th.Space(3), th.Space(3)).Gap(th.Space(0.5)).Children(func() {
			ui.Column(c).Grow(1).MinHeight(0).Gap(th.Space(0.5)).Children(func() { p.Sidebar(c) })
			ui.Column(c).Gap(th.Space(0.5)).Padding(th.Space(2), sidebarInset(c), 0, 0).Children(func() { a.sidebarFoot(c) })
		})
	}
	row.Children(func() {
		content := func() {
			panel := ui.Column(c).Fill()
			kit.ToastAnchor(c, panel)
			panel.Children(func() { p.Content(c) })
		}
		if a.sidebarHidden {
			a.edgeGrip(c, visible, sidebar, content)
			return
		}
		pane := visible + gap
		before := pane
		changed, dragging := kit.ResizerOver(c, &pane, th.Space(1), i18n.T("shell.resize"), nil, sidebar, content)
		a.sidebarW = width + (pane - before)
		a.collapsePull(width, dragging)
		a.sidebarW = min(max(a.sidebarW, theme.SidebarMinWidth), theme.SidebarMaxWidth)
		if changed {
			a.saveWidths()
		}
	})
}

// edgeGrip is the folded sidebar's way back: the same grip in the window's
// left inset, beside content; dragging it SidebarCollapsePull to the right
// (or a click, or, focused, the right arrow) brings the sidebar back at its width.
func (a *App) edgeGrip(c *ui.Context, width float32, sidebar, content func()) {
	pulled := width
	open := func() { a.sidebarHidden = false }
	changed, dragging := kit.Resizer(c, &pulled, c.Theme().Space(2), i18n.T("shell.resize"), open, sidebar, content)
	pulled -= width
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

// pageSwitch is the Mail / Contacts / Wit switch at the top of their sidebars.
func (a *App) pageSwitch(c *ui.Context) ui.Element {
	routes := []Route{{Page: PageMail, View: core.View{Unified: "inbox"}}, {Page: PageContacts}, {Page: PageWit}}
	sel := slices.IndexFunc(routes, func(r Route) bool { return r.Page == a.route.Page })
	return kit.Tabs(c, &sel, kit.TabsOpts{}, []kit.TabItem{
		{Icon: "mail", Label: i18n.T("shell.nav.mail")},
		{Icon: "contacts", Label: i18n.T("shell.nav.contacts")},
		{Icon: "sparkles", Label: i18n.T("shell.nav.wit")},
	}).OnChange(func() { a.navigate(routes[sel]) })
}

// sidebarInset is the sidebar's right inset, including the gap to the content
// panel. The sidebar itself reaches the content panel's edge so sidebarScroll's
// scroll bar runs flush along the panel's border, clear of the items and counts.
func sidebarInset(c *ui.Context) float32 { return c.Theme().Space(3) + c.Theme().Space(2) }

// sidebarScroll scrolls a page's sidebar list (sidebarInset).
func sidebarScroll(c *ui.Context, children func()) {
	ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
		ui.Column(c).Gap(c.Theme().Space(0.5)).Padding(0, sidebarInset(c), 0, 0).Children(children)
	})
}

// sidebarFoot is pinned under the page's sidebar: the update card, and a single
// row holding Settings on the left and the sync status on the right (plus Kit
// above in dev builds); in settings, a hairline-separated back row in the same
// spot returns directly to the page settings was opened from.
func (a *App) sidebarFoot(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	a.updateCard(c)
	if a.route.Page == PageSettings {
		r := a.leaveSettings()
		name := i18n.T("shell.nav.mail")
		switch r.Page {
		case PageWit:
			name = i18n.T("shell.nav.wit")
		case PageContacts:
			name = i18n.T("shell.nav.contacts")
		}
		ui.Column(c).FillWidth().Padding(th.Space(2), 0, 0, 0).
			BorderWidth(theme.Hairline, 0, 0, 0).BorderColor(t.Border).Children(func() {
			b := kit.SidebarItem(c, kit.SidebarItemOpts{Small: true, OnClick: func() { a.navigate(r) }}).
				FillWidth().Gap(th.Space(2))
			b.Children(func() {
				kit.SidebarIcon(c, b, "arrow-left", false)
				kit.Text(c, i18n.Tf("shell.nav.back_to", name), t.Text.Sm).SingleLine()
			})
		})
		return
	}
	if a.dev {
		b := kit.SidebarItem(c, kit.SidebarItemOpts{OnClick: func() { a.navigate(Route{Page: PageKit}) }}).Gap(th.Space(2.5))
		b.Children(func() {
			kit.SidebarIcon(c, b, "component", false)
			ui.Text(c, "Kit")
		})
	}
	hs := a.syncHealths()
	alertCard, hasAlert := a.syncAlertCard(c, hs)
	foot := ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(th.Space(1))
	foot.Children(func() {
		b := kit.SidebarItem(c, kit.SidebarItemOpts{Small: true, OnClick: func() { a.navigate(Route{Page: PageSettings}) }}).
			Gap(th.Space(2)).Shrink(0)
		b.Children(func() {
			kit.SidebarIcon(c, b, "sliders-horizontal", false)
			kit.Text(c, i18n.T("shell.nav.settings"), t.Text.Sm).SingleLine()
		})
		a.syncStatus(c, hs, hasAlert)
	})
	if len(hs) > 0 {
		anchor := foot
		if hasAlert {
			anchor = alertCard
		}
		kit.PopoverAt(c, anchor, &a.syncOpen, ui.AnchorTopStart, ui.AnchorBottomStart, -theme.PopupOffset, func() { a.syncMenu(c, hs) })
	}
}

// updateCard offers the newer version the update feed has, above Settings:
// 重启以更新 when it installed itself, else 有新版本. Either opens the
// updater's window (the Store's page), which relaunches or installs.
func (a *App) updateCard(c *ui.Context) {
	if a.latest == "" {
		return
	}
	t, th := theme.Of(c), c.Theme()
	title := i18n.T("shell.update.available")
	if a.latestInstalls {
		title = i18n.T("shell.update.relaunch")
	}
	ver := "v" + a.latest
	b := kit.Pressable(c, kit.PressableOpts{Bare: true, OnClick: a.plat.CheckForUpdates}).Label(title+" "+ver).
		Padding(th.Space(2.5), th.Space(3)).Gap(th.Space(2)).Margin(0, 0, th.Space(1.5), 0).
		Radius(t.Radius.Xl).Border(theme.Hairline, t.Border)
	if t.Dark { // Background is darker than the window material there: a step lighter and a stronger edge, as a shadow would not show
		b.Background(t.HoverTint).Border(theme.Hairline, t.Input)
	} else {
		kit.Elevate(c, b.Background(t.Background), theme.ShadowSm)
	}
	arrow := t.Muted
	if b.Hovered() {
		arrow = t.Foreground
	}
	b.Children(func() {
		ui.Icon(c, logo).Size(theme.IconXl, theme.IconXl).TextColor(t.Foreground).Role(ui.RoleNone)
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Children(func() {
			ui.Text(c, title).FontWeight(500).SingleLine().Role(ui.RoleNone)
			kit.Text(c, ver, t.Text.Xs).TextColor(t.Muted).Role(ui.RoleNone)
		})
		ui.Icon(c, icons.Get("arrow-right")).Size(theme.Icon, theme.Icon).TextColor(arrow)
	})
}

// leaveSettings is where settings' way back goes: the last page before
// settings in the history, else the inbox.
func (a *App) leaveSettings() Route {
	for i := len(a.history) - 1; i >= 0; i-- {
		if r := a.history[i]; r.Page == PageMail || r.Page == PageWit || r.Page == PageContacts {
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
	kit.Dialog(c, &a.closeAsk, i18n.T("close.unsaved.title"), kit.DialogOpts{Size: kit.DialogSm, Alert: true, Desc: humanize(a.closeErr), Close: i18n.T("common.close")}, nil, func() {
		kit.Button(c, i18n.T("close.unsaved.back"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(func() {
			a.closeAsk = false
			a.QuitCancelled()
		})
		kit.Button(c, i18n.T("close.unsaved.quit"), kit.ButtonOpts{Variant: kit.Ghost, Tone: kit.ToneDanger}).OnClick(func() {
			a.closeAsk = false
			a.closeAnyway()
		})
	})
}

// QuitCancelled tells the backend the user kept the interface open at a
// quit's prompt, so a quit the tray's 退出并停止收信 began does not stop it
// later (CancelTrayQuit). UI thread.
func (a *App) QuitCancelled() {
	a.do(func(ctx context.Context) error { return a.svc.CancelTrayQuit(ctx) }, nil)
}
