package app

import (
	"context"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// palAct is an item of a palette row's ⌥↵ menu; mods and key, when set,
// run it while the menu shows.
type palAct struct {
	title string
	run   func()
	mods  ui.Modifiers
	key   ui.Key
	icon  string // the line icon before the title, "" for none
}

// kbd is a's key as the menu shows it: ⇧R, ⌘S.
func (a palAct) kbd() string {
	if a.key < ui.KeyA || a.key > ui.KeyZ {
		return ""
	}
	s := ""
	if a.mods&ui.Shift != 0 {
		s += "⇧"
	}
	if a.mods&ui.Cmd != 0 {
		s += "⌘"
	}
	return s + string(rune('A'+a.key-ui.KeyA))
}

// menuShortcut runs the menu's item of the key pressed, if any.
func (p *palette) menuShortcut(mods ui.Modifiers, key ui.Key) bool {
	for i, act := range p.m.acts {
		if act.key != 0 && act.key == key && act.mods == mods {
			p.runAct(i)
			return true
		}
	}
	return false
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
	title := p.rows[p.sel].title
	kit.MenuAt(c, row, &p.m.open, ui.AnchorBottomRight, ui.AnchorTopRight, theme.PopupOffset, title, func() {
		ui.Column(c).Width(theme.LabelMenuWidth).Gap(th.Space(0.5)).Children(func() {
			kit.MenuLabel(c, title)
			for i, act := range p.m.acts {
				kit.MenuItem(c.Key(i), i == p.m.sel).Label(act.title).OnClick(func() { p.runAct(i) }).Children(func() {
					if act.icon != "" {
						ui.Icon(c, icons.Get(act.icon)).Size(theme.IconSm, theme.IconSm).Shrink(0)
					}
					ui.Text(c, act.title).SingleLine().Shrink(1)
					if k := act.kbd(); k != "" {
						ui.Spacer(c)
						kit.Kbd(c, k).Shrink(0)
					}
				})
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
			kit.Button(c, i18n.T("common.delete"), kit.ButtonOpts{Variant: kit.Ghost, Tone: kit.ToneDanger}).OnClick(func() {
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
		read, readIcon := i18n.T("mail.action.read"), mailActionIcons["read"]
		if !unread {
			read, readIcon = i18n.T("mail.action.unread"), mailActionIcons["unread"]
		}
		acts := []palAct{
			{title: i18n.T("reader.reply"), run: reply("reply"), key: ui.KeyR, icon: mailActionIcons["reply"]},
			{title: i18n.T("reader.reply_all"), run: reply("reply_all"), mods: ui.Shift, key: ui.KeyR, icon: mailActionIcons["reply_all"]},
			{title: i18n.T("mail.action.archive"), run: batch("archive"), key: ui.KeyE, icon: mailActionIcons["archive"]},
			{title: read, run: batch("toggle_read"), key: ui.KeyU, icon: readIcon},
		}
		if len(a.labels) > 0 {
			acts = append(acts, palAct{title: i18n.T("reader.labels.add_label"), run: func() { p.m.open, p.m.labeling, p.m.sel = true, true, 0 }, key: ui.KeyL, icon: "tag"})
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
		out = append(out, palAct{title: l.Name, run: func() {
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
			{title: i18n.T("palette.hint.open"), run: func() { p.open = false; open() }},
			{title: i18n.T("ask.delete"), run: func() { p.m.delAsk, p.m.delID = true, id }},
		}
	}
}

// contactActs are a contact's: write to them, list their mail, copy the
// address.
func (p *palette) contactActs(addr string) func() []palAct {
	return func() []palAct {
		return []palAct{
			{title: i18n.T("palette.action.compose"), run: func() { p.open = false; p.a.composeTo(addr) }},
			{title: i18n.T("palette.act.contact_mail"), run: func() { p.open = false; p.a.mail.mailFrom(addr) }},
			{title: i18n.T("palette.act.copy_address"), run: func() { p.m.clip = addr }, mods: ui.Cmd, key: ui.KeyC},
		}
	}
}

// attachmentActs are an attachment's: open it with its application, save
// it, open its mail.
func (p *palette) attachmentActs(at core.AttachmentInfo, open func()) func() []palAct {
	return func() []palAct {
		a := p.a
		return []palAct{
			{title: i18n.T("palette.act.preview"), run: func() { a.openAttachment(at) }},
			{title: i18n.T("reader.save_as"), run: func() { a.saveAttachment(at) }, mods: ui.Cmd, key: ui.KeyS},
			{title: i18n.T("palette.act.open_mail"), run: func() { p.open = false; open() }},
		}
	}
}
