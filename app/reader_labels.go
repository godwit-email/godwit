package app

import (
	"context"
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// labelRow is under the subject: a thread's count of messages, then its
// labels, each with ×, then the button of the label menu. Changes apply to
// every message of the thread.
func (r *reader) labelRow(c *ui.Context) {
	a := r.p.a
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).Wrap().Gap(th.Space(1)).AlignItems(ui.Center).Children(func() {
		if len(r.msgs) > 1 {
			kit.Text(c, i18n.Tf("reader.thread.count", len(r.msgs)), t.Text.Sm).TextColor(t.Muted)
			ui.Box(c).Size(theme.Stroke, th.Space(3)).Margin(0, th.Space(1.5)).BorderWidth(0, 0, 0, theme.Hairline).BorderColor(t.Border)
		}
		for _, l := range r.labels {
			id := l.LabelID
			a.labelChip(c.Key(id), l, func(m *ui.Menu) { r.labelMenu(m, t) }, func() { r.setLabel(id, false) })
		}
		add := kit.Pressable(c, kit.PressableOpts{Bordered: true}).
			Label(i18n.T("reader.labels.add.label")).Height(theme.LabelChip).PaddingX(th.Space(1.5)).
			Gap(th.Space(1)).Radius(t.Radius.Sm).TextColor(t.Muted).Menu(func(m *ui.Menu) { r.labelMenu(m, t) })
		add.Children(func() {
			ui.Icon(c, icons.Get("plus")).Size(theme.IconXs, theme.IconXs)
			kit.Text(c, i18n.T("reader.labels.add"), t.Text.Sm)
		})
		// 退订, once a thread: it leaves the list, whichever message says how.
		if id := r.unsubMessage(); id != 0 {
			ui.Spacer(c)
			if r.bodies[id].unsubbing {
				kit.Spinner(c)
			} else {
				// as tall as the label chips beside it, on the secondary button's fill
				b := kit.Pressable(c, kit.PressableOpts{OnClick: func() { r.unsubscribe(id) }}).Label(i18n.T("reader.unsubscribe")).
					Height(theme.LabelChip).PaddingX(th.Space(1.5)).Gap(th.Space(1)).Radius(t.Radius.Sm).Background(t.Hover)
				b.Children(func() {
					ui.Icon(c, icons.Get("mail-forbid")).Size(theme.IconXs, theme.IconXs)
					kit.Text(c, i18n.T("reader.unsubscribe"), t.Text.Sm)
				})
			}
		}
	})
}

// labelState says whether a label is on the thread, and whether a model put it on.
func (r *reader) labelState(id int64) (on, auto bool) {
	l, on := appliedLabel(r.labels, id)
	return on, l.Auto
}

// labelMenu lists every label, checked when on the thread, and the way to manage them.
func (r *reader) labelMenu(m *ui.Menu, t theme.Tokens) {
	a := r.p.a
	labelItems(m, t, a.labels, r.labelState, r.setLabel)
	if len(a.labels) > 0 {
		m.Separator()
	}
	if m.Item(i18n.T("reader.labels.manage")).Chosen() {
		a.navigate(Route{Page: PageSettings, Settings: "labels"})
	}
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
