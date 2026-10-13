package app

import (
	"context"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// labelRow is the thread's labels under its subject, each with ×, then the
// button of the label menu. Changes apply to every message of the thread.
func (r *reader) labelRow(c *ui.Context) {
	a := r.p.a
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).Wrap().Gap(th.Space(1)).AlignItems(ui.Center).Children(func() {
		for _, l := range r.labels {
			id := l.LabelID
			a.labelChip(c.Key(id), l, func() { r.labelMenu = true }, func() { r.setLabel(id, false) })
		}
		add := kit.Pressable(c, kit.PressableOpts{Bordered: true, OnClick: func() { r.labelMenu = !r.labelMenu }}).
			Label(i18n.T("reader.labels.add.label")).Expanded(r.labelMenu).Height(theme.LabelChip).PaddingX(th.Space(1.5)).
			Gap(th.Space(1)).Radius(t.Radius.Sm).TextColor(t.Muted)
		add.Children(func() {
			ui.Icon(c, icons.Get("plus")).Size(theme.IconXs, theme.IconXs)
			kit.Text(c, i18n.T("reader.labels.add"), t.Text.Sm)
		})
		kit.Popover(c, add, &r.labelMenu, func() { r.labelMenuBody(c) })
	})
}

// labelMenuBody lists every label, checked when on the thread; the query
// filters them and offers to create one by a new name.
func (r *reader) labelMenuBody(c *ui.Context) {
	a := r.p.a
	t, th := theme.Of(c), c.Theme()
	kit.Input(c, &r.labelQuery, kit.InputOpts{Placeholder: i18n.T("reader.labels.search")}).Width(theme.LabelMenuWidth).AutoFocus().
		Label(i18n.T("reader.labels.search"))
	q := strings.TrimSpace(r.labelQuery)
	exact := false
	item := func(c *ui.Context, label string, on bool, click func(), body func()) {
		kit.Pressable(c, kit.PressableOpts{OnClick: click}).Label(label).Checked(on).Width(theme.LabelMenuWidth).
			Padding(th.Space(1.5), th.Space(2)).Gap(th.Space(2)).AlignItems(ui.Center).Radius(t.Radius.Md).Children(body)
	}
	for _, l := range a.labels {
		exact = exact || strings.EqualFold(l.Name, q)
		if q != "" && !strings.Contains(strings.ToLower(l.Name), strings.ToLower(q)) {
			continue
		}
		i := slices.IndexFunc(r.labels, func(x core.AppliedLabel) bool { return x.LabelID == l.ID })
		on, id := i >= 0, l.ID
		item(c.Key(id), l.Name, on, func() { r.setLabel(id, !on) }, func() {
			ui.Row(c).Width(theme.Icon).Children(func() {
				if on {
					ui.Icon(c, icons.Get("check")).Size(theme.Icon, theme.Icon)
				}
			})
			ui.Icon(c, icons.Get("label-"+l.Icon)).Size(theme.Icon, theme.Icon).TextColor(t.Label(l.Color).Text)
			kit.Text(c, l.Name, t.Text.Base).SingleLine().Grow(1).Shrink(1)
			if on && r.labels[i].Auto {
				kit.Text(c, i18n.T("reader.labels.auto"), t.Text.Sm).TextColor(t.Muted)
			}
		})
	}
	if q != "" && !exact {
		name := i18n.Tf("reader.labels.create", q)
		item(c, name, false, func() { r.createLabel(q) }, func() {
			ui.Icon(c, icons.Get("plus")).Size(theme.Icon, theme.Icon)
			kit.Text(c, name, t.Text.Base).SingleLine()
		})
	}
	kit.Separator(c, false)
	manage := i18n.T("reader.labels.manage")
	item(c, manage, false, func() {
		r.labelMenu = false
		a.navigate(Route{Page: PageSettings, Settings: "labels"})
	}, func() { kit.Text(c, manage, t.Text.Base) })
}

// setLabel puts a label on the thread by hand, or takes it off; the chips
// change at once and the change event reads them again.
func (r *reader) setLabel(id int64, on bool) {
	a, ids := r.p.a, r.threadIDs
	if on {
		if !slices.ContainsFunc(r.labels, func(l core.AppliedLabel) bool { return l.LabelID == id }) {
			r.labels = append(r.labels, core.AppliedLabel{LabelID: id})
		}
	} else {
		r.labels = slices.DeleteFunc(r.labels, func(l core.AppliedLabel) bool { return l.LabelID == id })
	}
	for i := range r.labels {
		if r.labels[i].LabelID == id {
			r.labels[i].Auto = false
		}
	}
	a.writes.Chain("labels", func(ctx context.Context) error { return a.svc.SetLabel(ctx, ids, id, on) }, a.toastErr)
}

// createLabel makes a gray label of name and puts it on the thread.
func (r *reader) createLabel(name string) {
	a, ids := r.p.a, r.threadIDs
	r.labelQuery = ""
	a.writes.Chain("labels", func(ctx context.Context) error {
		l, err := a.svc.CreateLabel(ctx, core.LabelInput{Name: name, Color: "gray", Icon: "folder-3"})
		if err != nil {
			return err
		}
		return a.svc.SetLabel(ctx, ids, l.ID, true)
	}, a.toastErr)
}
