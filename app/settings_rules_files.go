package app

import (
	"context"
	"path/filepath"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// namingCustom is the naming select's 自定义… value.
const namingCustom = "custom"

// namingSample is the example mail the path preview fills the naming with.
var namingSample = map[string]string{"date": "2026-11-03", "subject": "Invoice #1234", "from": "Stripe", "domain": "stripe.com",
	"year": "2026", "month": "11", "account": "you@example.com"}

func fileAction(kind string) bool { return slices.Contains(core.RuleFileActions, kind) }

// fileKindChanged gives action i, just made a file action, the folder and
// naming of the file action before it, or the defaults.
func fileKindChanged(d *core.RuleDoc, i int) {
	ac := &d.Actions[i]
	if ac.Naming != "" {
		return
	}
	ac.Subfolder, ac.Naming = true, core.NamingPresets[0]
	for j, b := range d.Actions {
		if j != i && fileAction(b.Kind) && b.Dir != "" {
			ac.Dir, ac.Subfolder, ac.Naming = b.Dir, b.Subfolder, b.Naming
			return
		}
	}
}

// leader is the first file action writing to action i's folder, whose
// subfolder and naming the others share.
func leader(d *core.RuleDoc, i int) int {
	if d.Actions[i].Dir == "" {
		return i
	}
	return slices.IndexFunc(d.Actions, func(b core.RuleAction) bool { return fileAction(b.Kind) && b.Dir == d.Actions[i].Dir })
}

// namingError is why a naming cannot be saved, "" if it can.
func namingError(tpl string) string {
	if !slices.ContainsFunc(core.NamingTokens, func(id string) bool { return strings.Contains(tpl, "{"+id+"}") }) {
		return i18n.T("settings.rules.files.no_token")
	}
	if _, err := core.FillNaming(tpl, namingSample); err != nil {
		return i18n.T("settings.rules.files.bad_level")
	}
	return ""
}

// fileReady reports whether the editor's file actions can be saved.
func fileReady(d *core.RuleDoc) bool {
	return !slices.ContainsFunc(d.Actions, func(ac core.RuleAction) bool {
		return fileAction(ac.Kind) && (ac.Dir == "" || namingError(ac.Naming) != "")
	})
}

// fileDir is action i's folder, a button that picks another.
func (v *rulesView) fileDir(c *ui.Context, d *core.RuleDoc, i int) {
	label := d.Actions[i].Dir
	if label == "" {
		label = i18n.T("settings.rules.files.choose")
	}
	kit.Button(c, label, kit.ButtonOpts{Variant: kit.Outline, Icon: "folder"}).Label(i18n.T("settings.rules.files.choose")).Shrink(1).
		OnClick(func() { v.pickDir(d, i) })
}

// pickDir asks for action i's folder off the UI thread; a folder another
// file action writes to brings its naming.
func (v *rulesView) pickDir(d *core.RuleDoc, i int) {
	a, plat := v.a, v.a.plat
	var dir string
	a.do(func(context.Context) (err error) { dir, err = plat.OpenDirectory(); return err }, func(err error) {
		if err != nil || dir == "" || !v.editing || i >= len(d.Actions) {
			a.toastErr(err)
			return
		}
		d.Actions[i].Dir = dir
		if l := leader(d, i); l != i {
			d.Actions[i].Subfolder, d.Actions[i].Naming = d.Actions[l].Subfolder, d.Actions[l].Naming
		}
	})
}

// fileOptions are action i's note and, on the first action to its folder,
// the subfolder switch, its naming and the path it gives; the others to
// that folder follow it.
func (v *rulesView) fileOptions(c *ui.Context, d *core.RuleDoc, i int) {
	t := theme.Of(c)
	kit.Text(c, i18n.T("settings.rules.files.note"), t.Text.Sm).TextColor(t.Muted)
	ac := &d.Actions[i]
	if l := leader(d, i); l != i {
		ac.Subfolder, ac.Naming = d.Actions[l].Subfolder, d.Actions[l].Naming
		return
	}
	kit.Checkbox(c, &ac.Subfolder, i18n.T("settings.rules.files.subfolder"))
	if !ac.Subfolder {
		return
	}
	if v.custom == nil {
		v.custom = map[string]bool{}
	}
	sel := namingCustom
	if slices.Contains(core.NamingPresets, ac.Naming) && !v.custom[ac.Dir] {
		sel = ac.Naming
	}
	var opts []kit.Option[string]
	for j, p := range core.NamingPresets {
		opts = append(opts, kit.Option[string]{Value: p, Label: i18n.T("settings.rules.files.preset." + []string{"date", "sender", "year"}[j])})
	}
	opts = append(opts, kit.Option[string]{Value: namingCustom, Label: i18n.T("settings.rules.files.custom")})
	was := sel
	ui.Row(c).Children(func() { settingSelect(c, i18n.T("settings.rules.files.naming"), &sel, opts) })
	if sel != was {
		if v.custom[ac.Dir] = sel == namingCustom; sel != namingCustom {
			ac.Naming = sel
		}
	}
	preview := ""
	if levels, err := core.FillNaming(ac.Naming, namingSample); err == nil {
		preview = filepath.Join(append([]string{cmpDir(ac.Dir)}, levels...)...) + "/"
	}
	if sel != namingCustom {
		kit.Text(c, preview, t.Text.Sm).TextColor(t.Muted).Font(theme.Mono).SingleLine()
		return
	}
	tokens := make([]kit.TemplateToken, len(core.NamingTokens))
	for j, id := range core.NamingTokens {
		tokens[j] = kit.TemplateToken{ID: id, Label: i18n.T("settings.rules.files.token." + id)}
	}
	kit.TemplateField(c, &ac.Naming, kit.TemplateOpts{Label: i18n.T("settings.rules.files.naming"), Tokens: tokens,
		Insert: i18n.T("settings.rules.files.insert"), Remove: func(l string) string { return i18n.Tf("settings.rules.files.remove", l) },
		Placeholder: i18n.T("settings.rules.files.placeholder"), Error: namingError(ac.Naming), Preview: preview})
}

// cmpDir is dir, or where the preview stands for a folder not picked yet.
func cmpDir(dir string) string {
	if dir == "" {
		return "…"
	}
	return dir
}
