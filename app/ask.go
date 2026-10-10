package app

import (
	"cmp"
	"context"
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// askPage is the Wit section: the conversations in the sidebar, the one of
// the route (0: a new one) in a Wit panel, and the mail its runs cited.
type askPage struct {
	a       *App
	wit     *witPanel
	convs   []core.Conversation
	listGen uint64

	start func(ctx context.Context, conv int64, input string) (<-chan core.WitEvent, error) // tests swap it
}

func newAskPage(a *App) *askPage {
	return &askPage{a: a, wit: newWitPanel(a), start: a.svc.Ask}
}

// refresh reads the conversations; navigated to, it opens the route's one.
// An approvals.changed event of the one shown reads it again.
func (p *askPage) refresh(hints []events.Event) {
	p.load()
	if hints == nil {
		p.wit.open(p.a.route.ConversationID)
		return
	}
	if conv := p.wit.conv; conv != 0 && slices.ContainsFunc(hints, func(e events.Event) bool {
		return e.Type == events.ApprovalsChanged && e.ConversationID == conv
	}) {
		p.wit.reconcile()
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
	if p.wit.conv == 0 {
		return "", ""
	}
	return p.title(p.wit.conv), i18n.T("ask.permissions")
}

// Sidebar: New conversation, then the recent ones; a right click deletes one.
func (p *askPage) Sidebar(c *ui.Context) {
	a := p.a
	t, th := theme.Of(c), c.Theme()
	ui.Text(c, i18n.T("ask.heading")).FontSize(t.Text.Xl).FontWeight(600).Padding(th.Space(1), th.Space(1.5), th.Space(2.5), th.Space(1.5))
	kit.SidebarItem(c, kit.SidebarItemOpts{OnClick: func() { a.navigate(Route{Page: PageWit}) }}).Gap(th.Space(2.5)).FontWeight(500).Children(func() {
		ui.Icon(c, icons.Get("square-pen")).Size(theme.Icon, theme.Icon)
		ui.Text(c, i18n.T("ask.new"))
	})
	if len(p.convs) == 0 {
		return
	}
	ui.Text(c, i18n.T("ask.recent")).FontSize(t.Text.Sm).TextColor(t.Muted).Padding(th.Space(3.5), th.Space(2), th.Space(1.5), th.Space(2))
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Gap(th.Space(0.5)).Children(func() {
			for _, conv := range p.convs {
				id := conv.ID
				on := id == p.wit.conv
				kit.SidebarItem(c, kit.SidebarItemOpts{Selected: on, OnClick: func() { a.navigate(Route{Page: PageWit, ConversationID: id}) }}).
					Children(func() { ui.Text(c, p.title(id)).SingleLine().Grow(1).Shrink(1) }).
					ContextMenu(func(m *ui.Menu) {
						if m.Item(i18n.T("ask.delete")).Chosen() {
							p.remove(id)
						}
					})
			}
		})
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

// Content is the Wit panel and, once runs cited mail, the cited mail;
// without a model, how to add one.
func (p *askPage) Content(c *ui.Context) {
	a := p.a
	if a.settings.DefaultModel == "" {
		p.noModel(c)
		return
	}
	conv, start, svc := p.wit.conv, p.start, a.svc
	o := witOptions{
		Placeholder: i18n.T("wit.placeholder"),
		MaxWidth:    theme.WitMaxWidth,
		Start: func(ctx context.Context, input string) (int64, <-chan core.WitEvent, func(), error) {
			id := conv
			if id == 0 {
				c, err := svc.NewConversation(ctx, "ask", 0)
				if err != nil {
					return 0, nil, nil, err
				}
				id = c.ID
			}
			ch, err := start(ctx, id, input)
			return id, ch, nil, err
		},
		Changed: func() {
			if r := &a.route; r.Page == PageWit && r.ConversationID == 0 {
				r.ConversationID = p.wit.conv // the conversation the first question made
			}
			p.load()
		},
	}
	ui.Row(c).Grow(1).AlignItems(ui.Stretch).Children(func() {
		ui.Column(c).Grow(1).Shrink(1).Children(func() { p.wit.View(c, o) })
		if len(p.wit.cites) > 0 {
			p.citations(c)
		}
	})
}

func (p *askPage) noModel(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	ui.Column(c).Grow(1).Center().Gap(th.Space(3)).Padding(th.Space(6)).Children(func() {
		ui.Text(c, i18n.T("ask.no_model")).TextColor(t.Muted).MaxWidth(theme.FormMaxWidth)
		kit.Button(c, i18n.T("ask.add_model"), kit.ButtonOpts{}).OnClick(func() {
			p.a.navigate(Route{Page: PageSettings, Settings: "models"})
		})
	})
}

// citations lists the mail the runs showed the model, numbered as the
// answers cite them, with sender and date; suspect mail is marked. A click
// opens the message.
func (p *askPage) citations(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	ui.Column(c).Width(theme.CitationsWidth).Gap(th.Space(2.5)).Padding(th.Space(4)).Background(t.Sidebar).
		BorderWidth(0, 0, 0, theme.Hairline).BorderColor(t.Border).Children(func() {
		ui.Text(c, i18n.T("ask.citations")).FontWeight(600)
		now := p.a.clock.Now()
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Gap(th.Space(2.5)).Children(func() {
				for i, ct := range p.wit.cited() {
					border := t.Border
					if ct.Suspect {
						border = t.WarningBorder
					}
					id := ct.MessageID
					kit.Pressable(c, kit.PressableOpts{Bordered: true, OnClick: func() { p.openCited(id) }}).Label(ct.Subject).Padding(th.Space(2.5), th.Space(3)).
						Border(theme.Hairline, border).Children(func() {
						ui.Column(c).Gap(th.Space(1)).Grow(1).Shrink(1).Children(func() {
							ui.Row(c).Gap(th.Space(2)).FontSize(t.Text.Sm).TextColor(t.Muted).Children(func() {
								ui.Text(c, citeRef(i+1)+" "+ct.From).SingleLine().Grow(1).Shrink(1)
								if !ct.Date.IsZero() {
									ui.Text(c, listTime(now, ct.Date)).Shrink(0)
								}
							})
							ui.Text(c, cmp.Or(ct.Subject, i18n.T("mail.no_subject")))
							if ct.Suspect {
								ui.Text(c, i18n.T("ask.citation.suspect")).FontSize(t.Text.Sm).TextColor(t.WarningText)
							}
						})
					})
				}
			})
		})
	})
}

// openCited opens a cited message in the reader.
func (p *askPage) openCited(id int64) {
	a := p.a
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
