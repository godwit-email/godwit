package app

import (
	"context"
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// outboxEntry is an outbox item with its account's name.
type outboxEntry struct {
	core.OutboxItem
	Account string
}

// outboxView lists mail not yet sent and filed, with its state; mail that
// may or may not have gone out, or failed, waits for the user's decision.
func (p *mailPage) outboxView(c *ui.Context) {
	t := theme.Of(c)
	th, small := c.Theme(), t.Text.Sm
	ui.Text(c, i18n.T("mail.outbox")).FontWeight(500).TextColor(t.Muted).Padding(th.Space(3), th.Space(4), th.Space(2), th.Space(4))
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(0, th.Space(4)).Gap(th.Space(3)).Children(func() {
			if len(p.outbox) == 0 {
				ui.Text(c, i18n.T("mail.empty")).TextColor(t.Muted)
			}
			for _, it := range p.outbox {
				ui.Column(c.Key(it.ID)).Gap(th.Space(1)).Padding(th.Space(2.5)).Radius(t.Radius.Lg).Border(theme.DeviceHairline, t.Border).Children(func() {
					subject := it.Subject
					if subject == "" {
						subject = i18n.T("mail.no_subject")
					}
					ui.Text(c, subject).FontWeight(500).SingleLine()
					kit.Text(c, it.Account, small).TextColor(t.Muted).SingleLine()
					kit.Text(c, i18n.T("outbox.state."+it.State), small).TextColor(t.Muted)
					if it.LastError != "" {
						kit.Text(c, it.LastError, small).TextColor(t.Danger)
					}
					if decidable(it.OutboxItem) {
						ui.Row(c).Wrap().Gap(th.Space(1.5)).Margin(th.Space(1), 0, 0, 0).Children(func() {
							for _, act := range []string{"confirm_sent", "resend", "discard"} {
								v := kit.Outline
								if act == "discard" {
									v = kit.Destructive
								}
								kit.Button(c, i18n.T("outbox."+act), kit.ButtonOpts{Variant: v, Size: kit.SizeSm}).
									OnClick(func() { p.resolve(it.ID, act) })
							}
						})
					}
				})
			}
		})
	})
}

func decidable(it core.OutboxItem) bool {
	return it.State == "send_uncertain" || it.State == "archive_uncertain" || it.State == "failed"
}

func (p *mailPage) resolve(id int64, action string) {
	a := p.a
	a.writes.Chain(fmt.Sprintf("outbox:%d", id), func(ctx context.Context) error { return a.svc.ResolveOutbox(ctx, id, action) }, func(err error) {
		if err != nil {
			a.toastErr(err)
		}
		p.reload()
	})
}
