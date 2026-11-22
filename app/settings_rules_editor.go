package app

import (
	"context"
	"encoding/json"
	"log"
	"slices"
	"strings"

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
	d.Conditions = cloneConds(d.Conditions) // the editor writes its own while the read runs
	d.Actions = slices.Clone(d.Actions)     // so with actions: the read encodes them off the UI thread
	b, _ := json.Marshal([]any{d.Account, d.Trigger, d.TriggerArg, d.Match, d.Conditions})
	if string(b) == v.prevKey {
		return
	}
	v.stopPreview()
	v.prevKey, v.prev = string(b), nil
	if slices.ContainsFunc(d.Rows(), func(cd core.RuleCondition) bool { return strings.TrimSpace(cd.Value) == "" }) ||
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

// ruleNote is a note in a tinted box with its icon.
func ruleNote(c *ui.Context, icon, text string, fill, edge, fg ui.Color) {
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).FillWidth().Gap(th.Space(2)).Padding(th.Space(2.5), th.Space(3)).Radius(t.Radius.Lg).
		Background(fill).Border(theme.Hairline, edge).TextColor(fg).Children(func() {
		ui.Icon(c, icons.Get(icon)).Size(theme.Icon, theme.Icon).Shrink(0)
		kit.Text(c, text, t.Text.Xs).Grow(1).Shrink(1)
	})
}

// drawDropLine paints the reorder drop indicator (dot + 2 px accent line) along
// the top or bottom edge of el, matching ui.List and mail_sidebar's drag cue.
func drawDropLine(el ui.Element, below bool, accent ui.Color) {
	el.DrawOver(func(pt *ui.Painter, r ui.Rect) {
		y := r.Y
		if below {
			y += r.H
		}
		pt.Fill(ui.Rect{X: r.X + 4, Y: y - 1, W: r.W - 4, H: 2}, accent, 1)
		pt.Fill(ui.Rect{X: r.X, Y: y - 4, W: 8, H: 8}, accent, 4)
	})
}

// ready reports whether the rule in the editor can be saved.
func (v *rulesView) ready() bool {
	d := &v.edit.Doc
	return len(d.Actions) > 0 &&
		!slices.ContainsFunc(d.Actions, func(ac core.RuleAction) bool {
			return (ac.Kind == core.ActionLabel || ac.Kind == core.ActionUnlabel) && ac.Label == "" ||
				ac.Kind == core.ActionForward && strings.TrimSpace(ac.To) == ""
		}) && (d.Trigger != core.TriggerLabel || d.TriggerArg != "") &&
		!slices.ContainsFunc(d.Rows(), func(cd core.RuleCondition) bool { return strings.TrimSpace(cd.Value) == "" }) && fileReady(d) &&
		(v.a.settings.RuleAI || !usesAI(*d))
}

// editorTitle is the editor's heading and breadcrumb: the rule's name, or 新建规则.
func (v *rulesView) editorTitle() string {
	if v.edit.ID != 0 {
		return ruleName(v.edit.Doc)
	}
	return i18n.T("settings.rules.new")
}

// editor is the full-height borderless split: the rule's form scrolling on the
// left (名称 & 运行方式, 当, 如果, 就) and the live matching-mail preview on the
// right. 规则 is the way back (Breadcrumb).
func (v *rulesView) editor(c *ui.Context) {
	th := c.Theme()
	d := &v.edit.Doc
	title, sub := v.editorTitle(), i18n.T("settings.rules.editor.sub")
	if v.edit.ID != 0 {
		sub = v.summary(*d)
	}
	v.schedulePreview()
	ui.Row(c).Grow(1).FillWidth().AlignItems(ui.Stretch).Children(func() {
		ui.Scroll(c).Grow(1).Shrink(1).MinWidth(0).Children(func() {
			ui.Column(c).FillWidth().MaxWidth(theme.ContentMaxWidth).Padding(th.Space(8)).Gap(th.Space(6)).Children(func() {
				pageHeading(c, title, sub, func() {
					ui.Row(c).Gap(th.Space(2)).Children(func() {
						settingButton(c, i18n.T("common.cancel"), kit.Outline, false).OnClick(v.closeEditor)
						settingButton(c, i18n.T("common.save"), kit.Default, !v.ready()).OnClick(v.save)
					})
				})
				v.banners(c)
				v.form(c)
			})
		})
		ui.Column(c).Width(theme.RuleAsideWidth).Shrink(0).Label(i18n.T("settings.rules.aside")).Children(func() { v.preview(c) })
	})
	v.saveDialog(c)
	v.confirmDialog(c)
}

// banners say where the editor's rule came from: a mail (with conditions
// to add) or Wit (with what was asked, and 回到对话).
func (v *rulesView) banners(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	d := &v.edit.Doc
	if v.from != "" {
		kit.Block(c, ui.Row(c), kit.ToneTint).FillWidth().AlignItems(ui.Center).Gap(th.Space(2.5)).Padding(th.Space(2.5), th.Space(3.5)).Children(func() {
			ui.Icon(c, icons.Get("mail")).Size(theme.Icon, theme.Icon).TextColor(t.Muted).Shrink(0)
			kit.Text(c, i18n.Tf("settings.rules.from_mail.banner", v.from), t.Text.Sm).Grow(1).Shrink(1)
			for i, cd := range v.suggest {
				kit.Button(c.Key(i), "＋ "+v.chipText(cd), kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeSm}).Shrink(0).OnClick(func() {
					if condGrouped(d.Conditions) {
						d.Conditions[0].Group = append(d.Conditions[0].Group, cd)
					} else {
						d.Conditions = append(d.Conditions, cd)
					}
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
				kit.Text(c, i18n.T("settings.rules.from_wit.banner"), t.Text.Sm)
				if w.ask != "" {
					kit.Text(c, i18n.Tf("settings.rules.from_wit.quote", w.ask), t.Text.Xs).TextColor(violet.Dot)
				}
			})
			if w.conv != 0 {
				conv := w.conv
				kit.Button(c, i18n.T("settings.rules.from_wit.back"), kit.ButtonOpts{Variant: kit.Link, Size: kit.SizeXs}).Shrink(0).
					OnClick(func() { v.a.navigate(Route{Page: PageWit, ConversationID: conv}) })
			}
		})
	}
}

// form is the editor's column of macOS-style settings cards: basic (名称 & 运行方式),
// 当, 如果 (unified condition groups), and 就 (draggable action rows).
func (v *rulesView) form(c *ui.Context) {
	a := v.a
	labels := make([]kit.Option[string], len(a.labels))
	for i, l := range a.labels {
		labels[i] = kit.Option[string]{Value: l.Slug, Label: l.Name}
	}
	v.basicSection(c)
	v.whenSection(c, labels)
	v.ifSection(c, labels)
	v.thenSection(c, labels)
}

// basicSection is 名称 and 运行方式 in one settings card.
func (v *rulesView) basicSection(c *ui.Context) {
	d := &v.edit.Doc
	settingsCard(c, func() {
		settingRow(c, i18n.T("settings.rules.name"), "", func() ui.Element {
			return kit.Input(c, &d.Name, kit.InputOpts{Placeholder: i18n.T("settings.rules.name.placeholder")}).
				Label(i18n.T("settings.rules.name")).Width(theme.SettingsControlWidth)
		})
	}, func() {
		runIdx := 0
		helpKey := "settings.rules.run.window.help"
		if v.edit.Background {
			runIdx = 1
			helpKey = "settings.rules.run.background.help"
		}
		settingRow(c, i18n.T("settings.rules.run"), i18n.T(helpKey), func() ui.Element {
			el := kit.Tabs(c, &runIdx, kit.TabsOpts{Look: kit.TabsSegmented},
				kit.TabLabels(i18n.T("settings.rules.run.window"), i18n.T("settings.rules.run.background")))
			v.edit.Background = runIdx == 1
			return el
		})
	})
}

// whenSection is 当: the trigger (with its label or verdict when applicable)
// and the receiving account scope.
func (v *rulesView) whenSection(c *ui.Context, labels []kit.Option[string]) {
	a := v.a
	th := c.Theme()
	d := &v.edit.Doc
	settingsSection(c, i18n.T("settings.rules.when"), "", func() {
		settingRow(c, i18n.T("settings.rules.trigger"), i18n.T("settings.rules.trigger."+d.Trigger+".help"), func() ui.Element {
			var first ui.Element
			ui.Row(c).AlignItems(ui.Center).Gap(th.Space(2)).Children(func() {
				was := d.Trigger
				var triggers []kit.Option[string]
				for _, tr := range []string{core.TriggerReceive, core.TriggerLabel, core.TriggerSecurity, core.TriggerDaily} {
					triggers = append(triggers, kit.Option[string]{Value: tr, Label: i18n.T("settings.rules.trigger." + tr)})
				}
				if d.Trigger == core.TriggerLabel || d.Trigger == core.TriggerSecurity {
					first = kit.SelectWith(c, kit.SelectOpts{Flat: true, MinWidth: th.Space(36), MaxWidth: theme.SettingsControlWidth}, &d.Trigger, triggers).
						Label(i18n.T("settings.rules.trigger"))
				} else {
					first = settingSelect(c, i18n.T("settings.rules.trigger"), &d.Trigger, triggers)
				}
				if d.Trigger != was {
					d.TriggerArg = map[string]string{core.TriggerSecurity: core.SecuritySuspicious}[d.Trigger]
					if d.Trigger == core.TriggerLabel && len(labels) > 0 {
						d.TriggerArg = labels[0].Value
					}
				}
				switch d.Trigger {
				case core.TriggerLabel:
					kit.SelectWith(c, kit.SelectOpts{Flat: true, MinWidth: th.Space(28), MaxWidth: theme.SettingsControlWidth}, &d.TriggerArg, labels).
						Label(i18n.T("settings.rules.trigger.label.which"))
				case core.TriggerSecurity:
					kit.SelectWith(c, kit.SelectOpts{Flat: true, MinWidth: th.Space(28), MaxWidth: theme.SettingsControlWidth}, &d.TriggerArg, []kit.Option[string]{
						{Value: core.SecuritySuspicious, Label: i18n.T("settings.rules.security.suspicious")},
						{Value: core.SecurityDangerous, Label: i18n.T("settings.rules.security.dangerous")}}).
						Label(i18n.T("settings.rules.trigger.security.which"))
				}
			})
			return first
		})
	}, func() {
		settingRow(c, i18n.T("settings.rules.account"), "", func() ui.Element {
			accts := []kit.Option[int64]{{Value: 0, Label: i18n.T("settings.rules.account.all")}}
			for _, acc := range a.accounts {
				accts = append(accts, kit.Option[int64]{Value: acc.ID, Label: accountName(acc)})
			}
			return settingSelect(c, i18n.T("settings.rules.account"), &d.Account, accts)
		})
	})
}

// condGrouped reports whether d's conditions are condition groups.
func condGrouped(conds []core.RuleCondition) bool {
	return len(conds) > 0 && conds[0].Group != nil
}

// tidyConds puts a last group's conditions back in the list, and a lone
// condition back to the default match, which it no longer shows.
func tidyConds(conds *[]core.RuleCondition, match *string) {
	if len(*conds) == 1 && condGrouped(*conds) {
		*match, *conds = (*conds)[0].Match, (*conds)[0].Group
	}
	if !condGrouped(*conds) && len(*conds) < 2 {
		*match = core.MatchAll
	}
}

// condGroupRows is how many conditions addCondGroup adds.
const condGroupRows = 2

// addCondGroup adds a group of two empty conditions, matching any: mixing and
// with or is why there is a group. Conditions so far become the first group.
func addCondGroup(conds *[]core.RuleCondition, match *string) {
	two := []core.RuleCondition{{Field: core.FieldFrom, Op: core.OpContains}, {Field: core.FieldFrom, Op: core.OpContains}}
	switch {
	case len(*conds) == 0:
		*conds, *match = two, core.MatchAny
	case condGrouped(*conds):
		*conds = append(*conds, core.RuleCondition{Match: core.MatchAny, Group: two})
	default:
		*conds = []core.RuleCondition{{Match: *match, Group: *conds}, {Match: core.MatchAny, Group: two}}
		*match = core.MatchAll
	}
}

// flipMatch switches a match between all and any.
func flipMatch(m *string) {
	if *m == core.MatchAny {
		*m = core.MatchAll
	} else {
		*m = core.MatchAny
	}
}

// condEditorOpts configures the shared condition group editor for Rules,
// Saved Views, and Saved View conversation groups (inline mode).
type condEditorOpts struct {
	title       string
	emptyText   string
	footnote    string
	inline      bool
	defaultCond func() core.RuleCondition
	row         func(*core.RuleCondition)
}

// condGroupConnector draws the centered ─── 且 ▾ ─── / ─── 或 ▾ ─── pill joining
// two condition groups; clicking flips between MatchAll and MatchAny.
func condGroupConnector(c *ui.Context, match *string) {
	th := c.Theme()
	word := i18n.T("settings.rules.logic." + *match)
	ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(th.Space(2.5)).Padding(th.Space(0.5), th.Space(4)).Children(func() {
		kit.Separator(c, false).Grow(1).Shrink(1).Basis(0)
		kit.Button(c, word, kit.ButtonOpts{Variant: kit.Outline, Flat: true, Round: true, Size: kit.SizeXs, Trailing: "chevron-down"}).
			Label(i18n.Tf("settings.rules.logic.flip", word)).Shrink(0).OnClick(func() { flipMatch(match) })
		kit.Separator(c, false).Grow(1).Shrink(1).Basis(0)
	})
}

// condMatchButton draws the compact 满足全部条件 ▾ / 满足任一条件 ▾ toggle in a
// group header; clicking flips the group's match mode.
func condMatchButton(c *ui.Context, match *string) {
	label := i18n.T("settings.rules.match." + *match)
	kit.Button(c, label, kit.ButtonOpts{Variant: kit.Outline, Flat: true, Size: kit.SizeXs, Trailing: "chevron-down"}).
		Label(i18n.Tf("settings.rules.logic.flip", label)).Shrink(0).OnClick(func() { flipMatch(match) })
}

// condEditor renders the unified default-1-group condition editor shared by
// Rules and Saved Views: a single card by default (showing 满足全部条件 ▾ once
// it has 2+ rows, and + 添加条件 inside), + 添加条件组 in the section header,
// and independent cards joined by ─── 且 ▾ ─── once 2+ groups exist.
func condEditor(c *ui.Context, conds *[]core.RuleCondition, match *string, o condEditorOpts) {
	t, th := theme.Of(c), c.Theme()
	count := len(core.RuleDoc{Conditions: *conds}.Rows())
	atLimit := count >= core.MaxRuleConditions
	noGroupRoom := count+condGroupRows > core.MaxRuleConditions

	nextCond := func(list []core.RuleCondition) core.RuleCondition {
		if len(list) == 0 && o.defaultCond != nil {
			return o.defaultCond()
		}
		add := core.RuleCondition{Field: core.FieldSubject, Op: core.OpContains}
		if len(list) > 0 {
			if f := list[len(list)-1].Field; textField(f) {
				add.Field = f // 发件人 a 或 发件人 b, most often
			}
		}
		return add
	}

	groupBody := func(list *[]core.RuleCondition, on *string, isMulti bool, removeRow func(int), removeGroup func(), inlineAddGroup bool) {
		showHeader := isMulti || len(*list) >= 2
		if showHeader {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(th.Space(2)).Padding(th.Space(2.5), th.Space(4), th.Space(1), th.Space(4)).Children(func() {
				condMatchButton(c, on)
				ui.Spacer(c)
				if isMulti && removeGroup != nil {
					kit.Button(c, i18n.T("settings.rules.cond_group.remove"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeXs}).
						TextColor(t.Muted).Shrink(0).OnClick(removeGroup)
				}
			})
		}
		topPad := th.Space(3)
		if showHeader {
			topPad = th.Space(1.5)
		}
		ui.Column(c).FillWidth().Gap(th.Space(2)).Padding(topPad, th.Space(4), th.Space(1), th.Space(4)).Children(func() {
			for i := range *list {
				ui.Row(c.Key(i)).FillWidth().Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
					o.row(&(*list)[i])
					kit.IconButton(c, "x", i18n.T("settings.rules.condition.remove"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeXs}).
						Shrink(0).OnClick(func() { removeRow(i) })
				})
			}
		})
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(th.Space(2)).Padding(th.Space(1), th.Space(4), th.Space(2.5), th.Space(3)).Children(func() {
			kit.Button(c, i18n.T("settings.rules.condition.add"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeXs, Icon: "plus", Disabled: atLimit}).
				TextColor(t.Muted).OnClick(func() { *list = append(*list, nextCond(*list)) })
			if inlineAddGroup {
				ui.Spacer(c)
				kit.Button(c, i18n.T("settings.rules.cond_group.add"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeXs, Icon: "plus", Disabled: noGroupRoom}).
					TextColor(t.Muted).OnClick(func() { addCondGroup(conds, match) })
			}
		})
	}

	emptyRow := func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(th.Space(3)).Padding(th.Space(3), th.Space(4)).Children(func() {
			if o.emptyText != "" {
				kit.Text(c, o.emptyText, t.Text.Xs).TextColor(t.Muted).Grow(1).Shrink(1)
			} else {
				ui.Spacer(c)
			}
			kit.Button(c, i18n.T("settings.rules.condition.add"), kit.ButtonOpts{Variant: kit.Outline, Flat: true, Size: kit.SizeSm, Icon: "plus", Disabled: atLimit}).
				Shrink(0).OnClick(func() { *conds = append(*conds, nextCond(*conds)) })
		})
	}

	if o.inline {
		ui.Column(c).FillWidth().Children(func() {
			switch {
			case len(*conds) == 0:
				emptyRow()
			case !condGrouped(*conds):
				groupBody(conds, match, false, func(i int) {
					*conds = slices.Delete(*conds, i, i+1)
					tidyConds(conds, match)
				}, nil, true)
			default:
				for g := range *conds {
					if g > 0 {
						condGroupConnector(c.Key(-1-g), match)
					}
					cg := &(*conds)[g]
					ui.Column(c.Key(g)).FillWidth().Label(i18n.T("settings.rules.cond_group")).Children(func() {
						groupBody(&cg.Group, &cg.Match, true, func(i int) {
							cg.Group = slices.Delete(cg.Group, i, i+1)
							if len(cg.Group) == 0 {
								*conds = slices.Delete(*conds, g, g+1)
							}
							tidyConds(conds, match)
						}, func() {
							*conds = slices.Delete(*conds, g, g+1)
							tidyConds(conds, match)
						}, g == len(*conds)-1)
					})
				}
			}
		})
		return
	}

	ui.Column(c).FillWidth().Gap(theme.SectionGap).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).PaddingX(th.Space(4)).Children(func() {
			kit.Text(c, o.title, t.Text.Sm).FontWeight(500).Grow(1)
			if count > 0 {
				kit.Button(c, i18n.T("settings.rules.cond_group.add"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeXs, Icon: "plus", Disabled: noGroupRoom}).
					TextColor(t.Muted).Shrink(0).OnClick(func() { addCondGroup(conds, match) })
			}
		})
		switch {
		case len(*conds) == 0:
			settingsCard(c, emptyRow)
		case !condGrouped(*conds):
			settingsCard(c, func() {
				groupBody(conds, match, false, func(i int) {
					*conds = slices.Delete(*conds, i, i+1)
					tidyConds(conds, match)
				}, nil, false)
			})
		default:
			for g := range *conds {
				if g > 0 {
					condGroupConnector(c.Key(-1-g), match)
				}
				cg := &(*conds)[g]
				ui.Column(c.Key(g)).FillWidth().Label(i18n.T("settings.rules.cond_group")).Children(func() {
					settingsCard(c, func() {
						groupBody(&cg.Group, &cg.Match, true, func(i int) {
							cg.Group = slices.Delete(cg.Group, i, i+1)
							if len(cg.Group) == 0 {
								*conds = slices.Delete(*conds, g, g+1)
							}
							tidyConds(conds, match)
						}, func() {
							*conds = slices.Delete(*conds, g, g+1)
							tidyConds(conds, match)
						}, false)
					})
				})
			}
		}
		if o.footnote != "" {
			kit.Text(c, o.footnote, t.Text.Xs).TextColor(t.Muted).PaddingX(th.Space(4))
		}
	})
}

// ifSection is 如果: the rule's conditions, using the shared condition editor.
func (v *rulesView) ifSection(c *ui.Context, labels []kit.Option[string]) {
	t := theme.Of(c)
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
	row := func(cd *core.RuleCondition) {
		th := c.Theme()
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
			kit.Text(c, i18n.T("settings.rules.op.older_than"), t.Text.Xs)
			kit.Input(c, &cd.Value, kit.InputOpts{Placeholder: "30"}).Label(i18n.T("settings.rules.days")).Width(th.Space(16))
			kit.Text(c, i18n.T("settings.rules.days_ago"), t.Text.Xs)
			ui.Row(c).Grow(1)
		default:
			ruleSelect(c, i18n.T("settings.rules.op"), &cd.Op, ops, theme.RuleOpWidth)
			ph := "settings.rules.value.text"
			if cd.Field == core.FieldFrom || cd.Field == core.FieldTo {
				ph = "settings.rules.value.addr"
			}
			kit.Input(c, &cd.Value, kit.InputOpts{Placeholder: i18n.T(ph)}).Label(i18n.T("settings.rules.value")).Grow(1).Shrink(1).Basis(0).MinWidth(0)
		}
	}
	condEditor(c, &d.Conditions, &d.Match, condEditorOpts{
		title:     i18n.T("settings.rules.if"),
		emptyText: i18n.T("settings.rules.no_conditions"),
		row:       row,
	})
}

// ruleSelect is a select in a rule card's row: w wide (a longer language
// may widen it a little), or filling what the row leaves when w is 0.
func ruleSelect[T comparable](c *ui.Context, label string, sel *T, opts []kit.Option[T], w float32) ui.Element {
	o := kit.SelectOpts{Flat: true, MinWidth: w, MaxWidth: w * 1.5}
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

// ruleActionDrag is an action's index in Doc.Actions while its grip drags it.
type ruleActionDrag int

// thenSection is 就: its header with + 添加动作, and a single card of
// draggable action rows separated by inset hairlines.
func (v *rulesView) thenSection(c *ui.Context, labels []kit.Option[string]) {
	t, th := theme.Of(c), c.Theme()
	d := &v.edit.Doc
	ui.Column(c).FillWidth().Gap(theme.SectionGap).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).PaddingX(th.Space(4)).Children(func() {
			kit.Text(c, i18n.T("settings.rules.then"), t.Text.Sm).FontWeight(500).Grow(1)
			v.addActionButton(c)
		})
		rows := make([]func(), len(d.Actions))
		for i := range d.Actions {
			idx := i
			rows[idx] = func() { v.actionRow(c.Key(idx), idx, labels) }
		}
		settingsCard(c, rows...)
	})
}

// actionRow is action i inside the 就 card: drag grip on the left, kind select
// and target control in the middle, × remove on the right, and optional sub-block
// (forward warning, local file export options, or AI note) aligned beneath the select.
func (v *rulesView) actionRow(c *ui.Context, i int, labels []kit.Option[string]) {
	t, th := theme.Of(c), c.Theme()
	d := &v.edit.Doc
	ac := &d.Actions[i]
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
	hasSub := ac.Kind == core.ActionForward || fileAction(ac.Kind) || slices.Contains(core.RuleAIActions, ac.Kind)
	item := ui.Column(c).FillWidth()
	if from, ok := ui.DragOver[ruleActionDrag](item); ok && int(from) != i {
		drawDropLine(item, int(from) < i, th.Accent)
	}
	if from, ok := ui.Drop[ruleActionDrag](item); ok && int(from) != i && int(from) >= 0 && int(from) < len(d.Actions) {
		f := int(from)
		moved := d.Actions[f]
		next := slices.Delete(slices.Clone(d.Actions), f, f+1)
		d.Actions = slices.Insert(next, i, moved)
	}
	item.Children(func() {
		pb := th.Space(2.5)
		if hasSub {
			pb = th.Space(1.5)
		}
		ui.Row(c).FillWidth().Gap(th.Space(2)).Padding(th.Space(2.5), th.Space(4), pb, th.Space(2.5)).AlignItems(ui.Center).Children(func() {
			grip := ui.Box(c).Size(th.Space(5), th.Space(8)).Center().Shrink(0).Drag(ruleActionDrag(i))
			if grip.Dragging() {
				item.Opacity(0.4)
			}
			grip.Children(func() {
				ui.Icon(c, icons.Get("grip")).Size(theme.Icon, theme.Icon).TextColor(t.Idle)
			})
			ruleSelect(c, i18n.Tf("settings.rules.action.n", i+1), &ac.Kind, kinds, th.Space(44))
			switch {
			case ac.Kind == core.ActionLabel || ac.Kind == core.ActionUnlabel:
				ac.To, ac.Dir, ac.Subfolder, ac.Naming = "", "", false, ""
				if ac.Label == "" {
					ac.Label = v.firstLabel()
				}
				ruleSelect(c, i18n.T("settings.rules.label"), &ac.Label, labels, 0)
			case ac.Kind == core.ActionForward:
				ac.Label, ac.Dir, ac.Subfolder, ac.Naming = "", "", false, ""
				kit.Input(c, &ac.To, kit.InputOpts{Placeholder: i18n.T("settings.rules.forward.to")}).
					Label(i18n.T("settings.rules.forward.to")).Grow(1).Shrink(1).Basis(0).MinWidth(0)
			case fileAction(ac.Kind):
				ac.Label, ac.To = "", ""
				fileKindChanged(d, i)
				v.fileDir(c, d, i)
			default:
				ac.Label, ac.To, ac.Dir, ac.Subfolder, ac.Naming = "", "", "", false, ""
				ui.Spacer(c)
			}
			// the grip is the pointer's; these move an action from the keyboard and screen readers
			kit.IconButton(c, "arrow-up", i18n.T("settings.rules.action.up"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeXs, Disabled: i == 0}).
				Shrink(0).OnClick(func() { d.Actions[i-1], d.Actions[i] = d.Actions[i], d.Actions[i-1] })
			kit.IconButton(c, "arrow-down", i18n.T("settings.rules.action.down"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeXs, Disabled: i == len(d.Actions)-1}).
				Shrink(0).OnClick(func() { d.Actions[i], d.Actions[i+1] = d.Actions[i+1], d.Actions[i] })
			kit.IconButton(c, "x", i18n.T("settings.rules.action.remove"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeXs, Disabled: len(d.Actions) == 1}).
				Shrink(0).OnClick(func() { d.Actions = slices.Delete(d.Actions, i, i+1) })
		})
		if !hasSub {
			return
		}
		ui.Column(c).FillWidth().Gap(th.Space(2.5)).Padding(0, th.Space(4), th.Space(3), th.Space(9.5)).Children(func() {
			switch {
			case ac.Kind == core.ActionForward:
				note := "settings.rules.forward.note"
				if k := mail.AddrKey(ac.To); k != "" && slices.Contains(v.edit.Confirmed, k) {
					note = "settings.rules.forward.confirmed"
				}
				ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(th.Space(1.5)).Children(func() {
					kit.BadgeWith(c, i18n.T("settings.rules.egress"), kit.BadgeOpts{Tone: kit.BadgeWarning}).Shrink(0)
					kit.Text(c, i18n.T(note), t.Text.Xs).TextColor(t.WarningText).Grow(1).Shrink(1)
				})
			case fileAction(ac.Kind):
				ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(th.Space(1.5)).Children(func() {
					kit.BadgeWith(c, i18n.T("settings.rules.kind.local_write"), kit.BadgeOpts{Tone: kit.BadgeNeutral, Muted: true}).Shrink(0)
					kit.Text(c, i18n.T("settings.rules.files.note"), t.Text.Xs).TextColor(t.Muted).Grow(1).Shrink(1)
				})
				v.fileOptions(c, d, i)
			case slices.Contains(core.RuleAIActions, ac.Kind):
				violet := t.Label("violet")
				noteKey := "settings.rules.ai.note"
				if v.aiOff(ac.Kind) {
					noteKey = "settings.rules.ai_off.hint"
				}
				ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(th.Space(1.5)).Children(func() {
					ruleTag(c, i18n.T("settings.rules.kind.ai"), violet.Fill, violet.Text, "")
					kit.Text(c, i18n.T(noteKey), t.Text.Xs).TextColor(t.Muted).Grow(1).Shrink(1)
				})
			}
		})
	})
}

// addActionButton is + 添加动作 in the 就 header and its grouped action menu.
func (v *rulesView) addActionButton(c *ui.Context) {
	t := theme.Of(c)
	d := &v.edit.Doc
	add := kit.Button(c, i18n.T("settings.rules.action.add"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeXs, Icon: "plus", Disabled: len(d.Actions) >= core.MaxRuleActions}).
		TextColor(t.Muted).Shrink(0).OnClick(func() { v.menu = !v.menu })
	if !v.menu {
		return
	}
	kit.MenuAt(c, add, &v.menu, ui.AnchorBottomRight, ui.AnchorTopRight, theme.PopupOffset, i18n.T("settings.rules.action.add"), func() {
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
						kit.Text(c, i18n.T("settings.rules.ai_off.hint"), t.Text.Xs).TextColor(t.Muted)
					}
				}).OnClick(func() {
					v.menu = false
					d.Actions = append(d.Actions, core.RuleAction{Kind: k})
				})
			}
		}
	})
}

// preview is the borderless right pane: the existing mail the rule matches,
// matching Saved Views' live preview layout and Mail/Row alignment.
func (v *rulesView) preview(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	ui.Column(c).Grow(1).FillWidth().Padding(th.Space(8), th.Space(6), th.Space(6), th.Space(2)).Gap(th.Space(2)).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Baseline).Gap(th.Space(2)).PaddingX(th.Space(2)).Children(func() {
			kit.Text(c, i18n.T("settings.rules.preview"), t.Text.Sm).FontWeight(500).Grow(1)
			kit.Text(c, i18n.Tf("settings.rules.preview.recent", core.RulePreviewDays), t.Text.Xs).TextColor(t.Muted)
		})
		p := v.prev
		muted := func(key string) {
			kit.Block(c, ui.Column(c), kit.ToneOutline).FillWidth().Margin(0, th.Space(2)).Padding(th.Space(4), th.Space(3)).
				AlignItems(ui.Center).Children(func() { kit.Text(c, i18n.T(key), t.Text.Xs).TextColor(t.Muted).TextAlign(ui.Center) })
		}
		switch {
		case p == nil && (v.prevStop != nil || v.prevCancel != nil):
			muted("settings.rules.preview.wait")
		case p == nil:
			muted("settings.rules.preview.incomplete")
		case p.Count == 0:
			muted("settings.rules.preview.none")
		default:
			kit.Text(c, i18n.Tf("settings.rules.preview.count", p.Count), t.Text.Xs).TextColor(t.Muted).PaddingX(th.Space(2))
			now := v.a.clock.Now()
			ui.Scroll(c).Grow(1).Shrink(1).FillWidth().Children(func() {
				ui.Column(c).FillWidth().Gap(th.Space(0.25)).Children(func() {
					for _, m := range p.Messages {
						who := displayName(m.From)
						ui.Row(c.Key(m.ID)).FillWidth().Gap(th.Space(2.5)).Padding(th.Space(2.5), th.Space(2)).AlignItems(ui.Start).Children(func() {
							kit.Avatar(c, who).Label("").Role(ui.RoleNone).Shrink(0)
							ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(th.Space(0.5)).Children(func() {
								ui.Row(c).FillWidth().Gap(th.Space(2)).AlignItems(ui.Baseline).Children(func() {
									kit.Text(c, who, t.Text.Sm).FontWeight(500).TextColor(t.Subdued).SingleLine().Grow(1).Shrink(1)
									kit.Text(c, listTime(now, m.Date), t.Text.Xs).TextColor(t.Muted).Shrink(0)
								})
								kit.Text(c, m.Subject, t.Text.Xs).TextColor(t.Subdued).SingleLine()
							})
						})
					}
				})
			})
		}
		if v.menu && !v.a.settings.RuleAI {
			violet := t.Label("violet")
			ruleNote(c, "sparkles", i18n.T("settings.rules.ai_off.note"), violet.Fill.Mix(t.Background, 0.5), violet.Edge, violet.Text)
		}
	})
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
	syncFollowers(&v.edit.Doc)
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
		kit.Block(c, ui.Column(c), kit.ToneOutline).Gap(th.Space(1)).Padding(th.Space(3)).Children(func() {
			if len(now) == 0 {
				v.runExisting = false
				kit.Text(c, i18n.T("settings.rules.save.existing.nothing"), t.Text.Xs).TextColor(t.Muted)
			} else {
				kit.Checkbox(c, &v.runExisting, i18n.Tf("settings.rules.save.existing", count))
				kit.Text(c, i18n.Tf("settings.rules.save.existing.only", strings.Join(now, sep)), t.Text.Xs).TextColor(t.Muted).PaddingX(th.Space(6))
			}
			if len(later) > 0 {
				kit.Text(c, i18n.Tf("settings.rules.save.existing.later", strings.Join(later, sep)), t.Text.Xs).TextColor(t.Muted).PaddingX(th.Space(6))
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
		for _, to := range v.unconfirmed { // the design's warning, one per address, its first time said under it
			first := ""
			if slices.ContainsFunc(v.firstTime, func(f string) bool { return mail.AddrKey(f) == mail.AddrKey(to) }) {
				first = i18n.T("settings.rules.confirm.first")
			}
			kit.TitledAlert(c.Key(to), kit.ToneWarning, i18n.Tf("settings.rules.confirm.to", to), first, nil)
		}
		ui.Column(c).Gap(th.Space(1.5)).Children(func() {
			for _, s := range []string{outbox, i18n.T("settings.rules.confirm.new_only"), i18n.T("settings.rules.confirm.again")} {
				kit.Text(c, "· "+s, t.Text.Sm)
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
