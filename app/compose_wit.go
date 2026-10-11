package app

import (
	"cmp"
	"math"
	netmail "net/mail"
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/internal/editor"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/richtext"
	"github.com/godwit-email/godwit/internal/theme"
	"github.com/godwit-email/godwit/internal/worddiff"
)

// adjustSkills are the 调整 menu's skills, in its order.
var adjustSkills = []string{"tone-formal", "tone-friendly", "shorter", "longer", "proofread"}

// languages are the 翻译 menu's targets: the reading language, then the
// common ones.
func (w *composeWit) languages() []struct{ tag, name string } {
	st := w.p.a.settings
	read := cmp.Or(st.ReadingLanguage, st.UILanguage, w.p.a.sysLang, "en")
	out := []struct{ tag, name string }{{read, langName(read)}}
	for _, l := range languages {
		if !slices.ContainsFunc(out, func(x struct{ tag, name string }) bool { return x.name == l.name }) {
			out = append(out, l)
		}
	}
	return out
}

// view is the panel beside the compose column: what Wit knows of the
// draft, then the conversation.
func (w *composeWit) view(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	ui.Column(c).Width(theme.WitPanelWidth).Shrink(0).BorderWidth(0, 0, 0, theme.Hairline).BorderColor(t.Border).Disabled(w.p.frozen.Load()).Children(func() {
		if chips := w.chips(); len(chips) > 0 {
			ui.Row(c).Wrap().Gap(th.Space(1.5)).Padding(th.Space(3), th.Space(4)).BorderWidth(0, 0, theme.Hairline, 0).BorderColor(t.Border).Children(func() {
				for _, s := range chips {
					ui.Text(c, s).FontSize(t.Text.Xs).TextColor(t.Muted).Padding(0, th.Space(2)).Radius(t.Radius.Full).Border(theme.Hairline, t.Border)
				}
			})
		}
		w.panel.View(c, w.options(nil))
	})
}

// chips name the context of an Assist run: the email replied to, the
// attachments, and how much mail with the recipients its last run read.
func (w *composeWit) chips() []string {
	p := w.p
	var out []string
	if p.src != 0 {
		out = append(out, i18n.T("wit.compose.source"))
	}
	for _, att := range p.atts {
		out = append(out, att.Filename)
	}
	var seen []int64
	for _, ct := range w.panel.cites {
		if ct.CallID == "" && ct.MessageID != p.src && !slices.Contains(seen, ct.MessageID) {
			seen = append(seen, ct.MessageID)
		}
	}
	if len(seen) > 0 && len(p.to) > 0 {
		who := p.to[0]
		if a, err := netmail.ParseAddress(who); err == nil {
			who = cmp.Or(a.Name, a.Address)
		}
		out = append(out, i18n.Tf("wit.compose.history", who, len(seen)))
	}
	return out
}

// tools are the 调整 and 翻译 menus above the input.
func (w *composeWit) tools(c *ui.Context) {
	th := c.Theme()
	off := w.locked()
	pill := kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeSm, Disabled: off}
	ui.Row(c).Gap(th.Space(1.5)).Children(func() {
		kit.Button(c, i18n.T("wit.compose.adjust"), pill).Menu(func(m *ui.Menu) {
			for _, sk := range adjustSkills {
				if label := i18n.T("wit.skill." + sk); m.Item(label).Chosen() && !off {
					w.runSkill(label, sk, "")
				}
			}
		})
		kit.Button(c, i18n.T("wit.compose.translate"), pill).Menu(func(m *ui.Menu) {
			for _, l := range w.languages() {
				if m.Item(l.name).Chosen() && !off {
					w.runSkill(i18n.Tf("wit.compose.translate_to", l.name), "translate-draft", l.tag)
				}
			}
		})
	})
}

// after notes, once the latest edit is in the body, that it is, with Undo.
func (w *composeWit) after(c *ui.Context) {
	if !w.applied || w.locked() {
		return
	}
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).Gap(th.Space(2)).FontSize(t.Text.Xs).TextColor(t.Muted).Children(func() {
		ui.Text(c, i18n.T("wit.compose.applied")).Padding(0, th.Space(1.5)).Radius(t.Radius.Sm).Background(t.Hover)
		kit.Button(c, i18n.T("wit.compose.undo"), kit.ButtonOpts{Variant: kit.Link, Size: kit.SizeSm}).OnClick(w.undo)
	})
}

// card is a draft edit's approval in the panel: what it changes, and
// Accept all / Reject all.
func (w *composeWit) card(c *ui.Context, ap core.Approval) bool {
	if !isDraftEdit(ap.Tool) {
		return false
	}
	t, th := theme.Of(c), c.Theme()
	r := w.review
	ui.Column(c).Radius(t.Radius.Xl).Border(theme.Hairline, t.Primary).Background(t.Background).Clip().Children(func() {
		ui.Row(c).Gap(th.Space(2)).Padding(th.Space(2), th.Space(3)).Background(t.Hover).Children(func() {
			ui.Text(c, i18n.T("wit.compose.review")).FontWeight(600)
			ui.Text(c, ap.Tool).Font("Geist Mono").FontSize(t.Text.Xs)
		})
		ui.Box(c).Padding(th.Space(2), th.Space(3)).Children(func() {
			switch {
			case r == nil || r.ap.ID != ap.ID:
				kit.Spinner(c)
			case r.inline():
				ui.Text(c, i18n.Plural("wit.compose.review.inline", len(r.changes)))
			default:
				ui.Text(c, w.rewriteText(true))
			}
		})
		if r != nil && r.ap.ID == ap.ID {
			ui.Row(c).Gap(th.Space(1.5)).Padding(th.Space(2), th.Space(3)).BorderWidth(theme.Hairline, 0, 0, 0).BorderColor(t.Border).Children(func() {
				w.decideButtons(c)
			})
		}
	})
	return true
}

func (w *composeWit) decideButtons(c *ui.Context) {
	busy := w.panel.deciding[w.review.ap.ID]
	kit.Button(c, i18n.T("wit.compose.reject_all"), kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeSm, Disabled: busy}).OnClick(func() { w.decideAll(false) })
	kit.Button(c, i18n.T("wit.compose.accept_all"), kit.ButtonOpts{Size: kit.SizeSm, Disabled: busy}).OnClick(func() { w.decideAll(true) })
}

// rewriteText tells how much a rewrite changed, as the banner or (card)
// the review in the panel says it.
func (w *composeWit) rewriteText(card bool) string {
	key := "wit.compose.rewrite"
	if card {
		key = "wit.compose.review.rewrite"
	}
	if !w.review.ok {
		return i18n.T(key + "_plain")
	}
	return i18n.Tf(key, int(math.Round(w.review.ratio*100)))
}

// banner is the bar above the body while Wit writes it or an edit waits.
func (w *composeWit) banner(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	r := w.review
	if err := w.reloadErr; err != nil {
		ui.Box(c).Padding(th.Space(2), th.Space(5)).Children(func() {
			kit.Alert(c, kit.Danger, cmp.Or(humanize(err), err.Error()), func() {
				kit.Button(c, i18n.T("common.retry"), kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeSm}).OnClick(w.reload)
			})
		})
	}
	var text string
	switch {
	case w.panel.running:
		text = i18n.T("wit.compose.writing_banner")
	case r != nil && r.inline():
		text = i18n.Plural("wit.compose.pending", r.open())
	case r != nil:
		text = w.rewriteText(false)
	default:
		return
	}
	ui.Row(c).Gap(th.Space(2)).Padding(th.Space(2), th.Space(5)).Background(t.Hover).BorderWidth(0, 0, theme.Hairline, 0).BorderColor(t.Border).Role(ui.RoleStatus).Children(func() {
		ui.Text(c, text).Grow(1).Shrink(1)
		small := kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeSm}
		switch {
		case w.panel.running:
			kit.Button(c, i18n.T("wit.stop"), small).OnClick(w.panel.stopRun)
		default:
			if !r.inline() {
				label := i18n.T("wit.compose.compare")
				if w.compare {
					label = i18n.T("wit.compose.back")
				}
				kit.Button(c, label, small).OnClick(func() { w.compare = !w.compare })
			}
			w.decideButtons(c)
		}
	})
}

// body draws what takes the editor's place: the review of an edit, or
// while Wit writes an empty body, a placeholder. It reports whether it drew.
func (w *composeWit) body(c *ui.Context) bool {
	t, th := theme.Of(c), c.Theme()
	opts := richtext.RenderOptions{OnLink: w.p.a.openLink}
	switch r := w.review; {
	case r != nil && !r.inline() && w.compare:
		ui.Row(c).Gap(th.Space(5)).AlignItems(ui.Start).Children(func() {
			side := func(title string, d richtext.Doc, border float32) {
				ui.Column(c).Grow(1).Basis(0).Gap(th.Space(2.5)).Padding(0, 0, 0, border*th.Space(5)).BorderWidth(0, 0, 0, border*theme.Hairline).BorderColor(t.Border).Children(func() {
					ui.Text(c, title).FontSize(t.Text.Xs).FontWeight(600)
					richtext.Render(c, d, opts)
				})
			}
			side(i18n.T("wit.compose.original"), r.old, 0)
			side(i18n.T("wit.compose.new"), r.new, 1)
		})
	case r != nil && !r.inline():
		richtext.Render(c, r.new, opts)
	case r != nil && w.reviewEd != nil:
		w.reviewView(c)
	case w.panel.running && mail.Blank(w.p.ed.HTML()):
		ui.Column(c).Gap(th.Space(2.5)).Children(func() {
			ui.Text(c, i18n.T("wit.compose.writing")).TextColor(t.Muted)
			for _, pct := range []float32{90, 75, 50} {
				ui.Box(c).Height(th.Space(3)).MaxWidthPercent(pct).Radius(t.Radius.Md).Background(t.Hover)
			}
		})
	default:
		return false
	}
	return true
}

// reviewView is the edit word by word: deletions struck, insertions
// marked; pointing at a change, or the caret in it, offers its Accept and
// Reject.
func (w *composeWit) reviewView(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	r := w.review
	var hs []editor.Highlight
	for _, m := range r.marks {
		h := editor.Highlight{From: m.from, To: m.to, Bg: t.InsBg, Fg: t.InsText}
		if m.kind == worddiff.Del {
			h = editor.Highlight{From: m.from, To: m.to, Bg: t.DelBg, Fg: t.DelText, Strike: true}
		}
		hs = append(hs, h)
	}
	ui.Box(c).Children(func() {
		el := w.reviewEd.View(c, editor.Options{Label: i18n.T("wit.compose.review_body"), ReadOnly: true, Highlights: hs,
			OnReadOnlyInput: w.lockedInput}).FillWidth()
		over := false
		if k := w.active; k >= 0 && r.decided[k] == 0 {
			b := w.changeBounds(k)
			pop := ui.Row(c).Absolute().Top(b.Y+b.H+th.Space(1)).Left(b.X).Gap(th.Space(1)).Padding(th.Space(1)).
				Radius(t.Radius.Lg).Border(theme.Hairline, t.Border).Background(t.Background).Children(func() {
				if u := r.linkNote(k); u != "" {
					ui.Text(c, i18n.Tf("wit.compose.link", u)).FontSize(t.Text.Sm).Padding(0, th.Space(1))
				}
				kit.Button(c, i18n.T("wit.compose.accept"), kit.ButtonOpts{Size: kit.SizeSm}).OnClick(func() { w.decideOne(k, true) })
				kit.Button(c, i18n.T("wit.compose.reject"), kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeSm}).OnClick(func() { w.decideOne(k, false) })
			})
			over = pop.Hovered() || pop.FocusWithin()
		}
		if over {
			return
		}
		if x, y, in := el.PointerPosition(); in {
			i, _ := w.reviewEd.IndexForPoint(ui.Point{X: x, Y: y})
			if k := r.changeAt(posAt(r.view, i)); k >= 0 {
				w.active = k
			}
		} else if el.Focused() {
			s, _ := w.reviewEd.DocumentSelection()
			if k := r.changeAt(s); k >= 0 {
				w.active = k
			}
		}
	})
}

// changeBounds is the box of change k's first line in the review, in the
// review's terms.
func (w *composeWit) changeBounds(k int) ui.Rect {
	r := w.review
	from, to, ok := r.span(k)
	if !ok {
		return ui.Rect{}
	}
	b, _, _ := w.reviewEd.BoundsForRange(ui.TextInputRange{Start: utf16At(r.view, from), End: utf16At(r.view, to)})
	return b
}

// editor is the body's editor, read-only while locked, with ⌘K's prompt
// below the selection.
func (w *composeWit) editor(c *ui.Context) {
	p := w.p
	t, th := theme.Of(c), c.Theme()
	locked := w.locked()
	o := editor.Options{Label: i18n.T("compose.body"), PasteHTML: p.a.plat.ClipboardHTML, ReadOnly: locked, OnReadOnlyInput: w.lockedInput}
	if !locked {
		o.Placeholder = i18n.T("wit.compose.empty")
	}
	if locked {
		w.cmdk = false
	}
	if from, to := p.ed.DocumentSelection(); !locked && from != to && p.a.settings.DefaultModel != "" && c.Shortcut(ui.Cmd, ui.KeyK) {
		w.cmdk, w.selFrom, w.selTo = true, from, to
	}
	ui.Box(c).Children(func() {
		p.ed.View(c, o).FillWidth()
		if !w.cmdk {
			return
		}
		b, _, _ := p.ed.BoundsForRange(p.ed.Selection().Range)
		box := ui.Column(c).Absolute().Top(b.Y+b.H+th.Space(1.5)).Left(0).Right(0).Gap(th.Space(1.5)).Padding(th.Space(2), th.Space(2.5)).
			Radius(t.Radius.Xl).Border(theme.Hairline, t.Border).Background(t.Background).Children(func() {
			ui.Row(c).Gap(th.Space(2)).Children(func() {
				ui.Icon(c, icons.Get("sparkles")).Size(theme.IconSm, theme.IconSm)
				in := kit.Input(c, &w.cmdkInput, kit.InputOpts{Bare: true}).Grow(1).Label(i18n.T("wit.compose.cmdk.input")).AutoFocus()
				if in.Submitted() {
					w.rewriteSelection()
				}
			})
			ui.Row(c).Gap(th.Space(1.5)).FontSize(t.Text.Xs).TextColor(t.Muted).Children(func() {
				ui.Text(c, i18n.T("wit.compose.cmdk")).Grow(1).Shrink(1)
				kit.Button(c, i18n.T("wit.compose.cmdk.esc"), kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeSm}).OnClick(func() { w.cmdk = false })
				kit.Button(c, i18n.T("wit.compose.cmdk.go"), kit.ButtonOpts{Size: kit.SizeSm}).OnClick(w.rewriteSelection)
			})
		})
		if box.Shortcut(0, ui.KeyEscape) {
			w.cmdk = false
		}
	})
}
