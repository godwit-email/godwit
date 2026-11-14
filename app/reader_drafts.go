package app

import (
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/richtext"
	"github.com/godwit-email/godwit/internal/theme"
)

// programDrafts shows, under the thread's messages, the drafts a program
// wrote in answer to it (core.Thread.Drafts, ADR 0042), each as last saved
// with 批准并发送 for exactly that revision and 继续编辑.
func (r *reader) programDrafts(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	p := r.p
	for i, d := range r.thread.Drafts {
		r.column(c).Key(threadDraft{d.ID}).Gap(th.Space(3)).Padding(th.Space(4)).Border(theme.Hairline, t.Border).Radius(t.Radius.Lg).Children(func() {
			programNotice(c)
			p.draftFields(c, d)
			if i < len(r.draftDocs) {
				ui.Column(c).Padding(th.Space(3), 0, 0, 0).BorderWidth(theme.Hairline, 0, 0, 0).BorderColor(t.Border).Children(func() {
					richtext.Render(c, r.draftDocs[i], richtext.RenderOptions{OnLink: p.a.openLink})
				})
			}
			draftAttachments(c, d.Attachments)
			ui.Row(c).Gap(th.Space(2)).Children(func() {
				if kit.Button(c, i18n.T("mail.draft.approve"), kit.ButtonOpts{Icon: "send", Disabled: p.sending.busy}).Clicked() {
					p.sendDraft(d)
				}
				if kit.Button(c, i18n.T("mail.draft.edit"), kit.ButtonOpts{Variant: kit.Outline, Icon: "square-pen"}).Clicked() {
					p.a.OpenDraft(d.ID)
				}
			})
		})
	}
}

// threadDraft keys a program's draft under the thread's messages.
type threadDraft struct{ id int64 }
