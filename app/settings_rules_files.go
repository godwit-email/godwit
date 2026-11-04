package app

import (
	"context"
	"path/filepath"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// namingCustom is the naming select's 自定义… value.
const namingCustom = "custom"

// namingSample is the example mail the path preview fills the naming with.
var namingSample = map[string]string{"date": "2026-11-03", "subject": "Invoice #1234 from Acme Inc", "from": "Stripe", "domain": "stripe.com",
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
		if slices.ContainsFunc(strings.Split(tpl, "/"), func(l string) bool { return strings.TrimSpace(l) == "" }) {
			return i18n.T("settings.rules.files.bad_slash")
		}
		return i18n.T("settings.rules.files.bad_dot")
	}
	return ""
}

// namingExts are the extension of the file each file action writes, named by the naming.
var namingExts = map[string]string{core.ActionPDF: ".pdf", core.ActionEML: ".eml", core.ActionKeyPoints: ".md"}

// filePreview is where action ac writes the example mail's file: in its
// folder, the naming's subfolder (with a subfolder per message), the file
// named as the last level, or an attachment's own name; "" for a naming
// that cannot be saved.
func filePreview(ac core.RuleAction) string {
	levels, err := core.FillNaming(ac.Naming, namingSample)
	if err != nil {
		return ""
	}
	parts := []string{cmpDir(ac.Dir)}
	if ac.Subfolder {
		parts = append(parts, levels...)
	}
	name := levels[len(levels)-1] + namingExts[ac.Kind]
	if ac.Kind == core.ActionAttachments {
		name = "invoice.pdf"
	}
	return filepath.Join(append(parts, name)...)
}

// fileReady reports whether the editor's file actions can be saved.
func fileReady(d *core.RuleDoc) bool {
	return !slices.ContainsFunc(d.Actions, func(ac core.RuleAction) bool {
		return fileAction(ac.Kind) && (ac.Dir == "" || namingError(ac.Naming) != "")
	})
}

// fileDir is action i's folder, a button that picks another.
func (v *rulesView) fileDir(c *ui.Context, d *core.RuleDoc, i int) {
	t, th := theme.Of(c), c.Theme()
	dir := d.Actions[i].Dir
	b := kit.Pressable(c, kit.PressableOpts{Bordered: true, OnClick: func() { v.pickDir(d, i) }}).
		Label(i18n.T("settings.rules.files.choose")).Height(th.Space(8)).PaddingX(th.Space(2.5)).Gap(th.Space(1.5)).
		Radius(t.Radius.Lg).AlignItems(ui.Center).Grow(1).Shrink(1).Basis(0).MinWidth(0)
	b.Children(func() {
		ui.Icon(c, icons.Get("folder")).Size(theme.Icon, theme.Icon).TextColor(t.Muted).Shrink(0)
		if dir == "" {
			kit.Text(c, i18n.T("settings.rules.files.choose"), t.Text.Sm).TextColor(t.Muted).SingleLine().Grow(1).Shrink(1).MinWidth(0)
			return
		}
		kit.Text(c, dir, t.Text.Xs).Font(theme.Mono).SingleLine().Grow(1).Shrink(1).MinWidth(0)
		kit.Text(c, i18n.T("settings.rules.files.change"), t.Text.Xs).TextColor(t.Muted).Shrink(0)
	})
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

// fileOptions are, on the first action to action i's folder, the
// subfolder switch and its naming; the others to that folder follow it.
// Each shows the path its file gets.
func (v *rulesView) fileOptions(c *ui.Context, d *core.RuleDoc, i int) {
	t, th := theme.Of(c), c.Theme()
	ac := &d.Actions[i]
	pathRow := func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(th.Space(1.5)).TextColor(t.Muted).Children(func() {
			ui.Icon(c, icons.Get("folder")).Size(theme.IconSm, theme.IconSm).Shrink(0)
			kit.Text(c, filePreview(*ac), t.Text.Sm).Font(theme.Mono).SingleLine().Shrink(1)
		})
	}
	if l := leader(d, i); l != i {
		ac.Subfolder, ac.Naming = d.Actions[l].Subfolder, d.Actions[l].Naming
		pathRow()
		return
	}
	kit.Checkbox(c, &ac.Subfolder, i18n.T("settings.rules.files.subfolder"))
	if !ac.Subfolder {
		pathRow()
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
	if sel != namingCustom {
		pathRow()
		return
	}
	// {account} is offered only to a naming that has it already, as the design's tokens leave it out.
	var tokens []kit.TemplateToken
	for _, id := range core.NamingTokens {
		if id != "account" || strings.Contains(ac.Naming, "{account}") {
			tokens = append(tokens, kit.TemplateToken{ID: id, Label: i18n.T("settings.rules.files.token." + id)})
		}
	}
	kit.TemplateField(c, &ac.Naming, kit.TemplateOpts{Label: i18n.T("settings.rules.files.naming"), Tokens: tokens,
		Insert: i18n.T("settings.rules.files.insert"), Remove: func(l string) string { return i18n.Tf("settings.rules.files.remove", l) },
		Placeholder: i18n.T("settings.rules.files.placeholder"), Error: namingError(ac.Naming),
		PreviewLabel: i18n.T("settings.rules.files.example"), Preview: filePreview(*ac)})
}

// cmpDir is dir, or where the preview stands for a folder not picked yet.
func cmpDir(dir string) string {
	if dir == "" {
		return "…"
	}
	return dir
}
