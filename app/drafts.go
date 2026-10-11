package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// draftsView lists the account's local drafts: a click opens one in a
// compose window, its menu deletes it.
func (p *mailPage) draftsView(c *ui.Context) {
	t := theme.Of(c)
	th, small := c.Theme(), t.Text.Sm
	title, _ := p.Title()
	ui.Text(c, title).FontWeight(500).TextColor(t.Muted).Padding(th.Space(3), th.Space(4), th.Space(2), th.Space(4))
	drafts := p.drafts
	p.draftList.Key = func(i int) any { return drafts[i].ID }
	kit.List(c, &p.draftList, len(drafts), func(i int) {
		d := drafts[i]
		subject, to := d.Subject, strings.Join(d.To, ", ")
		if subject == "" {
			subject = i18n.T("mail.no_subject")
		}
		if to == "" {
			to = i18n.T("mail.no_recipient")
		}
		kit.Pressable(c, kit.PressableOpts{OnClick: func() { p.a.plat.OpenCompose(d.ID) }}).Padding(th.Space(2.5)).Children(func() {
			ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(0.5)).Children(func() {
				ui.Row(c).Gap(th.Space(1.5)).Children(func() {
					ui.Text(c, to).SingleLine().Grow(1).Shrink(1)
					ui.Text(c, listTime(p.a.clock.Now(), d.UpdatedAt)).FontSize(small).TextColor(t.Muted)
				})
				ui.Text(c, subject).SingleLine()
			})
		}).ContextMenu(func(m *ui.Menu) {
			if m.Item(i18n.T("mail.draft.delete")).Chosen() {
				p.deleteDraft(d.ID)
			}
		})
	}).Grow(1).Padding(0, th.Space(2)).Children(func() {
		if len(drafts) == 0 && !p.loading {
			ui.Text(c, i18n.T("mail.empty")).TextColor(t.Muted).Padding(th.Space(3))
		}
	})
}

func (p *mailPage) deleteDraft(id int64) {
	a := p.a
	a.writes.Chain(fmt.Sprintf("draft:%d", id), func(ctx context.Context) error { return a.svc.DeleteDraft(ctx, id) }, func(err error) {
		if err != nil {
			a.toastErr(err)
		} else {
			a.plat.DraftDeleted(id)
		}
		p.reload()
	})
}
