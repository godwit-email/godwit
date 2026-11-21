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
	kit.Text(c, title, t.Text.Xs).FontWeight(500).TextColor(t.Muted).Padding(th.Space(3), th.Space(4), th.Space(2), th.Space(4))
	drafts := p.drafts
	p.draftList.Key = func(i int) any { return drafts[i].ID }
	kit.List(c, p.draftList, len(drafts), func(i int) {
		d := drafts[i]
		b := kit.Pressable(c, kit.PressableOpts{Selected: d.ID == p.draftSel, OnClick: func() { p.draftSel = d.ID }}).
			Padding(th.Space(2.5), th.Space(2))
		b.Children(func() { p.draftRow(c, d) }).ContextMenu(func(m *ui.Menu) {
			if m.Item(i18n.T("mail.draft.delete")).Chosen() {
				p.deleteDraft(d.ID)
			}
		})
		if b.DoubleClicked() || b.Shortcut(0, ui.KeyEnter) {
			p.a.OpenDraft(d.ID)
		}
	}).Grow(1).Padding(0, th.Space(2)).Children(func() {
		if len(drafts) == 0 && !p.list.loading {
			listEmpty(c, kit.EmptyOpts{Icon: "file-pen", Title: i18n.T("mail.empty")})
		}
	})
}

// draftInfo is what a draft's row and pane show, worked out once per load.
type draftInfo struct {
	preview string
	doc     richtext.Doc
	editing bool // a compose window has it
	cut     bool // doc is not all of the draft: too long to show, so not to approve here
}

// setDrafts parses the loaded drafts, asks the platform which are being
// edited, and drops a choice whose draft is gone.
func (p *mailPage) setDrafts() {
	p.draftView = make(map[int64]draftInfo, len(p.drafts))
	found := false
	for _, d := range p.drafts {
		doc, cut := richtext.FromComposeHTMLCut(d.HTML)
		p.draftView[d.ID] = draftInfo{strings.Join(strings.Fields(doc.PlainText()), " "), doc, p.a.plat.ComposeOpen(d.ID), cut}
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

// draftRow is the inbox row's lines, beside the avatar of who it goes to
// (names only; "?" for none): who and when, the subject, the start of the
// text, and 编辑中 while a compose window has it.
func (p *mailPage) draftRow(c *ui.Context, d core.Draft) {
	t, th := theme.Of(c), c.Theme()
	to := recipients(d.To)
	var img ui.ImageSource
	if len(d.To) > 0 {
		img = p.addrAvatar(d.To[0], 0)
	}
	withAvatar(c, to, img, func() {
		ui.Row(c).Gap(th.Space(1.5)).Children(func() {
			ui.Text(c, cmp.Or(to, i18n.T("mail.no_recipient"))).FontWeight(500).SingleLine().Grow(1).Shrink(1) // the lead line, as a list row's
			kit.Text(c, listTime(p.a.clock.Now(), d.UpdatedAt), t.Text.Xs).TextColor(t.Muted)
		})
		ui.Text(c, cmp.Or(d.Subject, i18n.T("mail.no_subject"))).SingleLine()
		if text := p.draftView[d.ID].preview; text != "" {
			kit.Text(c, text, t.Text.Xs).TextColor(t.Muted).SingleLine()
		}
		if editing := p.draftView[d.ID].editing; editing || d.FromProgram {
			ui.Row(c).Gap(th.Space(1)).Margin(th.Space(0.5), 0, 0, 0).Children(func() {
				if editing {
					draftChip(c, i18n.T("mail.draft.chip"))
				}
				if d.FromProgram {
					draftChip(c, i18n.T("mail.draft.from_program"))
				}
			})
		}
	})
}

// draftChip is a small bordered label on a draft: 编辑中, 来自程序的草稿.
func draftChip(c *ui.Context, text string) {
	kit.BadgeWith(c, text, kit.BadgeOpts{Tone: kit.BadgeOutline}).AlignSelf(ui.Start)
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
	col.Children(func() {
		p.sendDialog(c)
		p.draftBar(c, d, info)
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).FillWidth().Padding(th.Space(5), th.Space(7)).Children(func() {
				ui.Column(c).FillWidth().MaxWidth(p.a.readerMaxWidth()).AlignSelf(ui.Center).Gap(th.Space(4)).Children(func() { // the reader's column
					kit.Text(c, cmp.Or(d.Subject, i18n.T("mail.no_subject")), t.Text.Xl).FontWeight(600)
					p.draftFields(c, d)
					ui.Column(c).Padding(th.Space(4), 0, 0, 0).BorderWidth(theme.Hairline, 0, 0, 0).BorderColor(t.Border).Children(func() {
						richtext.Render(c, info.doc, richtext.RenderOptions{OnLink: p.a.openLink})
					})
					draftAttachments(c, d.Attachments)
				})
			})
		})
	})
}

// draftBar heads the pane as the design's notice bar: the sidebar's fill over
// a hairline. A program's draft (ADR 0043) says so beside an icon, with what
// to check; every draft says when it was saved, and that a compose window
// has it, or that it is too long to show whole, so it is checked and sent
// from the compose window instead. 继续编辑 and the send are at its end, the
// send named by why it cannot be pressed when it cannot.
func (p *mailPage) draftBar(c *ui.Context, d core.Draft, info draftInfo) {
	t, th := theme.Of(c), c.Theme()
	editing, cut := info.editing, d.FromProgram && info.cut // an approval would send text the pane does not show
	send := i18n.T("compose.send")
	if d.FromProgram {
		send = i18n.T("mail.draft.approve")
	}
	sendTip := send
	if cut {
		sendTip = i18n.T("mail.draft.too_long")
	}
	if editing {
		sendTip = i18n.T("mail.draft.send_disabled")
	}
	saved := d.UpdatedAt.In(p.a.clock.Now().Location()).Format("15:04")
	status := i18n.Tf("mail.draft.saved", saved)
	if editing {
		status = i18n.Tf("mail.draft.saved_editing", saved)
	}
	ui.Row(c).AlignItems(ui.Center).Gap(th.Space(3)).Padding(th.Space(2.5), th.Space(4)).Background(t.Sidebar).
		BorderWidth(0, 0, theme.Hairline, 0).BorderColor(t.Border).Children(func() {
		if d.FromProgram {
			ui.Icon(c, icons.Get("info")).Size(theme.Icon, theme.Icon).Shrink(0).TextColor(t.Muted)
		}
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(th.Space(0.5)).Children(func() {
			if d.FromProgram {
				ui.Text(c, i18n.T("mail.draft.from_program")).FontWeight(500)
				status = i18n.T("mail.draft.from_program_help") + " · " + status
			}
			kit.Text(c, status, t.Text.Xs).TextColor(t.Muted)
			if editing {
				kit.Text(c, i18n.T("mail.draft.notice"), t.Text.Xs).TextColor(t.Muted)
			}
			if cut {
				kit.Text(c, i18n.T("mail.draft.too_long"), t.Text.Xs).FontWeight(500)
			}
		})
		kit.Button(c, i18n.T("mail.draft.edit"), kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeSm, Icon: "square-pen"}).Shrink(0).OnClick(func() { p.a.OpenDraft(d.ID) })
		sendBtn := kit.Button(c, send, kit.ButtonOpts{Size: kit.SizeSm, Icon: "send", Disabled: editing || cut || p.sending.busy}).Label(sendTip).Shrink(0).OnClick(func() { p.sendDraft(d) })
		kit.TooltipLabelled(c, sendBtn, sendTip)
		kit.IconButton(c, "trash-2", i18n.T("mail.draft.delete"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).Shrink(0).OnClick(func() { p.deleteDraft(d.ID) })
	})
}

// draftFields are the draft's headers: every address it goes to (Cc and Bcc
// when it has them: approving sends to all of them), its subject and sender.
func (p *mailPage) draftFields(c *ui.Context, d core.Draft) {
	t, th := theme.Of(c), c.Theme()
	fields := [][2]string{{i18n.T("compose.to"), cmp.Or(strings.Join(d.To, ", "), i18n.T("mail.no_recipient"))}}
	if len(d.Cc) > 0 {
		fields = append(fields, [2]string{i18n.T("compose.cc"), strings.Join(d.Cc, ", ")})
	}
	if len(d.Bcc) > 0 {
		fields = append(fields, [2]string{i18n.T("compose.bcc"), strings.Join(d.Bcc, ", ")})
	}
	fields = append(fields, [2]string{i18n.T("compose.subject"), cmp.Or(d.Subject, i18n.T("mail.no_subject"))},
		[2]string{i18n.T("compose.from"), p.accountEmail(d.AccountID)})
	ui.Column(c).Gap(th.Space(2)).Children(func() {
		for _, f := range fields {
			ui.Row(c).Gap(th.Space(3)).Children(func() {
				ui.Text(c, f[0]).TextColor(t.Muted).MinWidth(th.Space(14)).Shrink(0)
				ui.Text(c, f[1]).Grow(1).Shrink(1)
			})
		}
	})
}

func draftAttachments(c *ui.Context, atts []core.DraftAttachment) {
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).Wrap().Gap(th.Space(2)).Children(func() {
		for _, att := range atts {
			kit.Block(c, ui.Row(c), kit.ToneOutline).Width(theme.DraftAttachmentWidth).Gap(th.Space(3)).Padding(th.Space(2), th.Space(3)).Children(func() {
				kit.Tile(c, th.Space(7), th.Space(8.5), kit.ToneTint).Children(func() {
					kit.Text(c, fileKind(core.AttachmentInfo{Filename: att.Filename, ContentType: att.ContentType}), t.Text.Xs).TextColor(t.Muted)
				})
				ui.Column(c).Shrink(1).Children(func() {
					ui.Text(c, att.Filename).SingleLine()
					kit.Text(c, byteSize(att.Size), t.Text.Xs).TextColor(t.Muted)
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

// sendDraft queues the draft as last saved, a program's through ApproveDraft
// (批准并发送): either way only the revision shown. A compose window opened
// since the pane was drawn owns the draft, so it is refused here, not at render.
func (p *mailPage) sendDraft(d core.Draft) {
	p.doSend(d.ID, d.Revision, func(ctx context.Context) error {
		if d.FromProgram {
			return p.a.svc.ApproveDraft(ctx, d.ID, d.Revision)
		}
		return p.a.svc.SendRevision(ctx, d.ID, d.Revision)
	})
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
	s := p.sending
	warningsDialog(c, &p.sending.ask, s.warnings, func() {
		p.doSend(s.id, s.rev, func(ctx context.Context) error {
			return p.a.svc.SendAcknowledgedRevision(ctx, s.id, s.rev, s.warnings.Digest)
		})
	})
}

// warningsDialog lists check's warnings while *ask; 仍然发送 closes it and
// calls anyway, so it sends once.
func warningsDialog(c *ui.Context, ask *bool, check core.SendCheck, anyway func()) {
	t, th := theme.Of(c), c.Theme()
	kit.Dialog(c, ask, i18n.T("compose.send"), kit.DialogOpts{Close: i18n.T("common.close")}, func() {
		ui.Column(c).Gap(th.Space(3)).Children(func() {
			for _, w := range check.Warnings {
				ui.Column(c).Children(func() {
					ui.Text(c, i18n.Tf("compose.warn", w.Message))
					kit.Text(c, i18n.Tf("compose.warn.from", w.Plugin), t.Text.Xs).TextColor(t.Muted)
				})
			}
		})
	}, func() {
		if kit.Button(c, i18n.T("common.cancel"), kit.ButtonOpts{Variant: kit.Outline}).Clicked() {
			*ask = false
		}
		if kit.Button(c, i18n.T("compose.send_anyway"), kit.ButtonOpts{}).Clicked() {
			*ask = false
			anyway()
		}
	})
}
