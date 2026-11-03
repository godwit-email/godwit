package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// rulesView is Settings › Rules: the rules in the order they run, each with
// its switch and, expanded, its last week of hits; the editor in place of
// the list while one is edited.
type rulesView struct {
	a     *App
	rules []core.Rule

	open int64          // the rule whose hits show, 0 none
	hits []core.RuleHit // open's

	counts map[int64]int // each rule's hits of the last week

	editing bool
	edit    core.Rule // the rule in the editor; ID 0 a new one
	any     int       // the editor's match: 0 all, 1 any
	menu    bool      // the add-action menu is open

	// The confirmation of forward recipients not confirmed yet, before saving.
	confirming  bool
	unconfirmed []string
	firstTime   []string        // of unconfirmed, the ones no account has written to
	custom      map[string]bool // the folders whose naming is 自定义…, in the token field

	from    string               // the subject of the mail the editor was opened from
	suggest []core.RuleCondition // conditions offered for it

	pending *core.MessageSummary // the mail to open the editor from, once the page shows
	witRule *core.Rule           // Wit's proposal to open the editor on, once the page shows
	wit     bool                 // the editor holds Wit's proposal

	prevKey    string            // the trigger and conditions prev is for
	prev       *core.RulePreview // the existing mail they match; nil not known
	prevStop   func()            // the debounce before the next preview
	prevCancel func()            // the preview under way

	saveAsk     bool // the save dialog is open
	runExisting bool // its 同时处理现有的邮件
}

// ruleActionGroup is a group of the add-action menu.
type ruleActionGroup struct {
	key   string   // its title is settings.rules.group.<key>
	kinds []string // its actions, in order; none hides the group
}

// ruleActionGroups are the actions the editor offers, by group, in order.
// A new action registers by appending its kind to its group.
var ruleActionGroups = []ruleActionGroup{
	{"organize", []string{core.ActionLabel, core.ActionUnlabel, core.ActionRead, core.ActionStar,
		core.ActionArchive, core.ActionJunk, core.ActionTrash, core.ActionStop}},
	{"forward", []string{core.ActionForward}},
	{"files", []string{core.ActionPDF, core.ActionEML, core.ActionAttachments}},
	{"wit", core.RuleAIActions},
}

// ruleTemplates open the editor filled in; nothing is saved or turned on
// until the user saves. A label a template names that is gone becomes the first.
var ruleTemplates = []struct {
	key string // settings.rules.template.<key> and .<key>.desc
	doc core.RuleDoc
}{
	{"bill", core.RuleDoc{Trigger: core.TriggerReceive, Match: core.MatchAll,
		Conditions: []core.RuleCondition{{Field: core.FieldSubject, Op: core.OpContains, Value: "invoice"}, {Field: core.FieldAttachments, Op: core.OpIs, Value: core.RuleYes}},
		Actions:    []core.RuleAction{{Kind: core.ActionLabel, Label: "bill"}, {Kind: core.ActionArchive}}}},
	{"newsletter", core.RuleDoc{Trigger: core.TriggerLabel, TriggerArg: "newsletter", Match: core.MatchAll,
		Actions: []core.RuleAction{{Kind: core.ActionRead}, {Kind: core.ActionArchive}}}},
	{"suspicious", core.RuleDoc{Trigger: core.TriggerSecurity, TriggerArg: core.SecuritySuspicious, Match: core.MatchAll,
		Actions: []core.RuleAction{{Kind: core.ActionJunk}}}},
}

// ruleSuggestWords are the subject words create-from-mail offers to match on.
var ruleSuggestWords = []string{"invoice", "receipt", "order", "statement", "newsletter", "发票", "账单", "订单", "收据"}

// rulePreviewShown is how many matching messages the editor lists.
const rulePreviewShown = 6

// ruleRoles are the folders a folder condition offers, by role.
var ruleRoles = []string{"inbox", "archive", "junk", "trash"}

func (v *rulesView) load() {
	a := v.a
	var rules []core.Rule
	var counts map[int64]int
	a.query(func(ctx context.Context) (err error) {
		if rules, err = a.svc.Rules(ctx); err != nil {
			return err
		}
		counts, err = a.svc.RuleHitCounts(ctx)
		return err
	}, func(err error) {
		if err == nil {
			v.rules, v.counts = rules, counts
		}
	})
	if v.open != 0 {
		v.loadHits()
	}
}

func (v *rulesView) loadHits() {
	a, id := v.a, v.open
	var hits []core.RuleHit
	a.query(func(ctx context.Context) (err error) { hits, err = a.svc.RuleHits(ctx, id); return err },
		func(err error) {
			if err == nil && v.open == id {
				v.hits = hits
			}
		})
}

func (v *rulesView) view(c *ui.Context) {
	if v.editing {
		v.editor(c)
		return
	}
	a := v.a
	t, th := theme.Of(c), c.Theme()
	pageHeading(c, i18n.T("settings.rules"), i18n.T("settings.rules.sub"), func() {
		if len(v.rules) > 0 {
			settingButton(c, i18n.T("settings.rules.new"), kit.Outline, false).OnClick(v.create)
		}
	})
	if len(v.rules) == 0 {
		kit.Empty(c, kit.EmptyOpts{Icon: "filter", Title: i18n.T("settings.rules.empty"), Desc: i18n.T("settings.rules.empty.desc"), Dashed: true,
			Actions: func() { kit.EmptyAction(c, i18n.T("settings.rules.new"), kit.ButtonOpts{}).OnClick(v.create) }})
	} else {
		rows := make([]func(), len(v.rules))
		for i, r := range v.rules {
			rows[i] = func() { v.row(c.Key(r.ID), r) }
		}
		settingsSection(c, i18n.T("settings.rules.list"), i18n.T("settings.rules.list.foot"), rows...)
	}
	tpls := make([]func(), len(ruleTemplates))
	for i, tp := range ruleTemplates {
		tpls[i] = func() {
			name := i18n.T("settings.rules.template." + tp.key)
			settingRow(c.Key(tp.key), name, i18n.T("settings.rules.template."+tp.key+".desc"), func() {
				settingButton(c, i18n.T("settings.rules.template.use"), kit.Outline, false).Label(i18n.Tf("settings.rules.template.use.label", name)).
					OnClick(func() { v.fromTemplate(tp.doc, name) })
			})
		}
	}
	settingsSection(c, i18n.T("settings.rules.templates"), i18n.T("settings.rules.templates.foot"), tpls...)
	// Emptying Trash and Junk is not a rule: it stays with the account's mailboxes.
	if len(a.accounts) > 0 {
		id := a.accounts[0].ID
		kit.TextLink(c, i18n.T("settings.rules.empty_trash"), t.Muted, t.Text.Sm).PaddingX(th.Space(4)).
			OnClick(func() { a.navigate(Route{Page: PageSettings, Settings: accountRoute(id)}) })
	}
}

// row is a rule: its switch, name and summary, edit, and its hits to expand.
func (v *rulesView) row(c *ui.Context, r core.Rule) {
	a := v.a
	t, th := theme.Of(c), c.Theme()
	name := ruleName(r.Doc)
	open := v.open == r.ID
	ui.Column(c).FillWidth().Children(func() {
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
				ui.Row(c).Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
					kit.Text(c, name, t.Text.Body).FontWeight(500).SingleLine().Shrink(1)
					if r.Background {
						kit.Badge(c, i18n.T("settings.rules.run.background"))
					}
					if forwards(r.Doc) {
						kit.Badge(c, i18n.T("settings.rules.egress"))
					}
				})
				kit.Text(c, v.summary(r.Doc), t.Text.Sm).TextColor(t.Muted).SingleLine()
			})
			hits := i18n.T("settings.rules.off")
			if r.Enabled {
				hits = i18n.Tf("settings.rules.hits.count", v.counts[r.ID])
			}
			kit.Text(c, hits, t.Text.Sm).TextColor(t.Muted).SingleLine().Shrink(0)
			kit.IconButton(c, "file-pen", i18n.Tf("settings.rules.edit", name), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).
				OnClick(func() { v.openEditor(r) })
			chev, label := "chevron-right", i18n.T("settings.rules.hits.show")
			if open {
				chev, label = "chevron-down", i18n.T("settings.rules.hits.hide")
			}
			kit.IconButton(c, chev, label, kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).OnClick(func() {
				v.hits = nil
				if v.open = r.ID; open {
					v.open = 0
					return
				}
				v.loadHits()
			})
		})
		if open {
			v.hitList(c, r)
		}
	})
}

// hitList is the open rule's hits of the last week, newest first, in a box
// about two and a half rows tall that scrolls past them.
func (v *rulesView) hitList(c *ui.Context, r core.Rule) {
	t, th := theme.Of(c), c.Theme()
	ui.Column(c).FillWidth().Gap(th.Space(2)).Padding(0, th.Space(4), th.Space(3), th.Space(4)).Children(func() {
		if len(v.hits) == 0 {
			kit.Text(c, i18n.T("settings.rules.hits.none"), t.Text.Sm).TextColor(t.Muted)
			return
		}
		kit.Text(c, i18n.Tf("settings.rules.hits.head", len(v.hits)), t.Text.Sm).TextColor(t.Muted)
		ui.Column(c).FillWidth().MaxHeight(th.Space(42)).Radius(t.Radius.Lg).Border(theme.Hairline, t.Border).Clip().Children(func() {
			ui.Scroll(c).Shrink(1).Children(func() {
				now := time.Now()
				for i, h := range v.hits {
					if i > 0 {
						kit.Separator(c, false)
					}
					v.hit(c.Key(h.RunID), r, h, now)
				}
			})
		})
	})
}

// hit is a run: when, the message, each action's result, and undo or retry.
func (v *rulesView) hit(c *ui.Context, r core.Rule, h core.RuleHit, now time.Time) {
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).FillWidth().Gap(th.Space(3)).Padding(th.Space(2.5), th.Space(3)).Children(func() {
		kit.Text(c, listTime(now, h.At), t.Text.Sm).TextColor(t.Muted).Width(th.Space(22)).Shrink(0)
		ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(1.5)).Children(func() {
			ui.Row(c).Gap(th.Space(2)).Children(func() {
				if h.MessageID == 0 {
					kit.Text(c, i18n.T("settings.rules.hits.gone"), t.Text.Body).TextColor(t.Muted)
					return
				}
				kit.Text(c, displayName(h.From), t.Text.Body).FontWeight(500).Shrink(0)
				kit.Text(c, h.Subject, t.Text.Body).SingleLine().Shrink(1)
			})
			ui.Row(c).Wrap().Gap(th.Space(1.5)).Children(func() {
				for i, res := range h.Actions {
					kit.Badge(c, v.result(r, i, res))
				}
			})
		})
		if h.State == core.RuleRunUndone {
			kit.Text(c, i18n.T("settings.rules.hits.undone"), t.Text.Sm).TextColor(t.Muted).Shrink(0)
			return
		}
		if slices.ContainsFunc(h.Actions, func(x core.RuleActionResult) bool {
			return x.State == core.RuleActionDone && x.Kind != core.ActionStop && x.Kind != core.ActionForward && !fileAction(x.Kind) // a forward has gone, files written stay
		}) {
			kit.Button(c, i18n.T("settings.rules.hits.undo"), kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeSm, Icon: "undo-2"}).Shrink(0).
				OnClick(func() { v.undo(h.RunID) })
		}
		if h.State == core.RuleRunFailed && h.MessageID != 0 {
			kit.Button(c, i18n.T("common.retry"), kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeSm}).Shrink(0).
				OnClick(func() { v.retry(h.RunID) })
		}
	})
}

// result is an action's outcome in a hit, named as the rule names it.
func (v *rulesView) result(r core.Rule, i int, res core.RuleActionResult) string {
	ac := core.RuleAction{Kind: res.Kind}
	if i < len(r.Doc.Actions) && r.Doc.Actions[i].Kind == res.Kind {
		ac = r.Doc.Actions[i]
	}
	s := v.actionText(ac)
	switch res.State {
	case core.RuleActionFailed:
		return i18n.Tf("settings.rules.result.failed", s, i18n.T("settings.rules.failure."+res.Failure))
	case core.RuleActionNotRun, core.RuleActionUndone, core.RuleActionKept, core.RuleActionSkipped:
		return i18n.Tf("settings.rules.result."+res.State, s)
	}
	return s
}

func (v *rulesView) undo(runID int64) {
	a := v.a
	var kept int
	a.do(func(ctx context.Context) (err error) { kept, err = a.svc.UndoRuleRun(ctx, runID); return err }, func(err error) {
		a.toastErr(err)
		if err == nil && kept > 0 {
			a.toast(i18n.Tf("settings.rules.hits.kept", kept))
		}
		v.loadHits()
	})
}

func (v *rulesView) retry(runID int64) {
	a := v.a
	a.do(func(ctx context.Context) error { return a.svc.RetryRuleRun(ctx, runID) }, func(err error) {
		a.toastErr(err)
		v.loadHits()
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
		s := v.condText(cd)
		if i > 0 {
			s = i18n.Tf("settings.rules.join."+d.Match, conds, s)
		}
		conds = s
	}
	acts := make([]string, len(d.Actions))
	for i, ac := range d.Actions {
		acts[i] = v.actionText(ac)
	}
	trigger, then := i18n.T("settings.rules.trigger."+d.Trigger), strings.Join(acts, " → ")
	switch d.Trigger {
	case core.TriggerLabel:
		trigger = i18n.Tf("settings.rules.trigger.label.summary", v.labelName(d.TriggerArg))
	case core.TriggerSecurity:
		trigger = i18n.Tf("settings.rules.trigger.security.summary", i18n.T("settings.rules.security."+d.TriggerArg))
	}
	if conds == "" {
		return i18n.Tf("settings.rules.summary.always", trigger, then)
	}
	return i18n.Tf("settings.rules.summary", trigger, conds, then)
}

func (v *rulesView) condText(cd core.RuleCondition) string {
	val := cd.Value
	switch cd.Field {
	case core.FieldAttachments:
		return i18n.T("settings.rules.attachments." + cd.Value)
	case core.FieldLabel:
		val = v.labelName(cd.Value)
	case core.FieldFolder:
		if slices.Contains(ruleRoles, cd.Value) {
			val = i18n.T("mail.folder." + cd.Value)
		}
	}
	return i18n.Tf("settings.rules.cond", i18n.T("settings.rules.field."+cd.Field), i18n.T("settings.rules.op."+cd.Op), val)
}

func (v *rulesView) actionText(ac core.RuleAction) string {
	if ac.Kind == core.ActionLabel || ac.Kind == core.ActionUnlabel {
		return i18n.Tf("settings.rules.action."+ac.Kind+".summary", v.labelName(ac.Label))
	}
	if ac.Kind == core.ActionForward && ac.To != "" {
		return i18n.Tf("settings.rules.action.forward.summary", ac.To)
	}
	if fileAction(ac.Kind) && ac.Dir != "" {
		return i18n.Tf("settings.rules.action."+ac.Kind+".summary", filepath.Base(ac.Dir))
	}
	return i18n.T("settings.rules.action." + ac.Kind)
}

// labelName is the label's name for its slug; the slug when it is gone.
func (v *rulesView) labelName(slug string) string {
	if i := slices.IndexFunc(v.a.labels, func(l core.Label) bool { return l.Slug == slug }); i >= 0 {
		return v.a.labels[i].Name
	}
	return slug
}

func (v *rulesView) firstLabel() string {
	if len(v.a.labels) > 0 {
		return v.a.labels[0].Slug
	}
	return ""
}

// create opens the editor on a new rule: a sender condition and the first label.
func (v *rulesView) create() {
	v.openEditor(core.Rule{Enabled: true, Doc: core.RuleDoc{Version: core.RuleVersion, Trigger: core.TriggerReceive, Match: core.MatchAll,
		Conditions: []core.RuleCondition{{Field: core.FieldFrom, Op: core.OpContains}},
		Actions:    []core.RuleAction{{Kind: core.ActionLabel, Label: v.firstLabel()}}}})
}

func (v *rulesView) openEditor(r core.Rule) {
	r.Doc.Conditions = slices.Clone(r.Doc.Conditions)
	r.Doc.Actions = slices.Clone(r.Doc.Actions)
	r.Confirmed = slices.Clone(r.Confirmed)
	v.closeEditor()
	v.editing, v.edit, v.any, v.custom = true, r, 0, nil
	if r.Doc.Match == core.MatchAny {
		v.any = 1
	}
}

// closeEditor leaves the editor, stopping its preview.
func (v *rulesView) closeEditor() {
	v.stopPreview()
	v.editing, v.menu, v.saveAsk, v.runExisting, v.wit = false, false, false, false, false
	v.from, v.suggest, v.prevKey, v.prev = "", nil, "", nil
}

// fromTemplate opens the editor on a new rule filled in from a template.
func (v *rulesView) fromTemplate(d core.RuleDoc, name string) {
	d.Version, d.Name = core.RuleVersion, name
	d.Conditions, d.Actions = slices.Clone(d.Conditions), slices.Clone(d.Actions)
	if d.Trigger == core.TriggerLabel && !v.hasLabel(d.TriggerArg) {
		d.TriggerArg = v.firstLabel()
	}
	for i, ac := range d.Actions {
		if ac.Label != "" && !v.hasLabel(ac.Label) {
			d.Actions[i].Label = v.firstLabel()
		}
	}
	v.openEditor(core.Rule{Enabled: true, Doc: d})
}

func (v *rulesView) hasLabel(slug string) bool {
	return slices.ContainsFunc(v.a.labels, func(l core.Label) bool { return l.Slug == slug })
}

// fromMail opens the editor on a new rule for mail like m: its sender, and
// the subject's known words and its attachments offered as conditions.
func (v *rulesView) fromMail(m core.MessageSummary) {
	addr := mail.AddrKey(m.From)
	v.openEditor(core.Rule{Enabled: true, Doc: core.RuleDoc{Version: core.RuleVersion, Name: i18n.Tf("settings.rules.from_mail.name", displayName(m.From)),
		Trigger: core.TriggerReceive, Match: core.MatchAll,
		Conditions: []core.RuleCondition{{Field: core.FieldFrom, Op: core.OpContains, Value: addr}},
		Actions:    []core.RuleAction{{Kind: core.ActionLabel, Label: v.firstLabel()}}}})
	v.from = m.Subject
	subject := strings.ToLower(m.Subject)
	for _, w := range ruleSuggestWords {
		if strings.Contains(subject, w) {
			v.suggest = append(v.suggest, core.RuleCondition{Field: core.FieldSubject, Op: core.OpContains, Value: w})
		}
	}
	if m.HasAttachments {
		v.suggest = append(v.suggest, core.RuleCondition{Field: core.FieldAttachments, Op: core.OpIs, Value: core.RuleYes})
	}
}

// schedulePreview reads, once typing pauses, the existing mail the editor's
// trigger and conditions match; a change meanwhile cancels the read under way.
func (v *rulesView) schedulePreview() {
	d := v.edit.Doc
	d.Conditions = slices.Clone(d.Conditions) // the editor writes its own while the read runs
	b, _ := json.Marshal([]any{d.Trigger, d.TriggerArg, d.Match, d.Conditions})
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

// preview is the existing mail the rule matches, of the last 30 days.
func (v *rulesView) preview(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	settingsSection(c, i18n.T("settings.rules.preview"), i18n.Tf("settings.rules.preview.foot", core.RulePreviewDays), func() {
		settingBlock(c, func() {
			p := v.prev
			switch {
			case p == nil:
				msg := "settings.rules.preview.incomplete"
				if v.prevStop != nil || v.prevCancel != nil {
					msg = "settings.rules.preview.wait"
				}
				kit.Text(c, i18n.T(msg), t.Text.Sm).TextColor(t.Muted)
				return
			case p.Count == 0:
				kit.Text(c, i18n.T("settings.rules.preview.none"), t.Text.Sm).TextColor(t.Muted)
				return
			}
			kit.Text(c, i18n.Tf("settings.rules.preview.count", p.Count), t.Text.Sm).TextColor(t.Muted)
			now := time.Now()
			for _, m := range p.Messages {
				ui.Row(c.Key(m.ID)).FillWidth().Gap(th.Space(3)).Children(func() {
					kit.Text(c, displayName(m.From), t.Text.Body).FontWeight(500).Shrink(0)
					kit.Text(c, m.Subject, t.Text.Body).SingleLine().Shrink(1).Grow(1)
					kit.Text(c, listTime(now, m.Date), t.Text.Sm).TextColor(t.Muted).Shrink(0)
				})
			}
		})
	})
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
	ready := len(d.Actions) > 0 &&
		!slices.ContainsFunc(d.Actions, func(ac core.RuleAction) bool {
			return (ac.Kind == core.ActionLabel || ac.Kind == core.ActionUnlabel) && ac.Label == "" ||
				ac.Kind == core.ActionForward && strings.TrimSpace(ac.To) == ""
		}) && (d.Trigger != core.TriggerLabel || d.TriggerArg != "") &&
		!slices.ContainsFunc(d.Conditions, func(cd core.RuleCondition) bool { return strings.TrimSpace(cd.Value) == "" }) && fileReady(d)
	pageHeading(c, title, sub, func() {
		ui.Row(c).Gap(th.Space(2)).Children(func() {
			settingButton(c, i18n.T("common.cancel"), kit.Outline, false).OnClick(v.closeEditor)
			settingButton(c, i18n.T("common.save"), kit.Default, !ready).OnClick(v.save)
		})
	})
	v.schedulePreview()
	v.saveDialog(c)
	if v.from != "" {
		kit.Alert(c, kit.Info, i18n.Tf("settings.rules.from_mail.banner", v.from), func() {
			for i, cd := range v.suggest {
				kit.AlertAction(c.Key(i), kit.Info, "+ "+v.condText(cd), func() {
					d.Conditions = append(d.Conditions, cd)
					v.suggest = slices.Delete(slices.Clone(v.suggest), i, i+1)
				})
			}
		})
	}
	if v.wit {
		kit.Alert(c, kit.Info, i18n.T("settings.rules.from_wit.banner"), nil)
	}
	settingsSection(c, i18n.T("settings.rules.name"), "", func() {
		settingBlock(c, func() {
			kit.Input(c, &d.Name, kit.InputOpts{Placeholder: i18n.T("settings.rules.name.placeholder")}).Label(i18n.T("settings.rules.name"))
		})
	})
	labels := make([]kit.Option[string], len(a.labels))
	for i, l := range a.labels {
		labels[i] = kit.Option[string]{Value: l.Slug, Label: l.Name}
	}
	settingsSection(c, i18n.T("settings.rules.when"), "", func() {
		settingRow(c, i18n.T("settings.rules.trigger"), i18n.T("settings.rules.trigger."+d.Trigger+".help"), func() {
			ui.Row(c).Gap(th.Space(2)).Children(func() {
				was := d.Trigger
				triggers := []kit.Option[string]{}
				for _, tr := range []string{core.TriggerReceive, core.TriggerLabel, core.TriggerSecurity, core.TriggerDaily} {
					triggers = append(triggers, kit.Option[string]{Value: tr, Label: i18n.T("settings.rules.trigger." + tr)})
				}
				settingSelect(c, i18n.T("settings.rules.trigger"), &d.Trigger, triggers)
				if d.Trigger != was {
					d.TriggerArg = map[string]string{core.TriggerSecurity: core.SecuritySuspicious}[d.Trigger]
					if d.Trigger == core.TriggerLabel && len(labels) > 0 {
						d.TriggerArg = labels[0].Value
					}
				}
				switch d.Trigger {
				case core.TriggerLabel:
					settingSelect(c, i18n.T("settings.rules.trigger.label.which"), &d.TriggerArg, labels)
				case core.TriggerSecurity:
					settingSelect(c, i18n.T("settings.rules.trigger.security.which"), &d.TriggerArg, []kit.Option[string]{
						{Value: core.SecuritySuspicious, Label: i18n.T("settings.rules.security.suspicious")},
						{Value: core.SecurityDangerous, Label: i18n.T("settings.rules.security.dangerous")}})
				}
			})
		})
	})
	conds := []func(){func() {
		settingRow(c, i18n.T("settings.rules.match"), "", func() {
			kit.Tabs(c, &v.any, i18n.T("settings.rules.match.all"), i18n.T("settings.rules.match.any"))
			d.Match = []string{core.MatchAll, core.MatchAny}[v.any]
		})
	}}
	var fields []kit.Option[string]
	for _, f := range []string{core.FieldFrom, core.FieldTo, core.FieldSubject, core.FieldBody, core.FieldAttachments, core.FieldLabel, core.FieldFolder} {
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
	for i := range d.Conditions {
		conds = append(conds, func() {
			settingBlock(c, func() {
				ui.Row(c.Key(i)).FillWidth().Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
					cd := &d.Conditions[i]
					was := cd.Field
					settingSelect(c, i18n.T("settings.rules.field"), &cd.Field, fields)
					if cd.Field != was {
						v.fieldChanged(cd, was)
					}
					switch cd.Field {
					case core.FieldAttachments:
						settingSelect(c, i18n.T("settings.rules.value"), &cd.Value, yesNo)
					case core.FieldLabel:
						settingSelect(c, i18n.T("settings.rules.op"), &cd.Op, setOps)
						settingSelect(c, i18n.T("settings.rules.label"), &cd.Value, labels)
					case core.FieldFolder:
						settingSelect(c, i18n.T("settings.rules.op"), &cd.Op, setOps)
						settingSelect(c, i18n.T("settings.rules.folder"), &cd.Value, roles)
					default:
						settingSelect(c, i18n.T("settings.rules.op"), &cd.Op, ops)
						kit.Input(c, &cd.Value, kit.InputOpts{Placeholder: i18n.T("settings.rules.value")}).Label(i18n.T("settings.rules.value")).Grow(1).Shrink(1)
					}
					ui.Row(c).Grow(1)
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
	// Wit's actions are chosen only once 「智能」 lets rules use Wit (an action that has one keeps it).
	aiOff := func(k string) bool { return !a.settings.RuleAI && slices.Contains(core.RuleAIActions, k) }
	kindsFor := func(cur string) []kit.Option[string] {
		var kinds []kit.Option[string]
		for _, g := range ruleActionGroups {
			for _, k := range g.kinds {
				if k == cur || !aiOff(k) {
					kinds = append(kinds, kit.Option[string]{Value: k, Label: i18n.T("settings.rules.action." + k)})
				}
			}
		}
		return kinds
	}
	acts := make([]func(), 0, len(d.Actions)+1)
	for i := range d.Actions {
		acts = append(acts, func() {
			settingBlock(c, func() {
				ui.Row(c.Key(i)).FillWidth().Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
					ac := &d.Actions[i]
					settingSelect(c, i18n.Tf("settings.rules.action.n", i+1), &ac.Kind, kindsFor(ac.Kind))
					if ac.Kind == core.ActionLabel || ac.Kind == core.ActionUnlabel {
						if ac.Label == "" {
							ac.Label = v.firstLabel()
						}
						settingSelect(c, i18n.T("settings.rules.label"), &ac.Label, labels)
					} else {
						ac.Label = ""
					}
					if ac.Kind == core.ActionForward {
						kit.Input(c, &ac.To, kit.InputOpts{Placeholder: i18n.T("settings.rules.forward.to")}).Label(i18n.T("settings.rules.forward.to")).Grow(1).Shrink(1)
					} else {
						ac.To = ""
					}
					if fileAction(ac.Kind) {
						fileKindChanged(d, i)
						v.fileDir(c, d, i)
					} else {
						ac.Dir, ac.Subfolder, ac.Naming = "", false, ""
					}
					ui.Row(c).Grow(1)
					kit.IconButton(c, "arrow-up", i18n.T("settings.rules.action.up"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm, Disabled: i == 0}).
						OnClick(func() { d.Actions[i-1], d.Actions[i] = d.Actions[i], d.Actions[i-1] })
					kit.IconButton(c, "arrow-down", i18n.T("settings.rules.action.down"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm, Disabled: i == len(d.Actions)-1}).
						OnClick(func() { d.Actions[i], d.Actions[i+1] = d.Actions[i+1], d.Actions[i] })
					kit.IconButton(c, "x", i18n.T("settings.rules.action.remove"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm, Disabled: len(d.Actions) == 1}).
						OnClick(func() { d.Actions = slices.Delete(d.Actions, i, i+1) })
				})
				if fileAction(d.Actions[i].Kind) {
					v.fileOptions(c, d, i)
				}
			})
		})
	}
	acts = append(acts, func() {
		settingBlock(c, func() {
			ui.Row(c).Children(func() {
				add := settingButton(c, i18n.T("settings.rules.action.add"), kit.Outline, len(d.Actions) >= core.MaxRuleActions).
					OnClick(func() { v.menu = !v.menu })
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
							label, off := i18n.T("settings.rules.action."+k), aiOff(k)
							kit.MenuItem(c.Key(k), false).Label(label).Disabled(off).Children(func() {
								ui.Text(c, label).SingleLine()
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
			})
		})
	})
	foot := ""
	if forwards(*d) {
		foot = i18n.T("settings.rules.forward.foot")
	}
	if !a.settings.RuleAI {
		foot = strings.TrimSpace(i18n.T("settings.rules.ai_off.note") + " " + foot)
	}
	settingsSection(c, i18n.T("settings.rules.then"), foot, acts...)
	v.preview(c)
	run := func(bg bool, key string) func() {
		return func() {
			settingBlock(c, func() {
				kit.Radio(c.Key(key), &v.edit.Background, bg, i18n.T("settings.rules.run."+key))
				kit.Text(c, i18n.T("settings.rules.run."+key+".help"), t.Text.Sm).TextColor(t.Muted)
			})
		}
	}
	settingsSection(c, i18n.T("settings.rules.run"), "", run(false, "window"), run(true, "background"))
	v.confirmDialog(c)
}

// forwards reports whether the rule forwards mail (外发).
func forwards(d core.RuleDoc) bool {
	return slices.ContainsFunc(d.Actions, func(ac core.RuleAction) bool { return ac.Kind == core.ActionForward })
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

// commit stores the rule; with existing mail it matches, it asks first
// whether to run it over that mail too.
func (v *rulesView) commit() {
	if v.prev != nil && v.prev.Count > 0 && !v.saveAsk {
		v.saveAsk = true
		return
	}
	a := v.a
	r, existing := v.edit, v.saveAsk && v.runExisting
	v.closeEditor()
	var n int
	a.writes.Chain("rules", func(ctx context.Context) error {
		id, err := a.svc.SaveRule(ctx, r)
		if err != nil || !existing {
			return err
		}
		n, err = a.svc.RunRuleOnExisting(ctx, id)
		return err
	}, func(err error) {
		if err == nil && n > 0 {
			a.toast(i18n.Tf("settings.rules.save.ran", n))
		}
		v.saved(err)
	})
}

// saveDialog asks, before a save, whether the rule also runs once over the
// existing mail it matches; only the actions that can run on it then do.
func (v *rulesView) saveDialog(c *ui.Context) {
	if !v.saveAsk || v.prev == nil {
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
	sep := i18n.T("settings.rules.sep")
	kit.Dialog(c, &v.saveAsk, i18n.Tf("settings.rules.save.title", ruleName(d)), kit.DialogOpts{Size: kit.DialogSm, Desc: i18n.T("settings.rules.save.desc"), Close: i18n.T("common.close")}, func() {
		ui.Column(c).Gap(th.Space(1)).Children(func() {
			if len(now) == 0 {
				v.runExisting = false
				kit.Text(c, i18n.T("settings.rules.save.existing.nothing"), t.Text.Sm).TextColor(t.Muted)
			} else {
				kit.Checkbox(c, &v.runExisting, i18n.Tf("settings.rules.save.existing", v.prev.Count))
				kit.Text(c, i18n.Tf("settings.rules.save.existing.only", strings.Join(now, sep)), t.Text.Sm).TextColor(t.Muted)
			}
			if len(later) > 0 {
				kit.Text(c, i18n.Tf("settings.rules.save.existing.later", strings.Join(later, sep)), t.Text.Sm).TextColor(t.Muted)
			}
		})
	}, func() {
		kit.Button(c, i18n.T("common.cancel"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(func() { v.saveAsk = false })
		kit.Button(c, i18n.T("settings.rules.save.confirm"), kit.ButtonOpts{}).OnClick(v.commit)
	})
}

// confirmDialog names the forward recipients to confirm, the ones never
// written to flagged; going back to edit saves nothing.
func (v *rulesView) confirmDialog(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	kit.Dialog(c, &v.confirming, i18n.T("settings.rules.confirm.title"), kit.DialogOpts{Size: kit.DialogSm, Alert: true,
		Desc: i18n.Tf("settings.rules.confirm.desc", ruleName(v.edit.Doc)), Close: i18n.T("common.close")}, func() {
		ui.Column(c).Gap(th.Space(1)).Children(func() {
			kit.Text(c, i18n.T("settings.rules.confirm.to"), t.Text.Sm).TextColor(t.Muted)
			for _, to := range v.unconfirmed {
				ui.Row(c).Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
					ui.Text(c, to).FontWeight(600).Selectable()
					if slices.ContainsFunc(v.firstTime, func(f string) bool { return mail.AddrKey(f) == mail.AddrKey(to) }) {
						kit.Badge(c, i18n.T("mcp_send.first"))
					}
				})
			}
		})
		for _, k := range []string{"outbox", "new_only", "again"} {
			kit.Text(c, i18n.T("settings.rules.confirm."+k), t.Text.Sm).TextColor(t.Muted)
		}
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

// saved shows a write's error and reads the rules again.
func (v *rulesView) saved(err error) {
	v.a.toastErr(err)
	v.load()
}
