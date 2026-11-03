package app

import (
	"context"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// rulesView is Settings › Rules: the rules in the order they run, each with
// its switch, and the editor in place of the list while one is edited.
type rulesView struct {
	a     *App
	rules []core.Rule

	editing bool
	edit    core.Rule // the rule in the editor; ID 0 a new one
	any     int       // the editor's match: 0 all, 1 any
}

func (v *rulesView) load() {
	a := v.a
	var rules []core.Rule
	a.query(func(ctx context.Context) (err error) { rules, err = a.svc.Rules(ctx); return err },
		func(err error) {
			if err == nil {
				v.rules = rules
			}
		})
}

func (v *rulesView) view(c *ui.Context) {
	if v.editing {
		v.editor(c)
		return
	}
	pageHeading(c, i18n.T("settings.rules"), i18n.T("settings.rules.sub"), func() {
		if len(v.rules) > 0 {
			settingButton(c, i18n.T("settings.rules.new"), kit.Outline, false).OnClick(v.create)
		}
	})
	if len(v.rules) == 0 {
		kit.Empty(c, kit.EmptyOpts{Icon: "filter", Title: i18n.T("settings.rules.empty"), Desc: i18n.T("settings.rules.empty.desc"), Dashed: true,
			Actions: func() { kit.EmptyAction(c, i18n.T("settings.rules.new"), kit.ButtonOpts{}).OnClick(v.create) }})
		return
	}
	rows := make([]func(), len(v.rules))
	for i, r := range v.rules {
		rows[i] = func() { v.row(c.Key(r.ID), r) }
	}
	settingsSection(c, i18n.T("settings.rules.list"), i18n.T("settings.rules.list.foot"), rows...)
}

// row is a rule: its switch, name and summary, edit.
func (v *rulesView) row(c *ui.Context, r core.Rule) {
	a := v.a
	t, th := theme.Of(c), c.Theme()
	name := ruleName(r.Doc)
	ui.Row(c).FillWidth().Gap(th.Space(3)).Padding(th.Space(2), th.Space(4)).MinHeight(th.Space(12)).AlignItems(ui.Center).Children(func() {
		on := r.Enabled
		kit.Switch(c, &on).Label(i18n.Tf("settings.rules.enable", name))
		if on != r.Enabled {
			id := r.ID
			v.rules = slices.Clone(v.rules)
			v.rules[slices.IndexFunc(v.rules, func(x core.Rule) bool { return x.ID == id })].Enabled = on // at once; load reads them again
			a.writes.Chain("rules", func(ctx context.Context) error { return a.svc.SetRuleEnabled(ctx, id, on) }, v.saved)
		}
		ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(0.5)).Children(func() {
			kit.Text(c, name, t.Text.Body).FontWeight(500).SingleLine()
			kit.Text(c, v.summary(r.Doc), t.Text.Sm).TextColor(t.Muted).SingleLine()
		})
		kit.IconButton(c, "file-pen", i18n.Tf("settings.rules.edit", name), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).
			OnClick(func() { v.open(r) })
	})
}

func ruleName(d core.RuleDoc) string {
	if d.Name != "" {
		return d.Name
	}
	return i18n.T("settings.rules.untitled")
}

// summary is the rule in a line: trigger · conditions → actions.
func (v *rulesView) summary(d core.RuleDoc) string {
	conds := ""
	for i, cd := range d.Conditions {
		s := i18n.Tf("settings.rules.cond", i18n.T("settings.rules.field."+cd.Field), i18n.T("settings.rules.op."+cd.Op), cd.Value)
		if i > 0 {
			s = i18n.Tf("settings.rules.join."+d.Match, conds, s)
		}
		conds = s
	}
	acts := make([]string, len(d.Actions))
	for i, ac := range d.Actions {
		acts[i] = i18n.Tf("settings.rules.action.label.summary", v.labelName(ac.Label))
	}
	trigger, then := i18n.T("settings.rules.trigger."+d.Trigger), strings.Join(acts, " → ")
	if conds == "" {
		return i18n.Tf("settings.rules.summary.always", trigger, then)
	}
	return i18n.Tf("settings.rules.summary", trigger, conds, then)
}

// labelName is the label's name for its slug; the slug when it is gone.
func (v *rulesView) labelName(slug string) string {
	if i := slices.IndexFunc(v.a.labels, func(l core.Label) bool { return l.Slug == slug }); i >= 0 {
		return v.a.labels[i].Name
	}
	return slug
}

// create opens the editor on a new rule: a sender condition and the first label.
func (v *rulesView) create() {
	label := ""
	if len(v.a.labels) > 0 {
		label = v.a.labels[0].Slug
	}
	v.open(core.Rule{Enabled: true, Doc: core.RuleDoc{Version: core.RuleVersion, Trigger: core.TriggerReceive, Match: core.MatchAll,
		Conditions: []core.RuleCondition{{Field: core.FieldFrom, Op: core.OpContains}},
		Actions:    []core.RuleAction{{Kind: core.ActionLabel, Label: label}}}})
}

func (v *rulesView) open(r core.Rule) {
	r.Doc.Conditions = slices.Clone(r.Doc.Conditions)
	r.Doc.Actions = slices.Clone(r.Doc.Actions)
	v.editing, v.edit, v.any = true, r, 0
	if r.Doc.Match == core.MatchAny {
		v.any = 1
	}
}

// editor is the rule's name, then when, if and then, each a section.
func (v *rulesView) editor(c *ui.Context) {
	a := v.a
	t, th := theme.Of(c), c.Theme()
	d := &v.edit.Doc
	title, sub := i18n.T("settings.rules.new"), i18n.T("settings.rules.editor.sub")
	if v.edit.ID != 0 {
		title, sub = ruleName(*d), v.summary(*d)
	}
	ready := len(d.Actions) > 0 && d.Actions[0].Label != "" && !slices.ContainsFunc(d.Conditions, func(cd core.RuleCondition) bool { return strings.TrimSpace(cd.Value) == "" })
	pageHeading(c, title, sub, func() {
		ui.Row(c).Gap(th.Space(2)).Children(func() {
			settingButton(c, i18n.T("common.cancel"), kit.Outline, false).OnClick(func() { v.editing = false })
			settingButton(c, i18n.T("common.save"), kit.Default, !ready).OnClick(v.save)
		})
	})
	settingsSection(c, i18n.T("settings.rules.name"), "", func() {
		settingBlock(c, func() {
			kit.Input(c, &d.Name, kit.InputOpts{Placeholder: i18n.T("settings.rules.name.placeholder")}).Label(i18n.T("settings.rules.name"))
		})
	})
	settingsSection(c, i18n.T("settings.rules.when"), "", func() {
		settingRow(c, i18n.T("settings.rules.trigger"), "", func() {
			settingSelect(c, i18n.T("settings.rules.trigger"), &d.Trigger, []kit.Option[string]{{Value: core.TriggerReceive, Label: i18n.T("settings.rules.trigger.receive")}})
		})
	})
	conds := []func(){func() {
		settingRow(c, i18n.T("settings.rules.match"), "", func() {
			kit.Tabs(c, &v.any, i18n.T("settings.rules.match.all"), i18n.T("settings.rules.match.any"))
			d.Match = []string{core.MatchAll, core.MatchAny}[v.any]
		})
	}}
	fields := []kit.Option[string]{{Value: core.FieldFrom, Label: i18n.T("settings.rules.field.from")}, {Value: core.FieldSubject, Label: i18n.T("settings.rules.field.subject")}}
	ops := []kit.Option[string]{{Value: core.OpContains, Label: i18n.T("settings.rules.op.contains")},
		{Value: core.OpNotContains, Label: i18n.T("settings.rules.op.not_contains")}, {Value: core.OpIs, Label: i18n.T("settings.rules.op.is")}}
	for i := range d.Conditions {
		conds = append(conds, func() {
			settingBlock(c, func() {
				ui.Row(c.Key(i)).FillWidth().Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
					cd := &d.Conditions[i]
					settingSelect(c, i18n.T("settings.rules.field"), &cd.Field, fields)
					settingSelect(c, i18n.T("settings.rules.op"), &cd.Op, ops)
					kit.Input(c, &cd.Value, kit.InputOpts{Placeholder: i18n.T("settings.rules.value")}).Label(i18n.T("settings.rules.value")).Grow(1).Shrink(1)
					kit.IconButton(c, "x", i18n.T("settings.rules.condition.remove"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).
						OnClick(func() { d.Conditions = slices.Delete(d.Conditions, i, i+1) })
				})
			})
		})
	}
	conds = append(conds, func() {
		settingBlock(c, func() {
			if len(d.Conditions) == 0 {
				kit.Text(c, i18n.T("settings.rules.no_conditions"), t.Text.Sm).TextColor(t.Muted)
			}
			ui.Row(c).Children(func() {
				settingButton(c, i18n.T("settings.rules.condition.add"), kit.Outline, len(d.Conditions) >= core.MaxRuleConditions).
					OnClick(func() {
						d.Conditions = append(d.Conditions, core.RuleCondition{Field: core.FieldSubject, Op: core.OpContains})
					})
			})
		})
	})
	settingsSection(c, i18n.T("settings.rules.if"), "", conds...)
	labels := make([]kit.Option[string], len(a.labels))
	for i, l := range a.labels {
		labels[i] = kit.Option[string]{Value: l.Slug, Label: l.Name}
	}
	settingsSection(c, i18n.T("settings.rules.then"), "", func() {
		settingRow(c, i18n.T("settings.rules.action.label"), "", func() {
			settingSelect(c, i18n.T("settings.rules.label"), &d.Actions[0].Label, labels)
		})
	})
}

func (v *rulesView) save() {
	a := v.a
	r := v.edit
	v.editing = false
	a.writes.Chain("rules", func(ctx context.Context) error { _, err := a.svc.SaveRule(ctx, r); return err }, v.saved)
}

// saved shows a write's error and reads the rules again.
func (v *rulesView) saved(err error) {
	v.a.toastErr(err)
	v.load()
}
