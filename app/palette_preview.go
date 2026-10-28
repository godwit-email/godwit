package app

import (
	"cmp"
	"context"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// palCard is what a mail's preview card loads: the sender's avatar, as the
// reader's, and the names of its attachments.
type palCard struct {
	avatar ui.ImageSource
	atts   []string
}

// preview builds the card of m: avatar, sender, full time, folder, subject,
// the start of the body with the query marked, and the attachments.
func (p *palette) preview(c *ui.Context, m core.MessageSummary) {
	t, th := theme.Of(c), c.Theme()
	pc := p.card(m)
	name := displayName(m.From)
	ui.Row(c).Gap(th.Space(2.5)).AlignItems(ui.Center).Children(func() {
		kit.AvatarImage(c, name, pc.avatar)
		ui.Column(c).Shrink(1).Children(func() {
			ui.Text(c, name).FontWeight(500).SingleLine()
			kit.Text(c, senderDate(p.a.clock.Now(), m.Date), t.Text.Xs).TextColor(t.Muted).SingleLine()
		})
	})
	if _, ok := p.a.mail.folder(m.FolderID); ok {
		kit.Text(c, p.a.mail.folderLine(messageItem(m)), t.Text.Xs).TextColor(t.Muted).SingleLine()
	}
	marked(c, cmp.Or(m.Subject, i18n.T("mail.no_subject")), p.hl).FontWeight(600).MaxLines(2)
	if m.Snippet != "" {
		marked(c, m.Snippet, p.hl).FontSize(t.Text.Sm).TextColor(t.Muted).MaxLines(4)
	}
	for _, f := range pc.atts {
		ui.Row(c).Gap(th.Space(1.5)).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icons.Get("paperclip")).Size(theme.IconSm, theme.IconSm).TextColor(t.Muted)
			kit.Text(c, f, t.Text.Sm).SingleLine().Shrink(1)
		})
	}
}

// card is m's palCard, loading it off the UI thread the first time. The
// avatar keeps to the remote-image setting, never this message's manual
// permission, and waits while settings are unsaved, as the reader's does.
func (p *palette) card(m core.MessageSummary) *palCard {
	if pc := p.cards[m.ID]; pc != nil {
		return pc
	}
	pc := &palCard{}
	if p.cards == nil {
		p.cards = map[int64]*palCard{}
	}
	p.cards[m.ID] = pc
	a, id, atts := p.a, m.ID, m.HasAttachments
	settled := a.settingsVer == a.settingsSavedVer
	var img ui.ImageSource
	var names []string
	a.do(func(ctx context.Context) error {
		if atts {
			mt, err := a.svc.GetMessageText(ctx, id)
			if err != nil {
				return err
			}
			for _, at := range mt.Attachments {
				if !at.Inline {
					names = append(names, at.Filename)
				}
			}
		}
		if !settled {
			return nil
		}
		av, err := a.svc.SenderAvatar(ctx, id, false)
		if err != nil || len(av.Data) == 0 {
			return err
		}
		img, err = avatarImage(av)
		return err
	}, func(error) {
		// shortcut: a failed load leaves the initial and no attachments; show the error if users miss them.
		pc.avatar, pc.atts = img, names
	})
	return pc
}
