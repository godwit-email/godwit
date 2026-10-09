package app

import (
	"context"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/theme"
)

// draftsView lists the account's local drafts: a click opens one in the
// composer, its menu deletes it.
func (p *mailPage) draftsView(c *ui.Context) {
	t := theme.Of(c)
	s, small := t.Scale, t.FontSize-1
	title, _ := p.Title()
	ui.Text(c, title).FontWeight(500).TextColor(t.Muted).Padding(12*s, 16*s, 8*s, 16*s)
	drafts := p.drafts
	p.draftList.Key = func(i int) any { return drafts[i].ID }
	ui.List(c, &p.draftList, len(drafts), func(i int) {
		d := drafts[i]
		subject, to := d.Subject, strings.Join(d.To, ", ")
		if subject == "" {
			subject = i18n.T("mail.no_subject")
		}
		if to == "" {
			to = i18n.T("mail.no_recipient")
		}
		ui.ButtonBase(c).Padding(10 * s).Radius(8).Children(func() {
			ui.Column(c).Grow(1).Shrink(1).Gap(2 * s).Children(func() {
				ui.Row(c).Gap(6 * s).Children(func() {
					ui.Text(c, to).SingleLine().Grow(1).Shrink(1)
					ui.Text(c, listTime(p.a.clock.Now(), d.UpdatedAt)).FontSize(small).TextColor(t.Muted)
				})
				ui.Text(c, subject).SingleLine()
			})
		}).OnClick(func() { p.a.navigate(Route{Page: PageCompose, DraftID: d.ID}) }).
			ContextMenu(func(m *ui.Menu) {
				if m.Item(i18n.T("mail.draft.delete")).Chosen() {
					p.deleteDraft(d.ID)
				}
			})
	}).Grow(1).Padding(0, 8*s).Children(func() {
		if len(drafts) == 0 && !p.loading {
			ui.Text(c, i18n.T("mail.empty")).TextColor(t.Muted).Padding(12 * s)
		}
	})
}

func (p *mailPage) deleteDraft(id int64) {
	a := p.a
	a.do(func(ctx context.Context) error { return a.svc.DeleteDraft(ctx, id) }, func(err error) {
		if err != nil {
			a.toastErr(err)
		}
		p.reload()
	})
}
