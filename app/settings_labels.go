package app

import (
	"context"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// rescanPoll is how often a running rescan's progress is read.
const rescanPoll = 2 * time.Second

// rescanDays are the rescan range's choices; 0 is everything synced.
var rescanDays = []int{7, 30, 90, 0}

// labelsView is Settings › Labels: the labels, the edit dialog, auto-labeling
// and rescans.
type labelsView struct {
	a *App

	dlg, confirm            bool  // the edit dialog; the delete confirmation over it
	editID                  int64 // 0: a new label
	name, desc, color, icon string

	order ui.ListState // the labels' list, its rows dragged to reorder them

	days    int // the rescan range, a rescanDays value
	mode    int // 0 unlabeled only, 1 judge all again
	started bool
	total   int // the running rescan's messages; pending of them left
	pending int
	poll    func() // stops the progress poll
}

func newLabelsView(a *App) *labelsView {
	return &labelsView{a: a, days: 30}
}

func (v *labelsView) view(c *ui.Context, p *settingsPage) {
	p.syncSettings()
	defer p.syncSettings()
	a := v.a
	pageHeading(c, i18n.T("settings.labels"), i18n.T("settings.labels.sub"), func() {
		settingButton(c, i18n.T("settings.labels.new"), kit.Outline, false).OnClick(func() { v.open(core.Label{Color: "gray", Icon: "folder-3"}) })
	})
	settingsSection(c, i18n.T("settings.labels.list"), i18n.T("settings.labels.list.foot"), func() { v.list(c) })

	display := p.st.LabelDisplay
	if display == "" {
		display = core.LabelDisplayBoth
	}
	settingsSection(c, i18n.T("settings.labels.display"), "",
		func() {
			settingRow(c, i18n.T("settings.labels.display.style"), i18n.T("settings.labels.display.style.help"), func() {
				opts := []kit.Option[string]{
					{Value: core.LabelDisplayBoth, Label: i18n.T("settings.labels.display.both")},
					{Value: core.LabelDisplayIcon, Label: i18n.T("settings.labels.display.icon")},
					{Value: core.LabelDisplayText, Label: i18n.T("settings.labels.display.text")},
				}
				settingSelect(c, i18n.T("settings.labels.display.style"), &display, opts)
				p.st.LabelDisplay = display
			})
		})
	settingsSection(c, i18n.T("settings.labels.auto"), "",
		func() {
			kit.SwitchRow(c, &p.st.AutoLabel, i18n.T("settings.labels.auto.on"), i18n.T("settings.labels.auto.on.help"))
		},
		func() {
			settingRow(c, i18n.T("settings.labels.model"), i18n.T("settings.labels.model.help"), func() {
				value := i18n.T("settings.labels.model.value")
				if !a.taskModels["labels"] {
					value = i18n.T("settings.labels.model.none")
				}
				kit.Text(c, value, theme.Of(c).Text.Body).TextColor(theme.Of(c).Muted)
				settingButton(c, i18n.T("settings.labels.model.go"), kit.Outline, false).OnClick(func() { a.navigate(Route{Page: PageSettings, Settings: "models"}) })
			})
		})
	v.rescan(c)
	v.dialog(c)
}

// list is the labels, one row each, in a List: dragging a row shows
// where it would go and moves it there (ListState.Reorder). The List is as
// tall as its rows, up to half the window.
func (v *labelsView) list(c *ui.Context) {
	a, th := v.a, c.Theme()
	n := len(a.labels)
	v.order.Key = func(i int) any { return a.labels[i].ID }
	v.order.Label = func(i int) string { return a.labels[i].Name }
	v.order.Reorder = func(rows []int, to int) { v.move(rows[0], to) }
	h := float32(n)*th.Space(12) + float32(max(n-1, 0))*theme.Stroke
	// A drag scrolls only the list under it, never the page: past half the window
	// the list scrolls itself, so both its edges fit on screen to drag toward.
	_, wh := c.Size()
	h = min(h, wh/2)
	kit.List(c, &v.order, n, func(i int) {
		ui.Column(c).FillWidth().Material(kit.Frost(theme.Of(c).Card)).Children(func() {
			if i > 0 {
				hairline(c)
			}
			v.row(c, a.labels[i])
		})
	}).FillWidth().Height(h)
}

// row is a label: grip, chip, description, built-in mark, count, edit.
func (v *labelsView) row(c *ui.Context, l core.Label) {
	t, th := theme.Of(c), c.Theme()
	row := ui.Row(c).FillWidth().Gap(th.Space(3)).Padding(th.Space(2), th.Space(4)).Height(th.Space(12)).AlignItems(ui.Center)
	row.Children(func() {
		ui.Icon(c, icons.Get("grip")).Size(theme.Icon, theme.Icon).TextColor(t.Idle)
		ui.Row(c).Width(th.Space(28)).Children(func() {
			kit.LabelChip(c, kit.LabelChipOpts{Text: l.Name, Icon: l.Icon, Color: t.Label(l.Color)})
		})
		desc, color := l.Description, t.Muted
		if desc == "" {
			desc, color = i18n.T("settings.labels.no_desc"), t.Idle
		}
		kit.Text(c, desc, t.Text.Sm).TextColor(color).SingleLine().Grow(1).Shrink(1)
		if l.Builtin {
			kit.Badge(c, i18n.T("settings.labels.builtin")).Background(t.Background)
		}
		kit.Text(c, i18n.Tf("settings.labels.count", l.Count), t.Text.Sm).TextColor(t.Muted)
		kit.IconButton(c, "file-pen", i18n.Tf("settings.labels.edit", l.Name), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).
			OnClick(func() { v.open(l) })
	})
}

// move puts the label at i before the one at to (len for the end), the
// others in order.
func (v *labelsView) move(i, to int) {
	a := v.a
	if to > i {
		to-- // i is taken out first
	}
	if to == i {
		return
	}
	ids := make([]int64, len(a.labels))
	for k, l := range a.labels {
		ids[k] = l.ID
	}
	id := ids[i]
	ids = slices.Insert(slices.Delete(ids, i, i+1), to, id)
	moved := slices.Clone(a.labels) // at once; the change event reads them again
	l := moved[i]
	a.labels = slices.Insert(slices.Delete(moved, i, i+1), to, l)
	a.writes.Chain("labels", func(ctx context.Context) error { return a.svc.ReorderLabels(ctx, ids) }, a.toastErr)
}

func (v *labelsView) open(l core.Label) {
	v.dlg, v.confirm, v.editID = true, false, l.ID
	v.name, v.desc, v.color, v.icon = l.Name, l.Description, l.Color, l.Icon
}

// taken reports whether another label has the name.
func (v *labelsView) taken(name string) bool {
	name = strings.TrimSpace(name)
	return slices.ContainsFunc(v.a.labels, func(l core.Label) bool { return l.ID != v.editID && strings.EqualFold(l.Name, name) })
}

// dialog edits a label or makes one: a preview, name, description, color, icon.
func (v *labelsView) dialog(c *ui.Context) {
	a := v.a
	t, th := theme.Of(c), c.Theme()
	title, save := i18n.T("settings.labels.dialog.edit"), i18n.T("common.save")
	if v.editID == 0 {
		title, save = i18n.T("settings.labels.dialog.new"), i18n.T("settings.labels.create")
	}
	taken := v.taken(v.name)
	long := utf8.RuneCountInString(strings.TrimSpace(v.name)) > core.MaxLabelName
	descErr := ""
	if utf8.RuneCountInString(strings.TrimSpace(v.desc)) > core.MaxLabelDescription {
		descErr = i18n.Tf("settings.labels.desc.long", core.MaxLabelDescription)
	}
	nameErr := ""
	switch {
	case taken:
		nameErr = i18n.T("settings.labels.name.exists")
	case long:
		nameErr = i18n.Tf("settings.labels.name.long", core.MaxLabelName)
	}
	kit.Dialog(c, &v.dlg, title, kit.DialogOpts{Desc: i18n.T("settings.labels.dialog.sub"), Close: i18n.T("common.close")}, func() {
		preview := cmpName(v.name)
		ui.Row(c).Gap(th.Space(6)).Padding(th.Space(3)).Radius(t.Radius.Lg).Background(t.Sidebar).Children(func() {
			for _, auto := range []bool{false, true} {
				caption := i18n.T("settings.labels.preview.manual")
				if auto {
					caption = i18n.T("settings.labels.preview.auto")
				}
				ui.Row(c).Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
					kit.LabelChip(c, kit.LabelChipOpts{Text: preview, Icon: v.icon, Color: t.Label(v.color), Auto: auto})
					kit.Text(c, caption, t.Text.Sm).TextColor(t.Muted)
				})
			}
		})
		kit.Field(c, i18n.T("settings.labels.name"), "", nameErr, func() {
			kit.Input(c, &v.name, kit.InputOpts{Placeholder: i18n.T("settings.labels.name.placeholder"), Invalid: nameErr != ""}).Label(i18n.T("settings.labels.name"))
		})
		kit.Field(c, i18n.T("settings.labels.desc"), "", descErr, func() {
			kit.TextArea(c, &v.desc, kit.InputOpts{Placeholder: i18n.T("settings.labels.desc.placeholder"), Invalid: descErr != ""}).Label(i18n.T("settings.labels.desc"))
		})
		kit.Field(c, i18n.T("settings.labels.color"), "", "", func() {
			// One row across the field: the gap is a floor, the space left spreads between.
			ui.Row(c).FillWidth().Justify(ui.SpaceBetween).Gap(th.Space(2)).Children(func() {
				for _, name := range core.LabelColors {
					on := name == v.color
					dot := t.Label(name).Dot
					sw := kit.Pressable(c.Key(name), kit.PressableOpts{Bare: true, Selected: on, OnClick: func() { v.color = name }}).
						Label(i18n.T("label.color."+name)).Checked(on).Size(theme.LabelSwatch, theme.LabelSwatch).Radius(t.Radius.Full).
						Justify(ui.Center).AlignItems(ui.Center)
					if on {
						sw.Border(theme.LabelSwatchRing, dot)
					}
					sw.Children(func() {
						ui.Box(c).Size(theme.LabelSwatchDot, theme.LabelSwatchDot).Radius(t.Radius.Full).Background(dot)
					})
				}
			})
		})
		kit.Field(c, i18n.T("settings.labels.icon"), "", "", func() {
			const perRow = 8
			ui.Column(c).Gap(th.Space(1)).Children(func() {
				for start := 0; start < len(core.LabelIcons); start += perRow {
					ui.Row(c.Key(start)).Gap(th.Space(1)).Children(func() {
						for _, name := range core.LabelIcons[start:min(start+perRow, len(core.LabelIcons))] {
							on := name == v.icon
							cell := kit.Pressable(c.Key(name), kit.PressableOpts{Bare: true, Selected: on, OnClick: func() { v.icon = name }}).
								Label(name).Checked(on).Height(theme.LabelIconCell).Grow(1).Radius(t.Radius.Lg).Justify(ui.Center).AlignItems(ui.Center).
								TextColor(t.Foreground)
							if on {
								cell.Background(t.Label(v.color).Fill).TextColor(t.Label(v.color).Text)
							}
							cell.Children(func() { ui.Icon(c, icons.Get("label-"+name)).Size(theme.IconLg, theme.IconLg) })
						}
					})
				}
			})
		})
	}, func() {
		if v.editID != 0 {
			kit.Button(c, i18n.T("settings.labels.delete"), kit.ButtonOpts{Variant: kit.Destructive}).OnClick(func() { v.confirm = true })
			ui.Spacer(c)
		}
		kit.Button(c, i18n.T("common.cancel"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(func() { v.dlg = false })
		kit.Button(c, save, kit.ButtonOpts{Disabled: nameErr != "" || descErr != "" || strings.TrimSpace(v.name) == ""}).OnClick(v.save)
	})
	if !v.confirm {
		return
	}
	l, _ := a.label(v.editID)
	kit.Dialog(c, &v.confirm, i18n.Tf("settings.labels.delete.confirm", l.Name),
		kit.DialogOpts{Size: kit.DialogSm, Desc: i18n.Tf("settings.labels.delete.confirm.help", l.Count), Close: i18n.T("common.close")}, nil, func() {
			kit.Button(c, i18n.T("common.cancel"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(func() { v.confirm = false })
			kit.Button(c, i18n.T("common.delete"), kit.ButtonOpts{Variant: kit.Destructive}).OnClick(func() {
				id := v.editID
				v.confirm, v.dlg = false, false
				a.writes.Chain("labels", func(ctx context.Context) error { return a.svc.DeleteLabel(ctx, id) }, a.toastErr)
			})
		})
}

// cmpName is the preview's text: the name, or a placeholder while empty.
func cmpName(name string) string {
	if name = strings.TrimSpace(name); name != "" {
		return name
	}
	return i18n.T("settings.labels.untitled")
}

func (v *labelsView) save() {
	a := v.a
	id, in := v.editID, core.LabelInput{Name: v.name, Description: v.desc, Color: v.color, Icon: v.icon}
	v.dlg = false
	a.writes.Chain("labels", func(ctx context.Context) (err error) {
		if id == 0 {
			_, err = a.svc.CreateLabel(ctx, in)
		} else {
			_, err = a.svc.UpdateLabel(ctx, id, in)
		}
		return err
	}, a.toastErr)
}

// rescan is the rescan section: range, mode, and start, or the progress and stop.
func (v *labelsView) rescan(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	opts := make([]kit.Option[int], len(rescanDays))
	for i, d := range rescanDays {
		opts[i] = kit.Option[int]{Value: d, Label: i18n.Tf("settings.labels.rescan.days", d)}
		if d == 0 {
			opts[i].Label = i18n.T("settings.labels.rescan.all_range")
		}
	}
	help := i18n.T("settings.labels.rescan.missing.help")
	if v.mode == 1 {
		help = i18n.T("settings.labels.rescan.redo.help")
	}
	settingsSection(c, i18n.T("settings.labels.rescan"), i18n.T("settings.labels.rescan.sub"),
		func() {
			settingRow(c, i18n.T("settings.labels.rescan.range"), "", func() {
				settingSelect(c, i18n.T("settings.labels.rescan.range"), &v.days, opts)
			})
		},
		func() {
			settingRow(c, i18n.T("settings.labels.rescan.mode"), help, func() {
				kit.Tabs(c, &v.mode, i18n.T("settings.labels.rescan.missing"), i18n.T("settings.labels.rescan.redo"))
			})
		},
		func() {
			settingBlock(c, func() {
				if !v.started {
					ready := v.a.taskModels["labels"]
					ui.Row(c).Gap(th.Space(6)).AlignItems(ui.Center).Children(func() {
						if ready {
							ui.Spacer(c)
						} else {
							kit.Text(c, i18n.T("settings.labels.rescan.no_model"), t.Text.Sm).TextColor(t.Muted).Grow(1).Shrink(1)
						}
						settingButton(c, i18n.T("settings.labels.rescan.start"), kit.Default, !ready).OnClick(v.start)
					})
					return
				}
				done := v.total - v.pending
				ui.Row(c).Gap(th.Space(6)).AlignItems(ui.Center).Children(func() {
					ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(1.5)).Children(func() {
						kit.Text(c, i18n.Tf("settings.labels.rescan.running", done, v.total), t.Text.Sm)
						ui.Row(c).FillWidth().Height(th.Space(1)).Radius(t.Radius.Full).Background(t.Background).Clip().
							Label(i18n.T("settings.labels.rescan.progress")).Children(func() {
							ui.Box(c).Grow(float32(done)).Background(t.Primary)
							ui.Box(c).Grow(float32(v.pending))
						})
					})
					settingButton(c, i18n.T("settings.labels.rescan.stop"), kit.Outline, false).OnClick(v.stop)
				})
			})
		})
}

func (v *labelsView) start() {
	a := v.a
	days, all := v.days, v.mode == 1
	var n int
	a.do(func(ctx context.Context) (err error) {
		n, err = a.svc.RescanLabels(ctx, days, all)
		return err
	}, func(err error) {
		if err != nil {
			a.toastErr(err)
			return
		}
		if n == 0 {
			return
		}
		v.started, v.total, v.pending = true, n, n
		v.schedule()
	})
}

// schedule reads the progress after rescanPoll; idle again at none left.
func (v *labelsView) schedule() {
	a := v.a
	v.poll = a.after(rescanPoll, func() {
		var n int
		a.do(func(ctx context.Context) (err error) {
			n, err = a.svc.PendingLabels(ctx)
			return err
		}, func(err error) {
			if !v.started {
				return
			}
			if err == nil {
				v.pending = min(n, v.total)
			}
			if v.pending == 0 {
				v.started = false
				return
			}
			v.schedule()
		})
	})
}

func (v *labelsView) stop() {
	a := v.a
	if v.poll != nil {
		v.poll()
	}
	v.started = false
	a.writes.Chain("labels", a.svc.StopRescan, a.toastErr)
}
