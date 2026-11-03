package app

import (
	"context"
	"encoding/json"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// schedulePreview reads, once typing pauses, the existing mail the editor's
// account, trigger and conditions match; a change meanwhile cancels the read under way.
func (v *rulesView) schedulePreview() {
	d := v.edit.Doc
	d.Conditions = slices.Clone(d.Conditions) // the editor writes its own while the read runs
	b, _ := json.Marshal([]any{d.Account, d.Trigger, d.TriggerArg, d.Match, d.Conditions})
	if string(b) == v.prevKey {
		return
	}
	v.stopPreview()
	v.prevKey, v.prev = string(b), nil
	if slices.ContainsFunc(d.Conditions, func(cd core.RuleCondition) bool { return strings.TrimSpace(cd.Value) == "" }) ||
		d.Trigger == core.TriggerLabel && d.TriggerArg == "" {
		return
	}
	a, key := v.a, v.prevKey
	v.prevStop = a.after(debounce, func() {
		v.prevStop = nil
		ctx, cancel := context.WithCancel(a.ctx)
		v.prevCancel = cancel
		var p core.RulePreview
		a.query(func(context.Context) (err error) { p, err = a.svc.PreviewRule(ctx, d, rulePreviewShown); return err }, func(err error) {
			cancel()
			if key != v.prevKey {
				return // a newer one is coming
			}
			v.prevCancel = nil
			if err == nil {
				v.prev = &p
			}
		})
	})
}

func (v *rulesView) stopPreview() {
	if v.prevStop != nil {
		v.prevStop()
		v.prevStop = nil
	}
	if v.prevCancel != nil {
		v.prevCancel()
		v.prevCancel = nil
	}
}

// textField reports whether a condition on f compares typed text.
func textField(f string) bool {
	return f == core.FieldFrom || f == core.FieldTo || f == core.FieldSubject || f == core.FieldBody
}

// fieldChanged resets cd's comparison and value for its new field, keeping
// typed text between text fields.
func (v *rulesView) fieldChanged(cd *core.RuleCondition, was string) {
	switch {
	case textField(cd.Field) && textField(was):
	case textField(cd.Field):
		cd.Op, cd.Value = core.OpContains, ""
	case cd.Field == core.FieldAttachments:
		cd.Op, cd.Value = core.OpIs, core.RuleYes
	case cd.Field == core.FieldLabel:
		cd.Op, cd.Value = core.OpIs, v.firstLabel()
	case cd.Field == core.FieldFolder:
		cd.Op, cd.Value = core.OpIs, ruleRoles[0]
	case cd.Field == core.FieldAge:
		cd.Op, cd.Value = core.OpOlderThan, "30"
	}
}

// ruleCard is one of the editor's node cards, edged and filled as given.
func ruleCard(c *ui.Context, edge, fill ui.Color, body func()) ui.Element {
	t, th := theme.Of(c), c.Theme()
	return ui.Column(c).FillWidth().Gap(th.Space(2.5)).Padding(th.Space(3), th.Space(4)).Radius(t.Radius.Xl).
		Background(fill).Border(theme.Hairline, edge).Children(body)
}

// cardHead is a node card's head: its icon in a tile, its word (当, 如果,
// 就), then extra on the same line.
func cardHead(c *ui.Context, icon, word string, tile, fg ui.Color, extra func()) {
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(th.Space(2)).Children(func() {
		ui.Box(c).Size(th.Space(6), th.Space(6)).Radius(t.Radius.Md).Background(tile).TextColor(fg).Center().Shrink(0).Children(func() {
			ui.Icon(c, icons.Get(icon)).Size(theme.IconSm, theme.IconSm)
		})
		kit.Text(c, word, t.Text.Body).FontWeight(600)
		if extra != nil {
			extra()
		}
	})
}

// connector is the line between two node cards.
func connector(c *ui.Context) {
	ui.Box(c).Size(c.Theme().Space(0.5), theme.RuleConnector).Background(theme.Of(c).Border).AlignSelf(ui.Center).Shrink(0)
}

// ruleNote is a note in a tinted box with its icon.
func ruleNote(c *ui.Context, icon, text string, fill, edge, fg ui.Color) {
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).FillWidth().Gap(th.Space(2)).Padding(th.Space(2.5), th.Space(3)).Radius(t.Radius.Lg).
		Background(fill).Border(theme.Hairline, edge).TextColor(fg).Children(func() {
		ui.Icon(c, icons.Get(icon)).Size(theme.Icon, theme.Icon).Shrink(0)
		kit.Text(c, text, t.Text.Sm).LineHeight(theme.LineFor(text)).Grow(1).Shrink(1)
	})
}

// actionTone is how an action's card and badge look by its kind: forward
// 外发 (amber), file 写入本机, Wit AI (violet); ok is false for a local one.
func actionTone(t theme.Tokens, kind string) (badge string, fill, fg, card, edge ui.Color, ok bool) {
	switch {
	case kind == core.ActionForward:
		amber := t.Label("amber")
		return i18n.T("settings.rules.egress"), amber.Fill, amber.Text, t.WarningBg, t.WarningBorder, true
	case slices.Contains(core.RuleAIActions, kind):
		violet := t.Label("violet")
		return i18n.T("settings.rules.kind.ai"), violet.Fill, violet.Text, t.Card, violet.Edge, true
	case fileAction(kind):
		return i18n.T("settings.rules.kind.local_write"), t.Hover, t.Foreground, t.Card, t.Border, true
	}
	return "", t.Hover, t.Foreground, t.Card, t.Border, false
}

// ready reports whether the rule in the editor can be saved.
func (v *rulesView) ready() bool {
	d := &v.edit.Doc
	return len(d.Actions) > 0 &&
		!slices.ContainsFunc(d.Actions, func(ac core.RuleAction) bool {
			return (ac.Kind == core.ActionLabel || ac.Kind == core.ActionUnlabel) && ac.Label == "" ||
				ac.Kind == core.ActionForward && strings.TrimSpace(ac.To) == ""
		}) && (d.Trigger != core.TriggerLabel || d.TriggerArg != "") &&
		!slices.ContainsFunc(d.Conditions, func(cd core.RuleCondition) bool { return strings.TrimSpace(cd.Value) == "" }) && fileReady(d) &&
		(v.a.settings.RuleAI || !usesAI(*d))
}

// editor is the back link and heading, then the rule as a column of node
// cards joined by lines (名称, 当, 如果, then each action) beside a side
// panel that stays in view: the existing mail it matches, 运行方式 and notes.
func (v *rulesView) editor(c *ui.Context) {
	th := c.Theme()
	d := &v.edit.Doc
	title, sub := i18n.T("settings.rules.new"), i18n.T("settings.rules.editor.sub")
	if v.edit.ID != 0 {
		title, sub = ruleName(*d), v.summary(*d)
	}
	ui.Column(c).Gap(theme.HeadingGap).Children(func() {
		settingsBack(c, i18n.T("settings.rules"), v.closeEditor)
		pageHeading(c, title, sub, func() {
			ui.Row(c).Gap(th.Space(2)).Children(func() {
				settingButton(c, i18n.T("common.cancel"), kit.Outline, false).OnClick(v.closeEditor)
				settingButton(c, i18n.T("common.save"), kit.Default, !v.ready()).OnClick(v.save)
			})
		})
	})
	v.schedulePreview()
	v.saveDialog(c)
	v.banners(c)
	grid := ui.Row(c).FillWidth().Gap(th.Space(6)).AlignItems(ui.Start)
	top := grid.LayoutBounds().Y
	grid.Children(func() {
		flow := ui.Column(c).Grow(1).Shrink(1).Basis(0).MinWidth(0)
		flowH := flow.LayoutBounds().H
		flow.Children(func() { v.flow(c) })
		aside := ui.Column(c).Width(theme.RuleAsideWidth).Shrink(0).Gap(th.Space(3)).Label(i18n.T("settings.rules.aside"))
		// Sticky: the panel moves down as the page scrolls past the grid's top, within the flow's height.
		off := max(0, min(v.viewTop+th.Space(4)-top, flowH-aside.LayoutBounds().H))
		if off != v.asideTop {
			v.asideTop = off
			c.Invalidate()
		}
		aside.Margin(v.asideTop, 0, 0, 0).Children(func() { v.aside(c) })
	})
	v.confirmDialog(c)
}

// banners say where the editor's rule came from: a mail (with conditions
// to add) or Wit (with what was asked, and 回到对话).
func (v *rulesView) banners(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	d := &v.edit.Doc
	if v.from != "" {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(th.Space(2.5)).Padding(th.Space(2.5), th.Space(3.5)).Radius(t.Radius.Lg).Background(t.Hover).Children(func() {
			ui.Icon(c, icons.Get("mail")).Size(theme.Icon, theme.Icon).TextColor(t.Muted).Shrink(0)
			kit.Text(c, i18n.Tf("settings.rules.from_mail.banner", v.from), t.Text.Body).Grow(1).Shrink(1)
			for i, cd := range v.suggest {
				kit.Button(c.Key(i), "＋ "+v.chipText(cd), kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeSm}).Shrink(0).OnClick(func() {
					d.Conditions = append(d.Conditions, cd)
					v.suggest = slices.Delete(slices.Clone(v.suggest), i, i+1)
				})
			}
		})
	}
	if w := v.wit; w != nil {
		violet := t.Label("violet")
		ui.Row(c).FillWidth().AlignItems(ui.Start).Gap(th.Space(2.5)).Padding(th.Space(2.5), th.Space(3.5)).Radius(t.Radius.Lg).
			Background(violet.Fill.Mix(t.Background, 0.5)).TextColor(violet.Text).Children(func() {
			ui.Icon(c, icons.Get("sparkles")).Size(theme.Icon, theme.Icon).Shrink(0).Margin(th.Space(0.5), 0, 0, 0)
			ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(0.5)).Children(func() {
				kit.Text(c, i18n.T("settings.rules.from_wit.banner"), t.Text.Body)
				if w.ask != "" {
					kit.Text(c, i18n.Tf("settings.rules.from_wit.quote", w.ask), t.Text.Sm).TextColor(violet.Dot)
				}
			})
			if w.conv != 0 {
				conv := w.conv
				kit.TextLink(c, i18n.T("settings.rules.from_wit.back"), violet.Dot, t.Text.Sm).Shrink(0).
					OnClick(func() { v.a.navigate(Route{Page: PageWit, ConversationID: conv}) })
			}
		})
	}
}

// flow is the editor's column of node cards.
func (v *rulesView) flow(c *ui.Context) {
	a := v.a
	t, th := theme.Of(c), c.Theme()
	d := &v.edit.Doc
	ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(th.Space(3)).Padding(th.Space(2), th.Space(4)).MinHeight(th.Space(12)).
		Radius(t.Radius.Xl).Background(t.Hover).Children(func() {
		kit.Text(c, i18n.T("settings.rules.name"), t.Text.Body).FontWeight(500).Shrink(0)
		kit.Input(c, &d.Name, kit.InputOpts{Placeholder: i18n.T("settings.rules.name.placeholder")}).Label(i18n.T("settings.rules.name")).Grow(1).Shrink(1)
	})
	ui.Box(c).Height(th.Space(4))
	labels := make([]kit.Option[string], len(a.labels))
	for i, l := range a.labels {
		labels[i] = kit.Option[string]{Value: l.Slug, Label: l.Name}
	}
	v.whenCard(c, labels)
	connector(c)
	v.ifCard(c, labels)
	for i := range d.Actions {
		connector(c.Key(-1 - i)) // keys apart from the cards'
		v.actionCard(c.Key(i), i, labels)
	}
	connector(c)
	v.addAction(c)
}

// whenCard is 当: the trigger, its label or verdict, and the account.
func (v *rulesView) whenCard(c *ui.Context, labels []kit.Option[string]) {
	a := v.a
	t, th := theme.Of(c), c.Theme()
	d := &v.edit.Doc
	ruleCard(c, t.Border, t.Card, func() {
		cardHead(c, "time", i18n.T("settings.rules.when"), t.Hover, t.Foreground, nil)
		ui.Row(c).FillWidth().Wrap().AlignItems(ui.Center).Gap(th.Space(2)).Children(func() {
			was := d.Trigger
			triggers := []kit.Option[string]{}
			for _, tr := range []string{core.TriggerReceive, core.TriggerLabel, core.TriggerSecurity, core.TriggerDaily} {
				triggers = append(triggers, kit.Option[string]{Value: tr, Label: i18n.T("settings.rules.trigger." + tr)})
			}
			triggerSelect(c, i18n.T("settings.rules.trigger"), &d.Trigger, triggers)
			if d.Trigger != was {
				d.TriggerArg = map[string]string{core.TriggerSecurity: core.SecuritySuspicious}[d.Trigger]
				if d.Trigger == core.TriggerLabel && len(labels) > 0 {
					d.TriggerArg = labels[0].Value
				}
			}
			switch d.Trigger {
			case core.TriggerLabel:
				triggerSelect(c, i18n.T("settings.rules.trigger.label.which"), &d.TriggerArg, labels)
			case core.TriggerSecurity:
				triggerSelect(c, i18n.T("settings.rules.trigger.security.which"), &d.TriggerArg, []kit.Option[string]{
					{Value: core.SecuritySuspicious, Label: i18n.T("settings.rules.security.suspicious")},
					{Value: core.SecurityDangerous, Label: i18n.T("settings.rules.security.dangerous")}})
			}
			kit.Text(c, i18n.T("settings.rules.in"), t.Text.Body).TextColor(t.Muted)
			accts := []kit.Option[int64]{{Value: 0, Label: i18n.T("settings.rules.account.all")}}
			for _, acc := range a.accounts {
				accts = append(accts, kit.Option[int64]{Value: acc.ID, Label: accountName(acc)})
			}
			triggerSelect(c, i18n.T("settings.rules.account"), &d.Account, accts)
		})
		kit.Text(c, i18n.T("settings.rules.trigger."+d.Trigger+".help"), t.Text.Sm).TextColor(t.Muted)
	})
}

// ifCard is 如果: how the conditions combine, then each, then 添加条件.
func (v *rulesView) ifCard(c *ui.Context, labels []kit.Option[string]) {
	t, th := theme.Of(c), c.Theme()
	d := &v.edit.Doc
	var fields []kit.Option[string]
	for _, f := range []string{core.FieldFrom, core.FieldTo, core.FieldSubject, core.FieldBody, core.FieldAttachments, core.FieldLabel, core.FieldFolder, core.FieldAge} {
		fields = append(fields, kit.Option[string]{Value: f, Label: i18n.T("settings.rules.field." + f)})
	}
	ops := []kit.Option[string]{{Value: core.OpContains, Label: i18n.T("settings.rules.op.contains")},
		{Value: core.OpNotContains, Label: i18n.T("settings.rules.op.not_contains")}, {Value: core.OpIs, Label: i18n.T("settings.rules.op.is")}}
	setOps := []kit.Option[string]{ops[2], ops[1]} // a label or folder is or is not the message's
	yesNo := []kit.Option[string]{{Value: core.RuleYes, Label: i18n.T("settings.rules.attachments.yes")}, {Value: core.RuleNo, Label: i18n.T("settings.rules.attachments.no")}}
	roles := make([]kit.Option[string], len(ruleRoles))
	for i, r := range ruleRoles {
		roles[i] = kit.Option[string]{Value: r, Label: i18n.T("mail.folder." + r)}
	}
	ruleCard(c, t.Border, t.Card, func() {
		cardHead(c, "filter", i18n.T("settings.rules.if"), t.Hover, t.Foreground, func() {
			ui.Row(c).Grow(1)
			kit.Tabs(c, &v.any, i18n.T("settings.rules.match.all"), i18n.T("settings.rules.match.any")).Label(i18n.T("settings.rules.match"))
			d.Match = []string{core.MatchAll, core.MatchAny}[v.any]
		})
		if len(d.Conditions) == 0 {
			kit.Text(c, i18n.T("settings.rules.no_conditions"), t.Text.Sm).TextColor(t.Muted)
		}
		for i := range d.Conditions {
			ui.Row(c.Key(i)).FillWidth().Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
				cd := &d.Conditions[i]
				was := cd.Field
				ruleSelect(c, i18n.T("settings.rules.field"), &cd.Field, fields, theme.RuleFieldWidth)
				if cd.Field != was {
					v.fieldChanged(cd, was)
				}
				switch cd.Field {
				case core.FieldAttachments:
					ruleSelect(c, i18n.T("settings.rules.value"), &cd.Value, yesNo, 0)
				case core.FieldLabel:
					ruleSelect(c, i18n.T("settings.rules.op"), &cd.Op, setOps, theme.RuleOpWidth)
					ruleSelect(c, i18n.T("settings.rules.label"), &cd.Value, labels, 0)
				case core.FieldFolder:
					ruleSelect(c, i18n.T("settings.rules.op"), &cd.Op, setOps, theme.RuleOpWidth)
					ruleSelect(c, i18n.T("settings.rules.folder"), &cd.Value, roles, 0)
				case core.FieldAge:
					kit.Text(c, i18n.T("settings.rules.op.older_than"), t.Text.Sm)
					kit.Input(c, &cd.Value, kit.InputOpts{Placeholder: "30"}).Label(i18n.T("settings.rules.days")).Width(th.Space(16))
					kit.Text(c, i18n.T("settings.rules.days_ago"), t.Text.Sm)
				default:
					ruleSelect(c, i18n.T("settings.rules.op"), &cd.Op, ops, theme.RuleOpWidth)
					ph := "settings.rules.value.text"
					if cd.Field == core.FieldFrom || cd.Field == core.FieldTo {
						ph = "settings.rules.value.addr"
					}
					kit.Input(c, &cd.Value, kit.InputOpts{Placeholder: i18n.T(ph)}).Label(i18n.T("settings.rules.value")).Grow(1).Shrink(1).Basis(0).MinWidth(0)
				}
				if cd.Field == core.FieldAge {
					ui.Row(c).Grow(1)
				}
				kit.IconButton(c, "x", i18n.T("settings.rules.condition.remove"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).
					OnClick(func() { d.Conditions = slices.Delete(d.Conditions, i, i+1) })
			})
		}
		kit.Button(c, i18n.T("settings.rules.condition.add"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm, Icon: "plus",
			Disabled: len(d.Conditions) >= core.MaxRuleConditions}).AlignSelf(ui.Start).TextColor(t.Muted).
			OnClick(func() {
				d.Conditions = append(d.Conditions, core.RuleCondition{Field: core.FieldSubject, Op: core.OpContains})
			})
	})
}

// triggerSelect is a select on the 当 card, as wide as its choice.
func triggerSelect[T comparable](c *ui.Context, label string, sel *T, opts []kit.Option[T]) ui.Element {
	return kit.SelectWith(c, kit.SelectOpts{Radius: theme.Of(c).Radius.Lg, Flat: true, MaxWidth: theme.SettingsSelectMax}, sel, opts).Label(label)
}

// ruleSelect is a select in a rule card's row: w wide (a longer language
// may widen it a little), or filling what the row leaves when w is 0.
func ruleSelect[T comparable](c *ui.Context, label string, sel *T, opts []kit.Option[T], w float32) ui.Element {
	o := kit.SelectOpts{Radius: theme.Of(c).Radius.Lg, Flat: true, MinWidth: w, MaxWidth: w * 1.5}
	if w == 0 {
		return kit.SelectWith(c, o, sel, opts).Label(label).Grow(1).Shrink(1).Basis(0).MinWidth(0)
	}
	return kit.SelectWith(c, o, sel, opts).Label(label).Shrink(0)
}

// aiOff reports whether an action of kind is a Wit one that 「智能」 does
// not let rules use: it is listed, but not chosen.
func (v *rulesView) aiOff(kind string) bool {
	return !v.a.settings.RuleAI && slices.Contains(core.RuleAIActions, kind)
}

// actionCard is action i: its icon, 就 (the first) or 然后, its kind's
// badge, moves and removal; its kind and what it takes; its note.
func (v *rulesView) actionCard(c *ui.Context, i int, labels []kit.Option[string]) {
	t, th := theme.Of(c), c.Theme()
	d := &v.edit.Doc
	ac := &d.Actions[i]
	badge, fill, fg, card, edge, tinted := actionTone(t, ac.Kind)
	verb := i18n.T("settings.rules.then")
	if i > 0 {
		verb = i18n.T("settings.rules.then.next")
	}
	var kinds []kit.Option[string]
	for _, g := range ruleActionGroups {
		for _, k := range g.kinds {
			label := i18n.T("settings.rules.action." + k)
			if v.aiOff(k) {
				label = i18n.Tf("settings.rules.ai_off.option", label)
			}
			kinds = append(kinds, kit.Option[string]{Value: k, Label: label, Disabled: v.aiOff(k) && k != ac.Kind})
		}
	}
	ruleCard(c, edge, card, func() {
		cardHead(c, actionIcon(ac.Kind), verb, fill, fg, func() {
			if tinted {
				ruleTag(c, badge, fill, fg, "")
			}
			ui.Row(c).Grow(1)
			kit.IconButton(c, "arrow-up", i18n.T("settings.rules.action.up"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm, Disabled: i == 0}).
				OnClick(func() { d.Actions[i-1], d.Actions[i] = d.Actions[i], d.Actions[i-1] })
			kit.IconButton(c, "arrow-down", i18n.T("settings.rules.action.down"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm, Disabled: i == len(d.Actions)-1}).
				OnClick(func() { d.Actions[i], d.Actions[i+1] = d.Actions[i+1], d.Actions[i] })
			kit.IconButton(c, "trash-2", i18n.T("settings.rules.action.remove"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm, Disabled: len(d.Actions) == 1}).
				OnClick(func() { d.Actions = slices.Delete(d.Actions, i, i+1) })
		})
		ui.Row(c).FillWidth().Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
			ruleSelect(c, i18n.Tf("settings.rules.action.n", i+1), &ac.Kind, kinds, theme.RuleActionWidth)
			if ac.Kind == core.ActionLabel || ac.Kind == core.ActionUnlabel {
				if ac.Label == "" {
					ac.Label = v.firstLabel()
				}
				ruleSelect(c, i18n.T("settings.rules.label"), &ac.Label, labels, 0)
			} else {
				ac.Label = ""
			}
			if ac.Kind == core.ActionForward {
				kit.Input(c, &ac.To, kit.InputOpts{Placeholder: i18n.T("settings.rules.forward.to")}).Label(i18n.T("settings.rules.forward.to")).Grow(1).Shrink(1).Basis(0).MinWidth(0)
			} else {
				ac.To = ""
			}
			if fileAction(ac.Kind) {
				fileKindChanged(d, i)
				v.fileDir(c, d, i)
			} else {
				ac.Dir, ac.Subfolder, ac.Naming = "", false, ""
			}
		})
		switch {
		case ac.Kind == core.ActionForward:
			note := "settings.rules.forward.note"
			if k := mail.AddrKey(ac.To); k != "" && slices.Contains(v.edit.Confirmed, k) {
				note = "settings.rules.forward.confirmed"
			}
			kit.Text(c, i18n.T(note), t.Text.Sm).TextColor(t.WarningText)
		case v.aiOff(ac.Kind):
			kit.Text(c, i18n.T("settings.rules.ai_off.hint"), t.Text.Sm).TextColor(t.Muted)
		case slices.Contains(core.RuleAIActions, ac.Kind):
			kit.Text(c, i18n.T("settings.rules.ai.note"), t.Text.Sm).TextColor(t.Muted)
		case fileAction(ac.Kind):
			kit.Text(c, i18n.T("settings.rules.files.note"), t.Text.Sm).TextColor(t.Muted)
		}
		if fileAction(ac.Kind) {
			v.fileOptions(c, d, i)
		}
	})
}

// addAction is 添加动作 and its menu of the actions by group, each with its icon.
func (v *rulesView) addAction(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	d := &v.edit.Doc
	add := kit.Button(c, i18n.T("settings.rules.action.add"), kit.ButtonOpts{Variant: kit.Outline, Flat: true, Icon: "plus", Disabled: len(d.Actions) >= core.MaxRuleActions}).
		PaddingX(th.Space(3.5)).BorderStyle(ui.BorderDashed).AlignSelf(ui.Center).OnClick(func() { v.menu = !v.menu })
	if !v.menu {
		return
	}
	kit.MenuAt(c, add, &v.menu, ui.AnchorBottomLeft, ui.AnchorTopLeft, theme.PopupOffset, i18n.T("settings.rules.action.add"), func() {
		for _, g := range ruleActionGroups {
			if len(g.kinds) == 0 {
				continue
			}
			kit.MenuLabel(c.Key("group:"+g.key), i18n.T("settings.rules.group."+g.key))
			for _, k := range g.kinds {
				label, off := i18n.T("settings.rules.action."+k), v.aiOff(k)
				fg := t.Foreground
				if off {
					fg = t.Muted
				}
				kit.MenuItem(c.Key(k), false).Label(label).Disabled(off).TextColor(fg).Children(func() {
					ui.Icon(c, icons.Get(actionIcon(k))).Size(theme.Icon, theme.Icon).Shrink(0)
					ui.Text(c, label).SingleLine().Grow(1)
					if off {
						kit.Text(c, i18n.T("settings.rules.ai_off.hint"), t.Text.Sm).TextColor(t.Muted)
					}
				}).OnClick(func() {
					v.menu = false
					d.Actions = append(d.Actions, core.RuleAction{Kind: k})
				})
			}
		}
	})
}

// aside is the side panel: the existing mail the rule matches, 运行方式,
// and the notes on forwarding and on Wit's actions.
func (v *rulesView) aside(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	// 运行方式 first: a choice of the rule's, over the preview that only shows.
	ui.Column(c).FillWidth().Gap(th.Space(2)).Padding(th.Space(3)).Radius(t.Radius.Xl).Background(t.Card).Border(theme.Hairline, t.Border).Children(func() {
		kit.Text(c, i18n.T("settings.rules.run"), t.Text.Body).FontWeight(600).PaddingX(th.Space(1))
		for _, o := range []struct {
			bg  bool
			key string
		}{{false, "window"}, {true, "background"}} {
			kit.RadioCard(c.Key(o.key), &v.edit.Background, o.bg, i18n.T("settings.rules.run."+o.key), i18n.T("settings.rules.run."+o.key+".help"))
		}
	})
	ui.Column(c).FillWidth().Gap(th.Space(2)).Padding(th.Space(3)).Radius(t.Radius.Xl).Background(t.Hover).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Baseline).Gap(th.Space(2)).PaddingX(th.Space(1)).Children(func() {
			kit.Text(c, i18n.T("settings.rules.preview"), t.Text.Body).FontWeight(600).Grow(1)
			kit.Text(c, i18n.Tf("settings.rules.preview.recent", core.RulePreviewDays), t.Text.Sm).TextColor(t.Muted)
		})
		p := v.prev
		muted := func(key string) {
			ui.Column(c).FillWidth().Padding(th.Space(4), th.Space(3)).Radius(t.Radius.Lg).Background(t.Background).Border(theme.Hairline, t.Border).
				AlignItems(ui.Center).Children(func() { kit.Text(c, i18n.T(key), t.Text.Sm).TextColor(t.Muted).TextAlign(ui.Center) })
		}
		switch {
		case p == nil && (v.prevStop != nil || v.prevCancel != nil):
			muted("settings.rules.preview.wait")
		case p == nil:
			muted("settings.rules.preview.incomplete")
		case p.Count == 0:
			muted("settings.rules.preview.none")
		default:
			kit.Text(c, i18n.Tf("settings.rules.preview.count", p.Count), t.Text.Sm).TextColor(t.Muted).PaddingX(th.Space(1))
			now := time.Now()
			ui.Column(c).FillWidth().Radius(t.Radius.Lg).Background(t.Background).Border(theme.Hairline, t.Border).Clip().Children(func() {
				for i, m := range p.Messages {
					if i > 0 {
						kit.Separator(c, false)
					}
					ui.Column(c.Key(m.ID)).FillWidth().Gap(th.Space(0.5)).Padding(th.Space(2), th.Space(3)).Children(func() {
						ui.Row(c).FillWidth().Gap(th.Space(2)).Children(func() {
							kit.Text(c, displayName(m.From), t.Text.Sm).FontWeight(500).SingleLine().Grow(1).Shrink(1)
							kit.Text(c, listTime(now, m.Date), t.Text.Sm).TextColor(t.Muted).Shrink(0)
						})
						kit.Text(c, m.Subject, t.Text.Sm).SingleLine()
					})
				}
			})
		}
	})
	if v.menu && !v.a.settings.RuleAI {
		violet := t.Label("violet")
		ruleNote(c, "sparkles", i18n.T("settings.rules.ai_off.note"), violet.Fill.Mix(t.Background, 0.5), violet.Edge, violet.Text)
	}
	if forwards(v.edit.Doc) {
		ruleNote(c, "forward", i18n.T("settings.rules.forward.aside"), t.WarningBg, t.WarningBorder, t.WarningText)
	}
}

// save saves the rule, first asking to confirm forward recipients it has
// not confirmed: a new one, or one changed since.
func (v *rulesView) save() {
	a := v.a
	var todo []string
	for _, ac := range v.edit.Doc.Actions {
		k := mail.AddrKey(ac.To)
		if ac.Kind == core.ActionForward && !slices.Contains(v.edit.Confirmed, k) && !slices.ContainsFunc(todo, func(x string) bool { return mail.AddrKey(x) == k }) {
			todo = append(todo, strings.TrimSpace(ac.To))
		}
	}
	if len(todo) == 0 {
		v.commit()
		return
	}
	var first []string
	a.query(func(ctx context.Context) (err error) { first, err = a.svc.FirstTimeRecipients(ctx, todo); return err }, func(err error) {
		a.toastErr(err)
		if err == nil {
			v.confirming, v.unconfirmed, v.firstTime = true, todo, first
		}
	})
}

// commit opens the save sheet, which asks whether to run the rule over the
// existing mail it matches too; from the sheet it stores the rule.
func (v *rulesView) commit() {
	if !v.saveAsk {
		v.saveAsk = true
		return
	}
	a := v.a
	r, name, wit := v.edit, ruleName(v.edit.Doc), v.wit
	n := 0
	if v.prev != nil && v.runExisting {
		n = v.prev.Count
	}
	v.closeEditor()
	var id int64
	a.writes.Chain("rules", func(ctx context.Context) (err error) {
		if id, err = a.svc.SaveRule(ctx, r); err == nil && wit != nil && wit.approval != 0 {
			// Its card shows it saved; the rule is saved either way (its conversation may be gone).
			if err := a.svc.ProposedRuleSaved(ctx, wit.approval, r); err != nil {
				log.Printf("rule proposal %d: %v", wit.approval, err)
			}
		}
		return err
	}, func(err error) {
		v.saved(err)
		if err != nil {
			return
		}
		if n == 0 {
			a.toastAs(kit.ToastSuccess, i18n.Tf("settings.rules.save.saved", name))
			return
		}
		a.toastAs(kit.ToastSuccess, i18n.Tf("settings.rules.save.saved_existing", name, n))
		a.writes.Chain("rules", func(ctx context.Context) error { _, err := a.svc.RunRuleOnExisting(ctx, id); return err }, v.saved)
	})
}

// saveDialog is the save sheet: from now on the rule runs on mail that
// matches, and, with existing mail it matches, whether it also runs once
// over that; only the actions that can run on it then do.
func (v *rulesView) saveDialog(c *ui.Context) {
	if !v.saveAsk {
		return
	}
	t, th := theme.Of(c), c.Theme()
	d := v.edit.Doc
	var now, later []string
	for _, ac := range d.Actions {
		switch {
		case ac.Kind == core.ActionStop:
		case core.RunsOnExisting(ac.Kind):
			now = append(now, v.actionText(ac))
		default:
			later = append(later, v.actionText(ac))
		}
	}
	count := 0
	if v.prev != nil {
		count = v.prev.Count
	}
	sep := i18n.T("settings.rules.sep")
	kit.Dialog(c, &v.saveAsk, i18n.Tf("settings.rules.save.title", ruleName(d)), kit.DialogOpts{Size: kit.DialogSm, Desc: i18n.T("settings.rules.save.desc"), Close: i18n.T("common.close")}, func() {
		if count == 0 {
			v.runExisting = false
			return
		}
		ui.Column(c).Gap(th.Space(1)).Padding(th.Space(3)).Radius(t.Radius.Lg).Background(t.Background).Border(theme.Hairline, t.Border).Children(func() {
			if len(now) == 0 {
				v.runExisting = false
				kit.Text(c, i18n.T("settings.rules.save.existing.nothing"), t.Text.Sm).TextColor(t.Muted)
			} else {
				kit.Checkbox(c, &v.runExisting, i18n.Tf("settings.rules.save.existing", count))
				kit.Text(c, i18n.Tf("settings.rules.save.existing.only", strings.Join(now, sep)), t.Text.Sm).TextColor(t.Muted).PaddingX(th.Space(6))
			}
			if len(later) > 0 {
				kit.Text(c, i18n.Tf("settings.rules.save.existing.later", strings.Join(later, sep)), t.Text.Sm).TextColor(t.Muted).PaddingX(th.Space(6))
			}
		})
	}, func() {
		kit.Button(c, i18n.T("common.cancel"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(func() { v.saveAsk = false })
		kit.Button(c, i18n.T("settings.rules.save.confirm"), kit.ButtonOpts{}).OnClick(v.commit)
	})
}

// confirmDialog names the forward recipients to confirm, the ones never
// written to flagged, and the account they go from when the rule is for
// one; going back to edit saves nothing.
func (v *rulesView) confirmDialog(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	outbox := i18n.T("settings.rules.confirm.outbox")
	if acc, ok := v.account(v.edit.Doc.Account); ok {
		outbox = i18n.Tf("settings.rules.confirm.outbox.from", acc.Email)
	}
	kit.Dialog(c, &v.confirming, i18n.T("settings.rules.confirm.title"), kit.DialogOpts{Size: kit.DialogSm, Alert: true,
		Desc: i18n.Tf("settings.rules.confirm.desc", ruleName(v.edit.Doc)), Close: i18n.T("common.close")}, func() {
		amber := t.Label("amber")
		ui.Column(c).FillWidth().Gap(th.Space(1.5)).Padding(th.Space(3.5)).Radius(t.Radius.Lg).Background(t.WarningBg).Border(theme.Hairline, t.WarningBorder).Children(func() {
			kit.Text(c, i18n.T("settings.rules.confirm.to"), t.Text.Sm).TextColor(t.WarningText)
			for _, to := range v.unconfirmed {
				ui.Row(c).Wrap().Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
					kit.Text(c, to, t.Text.Lg).Font(theme.Mono).FontWeight(500).Selectable()
					if slices.ContainsFunc(v.firstTime, func(f string) bool { return mail.AddrKey(f) == mail.AddrKey(to) }) {
						ruleTag(c, i18n.T("settings.rules.confirm.first"), amber.Fill, amber.Text, "")
					}
				})
			}
		})
		ui.Column(c).Gap(th.Space(1.5)).Children(func() {
			for _, s := range []string{outbox, i18n.T("settings.rules.confirm.new_only"), i18n.T("settings.rules.confirm.again")} {
				kit.Text(c, "· "+s, t.Text.Base).LineHeight(theme.LineFor(s))
			}
		})
	}, func() {
		kit.Button(c, i18n.T("settings.rules.confirm.back"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(func() { v.confirming = false })
		kit.Button(c, i18n.T("settings.rules.confirm.ok"), kit.ButtonOpts{}).OnClick(func() {
			v.confirming = false
			for _, to := range v.unconfirmed {
				v.edit.Confirmed = append(v.edit.Confirmed, mail.AddrKey(to))
			}
			v.commit()
		})
	})
}
