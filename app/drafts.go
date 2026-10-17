package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/richtext"
	"github.com/godwit-email/godwit/internal/theme"
)

// draftsView lists the account's local drafts in the inbox's row layout. A
// click chooses one for the pane beside; a double click, Enter or 继续编辑
// opens it in a compose window; its menu deletes it.
func (p *mailPage) draftsView(c *ui.Context) {
	t := theme.Of(c)
	th := c.Theme()
	title, _ := p.Title()
	if acc, ok := draftsOf(p.folders, p.view); ok {
		if email := p.accountEmail(acc); email != "" {
			title = i18n.Tf("mail.draft.heading", email)
		}
	}
	kit.Text(c, title, t.Text.Base).FontWeight(500).TextColor(t.Muted).Padding(th.Space(3), th.Space(4), th.Space(2), th.Space(4))
	drafts := p.drafts
	p.draftList.Key = func(i int) any { return drafts[i].ID }
	kit.List(c, &p.draftList, len(drafts), func(i int) {
		d := drafts[i]
		b := kit.Pressable(c, kit.PressableOpts{Selected: d.ID == p.draftSel, OnClick: func() { p.draftSel = d.ID }}).
			Padding(th.Space(2.5), th.Space(2.5), th.Space(2.5), th.Space(5))
		b.Children(func() { p.draftRow(c, d) }).ContextMenu(func(m *ui.Menu) {
			if m.Item(i18n.T("mail.draft.delete")).Chosen() {
				p.deleteDraft(d.ID)
			}
		})
		if b.DoubleClicked() || b.Shortcut(0, ui.KeyEnter) {
			p.a.plat.OpenCompose(d.ID)
		}
	}).Grow(1).Padding(0, th.Space(2)).Children(func() {
		if len(drafts) == 0 && !p.loading {
			ui.Text(c, i18n.T("mail.empty")).TextColor(t.Muted).Padding(th.Space(3))
		}
	})
}

// draftInfo is what a draft's row and pane show, worked out once per load.
type draftInfo struct {
	preview string
	doc     richtext.Doc
	editing bool // a compose window has it
}

// setDrafts parses the loaded drafts, asks the platform which are being
// edited, and drops a choice whose draft is gone.
func (p *mailPage) setDrafts() {
	p.draftView = make(map[int64]draftInfo, len(p.drafts))
	found := false
	for _, d := range p.drafts {
		doc := richtext.FromComposeHTML(d.HTML)
		p.draftView[d.ID] = draftInfo{strings.Join(strings.Fields(doc.PlainText()), " "), doc, p.a.plat.ComposeOpen(d.ID)}
		found = found || d.ID == p.draftSel
	}
	if !found {
		p.draftSel = 0
	}
}

// accountByID is the loaded account with id.
func (a *App) accountByID(id int64) (core.Account, bool) {
	for _, acc := range a.accounts {
		if acc.ID == id {
			return acc, true
		}
	}
	return core.Account{}, false
}

func (p *mailPage) accountEmail(id int64) string {
	acc, _ := p.a.accountByID(id)
	return acc.Email
}

// draftRow is the inbox row's lines: who it goes to (names only) and when,
// the subject, the start of the text, and 编辑中 while a compose window has it.
func (p *mailPage) draftRow(c *ui.Context, d core.Draft) {
	t, th := theme.Of(c), c.Theme()
	ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(0.5)).Children(func() {
		ui.Row(c).Gap(th.Space(1.5)).Children(func() {
			ui.Text(c, cmp.Or(recipients(d.To), i18n.T("mail.no_recipient"))).SingleLine().Grow(1).Shrink(1)
			kit.Text(c, listTime(p.a.clock.Now(), d.UpdatedAt), t.Text.Sm).TextColor(t.Muted)
		})
		ui.Text(c, cmp.Or(d.Subject, i18n.T("mail.no_subject"))).SingleLine()
		if text := p.draftView[d.ID].preview; text != "" {
			kit.Text(c, text, t.Text.Base).TextColor(t.Muted).SingleLine()
		}
		if p.draftView[d.ID].editing {
			ui.Box(c).AlignSelf(ui.Start).Margin(th.Space(0.5), 0, 0, 0).Padding(0, th.Space(1.5)).Radius(t.Radius.Sm).Border(theme.DeviceHairline, t.Border).Children(func() {
				kit.Text(c, i18n.T("mail.draft.chip"), t.Text.Sm)
			})
		}
	})
}

// recipients are the display names of addresses, joined.
func recipients(to []string) string {
	names := make([]string, len(to))
	for i, a := range to {
		names[i] = displayName(a)
	}
	return strings.Join(names, i18n.T("mail.list_sep"))
}

// draftPane is the reading pane of the drafts folder: the chosen draft as
// last saved (a compose window editing it saves behind this, unseen until
// it closes), or the watermark. A draft gone from the list is not chosen.
func (p *mailPage) draftPane(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	col := ui.Column(c).Grow(1).Shrink(1)
	i := slices.IndexFunc(p.drafts, func(d core.Draft) bool { return d.ID == p.draftSel })
	if i < 0 {
		col.Center().Children(func() {
			ui.Icon(c, icons.Bird).Size(theme.Watermark, theme.Watermark).TextColor(t.Border)
		})
		return
	}
	d := p.drafts[i]
	info := p.draftView[d.ID]
	editing := info.editing
	sendTip := i18n.T("compose.send")
	if editing {
		sendTip = i18n.T("mail.draft.send_disabled")
	}
	saved := d.UpdatedAt.In(p.a.clock.Now().Location()).Format("15:04")
	col.Children(func() {
		p.sendDialog(c)
		ui.Row(c).AlignItems(ui.Center).Gap(th.Space(2)).Padding(th.Space(2), th.Space(4)).BorderWidth(0, 0, theme.DeviceHairline, 0).BorderColor(t.Border).Children(func() {
			kit.ButtonGroup(c,
				kit.GroupItem{Icon: "square-pen", Tip: i18n.T("mail.draft.edit"), OnClick: func() { p.a.plat.OpenCompose(d.ID) }},
				kit.GroupItem{Icon: "send", Tip: sendTip, Disabled: editing || p.sending.busy, OnClick: func() { p.sendDraft(d) }},
				kit.GroupItem{Icon: "trash-2", Tip: i18n.T("mail.draft.delete"), OnClick: func() { p.deleteDraft(d.ID) }})
			ui.Spacer(c)
			status := i18n.Tf("mail.draft.saved", saved)
			if editing {
				status = i18n.Tf("mail.draft.saved_editing", saved)
			}
			kit.Text(c, status, t.Text.Sm).TextColor(t.Muted)
		})
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).FillWidth().Padding(th.Space(5), th.Space(7)).Children(func() {
				ui.Column(c).FillWidth().MaxWidth(theme.ReaderMaxWidth).Gap(th.Space(4)).Children(func() {
					if editing {
						kit.Text(c, i18n.T("mail.draft.notice"), t.Text.Sm).Padding(th.Space(2), th.Space(3)).Radius(t.Radius.Lg).Background(t.Hover)
					}
					kit.Text(c, cmp.Or(d.Subject, i18n.T("mail.no_subject")), t.Text.X2l).FontWeight(600).LineHeight(theme.LineSubject)
					ui.Column(c).Gap(th.Space(1)).Children(func() {
						for _, f := range [][2]string{
							{i18n.T("compose.to"), cmp.Or(strings.Join(d.To, ", "), i18n.T("mail.no_recipient"))},
							{i18n.T("compose.subject"), cmp.Or(d.Subject, i18n.T("mail.no_subject"))},
							{i18n.T("compose.from"), p.accountEmail(d.AccountID)},
						} {
							ui.Row(c).Gap(th.Space(3)).Children(func() {
								ui.Text(c, f[0]).TextColor(t.Muted).LineHeight(theme.LineCJK).MinWidth(th.Space(12)).Shrink(0)
								ui.Text(c, f[1]).LineHeight(theme.LineCJK).Grow(1).Shrink(1)
							})
						}
					})
					ui.Column(c).Padding(th.Space(4), 0, 0, 0).BorderWidth(theme.DeviceHairline, 0, 0, 0).BorderColor(t.Border).Children(func() {
						richtext.Render(c, info.doc, richtext.RenderOptions{OnLink: p.a.openLink})
					})
					p.draftAttachments(c, d.Attachments)
				})
			})
		})
	})
}

func (p *mailPage) draftAttachments(c *ui.Context, atts []core.DraftAttachment) {
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).Wrap().Gap(th.Space(2)).Children(func() {
		for _, att := range atts {
			ui.Row(c).Width(theme.DraftAttachmentWidth).Gap(th.Space(3)).Padding(th.Space(2), th.Space(3)).Border(theme.DeviceHairline, t.Border).Radius(t.Radius.Lg).Children(func() {
				ui.Box(c).Size(th.Space(7), th.Space(8.5)).Radius(t.Radius.Sm).Background(t.Hover).Center().Shrink(0).Children(func() {
					kit.Text(c, fileKind(core.AttachmentInfo{Filename: att.Filename, ContentType: att.ContentType}), t.Text.Xs).TextColor(t.Muted)
				})
				ui.Column(c).Shrink(1).Children(func() {
					ui.Text(c, att.Filename).SingleLine()
					kit.Text(c, byteSize(att.Size), t.Text.Sm).TextColor(t.Muted)
				})
			})
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

// draftSend is the pane's send: busy while a call is out, and the warnings a
// plugin raised for revision rev of draft id, shown until sent or cancelled.
type draftSend struct {
	busy     bool
	ask      bool
	id, rev  int64
	warnings core.SendCheck
}

// sendDraft queues the draft as last saved. A compose window opened since
// the pane was drawn owns the draft, so it is refused here, not at render.
func (p *mailPage) sendDraft(d core.Draft) {
	p.doSend(d.ID, d.Revision, func(ctx context.Context) error { return p.a.svc.SendRevision(ctx, d.ID, d.Revision) })
}

func (p *mailPage) doSend(id, rev int64, call func(context.Context) error) {
	a := p.a
	if p.sending.busy {
		return
	}
	if a.plat.ComposeOpen(id) {
		a.toast(i18n.T("mail.draft.send_disabled"))
		p.reload() // refresh the chip and the disabled button
		return
	}
	p.sending.busy = true
	a.writes.Chain(fmt.Sprintf("draft:%d", id), call, func(err error) {
		p.sending.busy = false
		var warn *core.SendWarningsError
		switch {
		case err == nil:
			a.plat.DraftDeleted(id)
			p.draftSel = 0
		case errors.As(err, &warn):
			p.sending.ask, p.sending.id, p.sending.rev, p.sending.warnings = true, id, rev, warn.Check
			return
		default:
			a.toastErr(err) // blocked, stale and the rest read from humanize
		}
		p.reload()
	})
}

// sendDialog lists the plugins' warnings; 仍然发送 sends with their digest.
func (p *mailPage) sendDialog(c *ui.Context) {
	if !p.sending.ask {
		return
	}
	t, th := theme.Of(c), c.Theme()
	s := p.sending
	kit.Dialog(c, &p.sending.ask, i18n.T("compose.send"), kit.DialogOpts{Close: i18n.T("common.close")}, func() {
		ui.Column(c).Gap(th.Space(3)).Children(func() {
			for _, w := range s.warnings.Warnings {
				ui.Column(c).Children(func() {
					ui.Text(c, i18n.Tf("compose.warn", w.Message))
					kit.Text(c, i18n.Tf("compose.warn.from", w.Plugin), t.Text.Sm).TextColor(t.Muted)
				})
			}
		})
	}, func() {
		if kit.Button(c, i18n.T("common.cancel"), kit.ButtonOpts{Variant: kit.Outline}).Clicked() {
			p.sending.ask = false
		}
		if kit.Button(c, i18n.T("compose.send_anyway"), kit.ButtonOpts{}).Clicked() {
			p.sending.ask = false
			p.doSend(s.id, s.rev, func(ctx context.Context) error {
				return p.a.svc.SendAcknowledgedRevision(ctx, s.id, s.rev, s.warnings.Digest)
			})
		}
	})
}
