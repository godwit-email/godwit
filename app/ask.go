package app

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// askPage is the Wit section: the conversations in the sidebar and, in the
// content, the Wit page (an embedded web page) showing the route's one (0:
// a new one).
type askPage struct {
	a       *App
	convs   []core.Conversation
	listGen uint64
	version int64 // the Wit page's snapshot as of the last navigation here
}

func newAskPage(a *App) *askPage { return &askPage{a: a} }

// convKey is the write-queue key of a conversation's writes.
func convKey(id int64) string { return "conv:" + strconv.FormatInt(id, 10) }

// refresh reads the conversations; navigated to, it tells the Wit page the
// route's one.
func (p *askPage) refresh(hints []events.Event) {
	p.load()
	if hints == nil && p.a.witOpen != nil {
		p.version = p.a.witOpen(p.a.route.ConversationID)
	}
}

func (p *askPage) load() {
	p.listGen++
	a, gen := p.a, p.listGen
	var cs []core.Conversation
	a.do(func(ctx context.Context) (err error) {
		cs, err = a.svc.ListConversations(ctx, "ask")
		return err
	}, func(err error) {
		if gen != p.listGen {
			return
		}
		if err != nil {
			a.toastErr(err)
			return
		}
		p.convs = cs
	})
}

func (p *askPage) title(id int64) string {
	if i := slices.IndexFunc(p.convs, func(c core.Conversation) bool { return c.ID == id }); i >= 0 && p.convs[i].Title != "" {
		return p.convs[i].Title
	}
	return i18n.T("ask.untitled")
}

func (p *askPage) Title() (string, string) {
	conv := p.a.route.ConversationID
	if conv == 0 {
		return "", ""
	}
	return p.title(conv), i18n.T("ask.permissions")
}

// Sidebar: the page switch, New conversation, then the recent ones; a right click deletes one.
func (p *askPage) Sidebar(c *ui.Context) {
	a := p.a
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).Padding(0, sidebarInset(c), th.Space(3), 0).Children(func() { a.pageSwitch(c) })
	newChat := kit.SidebarItem(c, kit.SidebarItemOpts{OnClick: func() { a.navigate(Route{Page: PageWit}) }}).Gap(th.Space(2.5)).Margin(0, sidebarInset(c), 0, 0)
	newChat.Children(func() {
		kit.SidebarIcon(c, newChat, "square-pen", false)
		ui.Text(c, i18n.T("ask.new"))
	})
	if len(p.convs) == 0 {
		return
	}
	kit.Text(c, i18n.T("ask.recent"), t.Text.Sm).TextColor(t.Muted).Padding(th.Space(3.5), th.Space(2)+sidebarInset(c), th.Space(1.5), th.Space(2))
	sidebarScroll(c, func() {
		for _, conv := range p.convs {
			id := conv.ID
			on := id == a.route.ConversationID
			kit.SidebarItem(c, kit.SidebarItemOpts{Selected: on, OnClick: func() { a.navigate(Route{Page: PageWit, ConversationID: id}) }}).
				Children(func() {
					ui.Text(c, p.title(id)).SingleLine().Grow(1).Shrink(1)
					// seen from another conversation: one answering, one waiting on the user
					switch {
					case conv.Answering:
						kit.Spinner(c).Size(th.Space(3.5), th.Space(3.5)).Shrink(0).Label(i18n.T("wit.thinking"))
					case conv.Awaiting:
						ui.Box(c).Padding(th.Space(0.5), th.Space(1.5)).Radius(t.Radius.Sm).Background(t.WarningBg).Shrink(0).Children(func() {
							kit.Text(c, i18n.T("wit.approval.pending"), t.Text.Xs).TextColor(t.WarningText)
						})
					}
				}).
				ContextMenu(func(m *ui.Menu) {
					if m.Item(i18n.T("ask.delete")).Chosen() {
						p.remove(id)
					}
				})
		}
	})
}

// remove deletes a conversation; shown, a new one takes its place, and it
// leaves the history.
func (p *askPage) remove(id int64) {
	a := p.a
	a.writes.Chain(convKey(id), func(ctx context.Context) error { return a.svc.DeleteConversation(ctx, id) }, func(err error) {
		if err != nil {
			a.toastErr(err)
			return
		}
		gone := func(r Route) bool { return r.Page == PageWit && r.ConversationID == id }
		a.history, a.ahead = slices.DeleteFunc(a.history, gone), slices.DeleteFunc(a.ahead, gone)
		if gone(a.route) {
			a.setRoute(Route{Page: PageWit})
			return
		}
		p.load()
	})
}

// Content is the Wit page; without a model, how to add one, on a panel.
func (p *askPage) Content(c *ui.Context) {
	if p.a.settings.DefaultModel == "" {
		kit.Panel(c, panelRadius(c)).Grow(1).Children(func() { p.noModel(c) })
		return
	}
	if v := p.a.witView; v != nil {
		ui.WebView(c, v).Grow(1) // transparent: the page draws its own panels
	}
}

func (p *askPage) noModel(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	ui.Column(c).Grow(1).Center().Gap(th.Space(3)).Padding(th.Space(6)).Children(func() {
		ui.Text(c, i18n.T("ask.no_model")).TextColor(t.Muted).MaxWidth(theme.FormMaxWidth)
		kit.Button(c, i18n.T("ask.add_model"), kit.ButtonOpts{}).OnClick(p.a.OpenModels)
	})
}

// OpenMessage opens message id in the reader (a mail the Wit page cites).
func (a *App) OpenMessage(id int64) {
	var m core.Message
	a.query(func(ctx context.Context) (err error) {
		m, err = a.svc.GetMessage(ctx, id, false)
		return err
	}, func(err error) {
		if err != nil {
			a.toastErr(err)
			return
		}
		a.navigate(Route{Page: PageMail, View: core.View{Unified: "inbox"}, ThreadID: m.ThreadID, MessageID: id})
	})
}

// OpenSettings opens the settings, as the app menu's Settings… does.
func (a *App) OpenSettings() { a.navigate(Route{Page: PageSettings}) }

// OpenModels opens the model settings (the Wit page's model chip).
func (a *App) OpenModels() { a.navigate(Route{Page: PageSettings, Settings: "models"}) }

// Navigate goes back (dir < 0) or forward (dir > 0), as ⌘[ and ⌘] do: the
// Wit page forwards them, as native shortcuts miss keys it has.
func (a *App) Navigate(dir int) {
	switch {
	case dir < 0:
		a.back()
	case dir > 0:
		a.forward()
	}
}

// ConversationCreated is told the Wit page's first question made
// conversation id, under snapshot version: while the route still shows
// that version's new conversation it takes id in place (back and forward
// keep it), and the page is told it as a new version, so a page that
// reloaded meanwhile shows it too; the sidebar reads the conversations
// again either way.
func (a *App) ConversationCreated(id, version int64) {
	p := a.pages[PageWit].(*askPage)
	if r := &a.route; r.Page == PageWit && r.ConversationID == 0 && version == p.version {
		r.ConversationID = id
		if a.witCreated != nil {
			p.version = a.witCreated(id)
		}
	}
	p.load()
}

// SetWitPage gives the App the Wit page's web view and open, which tells
// the page the route's conversation and returns its snapshot version;
// created does the same for a conversation the page's first question made.
// Call it before Start.
func (a *App) SetWitPage(view ui.NativeWebView, open, created func(conv int64) (version int64)) {
	a.witView, a.witOpen, a.witCreated = view, open, created
}

// SetWitAsk gives the App the hand-off of a question to the Wit page (the
// page starts the run itself, so it follows it and Stop works). Call it
// before Start.
func (a *App) SetWitAsk(ask func(query string)) { a.witAsk = ask }

// askWit shows the Wit page with query asked in a new conversation. Without
// a default model it only navigates: the page says how to add one.
func (a *App) askWit(query string) {
	a.navigate(Route{Page: PageWit})
	if a.settings.DefaultModel != "" && a.witAsk != nil && strings.TrimSpace(query) != "" {
		a.witAsk(query)
	}
}
