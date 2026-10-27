package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// maskDraft is General › 隐私脱敏's text fields: the words, rules and
// allowlist as edited, one per line, until saved; and the preview.
type maskDraft struct {
	words, rules, allow string
	editing             bool   // the fields were edited since the last save: they don't follow the settings
	err                 string // why the last save was refused
	preview             string // the text pasted to preview
	shown, shownFor     string // the preview masked, and what it was masked for
}

// maskingView is 隐私脱敏 (docs/adr/0025-mask-at-cloud-egress.md), under
// the AI section: the switch and the categories save as they change; the
// words, rules and allowlist with their button, once checked.
func (p *settingsPage) maskingView(c *ui.Context) {
	st := &p.st
	rows := []func(){func() {
		kit.SwitchRow(c, &st.Masking, i18n.T("settings.masking.on"), i18n.T("settings.masking.on.help"))
	}}
	if st.Masking {
		for name, cat := range st.MaskCategories.All() {
			rows = append(rows, func() { maskCategoryRow(c, name, cat) })
		}
	}
	settingsSection(c, i18n.T("settings.masking"), i18n.T("settings.masking.foot"), rows...)
	if st.Masking {
		p.maskCustom(c)
		p.maskPreview(c)
	}
	p.maskLogView(c)
}

// maskLogShown is how many of the newest records 脱敏记录 shows.
const maskLogShown = 100

// readMaskLog reads 脱敏记录 for the General page.
func (p *settingsPage) readMaskLog() {
	a := p.a
	var log []core.MaskRecord
	a.query(func(ctx context.Context) (err error) { log, err = a.svc.MaskingLog(ctx); return err },
		func(err error) {
			if err == nil {
				p.maskLog = log[:min(len(log), maskLogShown)]
			}
		})
}

// maskLogView is 脱敏记录: what the last 30 days' requests held, newest
// first, each by its first character and length, never the value.
func (p *settingsPage) maskLogView(c *ui.Context) {
	t := theme.Of(c)
	var rows []func()
	for _, r := range p.maskLog {
		rows = append(rows, func() {
			label := i18n.T("settings.masking.feature."+cmp.Or(r.Feature, "other")) + " · " + maskCategoryName(r.Category)
			help := i18n.Tf("settings.masking.log.value", r.First, r.Len) + " · " + listTime(p.a.clock.Now(), r.At)
			settingRow(c, label, help, func() {
				kit.Text(c, i18n.T("settings.masking.action."+r.Action), t.Text.Sm).TextColor(t.Muted)
			})
		})
	}
	if len(rows) == 0 {
		rows = append(rows, func() {
			settingBlock(c, func() { ui.Text(c, i18n.T("settings.masking.log.empty")).TextColor(t.Muted) })
		})
	}
	settingsSection(c, i18n.T("settings.masking.log"), i18n.T("settings.masking.log.foot"), rows...)
}

// maskCategoryName is a category as the settings name it; "" is the user's own.
func maskCategoryName(cat string) string {
	if cat == "" {
		return i18n.T("settings.masking.log.own")
	}
	return i18n.T("settings.masking.cat." + cat)
}

// maskCategories names cats as a list.
func maskCategories(cats []string) string {
	names := make([]string, len(cats))
	for i, c := range cats {
		names[i] = maskCategoryName(c)
	}
	return strings.Join(names, i18n.T("settings.masking.sep"))
}

// maskCategoryRow is a category's switch and, while it is on, what is done
// with what it finds.
func maskCategoryRow(c *ui.Context, name string, cat *core.MaskCategory) {
	label := i18n.T("settings.masking.cat." + name)
	settingRow(c, label, i18n.T("settings.masking.cat."+name+".help"), func() {
		ui.Row(c).Gap(c.Theme().Space(3)).AlignItems(ui.Center).Children(func() {
			if cat.On {
				action := cmp.Or(cat.Action, core.MaskHide)
				settingSelect(c, i18n.T("settings.masking.action"), &action, []kit.Option[string]{
					{Value: core.MaskHide, Label: i18n.T("settings.masking.action.mask")},
					{Value: core.MaskWarn, Label: i18n.T("settings.masking.action.warn")},
					{Value: core.MaskBlock, Label: i18n.T("settings.masking.action.block")},
				})
				if action != cmp.Or(cat.Action, core.MaskHide) {
					cat.Action = action
				}
			}
			kit.Switch(c, &cat.On).Label(label)
		})
	})
}

// maskCustom is the user's words, rules and allowlist, and their save button.
func (p *settingsPage) maskCustom(c *ui.Context) {
	st, d, th := &p.st, &p.mask, c.Theme()
	words, rules, allow := strings.Join(st.MaskWords, "\n"), ruleLines(st.MaskRules), strings.Join(st.MaskAllow, "\n")
	if !d.editing {
		d.words, d.rules, d.allow = words, rules, allow
	}
	area := func(label, help, errText string, s *string, mono bool) func() {
		return func() {
			settingBlock(c, func() {
				kit.Field(c, label, help, errText, func() {
					if kit.TextArea(c, s, kit.InputOpts{Mono: mono, Invalid: errText != ""}).Height(th.Space(20)).Label(label).Changed() {
						d.editing = true
					}
				})
			})
		}
	}
	settingsSection(c, i18n.T("settings.masking.custom"), i18n.T("settings.masking.custom.foot"),
		area(i18n.T("settings.masking.words"), i18n.T("settings.masking.words.help"), "", &d.words, false),
		func() {
			kit.SwitchRow(c, &st.MaskWordsCase, i18n.T("settings.masking.words.case"), "")
		},
		func() {
			kit.SwitchRow(c, &st.MaskWordsWhole, i18n.T("settings.masking.words.whole"), i18n.T("settings.masking.words.whole.help"))
		},
		area(i18n.T("settings.masking.rules"), i18n.T("settings.masking.rules.help"), d.err, &d.rules, true),
		area(i18n.T("settings.masking.allow"), i18n.T("settings.masking.allow.help"), "", &d.allow, false),
		func() {
			settingBlock(c, func() {
				if settingButton(c, i18n.T("settings.masking.save"), kit.Default, !d.editing).AlignSelf(ui.End).Clicked() {
					p.saveMasking()
				}
			})
		})
}

// saveMasking puts the edited words, rules and allowlist in the settings,
// which syncSettings saves; rules that can't be used are not, and the
// page says why.
func (p *settingsPage) saveMasking() {
	d := &p.mask
	st, lines := d.edited(p.st)
	if d.err = maskCheck(st, lines); d.err != "" {
		return
	}
	p.st, d.editing = st, false
}

// edited is st with the words, rules and allowlist as d has them, and the
// line each rule is on.
func (d *maskDraft) edited(st core.Settings) (core.Settings, []int) {
	rules, lines := parseRules(d.rules)
	st.MaskWords, st.MaskRules, st.MaskAllow = parseLines(d.words), rules, parseLines(d.allow)
	return st, lines
}

// maskPreview masks the pasted text as the page's settings say, the fields
// being edited too, and saves nothing.
func (p *settingsPage) maskPreview(c *ui.Context) {
	d, t, th := &p.mask, theme.Of(c), c.Theme()
	st, lines := d.edited(p.st)
	if key := fmt.Sprintf("%q %+v %v %v %v %v", d.preview, st.MaskCategories, st.MaskWords, st.MaskWordsCase, st.MaskWordsWhole, st.MaskAllow) + ruleLines(st.MaskRules); key != d.shownFor {
		d.shownFor = key
		if msg := maskCheck(st, lines); msg != "" {
			d.shown = msg
		} else if out, err := p.a.svc.PreviewMasking(context.Background(), st, d.preview); err != nil {
			d.shown = humanize(err)
		} else {
			d.shown = out
		}
	}
	settingsSection(c, i18n.T("settings.masking.preview"), i18n.T("settings.masking.preview.help"), func() {
		settingBlock(c, func() {
			kit.TextArea(c, &d.preview, kit.InputOpts{}).Height(th.Space(20)).Label(i18n.T("settings.masking.preview"))
			if d.preview != "" {
				kit.Text(c, d.shown, t.Text.Sm).Font(theme.Mono).Selectable()
			}
		})
	})
}

// parseLines is s's lines, trimmed, without the empty ones.
func parseLines(s string) []string {
	var out []string
	for l := range strings.SplitSeq(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// parseRules is the rules s writes one per line, a kind and a pattern
// after a space, and the line each is on.
func parseRules(s string) (rules []core.MaskRule, lines []int) {
	for i, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l == "" {
			continue
		}
		kind, re := l, ""
		if j := strings.IndexAny(l, " \t"); j >= 0 {
			kind, re = l[:j], strings.TrimSpace(l[j:])
		}
		rules = append(rules, core.MaskRule{Kind: strings.ToUpper(kind), Regex: re})
		lines = append(lines, i+1)
	}
	return rules, lines
}

// ruleLines writes rules as parseRules reads them.
func ruleLines(rules []core.MaskRule) string {
	var b strings.Builder
	for i, r := range rules {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(r.Kind + " " + r.Regex)
	}
	return b.String()
}

// maskCheck says why st's rules (on lines) or allowlist can't be saved; ""
// if they can.
func maskCheck(st core.Settings, lines []int) string {
	err := core.CheckMasking(st.MaskRules, st.MaskAllow)
	var re *core.MaskRuleError
	switch {
	case err == nil:
		return ""
	case errors.Is(err, core.ErrMaskTooManyRules):
		return i18n.Tf("settings.masking.rules.too_many", core.MaxMaskRules)
	case errors.Is(err, core.ErrMaskTooManyAllow):
		return i18n.Tf("settings.masking.allow.too_many", core.MaxMaskAllow)
	case !errors.As(err, &re):
		return humanize(err)
	case errors.Is(err, core.ErrMaskKind):
		return i18n.Tf("settings.masking.rules.kind", lines[re.Index])
	case errors.Is(err, core.ErrMaskEmptyMatch):
		return i18n.Tf("settings.masking.rules.empty", lines[re.Index])
	}
	return i18n.Tf("settings.masking.rules.invalid", lines[re.Index], re.Err.Error())
}
