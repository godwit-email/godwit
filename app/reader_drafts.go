package app

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/richtext"
	"github.com/godwit-email/godwit/internal/theme"
)

// programReply is a reply a program drafted for the open thread (ADR 0043):
// the reply bar names it, and 查看 opens it in the compose window, where the
// user checks it all and sends it as any draft.
type programReply struct {
	id      int64
	preview string // its first words
}

// newProgramReply is the newest of a thread's program drafts (core.Thread.Drafts).
func newProgramReply(ds []core.Draft) programReply {
	if len(ds) == 0 {
		return programReply{}
	}
	d := ds[len(ds)-1]
	return programReply{d.ID, strings.Join(strings.Fields(richtext.FromComposeHTML(d.HTML).PlainText()), " ")}
}

// programReplyRow heads the reply bar while a program's reply waits.
func (r *reader) programReplyRow(c *ui.Context) {
	pr := r.programReply
	if pr.id == 0 {
		return
	}
	t, th := theme.Of(c), c.Theme()
	kit.Block(c, ui.Row(c), kit.ToneInfo).AlignItems(ui.Center).Gap(th.Space(2)).Padding(th.Space(1), th.Space(1), th.Space(1), th.Space(3)).Children(func() {
		ui.Icon(c, icons.Get("square-pen")).Size(theme.IconSm, theme.IconSm)
		kit.Text(c, i18n.Tf("reader.program_reply", pr.preview), t.Text.Sm).SingleLine().Grow(1).Shrink(1)
		kit.Button(c, i18n.T("reader.program_reply_open"), kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeSm, Tone: kit.ToneInfo}).Shrink(0).
			OnClick(func() { r.p.a.OpenDraft(pr.id) })
	})
}
