package app

import (
	"context"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// palAct is an item of a palette row's ⌥↵ menu.
type palAct struct {
	title string
	run   func()
}

// paletteMenu is the ⌥↵ menu of the selected row, or the labels its
// 添加标签 lists; and the conversation its 删除 asks to confirm.
type paletteMenu struct {
	open     bool
	sel      int
	labeling bool     // the menu lists the labels to put on the mail
	acts     []palAct // as last built, which the keys act on
	delAsk   bool
	delID    int64
	clip     string // an address to copy in the next frame
}

// openMenu shows the selected row's menu, when it has one.
func (p *palette) openMenu() {
	if p.sel < len(p.rows) && p.rows[p.sel].acts != nil {
		p.m.open, p.m.sel, p.m.labeling = true, 0, false
	}
}

// menuKey is the search box's key while the menu shows: ↑↓ move in it and
// wrap, ↵ runs the item, ⌥↵ closes it. Escape goes on to close it.
func (p *palette) menuKey(k ui.TextKey) bool {
	n := len(p.m.acts)
	switch {
	case k.Mods == 0 && (k.Key == ui.KeyDown || k.Key == ui.KeyUp):
		if n > 0 {
			p.m.sel = wrapStep(p.m.sel, n, k.Key == ui.KeyUp)
		}
	case k.Mods == 0 && k.Key == ui.KeyEnter:
		p.runAct(p.m.sel)
	case k.Mods == ui.Alt && k.Key == ui.KeyEnter:
		p.m.open = false
	default:
		return false
	}
	return true
}

// runAct closes the menu and runs its item i.
func (p *palette) runAct(i int) {
	if i < 0 || i >= len(p.m.acts) {
		return
	}
	run := p.m.acts[i].run
	p.m.open, p.m.labeling = false, false
	run()
}

// menuView builds the menu's items below the selected row.
func (p *palette) menuView(c *ui.Context, row ui.Element) {
	th := c.Theme()
	kit.PopoverAt(c, row, &p.m.open, ui.AnchorBottomRight, ui.AnchorTopRight, theme.PopupOffset, func() {
		ui.Column(c).Role(ui.RoleMenu).Width(theme.LabelMenuWidth).Gap(th.Space(0.5)).Children(func() {
			for i, act := range p.m.acts {
				kit.PaletteRow(c.Key(i), i == p.m.sel).Label(act.title).OnClick(func() { p.runAct(i) }).
					Children(func() { ui.Text(c, act.title).SingleLine() })
			}
		})
	})
}

// menuFrame does what the menu left to the frame: the address to copy,
// the delete to confirm.
func (p *palette) menuFrame(c *ui.Context) {
	if p.m.clip != "" {
		c.WriteClipboard(p.m.clip)
		p.m.clip = ""
	}
	if !p.m.delAsk {
		return
	}
	kit.Dialog(c, &p.m.delAsk, i18n.T("palette.delete.title"),
		kit.DialogOpts{Size: kit.DialogSm, Alert: true, Desc: i18n.T("palette.delete.desc"), Close: i18n.T("common.close")}, nil, func() {
			kit.Button(c, i18n.T("common.cancel"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(func() { p.m.delAsk = false })
			kit.Button(c, i18n.T("common.delete"), kit.ButtonOpts{Variant: kit.Destructive}).OnClick(func() {
				p.m.delAsk = false
				p.a.deleteConversation(p.m.delID)
				p.refreshAfterWrites()
			})
		})
}

// menuActs are the items the menu shows now.
func (p *palette) menuActs() []palAct {
	if !p.m.open || p.sel >= len(p.rows) || p.rows[p.sel].acts == nil {
		p.m.open = false
		return nil
	}
	if p.m.labeling {
		return p.labelActs(*p.rows[p.sel].msg)
	}
	return p.rows[p.sel].acts()
}

// refreshAfterWrites searches again, keeping the selection, once the
// writes asked for are done.
func (p *palette) refreshAfterWrites() {
	p.a.writes.Idle(func() {
		if !p.open {
			return
		}
		sel := p.sel
		p.find()
		p.sel = sel
	})
}

// mailActs are a mail's: reply, reply all, archive, mark read or unread,
// put a label on.
func (p *palette) mailActs(m core.MessageSummary, unread bool) func() []palAct {
	return func() []palAct {
		a := p.a
		batch := func(act string) func() {
			return func() {
				a.mail.batchScoped(act, []mailItem{messageItem(m)}, true) // a hit is in no view: archive it wherever it is
				p.refreshAfterWrites()
			}
		}
		reply := func(mode string) func() {
			return func() {
				p.open = false
				a.draft(m, 0, mode)
			}
		}
		read := i18n.T("mail.action.read")
		if !unread {
			read = i18n.T("mail.action.unread")
		}
		acts := []palAct{
			{i18n.T("reader.reply"), reply("reply")},
			{i18n.T("reader.reply_all"), reply("reply_all")},
			{i18n.T("mail.action.archive"), batch("archive")},
			{read, batch("toggle_read")},
		}
		if len(a.labels) > 0 {
			acts = append(acts, palAct{i18n.T("reader.labels.add_label"), func() { p.m.open, p.m.labeling, p.m.sel = true, true, 0 }})
		}
		return acts
	}
}

// labelActs put a label on m, one each.
// shortcut: only puts labels on, unchecked; load MessageLabels to show and take off the ones on.
func (p *palette) labelActs(m core.MessageSummary) []palAct {
	a := p.a
	out := make([]palAct, 0, len(a.labels))
	for _, l := range a.labels {
		id := l.ID
		out = append(out, palAct{l.Name, func() {
			a.writes.Chain("labels", func(ctx context.Context) error { return a.svc.SetLabel(ctx, []int64{m.ID}, id, true) }, a.toastErr)
			p.refreshAfterWrites()
		}})
	}
	return out
}

// convActs are a conversation's: open it, delete it once confirmed.
func (p *palette) convActs(id int64, open func()) func() []palAct {
	return func() []palAct {
		return []palAct{
			{i18n.T("palette.hint.open"), func() { p.open = false; open() }},
			{i18n.T("ask.delete"), func() { p.m.delAsk, p.m.delID = true, id }},
		}
	}
}

// contactActs are a contact's: write to them, list their mail, copy the
// address.
func (p *palette) contactActs(addr string, open func()) func() []palAct {
	return func() []palAct {
		return []palAct{
			{i18n.T("palette.action.compose"), func() { p.open = false; p.a.mailto("mailto:"+addr, 0) }},
			{i18n.T("palette.act.contact_mail"), func() { p.open = false; open() }},
			{i18n.T("palette.act.copy_address"), func() { p.m.clip = addr }},
		}
	}
}

// attachmentActs are an attachment's: open it with its application, save
// it, open its mail.
func (p *palette) attachmentActs(at core.AttachmentInfo, open func()) func() []palAct {
	return func() []palAct {
		a := p.a
		return []palAct{
			{i18n.T("palette.act.preview"), func() { a.openAttachment(at) }},
			{i18n.T("reader.save_as"), func() { a.saveAttachment(at) }},
			{i18n.T("palette.act.open_mail"), func() { p.open = false; open() }},
		}
	}
}
