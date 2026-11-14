package app

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// viewPreviewShown is how many conversations of a group the editor's preview lists.
const viewPreviewShown = 20

// viewEditor creates or edits a view in place of the list and the
// reader: the form, with a live preview beside it. Nothing is stored until 保存.
type viewEditor struct {
	p    *mailPage
	box  core.SavedView
	note string // where a new one's conditions came from
	base string // the form as it opened: anything else is unsaved

	exceptions []core.ViewException
	templates  []core.ViewTemplate
	iconOpen   bool
	menus      map[string]*bool // each list of conditions' 添加 menu

	prev       *core.ViewContents
	prevKey    string
	prevStop   func()
	prevCancel func()

	viewTop, asideTop float32
}

func newViewEditor(p *mailPage) *viewEditor {
	return &viewEditor{p: p, menus: map[string]*bool{}, box: core.SavedView{Icon: "folder-3", Doc: core.SavedViewDoc{Match: core.MatchAll, Conditions: []core.RuleCondition{}}}}
}

// openViewEditor shows e in place of the list and loads what it lists.
func (p *mailPage) openViewEditor(e *viewEditor) {
	if p.askToLeave(func() { p.openViewEditor(e) }) {
		return
	}
	if p.viewEdit != nil {
		p.viewEdit.stopPreview()
	}
	p.viewEdit, e.base = e, e.snapshot()
	a, id := p.a, e.box.ID
	var exceptions []core.ViewException
	var templates []core.ViewTemplate
	a.do(func(ctx context.Context) (err error) {
		if id != 0 {
			exceptions, err = a.svc.ViewExceptions(ctx, id)
			return err
		}
		templates, err = a.svc.ViewTemplates(ctx)
		return err
	}, func(err error) {
		if err != nil {
			a.toastErr(err)
			return
		}
		e.exceptions, e.templates = exceptions, templates
	})
}

// editView opens the editor on b, showing its list when the editor closes.
func (p *mailPage) editView(b core.SavedView) {
	if p.askToLeave(func() { p.editView(b) }) {
		return
	}
	e := newViewEditor(p)
	e.box = b
	e.box.Doc = cloneViewDoc(b.Doc)
	p.a.navigate(Route{Page: PageMail, View: savedRoute(b.ID)})
	p.openViewEditor(e)
}

// cloneViewDoc is d's own copy: the form writes into it until 保存.
func cloneViewDoc(d core.SavedViewDoc) core.SavedViewDoc {
	d.Conditions = cloneConds(d.Conditions)
	d.Groups = slices.Clone(d.Groups)
	for i := range d.Groups {
		d.Groups[i].Conditions = cloneConds(d.Groups[i].Conditions)
	}
	return d
}

// snapshot is what 保存 would write, to compare.
func (e *viewEditor) snapshot() string {
	b, _ := json.Marshal(core.SavedView{Name: e.box.Name, Icon: e.box.Icon, Doc: e.box.Doc})
	return string(b)
}

// viewUnsaved reports whether the editor has changes 保存 would write.
func (p *mailPage) viewUnsaved() bool {
	return p.viewEdit != nil && p.viewEdit.snapshot() != p.viewEdit.base
}

// askToLeave asks first when the editor has unsaved changes, and reports
// whether it did; then runs once the user gives them up.
func (p *mailPage) askToLeave(then func()) bool {
	if !p.viewUnsaved() {
		return false
	}
	p.viewLeave = then
	return true
}

// viewLeaveDialog asks before giving up the editor's changes.
func (p *mailPage) viewLeaveDialog(c *ui.Context) {
	p.viewLeaveOpen = p.viewLeave != nil
	kit.Dialog(c, &p.viewLeaveOpen, i18n.T("close.unsaved.title"), kit.DialogOpts{Size: kit.DialogSm, Alert: true, Desc: i18n.T("view.leave.desc"), Close: i18n.T("common.close")}, nil, func() {
		kit.Button(c, i18n.T("view.leave.keep"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(func() { p.viewLeave = nil })
		kit.Button(c, i18n.T("view.leave.discard"), kit.ButtonOpts{Variant: kit.Destructive}).OnClick(func() {
			then := p.viewLeave
			p.viewLeave = nil
			p.closeViewEditor()
			then()
		})
	})
	if !p.viewLeaveOpen {
		p.viewLeave = nil
	}
}

// closeViewEditor goes back to the list.
func (p *mailPage) closeViewEditor() {
	if p.viewEdit != nil {
		p.viewEdit.stopPreview()
		p.viewEdit = nil
	}
}

var viewDateNames = []string{core.ViewDateToday, core.ViewDateYesterday, core.ViewDateThisWeek, core.ViewDateLastWeek, core.ViewDateThisMonth, core.ViewDateLastMonth}

// docProblem says what keeps the document from being a view, "" for nothing.
func (e *viewEditor) docProblem() string {
	d := e.box.Doc
	check := func(conds []core.RuleCondition) string {
		for _, cd := range (core.RuleDoc{Conditions: conds}).Rows() {
			switch {
			case strings.TrimSpace(cd.Value) == "":
				return i18n.T("view.problem.value")
			case cd.Field == core.FieldLabel && !slices.ContainsFunc(e.p.a.labels, func(l core.Label) bool { return l.Slug == cd.Value }):
				return i18n.T("view.problem.label") // deleted since
			case cd.Field == core.FieldDate && !slices.Contains(viewDateNames, cd.Value):
				if n, err := strconv.Atoi(cd.Value); err != nil || n < 1 || n > core.MaxViewDateDays {
					return i18n.Tf("view.problem.days", core.MaxViewDateDays)
				}
			case cd.Field == core.FieldReply && cd.Op == core.OpOlderThan:
				if n, err := strconv.Atoi(cd.Value); err != nil || n < 1 || n > core.MaxViewWaitDays {
					return i18n.Tf("view.problem.days", core.MaxViewWaitDays)
				}
			}
		}
		return ""
	}
	if p := check(d.Conditions); p != "" {
		return p
	}
	for _, g := range d.Groups {
		if strings.TrimSpace(g.Name) == "" {
			return i18n.T("view.problem.group_name")
		}
		if p := check(g.Conditions); p != "" {
			return p
		}
	}
	return ""
}

// problem is why it can't be saved yet, "" when it can.
func (e *viewEditor) problem() string {
	if strings.TrimSpace(e.box.Name) == "" {
		return i18n.T("view.problem.name")
	}
	return e.docProblem()
}

// schedulePreview reads, once typing pauses, what the document shows; a change
// meanwhile cancels the read under way.
func (e *viewEditor) schedulePreview() {
	d := e.box.Doc
	b, _ := json.Marshal(d)
	if string(b) == e.prevKey {
		return
	}
	e.stopPreview()
	e.prevKey, e.prev = string(b), nil
	if e.docProblem() != "" {
		return
	}
	doc := cloneViewDoc(d) // the form writes its own while the read runs
	a, key, id := e.p.a, e.prevKey, e.box.ID
	e.prevStop = a.after(debounce, func() {
		e.prevStop = nil
		ctx, cancel := context.WithCancel(a.ctx)
		e.prevCancel = cancel
		var got core.ViewContents
		a.query(func(context.Context) (err error) {
			got, err = a.svc.PreviewView(ctx, doc, id, viewPreviewShown)
			return err
		}, func(err error) {
			cancel()
			if key != e.prevKey {
				return // a newer one is coming
			}
			e.prevCancel = nil
			if err == nil {
				e.prev = &got
			}
		})
	})
}

func (e *viewEditor) stopPreview() {
	if e.prevStop != nil {
		e.prevStop()
		e.prevStop = nil
	}
	if e.prevCancel != nil {
		e.prevCancel()
		e.prevCancel = nil
	}
}

// save stores the view and shows it.
func (e *viewEditor) save() {
	p, a, box := e.p, e.p.a, e.box
	box.Name = strings.TrimSpace(box.Name)
	var id int64
	a.writes.Chain("saved", func(ctx context.Context) (err error) {
		id, err = a.svc.SaveView(ctx, box)
		return err
	}, func(err error) {
		if err != nil {
			a.toastErr(err)
			return
		}
		p.closeViewEditor()
		if _, ok := p.savedView(id); !ok { // the route to it must not look gone
			box.ID = id
			p.views = append(p.views, box)
		}
		a.navigate(Route{Page: PageMail, View: savedRoute(id)})
	})
}

// view is the page: its heading and buttons, the form, and the preview beside
// it, which stays in view as the form scrolls.
func (e *viewEditor) view(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	title, sub, primary := i18n.T("view.new"), i18n.T("view.editor.sub.new"), i18n.T("view.editor.create")
	if e.box.ID != 0 {
		title, sub, primary = i18n.Tf("view.editor.edit", e.box.Name), i18n.T("view.editor.sub.edit"), i18n.T("common.save")
	}
	e.schedulePreview()
	problem := e.problem()
	scroll := ui.Scroll(c).Grow(1)
	e.viewTop = scroll.LayoutBounds().Y
	scroll.Children(func() {
		ui.Column(c).FillWidth().Padding(th.Space(8)).Children(func() {
			ui.Column(c).FillWidth().MaxWidth(theme.ContentMaxWidth).AlignSelf(ui.Center).Gap(th.Space(6)).Children(func() {
				pageHeading(c, title, sub, func() {
					ui.Row(c).AlignItems(ui.Center).Gap(th.Space(2)).Children(func() {
						if problem != "" {
							kit.Text(c, problem, t.Text.Xs).TextColor(t.Muted)
						}
						settingButton(c, i18n.T("common.cancel"), kit.Outline, false).OnClick(e.p.closeViewEditor)
						settingButton(c, primary, kit.Default, problem != "").OnClick(e.save)
					})
				})
				grid := ui.Row(c).FillWidth().Gap(th.Space(6)).AlignItems(ui.Start)
				top := grid.LayoutBounds().Y
				grid.Children(func() {
					form := ui.Column(c).Grow(1).Shrink(1).Basis(0).MinWidth(0).Gap(th.Space(6))
					formH := form.LayoutBounds().H
					form.Children(func() { e.form(c) })
					aside := ui.Column(c).Width(theme.RuleAsideWidth).Shrink(0).Label(i18n.T("view.preview"))
					off := max(0, min(e.viewTop+th.Space(4)-top, formH-aside.LayoutBounds().H))
					if off != e.asideTop {
						e.asideTop = off
						c.Invalidate()
					}
					aside.Margin(e.asideTop, 0, 0, 0).Children(func() { e.preview(c) })
				})
			})
		})
	})
}

// form is 名称, 图标 and 账户, then 条件, 分组, 从模板创建, 例外 and the rest.
func (e *viewEditor) form(c *ui.Context) {
	a := e.p.a
	t, th := theme.Of(c), c.Theme()
	d := &e.box.Doc
	if e.note != "" {
		kit.Block(c, ui.Row(c), kit.ToneTint).FillWidth().AlignItems(ui.Center).Gap(th.Space(2.5)).Padding(th.Space(2.5), th.Space(3.5)).Children(func() {
			ui.Icon(c, icons.Get("mail")).Size(theme.Icon, theme.Icon).TextColor(t.Muted).Shrink(0)
			kit.Text(c, e.note, t.Text.Sm).Grow(1).Shrink(1)
		})
	}
	settingsCard(c, func() {
		settingRow(c, i18n.T("view.editor.name"), "", func() {
			kit.Input(c, &e.box.Name, kit.InputOpts{Placeholder: i18n.T("view.editor.name.placeholder")}).Label(i18n.T("view.editor.name")).Width(theme.SettingsSelectMin)
		})
	}, func() {
		settingRow(c, i18n.T("view.editor.icon"), "", func() { e.iconButton(c) })
	}, func() {
		settingRow(c, i18n.T("view.editor.account"), "", func() {
			accts := []kit.Option[int64]{{Value: 0, Label: i18n.T("settings.rules.account.all")}}
			for _, acc := range a.accounts {
				accts = append(accts, kit.Option[int64]{Value: acc.ID, Label: accountName(acc)})
			}
			settingSelect(c, i18n.T("view.editor.account"), &d.Account, accts)
		})
	})
	ui.Column(c).FillWidth().Gap(theme.SectionGap).Children(func() {
		sectionTitle(c, i18n.T("view.editor.conditions"))
		settingsCard(c, func() { e.conditionRows(c, &d.Conditions, &d.Match, "view") })
		if note := e.conditionsNote(); note != "" {
			kit.Text(c, note, t.Text.Xs).TextColor(t.Muted).PaddingX(th.Space(4))
		}
	})
	ui.Column(c).FillWidth().Gap(theme.SectionGap).Children(func() {
		sectionTitle(c, i18n.T("view.editor.groups"))
		for i := range d.Groups {
			e.groupCard(c.Key(d.Groups[i].ID), i)
		}
		kit.Button(c, i18n.T("view.editor.group.add"), kit.ButtonOpts{Variant: kit.Outline, Flat: true, Icon: "plus", Disabled: len(d.Groups) >= core.MaxViewGroups}).
			AlignSelf(ui.Start).OnClick(e.addGroup)
		kit.Text(c, e.groupsNote(), t.Text.Xs).TextColor(t.Muted).PaddingX(th.Space(4))
	})
	if len(e.templates) > 0 {
		ui.Column(c).FillWidth().Gap(theme.SectionGap).Children(func() {
			sectionTitle(c, i18n.T("view.editor.templates"))
			ui.Row(c).FillWidth().Wrap().Gap(th.Space(2)).Children(func() {
				for _, tpl := range e.templates {
					kit.Button(c.Key(tpl.ID), tpl.Name, kit.ButtonOpts{Variant: kit.Outline, Flat: true, Icon: labelIcon(tpl.Icon)}).OnClick(func() { e.useTemplate(tpl) })
				}
			})
		})
	}
	if len(e.exceptions) > 0 {
		e.exceptionsCard(c)
	}
	if e.box.ID != 0 {
		ui.Row(c).FillWidth().Gap(th.Space(2)).Children(func() {
			settingButton(c, i18n.T("common.delete"), kit.Destructive, false).OnClick(func() { b := e.box; e.p.viewDelete = &b })
		})
	}
}

func usesField(conds []core.RuleCondition, fields ...string) bool {
	return slices.ContainsFunc(core.RuleDoc{Conditions: conds}.Rows(), func(cd core.RuleCondition) bool { return slices.Contains(fields, cd.Field) })
}

// conditionsNote says what the view's conditions take in.
func (e *viewEditor) conditionsNote() string {
	conds := e.box.Doc.Conditions
	switch {
	case len(conds) == 0:
		return i18n.T("view.editor.conditions.empty")
	case usesField(conds, core.FieldReply):
		return i18n.T("view.editor.groups.days")
	case usesField(conds, core.FieldRead):
		return ""
	}
	return i18n.T("view.editor.conditions.note")
}

// groupsNote says where conversations go with these groups.
func (e *viewEditor) groupsNote() string {
	d := e.box.Doc
	if len(d.Groups) == 0 {
		return i18n.T("view.editor.groups.none")
	}
	parts := []string{i18n.T("view.editor.groups.first"), i18n.T("view.editor.groups.other")}
	if !usesField(d.Conditions, core.FieldReply) && slices.ContainsFunc(d.Groups, func(g core.ViewGroup) bool { return usesField(g.Conditions, core.FieldReply) }) {
		parts = append(parts, i18n.T("view.editor.groups.days"))
	}
	return strings.Join(parts, " ")
}

// iconButton is the icon chosen, opening the grid of the icons labels use.
func (e *viewEditor) iconButton(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	b := kit.IconButton(c, labelIcon(e.box.Icon), i18n.T("view.editor.icon"), kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeLg, Flat: true}).
		OnClick(func() { e.iconOpen = !e.iconOpen })
	if !e.iconOpen {
		return
	}
	const perRow = 8
	kit.MenuAt(c, b, &e.iconOpen, ui.AnchorBottomRight, ui.AnchorTopRight, theme.PopupOffset, i18n.T("view.editor.icon"), func() {
		ui.Column(c).Gap(th.Space(1)).Children(func() {
			for start := 0; start < len(core.LabelIcons); start += perRow {
				ui.Row(c.Key(start)).Gap(th.Space(1)).Children(func() {
					for _, name := range core.LabelIcons[start:min(start+perRow, len(core.LabelIcons))] {
						on := name == e.box.Icon
						cell := kit.Pressable(c.Key(name), kit.PressableOpts{Bare: true, Selected: on, OnClick: func() { e.box.Icon, e.iconOpen = name, false }}).
							Label(name).Checked(on).Size(theme.LabelIconCell, theme.LabelIconCell).Radius(t.Radius.Lg).Justify(ui.Center).AlignItems(ui.Center).TextColor(t.Foreground)
						if on {
							cell.Background(t.Hover)
						}
						cell.Children(func() { ui.Icon(c, icons.Get("label-"+name)).Size(theme.IconLg, theme.IconLg) })
					}
				})
			}
		})
	})
}

// viewTextField reports whether a condition on f compares typed text.
func viewTextField(f string) bool { return textField(f) || f == core.FieldAny }

// fieldChanged resets cd's comparison and value for its new field, keeping
// typed text between text fields.
func (e *viewEditor) fieldChanged(cd *core.RuleCondition, was string) {
	switch {
	case viewTextField(cd.Field) && viewTextField(was):
		if cd.Field == core.FieldAny && cd.Op == core.OpIs {
			cd.Op = core.OpContains
		}
	case viewTextField(cd.Field):
		cd.Op, cd.Value = core.OpContains, ""
	case cd.Field == core.FieldAttachments:
		cd.Op, cd.Value = core.OpIs, core.RuleYes
	case cd.Field == core.FieldLabel:
		cd.Op, cd.Value = core.OpIs, ""
		if ls := e.p.a.labels; len(ls) > 0 {
			cd.Value = ls[0].Slug
		}
	case cd.Field == core.FieldFolder:
		cd.Op, cd.Value = core.OpIs, ruleRoles[0]
	case cd.Field == core.FieldReply:
		cd.Op, cd.Value = core.OpIs, core.ViewReplyUnreplied
	case cd.Field == core.FieldRead:
		cd.Op, cd.Value = core.OpIs, core.ViewReadUnread
	case cd.Field == core.FieldDate:
		cd.Op, cd.Value = core.OpIs, core.ViewDateThisWeek
	case cd.Field == core.FieldStarred || cd.Field == core.FieldWrote:
		cd.Op, cd.Value = core.OpIs, core.RuleYes
	}
}

// conditionRows is a list of conditions, as a rule's are; key keeps its 添加 menu.
func (e *viewEditor) conditionRows(c *ui.Context, conds *[]core.RuleCondition, match *string, key string) {
	th := c.Theme()
	ui.Column(c).FillWidth().Gap(th.Space(2)).Padding(th.Space(2.5), th.Space(4)).Children(func() {
		condEditor(c, conds, match, boolAt(e.menus, key, false), func(cd *core.RuleCondition) { e.condRow(c, cd) })
	})
}

// condRow is one condition's controls.
func (e *viewEditor) condRow(c *ui.Context, cd *core.RuleCondition) {
	a := e.p.a
	t, th := theme.Of(c), c.Theme()
	var fields []kit.Option[string]
	for _, f := range []string{core.FieldFrom, core.FieldTo, core.FieldSubject, core.FieldBody, core.FieldAny, core.FieldAttachments, core.FieldLabel, core.FieldFolder, core.FieldDate, core.FieldStarred, core.FieldWrote, core.FieldReply, core.FieldRead} {
		fields = append(fields, kit.Option[string]{Value: f, Label: i18n.T("settings.rules.field." + f)})
	}
	contains := kit.Option[string]{Value: core.OpContains, Label: i18n.T("settings.rules.op.contains")}
	notContains := kit.Option[string]{Value: core.OpNotContains, Label: i18n.T("settings.rules.op.not_contains")}
	is := kit.Option[string]{Value: core.OpIs, Label: i18n.T("settings.rules.op.is")}
	labels := make([]kit.Option[string], len(a.labels))
	for i, l := range a.labels {
		labels[i] = kit.Option[string]{Value: l.Slug, Label: l.Name}
	}
	roles := make([]kit.Option[string], len(ruleRoles))
	for i, r := range ruleRoles {
		roles[i] = kit.Option[string]{Value: r, Label: i18n.T("mail.folder." + r)}
	}
	was := cd.Field
	ruleSelect(c, i18n.T("settings.rules.field"), &cd.Field, fields, theme.RuleFieldWidth)
	if cd.Field != was {
		e.fieldChanged(cd, was)
	}
	switch cd.Field {
	case core.FieldAttachments:
		ruleSelect(c, i18n.T("settings.rules.value"), &cd.Value, []kit.Option[string]{
			{Value: core.RuleYes, Label: i18n.T("settings.rules.attachments.yes")}, {Value: core.RuleNo, Label: i18n.T("settings.rules.attachments.no")}}, 0)
	case core.FieldLabel:
		ruleSelect(c, i18n.T("settings.rules.op"), &cd.Op, []kit.Option[string]{is, notContains}, theme.RuleOpWidth)
		ruleSelect(c, i18n.T("settings.rules.label"), &cd.Value, labels, 0)
	case core.FieldFolder:
		ruleSelect(c, i18n.T("settings.rules.op"), &cd.Op, []kit.Option[string]{is, notContains}, theme.RuleOpWidth)
		ruleSelect(c, i18n.T("settings.rules.folder"), &cd.Value, roles, 0)
	case core.FieldReply:
		op := cd.Op
		ruleSelect(c, i18n.T("settings.rules.field.reply"), &cd.Op, []kit.Option[string]{
			{Value: core.OpIs, Label: i18n.T("view.field.reply.unreplied")}, {Value: core.OpOlderThan, Label: i18n.T("view.field.reply.waiting")}}, 0)
		if cd.Op != op {
			cd.Value = core.ViewReplyUnreplied
			if cd.Op == core.OpOlderThan {
				cd.Value = "3"
			}
		}
		if cd.Op == core.OpOlderThan {
			kit.Input(c, &cd.Value, kit.InputOpts{Placeholder: "3"}).Label(i18n.T("settings.rules.days")).Width(th.Space(16))
			kit.Text(c, i18n.T("view.field.reply.days"), t.Text.Xs).Shrink(0)
		}
	case core.FieldDate:
		ruleSelect(c, i18n.T("settings.rules.op"), &cd.Op, []kit.Option[string]{is, {Value: core.OpNotContains, Label: i18n.T("view.op.not")}}, theme.RuleOpWidth)
		days := cd.Value // the 最近几天 option is whatever number is there
		var dates []kit.Option[string]
		for _, d := range viewDateNames {
			dates = append(dates, kit.Option[string]{Value: d, Label: i18n.T("view.date." + d)})
			if cd.Value == d {
				days = "7"
			}
		}
		dates = append(dates, kit.Option[string]{Value: days, Label: i18n.T("view.date.days")})
		ruleSelect(c, i18n.T("settings.rules.field.date"), &cd.Value, dates, 0)
		if days == cd.Value {
			kit.Input(c, &cd.Value, kit.InputOpts{Placeholder: "7"}).Label(i18n.T("settings.rules.days")).Width(th.Space(16))
			kit.Text(c, i18n.T("view.field.reply.days"), t.Text.Xs).Shrink(0)
		}
	case core.FieldStarred:
		ruleSelect(c, i18n.T("settings.rules.field.starred"), &cd.Value, []kit.Option[string]{
			{Value: core.RuleYes, Label: i18n.T("view.starred.yes")}, {Value: core.RuleNo, Label: i18n.T("view.starred.no")}}, 0)
	case core.FieldWrote:
		ruleSelect(c, i18n.T("settings.rules.field.wrote"), &cd.Value, []kit.Option[string]{
			{Value: core.RuleYes, Label: i18n.T("view.wrote.yes")}, {Value: core.RuleNo, Label: i18n.T("view.wrote.no")}}, 0)
	case core.FieldRead:
		ruleSelect(c, i18n.T("settings.rules.field.read"), &cd.Value, []kit.Option[string]{
			{Value: core.ViewReadUnread, Label: i18n.T("view.field.read.unread")}, {Value: core.ViewReadRead, Label: i18n.T("view.field.read.read")}}, 0)
	default:
		ops := []kit.Option[string]{contains, notContains, is}
		if cd.Field == core.FieldAny {
			ops = ops[:2]
		}
		ruleSelect(c, i18n.T("settings.rules.op"), &cd.Op, ops, theme.RuleOpWidth)
		ph := "settings.rules.value.text"
		if cd.Field == core.FieldFrom || cd.Field == core.FieldTo {
			ph = "settings.rules.value.addr"
		}
		kit.Input(c, &cd.Value, kit.InputOpts{Placeholder: i18n.T(ph)}).Label(i18n.T("settings.rules.value")).Grow(1).Shrink(1).Basis(0).MinWidth(0)
	}
}

// groupCard is group i: its name, how its conditions combine, moves and
// removal, then its conditions.
func (e *viewEditor) groupCard(c *ui.Context, i int) {
	t, th := theme.Of(c), c.Theme()
	d := &e.box.Doc
	g := &d.Groups[i]
	settingsCard(c, func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(th.Space(2)).Padding(th.Space(2), th.Space(4)).Children(func() {
			kit.Input(c, &g.Name, kit.InputOpts{Placeholder: i18n.T("view.editor.group.name")}).Label(i18n.T("view.editor.group.name")).Width(theme.SettingsSelectMin)
			ui.Spacer(c)
			kit.IconButton(c, "arrow-up", i18n.T("view.editor.group.up"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm, Disabled: i == 0}).
				OnClick(func() { d.Groups[i-1], d.Groups[i] = d.Groups[i], d.Groups[i-1] })
			kit.IconButton(c, "arrow-down", i18n.T("view.editor.group.down"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm, Disabled: i == len(d.Groups)-1}).
				OnClick(func() { d.Groups[i], d.Groups[i+1] = d.Groups[i+1], d.Groups[i] })
			kit.IconButton(c, "trash-2", i18n.T("view.editor.group.remove"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).
				OnClick(func() { d.Groups = slices.Delete(slices.Clone(d.Groups), i, i+1) })
		})
	}, func() {
		e.conditionRows(c, &g.Conditions, &g.Match, g.ID)
		if len(g.Conditions) == 0 {
			kit.Text(c, i18n.T("view.editor.group.empty"), t.Text.Xs).TextColor(t.Muted).Padding(0, th.Space(4), th.Space(2.5), th.Space(4))
		}
	})
}

// addGroup adds a group that shows what needs a reply, the usual first one.
func (e *viewEditor) addGroup() {
	d := &e.box.Doc
	id := ""
	for n := len(d.Groups) + 1; ; n++ {
		id = fmt.Sprintf("g%d", n)
		if !slices.ContainsFunc(d.Groups, func(g core.ViewGroup) bool { return g.ID == id }) {
			break
		}
	}
	d.Groups = append(d.Groups, core.ViewGroup{ID: id, Match: core.MatchAll,
		Conditions: []core.RuleCondition{{Field: core.FieldReply, Op: core.OpIs, Value: core.ViewReplyUnreplied}}})
}

// useTemplate fills the form from tpl.
func (e *viewEditor) useTemplate(tpl core.ViewTemplate) {
	e.box.Name, e.box.Icon, e.box.Doc, e.note = tpl.Name, tpl.Icon, cloneViewDoc(tpl.Doc), ""
}

// exceptionsCard lists what was added or removed by hand, each to take back.
func (e *viewEditor) exceptionsCard(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	rows := make([]func(), len(e.exceptions))
	for i, x := range e.exceptions {
		rows[i] = func() {
			ui.Row(c.Key(x.ThreadID)).FillWidth().AlignItems(ui.Center).Gap(th.Space(3)).Padding(th.Space(2.5), th.Space(4)).Children(func() {
				word := i18n.T("view.exception.added")
				if x.Kind == core.ViewExceptionRemoved {
					word = i18n.T("view.exception.removed")
				}
				kit.Text(c, word, t.Text.Xs).TextColor(t.Muted).Shrink(0)
				kit.Text(c, i18n.Tf("view.exception.row", x.From, cmpSubject(x.Subject)), t.Text.Sm).SingleLine().Grow(1).Shrink(1)
				settingButton(c, i18n.T("compose.undo"), kit.Ghost, false).OnClick(func() { e.undoException(x.ThreadID) })
			})
		}
	}
	settingsSection(c, i18n.T("view.editor.exceptions"), "", rows...)
}

func cmpSubject(s string) string {
	if s == "" {
		return i18n.T("mail.no_subject")
	}
	return s
}

func (e *viewEditor) undoException(thread int64) {
	p, id := e.p, e.box.ID
	p.setViewExceptions(id, []int64{thread}, "", func() {
		e.exceptions = slices.DeleteFunc(slices.Clone(e.exceptions), func(x core.ViewException) bool { return x.ThreadID == thread })
		e.prevKey = "" // the preview applies them
	})
}

// preview is 实时预览: what the document shows, as the list will show it.
func (e *viewEditor) preview(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	kit.Card(c, ui.Column(c), kit.ToneTint).FillWidth().Gap(th.Space(2)).Padding(th.Space(3)).Children(func() {
		total := 0
		if e.prev != nil {
			for _, g := range e.prev.Groups {
				total += g.Total
			}
		}
		ui.Row(c).FillWidth().AlignItems(ui.Baseline).Gap(th.Space(2)).PaddingX(th.Space(1)).Children(func() {
			kit.Text(c, i18n.T("view.preview"), t.Text.Sm).FontWeight(600).Grow(1)
			if e.prev != nil {
				kit.Text(c, i18n.Tf("view.count", total), t.Text.Xs).TextColor(t.Muted)
			}
		})
		note := func(title, desc string) {
			kit.Block(c, ui.Column(c), kit.ToneOutline).FillWidth().Gap(th.Space(1)).Padding(th.Space(4), th.Space(3)).
				AlignItems(ui.Center).Children(func() {
				kit.Text(c, title, t.Text.Xs).FontWeight(500).TextAlign(ui.Center)
				kit.Text(c, desc, t.Text.Xs).TextColor(t.Muted).TextAlign(ui.Center)
			})
		}
		switch {
		case e.prev == nil && (e.prevStop != nil || e.prevCancel != nil):
			note(i18n.T("settings.rules.preview.wait"), "")
		case e.prev == nil:
			note(i18n.T("view.preview.none"), i18n.T("view.preview.none.desc"))
		case total == 0:
			note(i18n.T("view.empty"), i18n.T("view.preview.empty.desc"))
		default:
			now := e.p.a.clock.Now()
			kit.Block(c, ui.Column(c), kit.ToneOutline).FillWidth().Clip().Children(func() {
				grouped := len(e.box.Doc.Groups) > 0
				for gi, g := range e.prev.Groups {
					if grouped {
						if gi > 0 {
							kit.Separator(c.Key("sep:"+g.ID), false)
						}
						ui.Row(c.Key("head:"+g.ID)).FillWidth().AlignItems(ui.Center).Gap(th.Space(2)).Padding(th.Space(1.5), th.Space(3)).Children(func() {
							name := g.Name
							if name == "" {
								name = i18n.T("view.other")
							}
							kit.Text(c, name, t.Text.Xs).FontWeight(500).TextColor(t.Muted).Grow(1).Shrink(1).SingleLine()
							kit.Text(c, strconv.Itoa(g.Total), t.Text.Xs).TextColor(t.Muted).Shrink(0)
						})
					}
					for _, r := range g.Items {
						kit.Separator(c.Key(fmt.Sprintf("s:%d", r.ID)), false)
						ui.Column(c.Key(r.ID)).FillWidth().Gap(th.Space(0.5)).Padding(th.Space(2), th.Space(3)).Children(func() {
							ui.Row(c).FillWidth().Gap(th.Space(2)).Children(func() {
								kit.Text(c, strings.Join(r.Participants, ", "), t.Text.Xs).FontWeight(500).SingleLine().Grow(1).Shrink(1)
								kit.Text(c, listTime(now, r.LastDate), t.Text.Xs).TextColor(t.Muted).Shrink(0)
							})
							kit.Text(c, cmpSubject(r.Subject), t.Text.Xs).SingleLine()
						})
					}
				}
			})
		}
	})
}
