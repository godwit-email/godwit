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

// maskDraft is 隐私与安全 › 隐私脱敏's text fields: the words, rules and
// allowlist as edited, one per line, until saved; and the preview.
type maskDraft struct {
	words, rules, allow string
	editing             bool   // the fields were edited since the last save: they don't follow the settings
	err                 string // why the last save was refused
	preview             string // the text pasted to preview
	shown, shownFor     string // the preview masked, and what it was masked for
}

// maskingView is 隐私脱敏 (docs/adr/0025-mask-at-cloud-egress.md), the
// privacy page's first tab: the switch and the categories save as they
// change; the words, rules and allowlist with their button, once checked;
// then the way into 脱敏记录.
func (p *settingsPage) maskingView(c *ui.Context) {
	st := &p.st
	rows := []func(){func() {
		kit.SwitchRow(c, &st.Masking, i18n.T("settings.masking.on"), i18n.T("settings.masking.on.help"))
	}}
	if st.Masking {
		rows = append(rows, func() { p.maskModelRow(c) })
		for name, cat := range st.MaskCategories.All() {
			rows = append(rows, func() { maskCategoryRow(c, name, cat) })
		}
	}
	settingsSection(c, i18n.T("settings.masking"), i18n.T("settings.masking.foot"), rows...)
	if st.Masking {
		p.maskCustom(c)
		p.maskPreview(c)
	}
	p.maskLogEntry(c)
}

// readMaskBackend reads what reads 识别模型, for its row: none while it is
// off or can't be loaded.
func (p *settingsPage) readMaskBackend() {
	a := p.a
	var b *core.MaskModelBackend
	a.query(func(ctx context.Context) (err error) { b, err = a.svc.MaskModelBackend(ctx); return err },
		func(error) { p.maskBackend = b })
}

// maskBackendText says what reads 识别模型: CoreML and its device, or the
// CPU and, on a Mac, why not CoreML.
func maskBackendText(b core.MaskModelBackend) string {
	switch {
	case b.Device != "":
		return i18n.Tf("settings.masking.model.coreml", b.Device)
	case b.NotCoreML != "":
		return i18n.Tf("settings.masking.model.cpu_why", b.NotCoreML)
	}
	return i18n.T("settings.masking.model.cpu")
}

// maskModelDownload is 识别模型's files coming down, as its switch started.
type maskModelDownload struct {
	cancel     context.CancelFunc
	got, total int64
}

// maskModelRow is 识别模型's switch. Turned on before its files are here,
// it downloads them, saying how far it has come, and is on once they all
// are; failed or canceled, it is off again, saying why it failed.
func (p *settingsPage) maskModelRow(c *ui.Context) {
	st, d, label := &p.st, p.maskDL, i18n.T("settings.masking.model")
	help := i18n.T("settings.masking.model.help")
	switch {
	case d != nil:
		help = i18n.Tf("settings.masking.model.downloading", d.got*100/max(d.total, 1), formatBytes(d.total))
	case p.maskModelErr != "":
		help += "\n" + p.maskModelErr
	case !p.maskModel && !st.MaskModel:
		help += "\n" + i18n.T("settings.masking.model.missing")
	}
	if b := p.maskBackend; st.MaskModel && b != nil {
		help += "\n" + maskBackendText(*b)
	}
	if d != nil {
		settingRow(c, label, help, func() {
			settingButton(c, i18n.T("settings.masking.model.cancel"), kit.Outline, false).OnClick(d.cancel)
		})
		return
	}
	on := st.MaskModel
	kit.SwitchRow(c, &on, label, help)
	if on && !st.MaskModel && !p.maskModel {
		p.downloadMaskModel()
	} else {
		st.MaskModel = on
	}
}

// downloadMaskModel downloads 识别模型's files and, once they are all here,
// turns it on. It goes on when the page closes, until it ends or the user
// cancels it.
func (p *settingsPage) downloadMaskModel() {
	a := p.a
	ctx, cancel := context.WithCancel(a.ctx)
	d := &maskModelDownload{cancel: cancel}
	p.maskDL, p.maskModelErr = d, ""
	a.do(func(context.Context) error {
		last := int64(-1)
		return a.svc.DownloadMaskModel(ctx, func(got, total int64) {
			if pct := got * 100 / max(total, 1); pct != last { // a redraw per percent, not per read
				last = pct
				a.onUI(func() { d.got, d.total = got, total })
			}
		})
	}, func(err error) {
		cancel()
		p.maskDL = nil
		switch {
		case err == nil:
			p.maskModel = true
			st := a.settings
			st.MaskModel = true
			a.changeSettings(st)
		case !errors.Is(err, context.Canceled):
			p.maskModelErr = i18n.Tf("settings.masking.model.failed", humanize(err))
		}
	})
}

// readMaskLog reads 脱敏记录 for the privacy page's entry and the log's own page.
func (p *settingsPage) readMaskLog() {
	a := p.a
	var log []core.MaskRecord
	a.query(func(ctx context.Context) (err error) { log, err = a.svc.MaskingLog(ctx); return err },
		func(err error) {
			if err == nil {
				p.maskLog = log
			}
		})
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
				action := cmp.Or(cat.Action, core.MaskMask)
				settingSelect(c, i18n.T("settings.masking.action"), &action, []kit.Option[string]{
					{Value: core.MaskMask, Label: i18n.T("settings.masking.action.mask")},
					{Value: core.MaskWarn, Label: i18n.T("settings.masking.action.warn")},
					{Value: core.MaskBlock, Label: i18n.T("settings.masking.action.block")},
				})
				if action != cmp.Or(cat.Action, core.MaskMask) {
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
	if d.err = p.maskCheck(st, lines); d.err != "" {
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
// being edited too, and saves nothing. It masks off the UI thread: 识别模型
// may take a while, the first time most.
func (p *settingsPage) maskPreview(c *ui.Context) {
	d, t, th := &p.mask, theme.Of(c), c.Theme()
	st, lines := d.edited(p.st)
	if key := fmt.Sprintf("%q %+v %v %v %v %v %v", d.preview, st.MaskCategories, st.MaskWords, st.MaskWordsCase, st.MaskWordsWhole, st.MaskAllow, st.MaskModel) + ruleLines(st.MaskRules); key != d.shownFor {
		d.shownFor = key
		if msg := p.maskCheck(st, lines); msg != "" {
			d.shown = msg
		} else {
			text, out := d.preview, ""
			p.a.query(func(ctx context.Context) (err error) { out, err = p.a.svc.PreviewMasking(ctx, st, text); return err },
				func(err error) {
					if d.shownFor == key { // not since edited
						d.shown = cmp.Or(humanize(err), out)
					}
				})
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
// if they can. A rule stored under a built-in kind before can stay.
func (p *settingsPage) maskCheck(st core.Settings, lines []int) string {
	err := core.CheckMasking(st.MaskRules, st.MaskAllow, p.a.settingsOK.MaskRules...)
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
	case errors.Is(err, core.ErrMaskReservedKind):
		return i18n.Tf("settings.masking.rules.reserved", lines[re.Index])
	case errors.Is(err, core.ErrMaskEmptyMatch):
		return i18n.Tf("settings.masking.rules.empty", lines[re.Index])
	}
	return i18n.Tf("settings.masking.rules.invalid", lines[re.Index], re.Err.Error())
}
