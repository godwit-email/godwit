package app

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// maskLogFilter is what 脱敏记录 lists: the result picked by its tiles
// (maskAll, MaskBlock: the blocked requests, MaskWarn: the sent ones that
// held a value sent as it was, MaskMask: the sent ones that masked one),
// a category ("" all, maskOwn the user's own) and a feature ("" all).
type maskLogFilter struct {
	result, cat, feature string
	open                 map[string]bool // the requests shown open, by maskRequest.key
	clearing             bool            // 清空记录's confirmation is open
	pages                int             // 显示更多 pressed: how many pages past the first are shown
}

// maskLogPage is how many requests show at a time; the tiles count them all.
const maskLogPage = 50

const (
	maskAll = ""
	maskOwn = "own"
)

// maskRequest is one request's records, newest first as the log is.
type maskRequest struct {
	key     string
	at      time.Time
	feature string
	values  []core.MaskRecord
}

// maskRequests groups log by request. A record kept from before requests
// were numbered goes with the others of its second and feature.
func maskRequests(log []core.MaskRecord) []maskRequest {
	var out []maskRequest
	at := map[string]int{}
	for _, r := range log {
		key := strconv.FormatInt(r.Request, 10)
		if r.Request == 0 {
			key = fmt.Sprintf("0:%d:%s", r.At.Unix(), r.Feature)
		}
		if i, ok := at[key]; ok {
			out[i].values = append(out[i].values, r)
			continue
		}
		at[key] = len(out)
		out = append(out, maskRequest{key: key, at: r.At, feature: r.Feature, values: []core.MaskRecord{r}})
	}
	for i := range out {
		slices.Reverse(out[i].values) // in the order the request held them
	}
	return out
}

// blocked: a value blocked the request, so none of its values was sent.
func (q maskRequest) blocked() bool {
	return slices.ContainsFunc(q.values, func(r core.MaskRecord) bool { return r.Action == core.MaskBlock })
}

// sent counts the values a sent request held that were done with as action.
func (q maskRequest) sent(action string) int {
	if q.blocked() {
		return 0
	}
	n := 0
	for _, r := range q.values {
		if r.Action == action {
			n++
		}
	}
	return n
}

func (q maskRequest) holds(cat string) bool {
	if cat == maskOwn {
		cat = ""
	}
	return slices.ContainsFunc(q.values, func(r core.MaskRecord) bool { return r.Category == cat })
}

func (q maskRequest) shows(f maskLogFilter) bool {
	switch f.result {
	case core.MaskBlock:
		if !q.blocked() {
			return false
		}
	case core.MaskWarn, core.MaskMask:
		if q.sent(f.result) == 0 {
			return false
		}
	}
	return (f.cat == "" || q.holds(f.cat)) && (f.feature == "" || q.feature == f.feature)
}

// maskLogSummary is the entry row's line under 查看脱敏记录.
func maskLogSummary(reqs []maskRequest) string {
	if len(reqs) == 0 {
		return i18n.T("settings.masking.log.empty")
	}
	blocked, warned := 0, 0
	for _, q := range reqs {
		if q.blocked() {
			blocked++
		}
		warned += q.sent(core.MaskWarn)
	}
	return i18n.Tf("settings.masking.log.summary", len(reqs), blocked, warned)
}

// maskLogEntry is the 隐私脱敏 tab's way into 脱敏记录.
func (p *settingsPage) maskLogEntry(c *ui.Context) {
	t, th, a := theme.Of(c), c.Theme(), p.a
	settingsSection(c, i18n.T("settings.masking.log"), "", func() {
		b := kit.Pressable(c, kit.PressableOpts{OnClick: func() { a.navigate(Route{Page: PageSettings, Settings: "masklog"}) }}).
			FillWidth().MinHeight(th.Space(12)).Padding(th.Space(2), th.Space(4)).Gap(th.Space(6)).AlignItems(ui.Center).Radius(0).Label(i18n.T("settings.masking.log.open"))
		b.Children(func() {
			ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(0.5)).Children(func() {
				kit.Text(c, i18n.T("settings.masking.log.open"), t.Text.Body).FontWeight(500)
				kit.Text(c, maskLogSummary(maskRequests(p.maskLog)), t.Text.Sm).TextColor(t.Muted)
			})
			ui.Icon(c, icons.Get("chevron-right")).Size(theme.Icon, theme.Icon).TextColor(t.Muted)
		})
	})
}

// settingsBack is the link back up from a settings subpage (a signature,
// 脱敏记录): a ghost button, muted until hovered, pulled left by its padding
// so its text lines up with the title under it.
func settingsBack(c *ui.Context, label string, onClick func()) {
	t, th := theme.Of(c), c.Theme()
	back := kit.Pressable(c, kit.PressableOpts{OnClick: onClick}).
		Label(label).AlignSelf(ui.Start).Height(th.Space(7)).PaddingX(th.Space(2)).Margin(0, 0, 0, -th.Space(2)).
		Gap(th.Space(1.5)).AlignItems(ui.Center).Radius(t.Radius.Md)
	fg := t.Muted
	if back.Hovered() {
		fg = t.Foreground
	}
	back.TextColor(fg).Children(func() {
		ui.Icon(c, icons.Get("arrow-left")).Size(theme.Icon, theme.Icon)
		kit.Text(c, label, t.Text.Base)
	})
}

// maskLogPage is 脱敏记录, a subpage of 隐私与安全: tiles that count and
// pick the requests by what was done, the category and feature filters, and
// the requests by day, each opening on the values it held.
func (p *settingsPage) maskLogPage(c *ui.Context) {
	t, th, a := theme.Of(c), c.Theme(), p.a
	f := &p.logFilter
	reqs := maskRequests(p.maskLog)
	ui.Column(c).Gap(theme.HeadingGap).Children(func() {
		settingsBack(c, i18n.T("settings.privacy"), func() { a.navigate(Route{Page: PageSettings, Settings: "privacy"}) })
		pageHeading(c, i18n.T("settings.masking.log"), i18n.T("settings.masking.log.foot"), func() {
			settingButton(c, i18n.T("settings.masking.log.clear"), kit.Outline, len(reqs) == 0).OnClick(func() { f.clearing = true })
		})
	})

	blocked, warned, masked := 0, 0, 0
	for _, q := range reqs {
		if q.blocked() {
			blocked++
		}
		warned += q.sent(core.MaskWarn)
		masked += q.sent(core.MaskMask)
	}
	ui.Row(c).FillWidth().Gap(th.Space(2)).Children(func() {
		for _, tile := range []struct {
			result, key string
			n           int
			color       ui.Color
		}{{maskAll, "all", len(reqs), t.Foreground}, {core.MaskBlock, "blocked", blocked, t.DangerText}, {core.MaskWarn, "warned", warned, t.WarningText}, {core.MaskMask, "masked", masked, t.Foreground}} {
			label := i18n.T("settings.masking.log." + tile.key)
			b := kit.Pressable(c, kit.PressableOpts{Bare: true, OnClick: func() { f.result = tile.result }}).
				Grow(1).Shrink(1).Basis(0).Padding(th.Space(3)).Radius(t.Radius.Xl).Background(t.Card).Label(label)
			if f.result == tile.result {
				b.Border(theme.Stroke, t.Foreground)
			} else {
				b.Border(theme.Stroke, t.Card)
			}
			b.Children(func() {
				ui.Column(c).Gap(th.Space(0.5)).Children(func() {
					kit.Text(c, label, t.Text.Sm).TextColor(t.Muted)
					color := tile.color
					if tile.n == 0 {
						color = t.Muted
					}
					kit.Text(c, strconv.Itoa(tile.n), t.Text.Xl).FontWeight(600).TextColor(color)
					kit.Text(c, i18n.T("settings.masking.log."+tile.key+".help"), t.Text.Sm).TextColor(t.Muted)
				})
			})
		}
	})

	cats := []kit.Option[string]{{Value: "", Label: i18n.T("settings.masking.log.cat.all")}}
	for name := range (&core.MaskCategories{}).All() {
		cats = append(cats, kit.Option[string]{Value: name, Label: maskCategoryName(name)})
	}
	cats = append(cats, kit.Option[string]{Value: maskOwn, Label: maskCategoryName("")})
	features := []kit.Option[string]{{Value: "", Label: i18n.T("settings.masking.log.feature.all")}}
	for _, name := range maskFeatures {
		if slices.ContainsFunc(reqs, func(q maskRequest) bool { return q.feature == name }) {
			features = append(features, kit.Option[string]{Value: name, Label: maskFeatureName(name)})
		}
	}
	ui.Row(c).Gap(th.Space(2)).Children(func() {
		settingSelect(c, i18n.T("settings.masking.log.cat.all"), &f.cat, cats)
		settingSelect(c, i18n.T("settings.masking.log.feature.all"), &f.feature, features)
	})

	now := a.clock.Now()
	var day string
	var rows []func()
	flush := func() {
		if len(rows) == 0 {
			return
		}
		title, rs := day, rows
		ui.Column(c).Gap(theme.SectionGap).Children(func() {
			kit.Text(c, title, t.Text.Sm).TextColor(t.Muted).PaddingX(th.Space(4))
			settingsCard(c, rs...)
		})
		rows = nil
	}
	shown, limit := 0, maskLogPage*(f.pages+1)
	for _, q := range reqs {
		if !q.shows(*f) {
			continue
		}
		if shown++; shown > limit {
			continue
		}
		if d := maskLogDay(now, q.at); d != day {
			flush()
			day = d
		}
		rows = append(rows, func() { p.maskRequestRow(c.Key(q.key), q) })
	}
	flush()
	if shown > limit {
		ui.Row(c).Justify(ui.Center).Children(func() {
			kit.Button(c, i18n.T("settings.masking.log.more"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(func() { f.pages++ })
		})
	}
	if shown == 0 {
		msg := i18n.T("settings.masking.log.empty")
		if len(reqs) > 0 {
			msg = i18n.T("settings.masking.log.none")
		}
		settingsCard(c, func() { settingBlock(c, func() { ui.Text(c, msg).TextColor(t.Muted) }) })
	}
	kit.Text(c, i18n.T("settings.masking.log.short"), t.Text.Sm).TextColor(t.Muted).PaddingX(th.Space(4))

	if f.clearing {
		dismiss := func() { f.clearing = false }
		kit.Dialog(c, &f.clearing, i18n.T("settings.masking.log.clear.title"), kit.DialogOpts{Size: kit.DialogSm, Alert: true, Desc: i18n.T("settings.masking.log.clear.body"), Close: i18n.T("common.close")}, nil, func() {
			kit.Button(c, i18n.T("common.cancel"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(dismiss)
			kit.Button(c, i18n.T("settings.masking.log.clear"), kit.ButtonOpts{Variant: kit.Destructive}).OnClick(func() {
				dismiss()
				a.do(func(ctx context.Context) error { return a.svc.ClearMaskingLog(ctx) }, func(err error) {
					if err != nil {
						a.toastErr(err)
					}
					p.readMaskLog()
				})
			})
		})
	}
}

// maskRequestRow is a request: its time, feature and the categories it
// held, flagged when it was blocked or sent a value as it was; open, the
// values under it.
func (p *settingsPage) maskRequestRow(c *ui.Context, q maskRequest) {
	t, th, f := theme.Of(c), c.Theme(), &p.logFilter
	open := f.open[q.key]
	ui.Column(c).FillWidth().Children(func() {
		feature := maskFeatureName(q.feature)
		b := kit.Pressable(c, kit.PressableOpts{OnClick: func() {
			if f.open == nil {
				f.open = map[string]bool{}
			}
			f.open[q.key] = !open
		}}).FillWidth().MinHeight(th.Space(11)).Padding(th.Space(2), th.Space(4)).Gap(th.Space(3)).AlignItems(ui.Center).Radius(0).Label(feature)
		b.Children(func() {
			kit.Text(c, q.at.In(p.a.clock.Now().Location()).Format("15:04"), t.Text.Sm).Width(th.Space(10)).Shrink(0).TextColor(t.Muted)
			kit.Text(c, feature, t.Text.Body).FontWeight(500).Grow(1).Shrink(1)
			per := map[string]int{}
			var order []string
			for _, r := range q.values {
				if per[r.Category] == 0 {
					order = append(order, r.Category)
				}
				per[r.Category]++
			}
			for _, cat := range order {
				statusPill(c, maskCategoryName(cat)+" ×"+strconv.Itoa(per[cat]), false)
			}
			switch {
			case q.blocked():
				dangerPill(c, i18n.T("settings.masking.log.flag.blocked"))
			case q.sent(core.MaskWarn) > 0:
				statusPill(c, i18n.T("settings.masking.log.flag.warned"), true)
			}
			turn := float32(0)
			if open {
				turn = 180
			}
			ui.Icon(c, icons.Get("chevron-down")).Size(theme.IconSm, theme.IconSm).Shrink(0).Rotate(turn).TextColor(t.Muted)
		})
		if !open {
			return
		}
		ui.Column(c).FillWidth().Gap(th.Space(1.5)).Padding(0, th.Space(4), th.Space(3), th.Space(17)).Children(func() {
			for _, r := range q.values {
				p.maskValueRow(c, r, q.blocked())
			}
		})
	})
}

// maskValueRow is a value a request held: its category and kind, its shape
// (first character and length; only the length when short), what found it
// (a rule or 识别模型) and what was done with it. In a blocked request the
// values not blocked were not sent.
func (p *settingsPage) maskValueRow(c *ui.Context, r core.MaskRecord, blocked bool) {
	t, th := theme.Of(c), c.Theme()
	shape := i18n.Tf("settings.masking.log.length", r.Len)
	if r.First != "" {
		shape = i18n.Tf("settings.masking.log.value", r.First, r.Len)
	}
	act, color := i18n.T("settings.masking.action."+r.Action), t.Muted
	switch {
	case blocked && r.Action != core.MaskBlock:
		act = i18n.T("settings.masking.log.unsent")
	case r.Action == core.MaskBlock:
		color = t.DangerText
	case r.Action == core.MaskWarn:
		color = t.WarningText
	}
	ui.Row(c).FillWidth().MinHeight(th.Space(8)).Padding(th.Space(1), th.Space(2.5)).Gap(th.Space(2.5)).AlignItems(ui.Center).Radius(t.Radius.Lg).Background(t.Background).Children(func() {
		kit.Text(c, maskCategoryName(r.Category), t.Text.Base).FontWeight(500).Shrink(0)
		kit.Text(c, r.Kind, t.Text.Sm).Font(theme.Mono).TextColor(t.Muted).Shrink(0)
		kit.Text(c, shape, t.Text.Sm).TextColor(t.Muted).Grow(1).Shrink(1)
		kit.Text(c, i18n.T("settings.masking.log.source."+r.Source), t.Text.Sm).TextColor(t.Muted).Shrink(0)
		kit.Text(c, act, t.Text.Sm).TextColor(color).Shrink(0)
	})
}

// dangerPill is statusPill in the danger colors: 已拦截.
func dangerPill(c *ui.Context, text string) {
	t, th := theme.Of(c), c.Theme()
	ui.Box(c).Padding(th.Space(0.5), th.Space(2)).Radius(t.Radius.Sm).Background(t.DangerBg).TextColor(t.DangerText).Children(func() { kit.Text(c, text, t.Text.Sm) })
}

// maskFeatures are the features the masking log names (core's
// MaskRecord.Feature), in the order the filter offers them.
var maskFeatures = []string{"wit", "compose", "skill", "quick_replies", "translate", "labels", "compact", "plugin"}

func maskFeatureName(feature string) string {
	if !slices.Contains(maskFeatures, feature) {
		feature = "other"
	}
	return i18n.T("settings.masking.feature." + feature)
}

// maskLogDay is the heading a request sits under: today, yesterday, else its date.
func maskLogDay(now, at time.Time) string {
	at = at.In(now.Location())
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	switch {
	case !at.Before(today):
		return i18n.T("mail.group.today")
	case !at.Before(today.AddDate(0, 0, -1)):
		return i18n.T("mail.group.yesterday")
	}
	return i18n.FormatDate(at, now)
}
