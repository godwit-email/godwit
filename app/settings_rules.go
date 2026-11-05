package app

import (
	"cmp"
	"context"
	"path/filepath"
	"slices"
	"strconv"
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

// rulesView is Settings › Rules: the rules in the order they run, each with
// its switch and, expanded, its last week of hits; the editor in place of
// the list while one is edited.
type rulesView struct {
	a     *App
	rules []core.Rule

	open int64          // the rule whose hits show, 0 none
	hits []core.RuleHit // open's

	counts map[int64]int // each rule's hits of the last week

	deleting *core.Rule        // the rule whose 删除规则 asks to confirm
	order    ui.ListState      // the rules' list, its rows dragged to reorder them
	heights  map[int64]float32 // each rule's row as last laid out, which the list is as tall as
	shape    [2]int64          // the open rule and its hits' count the rows were built with

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
	witRule *witProposal         // Wit's proposal to open the editor on, once the page shows
	wit     *witProposal         // the proposal the editor holds, nil none

	prevKey    string            // the trigger and conditions prev is for
	prev       *core.RulePreview // the existing mail they match; nil not known
	prevStop   func()            // the debounce before the next preview
	prevCancel func()            // the preview under way

	saveAsk     bool // the save dialog is open
	runExisting bool // its 同时处理现有的邮件

	viewTop  float32 // the settings page's scroller's top, last frame (window coordinates)
	asideTop float32 // how far the editor's side panel moved down to stay in view
}

// witProposal is a rule Wit proposed, taken to the editor: the conversation
// it was proposed in (回到对话) and what the user asked there.
type witProposal struct {
	rule     core.Rule
	conv     int64
	approval int64 // the proposal's, which saving the rule ends
	ask      string
}

// ruleActionGroup is a group of the add-action menu.
type ruleActionGroup struct {
	key   string   // its title is settings.rules.group.<key>
	kinds []string // its actions, in order; none hides the group
}

// ruleActionGroups are the actions the editor offers, by group, in order.
// A new action registers by appending its kind to its group, and its icon
// to ruleActionIcons.
var ruleActionGroups = []ruleActionGroup{
	{"organize", []string{core.ActionLabel, core.ActionUnlabel, core.ActionRead, core.ActionStar,
		core.ActionArchive, core.ActionJunk, core.ActionTrash, core.ActionBlock, core.ActionStop}},
	{"forward", []string{core.ActionForward}},
	{"files", []string{core.ActionPDF, core.ActionEML, core.ActionAttachments}},
	{"wit", core.RuleAIActions},
}

// ruleActionIcons are each action's icon, on its card, in the menu and on its results.
var ruleActionIcons = map[string]string{
	core.ActionLabel: "tag", core.ActionUnlabel: "tag", core.ActionRead: "mail-open", core.ActionStar: "star",
	core.ActionArchive: "archive", core.ActionJunk: "spam", core.ActionTrash: "trash-2", core.ActionBlock: "spam", core.ActionStop: "circle-x",
	core.ActionForward: "forward", core.ActionPDF: "file-text", core.ActionEML: "mail", core.ActionAttachments: "paperclip",
	core.ActionDraftReply: "quill-pen", core.ActionKeyPoints: "file-text",
}

func actionIcon(kind string) string {
	if i, ok := ruleActionIcons[kind]; ok {
		return i
	}
	return "check"
}

// ruleTemplates open the editor filled in; nothing is saved or turned on
// until the user saves. A label a template names that is gone becomes the
// first; a folder, sender or recipient left empty is for the user to fill.
var ruleTemplates = []struct {
	key  string // settings.rules.template.<key> and .<key>.desc
	icon string
	doc  core.RuleDoc
}{
	{"bill", "tag", core.RuleDoc{Trigger: core.TriggerReceive, Match: core.MatchAll,
		Conditions: []core.RuleCondition{{Field: core.FieldSubject, Op: core.OpContains, Value: "invoice"}, {Field: core.FieldAttachments, Op: core.OpIs, Value: core.RuleYes}},
		Actions:    []core.RuleAction{{Kind: core.ActionLabel, Label: "bill"}, {Kind: core.ActionPDF}, {Kind: core.ActionArchive}}}},
	{"newsletter", "mail-open", core.RuleDoc{Trigger: core.TriggerLabel, TriggerArg: "newsletter", Match: core.MatchAll,
		Actions: []core.RuleAction{{Kind: core.ActionRead}, {Kind: core.ActionArchive}}}},
	{"suspicious", "shield-check", core.RuleDoc{Trigger: core.TriggerSecurity, TriggerArg: core.SecuritySuspicious, Match: core.MatchAll,
		Actions: []core.RuleAction{{Kind: core.ActionJunk}}}},
	{"forward", "forward", core.RuleDoc{Trigger: core.TriggerReceive, Match: core.MatchAll,
		Conditions: []core.RuleCondition{{Field: core.FieldFrom, Op: core.OpContains}, {Field: core.FieldSubject, Op: core.OpContains, Value: "invoice"}},
		Actions:    []core.RuleAction{{Kind: core.ActionForward}, {Kind: core.ActionArchive}}}},
	{"contract", "paperclip", core.RuleDoc{Trigger: core.TriggerReceive, Match: core.MatchAll,
		Conditions: []core.RuleCondition{{Field: core.FieldSubject, Op: core.OpContains, Value: "合同"}, {Field: core.FieldAttachments, Op: core.OpIs, Value: core.RuleYes}},
		Actions:    []core.RuleAction{{Kind: core.ActionAttachments}}}},
	{"reply", "quill-pen", core.RuleDoc{Trigger: core.TriggerReceive, Match: core.MatchAll,
		Conditions: []core.RuleCondition{{Field: core.FieldFrom, Op: core.OpContains}},
		Actions:    []core.RuleAction{{Kind: core.ActionStar}, {Kind: core.ActionDraftReply}}}},
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
			kit.Button(c, i18n.T("settings.rules.new"), kit.ButtonOpts{Icon: "plus"}).PaddingX(th.Space(3)).OnClick(v.create)
		}
	})
	if len(v.rules) == 0 {
		kit.Empty(c, kit.EmptyOpts{Icon: "filter", Title: i18n.T("settings.rules.empty"), Desc: i18n.T("settings.rules.empty.desc"), Dashed: true,
			Actions: func() {
				kit.EmptyAction(c, i18n.T("settings.rules.new"), kit.ButtonOpts{}).OnClick(v.create)
				kit.EmptyAction(c, i18n.T("settings.rules.empty.wit"), kit.ButtonOpts{Variant: kit.Outline, Icon: "sparkles"}).OnClick(a.NewConversation)
			}})
	} else {
		settingsSection(c, i18n.T("settings.rules.list"), i18n.T("settings.rules.list.foot"), func() { v.list(c) })
	}
	ui.Column(c).Gap(theme.SectionGap).Children(func() {
		ui.Column(c).Gap(th.Space(0.5)).Children(func() {
			sectionTitle(c, i18n.T("settings.rules.templates"))
			kit.Text(c, i18n.T("settings.rules.templates.foot"), t.Text.Sm).TextColor(t.Muted).PaddingX(th.Space(4))
		})
		for i := 0; i < len(ruleTemplates); i += 2 {
			ui.Row(c.Key(i)).FillWidth().Gap(th.Space(2)).AlignItems(ui.Stretch).Children(func() {
				for _, tp := range ruleTemplates[i:min(i+2, len(ruleTemplates))] {
					v.templateCard(c.Key(tp.key), tp.key, tp.icon, tp.doc)
				}
			})
		}
		// Emptying Trash and Junk is not a rule: it stays with the account's mailboxes.
		if len(a.accounts) > 0 {
			id := a.accounts[0].ID
			kit.TextLink(c, i18n.T("settings.rules.empty_trash"), t.Muted, t.Text.Sm).PaddingX(th.Space(4)).AlignSelf(ui.Start).
				OnClick(func() { a.navigate(Route{Page: PageSettings, Settings: accountRoute(id)}) })
		}
	})
	v.deleteDialog(c)
}

// templateCard is a template: its icon, name and what it does; it opens the editor.
func (v *rulesView) templateCard(c *ui.Context, key, icon string, d core.RuleDoc) {
	t, th := theme.Of(c), c.Theme()
	name := i18n.T("settings.rules.template." + key)
	kit.Pressable(c, kit.PressableOpts{Bordered: true, OnClick: func() { v.fromTemplate(d, name) }}).
		Label(i18n.Tf("settings.rules.template.use.label", name)).Grow(1).Basis(0).Shrink(1).
		AlignItems(ui.Start).Gap(th.Space(3)).Padding(th.Space(3), th.Space(3.5)).Radius(t.Radius.Xl).Children(func() {
		ui.Box(c).Size(th.Space(8), th.Space(8)).Radius(t.Radius.Lg).Background(t.Hover).Center().Shrink(0).Children(func() {
			ui.Icon(c, icons.Get(icon)).Size(theme.Icon, theme.Icon).TextColor(t.Foreground)
		})
		ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(0.5)).Children(func() {
			kit.Text(c, name, t.Text.Body).FontWeight(500)
			kit.Text(c, i18n.T("settings.rules.template."+key+".desc"), t.Text.Sm).TextColor(t.Muted)
		})
	})
}

// ruleTag is a small tinted label: a rule's or an action's kind, a hit's result.
func ruleTag(c *ui.Context, text string, fill, fg ui.Color, icon string) ui.Element {
	t, th := theme.Of(c), c.Theme()
	return ui.Row(c).AlignItems(ui.Center).Gap(th.Space(1)).Padding(th.Space(0.5), th.Space(1.5)).Radius(t.Radius.Sm).
		Background(fill).TextColor(fg).Shrink(0).Children(func() {
		if icon != "" {
			ui.Icon(c, icons.Get(icon)).Size(theme.IconSm, theme.IconSm)
		}
		kit.Text(c, text, t.Text.Sm).SingleLine()
	})
}

// list is the rules, one row each, in a List as the labels' is: dragging
// a row shows where it would go and moves it there (ListState.Reorder).
// The List is as tall as its rows, an open one's hits included, as they
// were last laid out: a List left to fit its rows grows with them but does
// not shrink back as a row closes.
func (v *rulesView) list(c *ui.Context) {
	th := c.Theme()
	v.order.Key = func(i int) any { return v.rules[i].ID }
	v.order.Label = func(i int) string { return ruleName(v.rules[i].Doc) }
	v.order.Reorder = func(rows []int, to int) { v.move(rows[0], to) }
	if v.heights == nil {
		v.heights = map[int64]float32{}
	}
	// A row opened, closed or filled is measured once laid out, in a frame
	// nothing else may ask for: ask for it.
	if shape := [2]int64{v.open, int64(len(v.hits))}; shape != v.shape {
		v.shape = shape
		c.Invalidate()
	}
	n := len(v.rules)
	var h float32
	for _, r := range v.rules {
		h += cmp.Or(v.heights[r.ID], th.Space(14)) // a row not laid out yet: one closed
	}
	kit.List(c, &v.order, n, func(i int) {
		id := v.rules[i].ID
		row := cardListRow(c, i, n, func() { v.row(c, v.rules[i]) })
		if b := row.LayoutBounds(); b.H > 0 && b.H != v.heights[id] {
			v.heights[id] = b.H
			c.Invalidate() // the list's height follows in the next frame
		}
	}).FillWidth().Height(h)
}

// row is a rule: grip, switch, name and summary (which expand it), and
// expanded, its hits, 编辑规则 and 删除规则.
func (v *rulesView) row(c *ui.Context, r core.Rule) {
	a := v.a
	t, th := theme.Of(c), c.Theme()
	name := ruleName(r.Doc)
	open := v.open == r.ID
	toggle := func() {
		v.hits = nil
		if v.open = r.ID; open {
			v.open = 0
			return
		}
		v.loadHits()
	}
	ui.Column(c).FillWidth().Children(func() {
		ui.Row(c).FillWidth().Gap(th.Space(3)).Padding(th.Space(2), th.Space(4), th.Space(2), th.Space(2)).MinHeight(th.Space(12)).AlignItems(ui.Center).
			Children(func() {
				ui.Icon(c, icons.Get("grip")).Size(theme.Icon, theme.Icon).TextColor(t.Idle).Shrink(0)
				on := r.Enabled
				kit.Switch(c, &on).Label(i18n.Tf("settings.rules.enable", name))
				if on != r.Enabled {
					id := r.ID
					v.rules = slices.Clone(v.rules)
					v.rules[slices.IndexFunc(v.rules, func(x core.Rule) bool { return x.ID == id })].Enabled = on // at once; load reads them again
					a.writes.Chain("rules", func(ctx context.Context) error { return a.svc.SetRuleEnabled(ctx, id, on) }, v.saved)
				}
				fg := t.Foreground
				if !r.Enabled {
					fg = t.Muted
				}
				kit.Pressable(c, kit.PressableOpts{Bare: true, OnClick: toggle}).Label(name).Grow(1).Shrink(1).Children(func() {
					ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(0.5)).Children(func() {
						ui.Row(c).Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
							kit.Text(c, name, t.Text.Body).FontWeight(500).TextColor(fg).SingleLine().Shrink(1)
							v.badges(c, r)
						})
						kit.Text(c, v.summary(r.Doc), t.Text.Sm).TextColor(t.Muted).SingleLine()
					})
				})
				hits := i18n.T("settings.rules.off")
				if r.Enabled {
					hits = i18n.Tf("settings.rules.hits.count", v.counts[r.ID])
				}
				kit.Text(c, hits, t.Text.Sm).TextColor(t.Muted).SingleLine().Shrink(0)
				chev, label := "chevron-right", i18n.T("settings.rules.hits.show")
				if open {
					chev, label = "chevron-down", i18n.T("settings.rules.hits.hide")
				}
				kit.IconButton(c, chev, label, kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).OnClick(toggle)
			})
		if open {
			v.hitList(c, r)
		}
	})
}

// move puts the rule at i before the one at to (len for the end), the
// others in order: rules run top to bottom.
func (v *rulesView) move(i, to int) {
	if to > i {
		to-- // i is taken out first
	}
	if to == i {
		return
	}
	a := v.a
	moved := slices.Clone(v.rules) // at once; load reads them again
	r := moved[i]
	v.rules = slices.Insert(slices.Delete(moved, i, i+1), to, r)
	ids := make([]int64, len(v.rules))
	for k, x := range v.rules {
		ids[k] = x.ID
	}
	a.writes.Chain("rules", func(ctx context.Context) error { return a.svc.ReorderRules(ctx, ids) }, v.saved)
}

// badges are a rule's kinds as the list tags them: 外发, AI, 后台运行.
func (v *rulesView) badges(c *ui.Context, r core.Rule) {
	t := theme.Of(c)
	if forwards(r.Doc) {
		amber := t.Label("amber")
		ruleTag(c, i18n.T("settings.rules.egress"), amber.Fill, amber.Text, "")
	}
	if usesAI(r.Doc) {
		violet := t.Label("violet")
		ruleTag(c, i18n.T("settings.rules.kind.ai"), violet.Fill, violet.Text, "")
	}
	if r.Background {
		blue := t.Label("blue")
		ruleTag(c, i18n.T("settings.rules.run.background"), blue.Fill, blue.Text, "")
	}
}

// hitList is the open rule's hits of the last week, newest first, in a box
// about two and a half rows tall that scrolls past them, then 编辑规则 and
// 删除规则.
func (v *rulesView) hitList(c *ui.Context, r core.Rule) {
	t, th := theme.Of(c), c.Theme()
	ui.Column(c).FillWidth().Gap(th.Space(2)).Padding(0, th.Space(4), th.Space(3), th.Space(4)+th.Space(11.5)).Children(func() {
		box := func() ui.Element {
			return ui.Column(c).FillWidth().MaxHeight(th.Space(42)).Radius(t.Radius.Lg).Background(t.Background).Border(theme.Hairline, t.Border).Clip()
		}
		if len(v.hits) == 0 {
			box().Padding(th.Space(4), th.Space(3)).AlignItems(ui.Center).Children(func() {
				kit.Text(c, i18n.T("settings.rules.hits.none"), t.Text.Sm).TextColor(t.Muted)
			})
		} else {
			kit.Text(c, i18n.Tf("settings.rules.hits.head", len(v.hits)), t.Text.Sm).TextColor(t.Muted)
			box().Children(func() {
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
		}
		ui.Row(c).Gap(th.Space(2)).Children(func() {
			kit.Button(c, i18n.T("settings.rules.edit_rule"), kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeSm}).OnClick(func() { v.openEditor(r) })
			kit.Button(c, i18n.T("settings.rules.delete"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).TextColor(t.Danger).
				OnClick(func() { v.deleting = &r })
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
					fill, fg, icon := t.Hover, t.Muted, actionIcon(res.Kind)
					switch res.State {
					case core.RuleActionDone:
						fill, fg = t.SuccessBg, t.SuccessText
					case core.RuleActionFailed:
						fill, fg, icon = t.DangerBg, t.DangerText, "triangle-alert"
					}
					ruleTag(c.Key(i), v.result(r, i, res), fill, fg, icon)
				}
			})
		})
		if h.State == core.RuleRunUndone {
			kit.Text(c, i18n.T("settings.rules.hits.undone"), t.Text.Sm).TextColor(t.Muted).Shrink(0)
			return
		}
		if slices.ContainsFunc(h.Actions, func(x core.RuleActionResult) bool {
			return x.State == core.RuleActionDone && core.RuleUndoable(x.Kind)
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

// result is an action's outcome in a hit, named as the action was when the
// run started (an edit since, of a recipient or a label, leaves it as it
// was); a run from before runs kept it is named from the rule as it is.
func (v *rulesView) result(r core.Rule, i int, res core.RuleActionResult) string {
	ac := res.Action
	if ac.Kind == "" {
		ac = core.RuleAction{Kind: res.Kind}
		if i < len(r.Doc.Actions) && r.Doc.Actions[i].Kind == res.Kind {
			ac = r.Doc.Actions[i]
		}
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

// deleteDialog asks before 删除规则 deletes a rule and its hits.
func (v *rulesView) deleteDialog(c *ui.Context) {
	if v.deleting == nil {
		return
	}
	a, r := v.a, *v.deleting
	name := ruleName(r.Doc)
	open := true
	kit.Dialog(c, &open, i18n.Tf("settings.rules.delete.title", name),
		kit.DialogOpts{Size: kit.DialogSm, Alert: true, Desc: i18n.T("settings.rules.delete.desc"), Close: i18n.T("common.close")}, nil, func() {
			kit.Button(c, i18n.T("common.cancel"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(func() { v.deleting = nil })
			kit.Button(c, i18n.T("common.delete"), kit.ButtonOpts{Variant: kit.Destructive}).OnClick(func() {
				v.deleting = nil
				if v.open == r.ID {
					v.open, v.hits = 0, nil
				}
				a.writes.Chain("rules", func(ctx context.Context) error { return a.svc.DeleteRule(ctx, r.ID) }, func(err error) {
					if err == nil {
						a.toast(i18n.Tf("settings.rules.deleted", name))
					}
					v.saved(err)
				})
			})
		})
	if !open {
		v.deleting = nil
	}
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
	if acc, ok := v.account(d.Account); ok {
		trigger = i18n.Tf("settings.rules.trigger.account", trigger, accountName(acc))
	}
	if conds == "" {
		return i18n.Tf("settings.rules.summary.always", trigger, then)
	}
	return i18n.Tf("settings.rules.summary", trigger, conds, then)
}

// account is the account a rule is for; false for every account, or one gone.
func (v *rulesView) account(id int64) (core.Account, bool) {
	if i := slices.IndexFunc(v.a.accounts, func(acc core.Account) bool { return acc.ID == id }); id != 0 && i >= 0 {
		return v.a.accounts[i], true
	}
	return core.Account{}, false
}

func (v *rulesView) condText(cd core.RuleCondition) string {
	val := cd.Value
	switch cd.Field {
	case core.FieldAttachments:
		return i18n.T("settings.rules.attachments." + cd.Value)
	case core.FieldAge:
		n, _ := strconv.Atoi(cd.Value)
		return i18n.Tf("settings.rules.cond.age", n)
	case core.FieldLabel:
		val = v.labelName(cd.Value)
	case core.FieldFolder:
		if slices.Contains(ruleRoles, cd.Value) {
			val = i18n.T("mail.folder." + cd.Value)
		}
	}
	return i18n.Tf("settings.rules.cond", i18n.T("settings.rules.field."+cd.Field), i18n.T("settings.rules.op."+cd.Op), val)
}

// chipText is a condition create-from-mail offers, as its chip says it.
func (v *rulesView) chipText(cd core.RuleCondition) string {
	if textField(cd.Field) && cd.Op == core.OpContains {
		return i18n.Tf("settings.rules.suggest", i18n.T("settings.rules.field."+cd.Field), cd.Value)
	}
	return v.condText(cd)
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
	v.editing, v.menu, v.saveAsk, v.runExisting, v.wit, v.asideTop = false, false, false, false, nil, 0
	v.from, v.suggest, v.prevKey, v.prev = "", nil, "", nil
}

// fromTemplate opens the editor on a new rule filled in from a template.
// Without 「智能」's switch for rules its Wit actions are left out, and the
// add-action menu opens to say why.
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
	aiOff := !v.a.settings.RuleAI && usesAI(d)
	if aiOff {
		d.Actions = slices.DeleteFunc(d.Actions, func(ac core.RuleAction) bool { return slices.Contains(core.RuleAIActions, ac.Kind) })
	}
	v.openEditor(core.Rule{Enabled: true, Doc: d})
	v.menu = aiOff
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

// fromWit opens the editor on Wit's proposal (在编辑器里修改).
func (v *rulesView) fromWit(p witProposal) {
	v.openEditor(p.rule)
	v.wit = &p
}

// forwards reports whether the rule forwards mail (外发).
func forwards(d core.RuleDoc) bool {
	return slices.ContainsFunc(d.Actions, func(ac core.RuleAction) bool { return ac.Kind == core.ActionForward })
}

// usesAI reports whether the rule has a Wit action (AI).
func usesAI(d core.RuleDoc) bool {
	return slices.ContainsFunc(d.Actions, func(ac core.RuleAction) bool { return slices.Contains(core.RuleAIActions, ac.Kind) })
}

// saved shows a write's error and reads the rules again.
func (v *rulesView) saved(err error) {
	v.a.toastErr(err)
	v.load()
}
