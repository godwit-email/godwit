package app

import (
	"context"
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// listChips is how many chips a list row shows before "+N".
const listChips = 2

// label is the label of an id, as the last load read it.
func (a *App) label(id int64) (core.Label, bool) {
	for _, l := range a.labels {
		if l.ID == id {
			return l, true
		}
	}
	return core.Label{}, false
}

// labelChip draws an applied label; onClick and onRemove may be nil.
func (a *App) labelChip(c *ui.Context, al core.AppliedLabel, onClick, onRemove func()) {
	l, ok := a.label(al.LabelID)
	if !ok {
		return
	}
	tip := i18n.T("label.tip.manual")
	if al.Auto {
		tip = i18n.T("label.tip.auto")
	}
	o := kit.LabelChipOpts{Text: l.Name, Icon: l.Icon, Tip: tip, Color: theme.Of(c).Label(l.Color), Auto: al.Auto, OnClick: onClick}
	if onRemove != nil {
		o.OnRemove, o.RemoveLabel = onRemove, i18n.Tf("reader.labels.remove", l.Name)
	}
	kit.LabelChip(c, o)
}

// rowChips is a list row's labels: the first listChips, then "+N".
func (a *App) rowChips(c *ui.Context, ls []core.AppliedLabel) {
	if len(ls) == 0 {
		return
	}
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).Gap(th.Space(1)).AlignItems(ui.Center).Margin(th.Space(1), 0, 0, 0).Children(func() {
		for _, l := range ls[:min(len(ls), listChips)] {
			a.labelChip(c, l, nil, nil)
		}
		if n := len(ls) - listChips; n > 0 {
			kit.Text(c, i18n.Tf("label.more", n), t.Text.Sm).TextColor(t.Muted)
		}
	})
}

// withLabels fills the items' labels: a thread's from any of its messages.
func withLabels(ctx context.Context, svc *core.Service, items []mailItem) error {
	var threads, msgs []int64
	for _, it := range items {
		if it.Thread {
			threads = append(threads, it.ID)
		} else {
			msgs = append(msgs, it.ID)
		}
	}
	byThread, err := svc.ThreadLabels(ctx, threads)
	if err != nil {
		return err
	}
	byMsg, err := svc.MessageLabels(ctx, msgs)
	if err != nil {
		return err
	}
	for i, it := range items {
		if it.Thread {
			items[i].Labels = byThread[it.ID]
		} else {
			items[i].Labels = byMsg[it.ID]
		}
	}
	return nil
}

// labelNav is the sidebar's label group: the labels on some mail.
func (p *mailPage) labelNav(c *ui.Context) {
	a := p.a
	t, th := theme.Of(c), c.Theme()
	shown := false
	for _, l := range a.labels {
		if l.Count == 0 {
			continue
		}
		if !shown {
			shown = true
			kit.Text(c, i18n.T("mail.labels"), t.Text.Sm).TextColor(t.Muted).Padding(th.Space(1), th.Space(2), th.Space(1), th.Space(2))
		}
		v := core.View{LabelID: l.ID}
		on := a.route.Page == PageMail && a.route.View == v && !p.searched
		item := kit.SidebarItem(c, kit.SidebarItemOpts{Selected: on, OnClick: func() { a.navigate(Route{Page: PageMail, View: v}) }})
		item.Children(func() {
			ui.Icon(c, icons.Get("label-"+l.Icon)).Size(theme.Icon, theme.Icon).TextColor(t.Label(l.Color).Text)
			ui.Text(c, l.Name).SingleLine().Grow(1).Shrink(1)
			if l.Unread > 0 {
				kit.Text(c, strconv.Itoa(l.Unread), t.Text.Sm).TextColor(t.Muted)
			}
		})
	}
	if shown {
		ui.Box(c).Height(th.Space(2.5))
	}
}

// goneLabel reports a route to the view of a label that no longer exists.
func (a *App) goneLabel(r Route) bool {
	if r.Page != PageMail || r.View.LabelID == 0 || !a.loaded {
		return false
	}
	_, ok := a.label(r.View.LabelID)
	return !ok
}
