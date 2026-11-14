package app

import (
	"cmp"
	"context"
	"regexp"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// directoryView is the plugin directory's subpage (ADR 0016, 0032): a card per
// plugin; a card opens its install dialog, or the row of what is installed.
func (e *extView) directoryView(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	ui.Column(c).Gap(theme.HeadingGap).Children(func() {
		settingsBack(c, i18n.T("settings.extensions"), func() { e.onDirectory = false })
		pageHeading(c, i18n.T("settings.ext.directory"), i18n.T("settings.ext.directory.sub"))
	})
	switch {
	case len(e.reg.Entries) == 0 && e.reg.Stale != "":
		kit.Block(c, ui.Row(c), kit.ToneCard).FillWidth().Gap(th.Space(6)).Padding(th.Space(3), th.Space(4)).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(0.5)).Children(func() {
				kit.Text(c, i18n.T("settings.ext.directory.offline"), t.Text.Sm).FontWeight(500)
				kit.Text(c, i18n.T("settings.ext.directory.offline.hint"), t.Text.Xs).TextColor(t.Muted)
			})
			kit.Button(c, i18n.T("common.retry"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(e.refreshRegistry)
		})
	case len(e.reg.Entries) == 0:
		ui.Text(c, i18n.T("settings.ext.registry.empty")).TextColor(t.Muted)
	default:
		ui.Column(c).FillWidth().Gap(th.Space(3)).Children(func() {
			for i := 0; i < len(e.reg.Entries); i += 2 {
				ui.Row(c.Key(i)).FillWidth().Gap(th.Space(3)).AlignItems(ui.Stretch).Children(func() {
					for _, en := range e.reg.Entries[i:min(i+2, len(e.reg.Entries))] {
						e.directoryCard(c.Key("registry:"+en.Name), en)
					}
					if i+1 == len(e.reg.Entries) { // a lone last card keeps its column's width; the card's padding counts toward its basis, so the filler's must too
						ui.Box(c).Grow(1).Shrink(1).Basis(0).MinWidth(0).Padding(th.Space(3))
					}
				})
			}
		})
	}
	foot := i18n.T("settings.ext.no_send")
	if e.reg.Stale != "" && len(e.reg.Entries) > 0 {
		foot = i18n.T("settings.ext.registry.stale") + " " + foot
	}
	kit.Text(c, foot, t.Text.Xs).TextColor(t.Muted).PaddingX(th.Space(4))
	e.installDialog(c)
}

func entryTitle(en core.RegistryEntry) string {
	if s := en.Title.For(i18n.Language()); s != "" {
		return s
	}
	return en.Name
}

func entryIcon(en core.RegistryEntry) string {
	switch en.Category {
	case plugins.CategoryTranslate:
		return "languages"
	case plugins.CategoryModels:
		return "brain-4"
	}
	return "puzzle"
}

func entryKind(en core.RegistryEntry) string {
	switch {
	case en.Native:
		return i18n.T("settings.ext.native")
	case en.Script:
		return i18n.T("settings.ext.script")
	}
	return ""
}

func flowLabel(flow string) string {
	switch flow {
	case plugins.DataFlowLocal:
		return i18n.T("settings.ext.flow.local")
	case plugins.DataFlowThirdParty:
		return i18n.T("settings.ext.flow.third")
	}
	return ""
}

func entryIconBox(c *ui.Context, en core.RegistryEntry, bg ui.Color) {
	t, th := theme.Of(c), c.Theme()
	ui.Box(c).Width(th.Space(10)).Height(th.Space(10)).Radius(t.Radius.Md).Background(bg).TextColor(t.Muted).AlignItems(ui.Center).Justify(ui.Center).Shrink(0).Children(func() {
		ui.Icon(c, icons.Get(entryIcon(en))).Size(th.Space(5), th.Space(5))
	})
}

func (e *extView) directoryCard(c *ui.Context, en core.RegistryEntry) {
	t, th := theme.Of(c), c.Theme()
	title := entryTitle(en)
	kit.Block(c, ui.Row(c), kit.ToneCard).Grow(1).Shrink(1).Basis(0).MinWidth(0).Gap(th.Space(2)).Padding(th.Space(3)).AlignItems(ui.Start).Children(func() {
		kit.Pressable(c, kit.PressableOpts{Bare: true, OnClick: func() { e.openEntry(en) }}).Label(i18n.Tf("settings.ext.directory.view", title)).Grow(1).Shrink(1).Children(func() {
			ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(2)).Children(func() {
				ui.Row(c).Gap(th.Space(3)).AlignItems(ui.Center).Children(func() {
					entryIconBox(c, en, t.Background)
					ui.Column(c).Shrink(1).Gap(th.Space(0.5)).Children(func() {
						kit.Text(c, title, t.Text.Sm).LineHeight(theme.LineFor(title)).FontWeight(500)
						if en.Publisher != "" {
							kit.Text(c, en.Publisher, t.Text.Xs).TextColor(t.Muted)
						}
					})
				})
				if s := en.Summary.For(i18n.Language()); s != "" {
					kit.Tooltip(c, kit.Text(c, s, t.Text.Xs).TextColor(t.Muted).MaxLines(2).Shrink(1), s)
				}
				ui.Row(c).Gap(th.Space(1.5)).AlignItems(ui.Center).Wrap().Children(func() {
					if l := flowLabel(en.DataFlow); l != "" {
						if en.DataFlow == plugins.DataFlowThirdParty {
							statusPill(c, l, false)
						} else {
							kit.Text(c, l, t.Text.Xs).TextColor(t.Muted)
						}
					}
					if m := strings.TrimPrefix(strings.TrimSuffix(entryKind(en)+" · "+cmp.Or(en.Version, en.Installed), " · "), " · "); m != "" {
						kit.Text(c, m, t.Text.Xs).TextColor(t.Muted)
					}
				})
			})
		})
		e.entryAction(c, en)
	})
}

// entryAction is a card's install, update or replace, or why it has none.
func (e *extView) entryAction(c *ui.Context, en core.RegistryEntry) {
	t := theme.Of(c)
	dialog := func(text string) {
		kit.Button(c, text, kit.ButtonOpts{Variant: kit.Outline, Disabled: e.installing != ""}).Label(i18n.Tf("settings.ext.registry.named", text, en.Name)).OnClick(func() { e.openDialog(en.Name) })
	}
	switch {
	case e.installing == en.Name:
		e.installProgressView(c)
	case en.Version == "":
		kit.Text(c, unavailable(en.Unavailable), t.Text.Xs).TextColor(t.Muted).Shrink(1)
	case en.Installed == "":
		dialog(i18n.T("settings.ext.registry.install"))
	case en.Update:
		e.updateButton(c, en)
	case en.InstalledTrust != "listed":
		dialog(i18n.T("settings.ext.registry.replace"))
	default:
		kit.Text(c, i18n.T("settings.ext.registry.installed"), t.Text.Xs).TextColor(t.Muted)
	}
}

// updateButton: a newer reviewed release of what the user already allowed.
func (e *extView) updateButton(c *ui.Context, en core.RegistryEntry) {
	text := i18n.Tf("settings.ext.registry.update", en.Version)
	kit.Button(c, text, kit.ButtonOpts{Variant: kit.Outline, Disabled: e.installing != ""}).Label(i18n.Tf("settings.ext.registry.named", text, en.Name)).
		OnClick(func() { e.installFromRegistry(en.Name, nil) })
}

func (e *extView) openEntry(en core.RegistryEntry) {
	switch {
	case en.Installed != "" && en.InstalledTrust == "listed":
		e.onDirectory, e.tab, e.plugin = false, 1, en.Name
	case en.Version != "":
		e.openDialog(en.Name)
	}
}

func (e *extView) openDialog(name string) {
	e.detail, e.detailOpen = name, true
	e.loadReadme(name)
}

// pluginReadme is a plugin's README as read for the install dialog.
type pluginReadme struct {
	loading bool
	blocks  []readmeBlock
}

// loadReadme reads it once per page visit's language; a failure just leaves
// the dialog without it, since what states the risk is the directory's.
func (e *extView) loadReadme(name string) {
	a := e.a
	lang := i18n.Language()
	key := name + "\x00" + lang
	if e.readmes[key] != nil {
		return
	}
	if e.readmes == nil {
		e.readmes = map[string]*pluginReadme{}
	}
	r := &pluginReadme{loading: true}
	e.readmes[key] = r
	var text string
	a.do(func(ctx context.Context) (err error) {
		text, err = a.svc.PluginReadme(ctx, name, lang)
		return err
	}, func(err error) {
		r.loading = false
		if err != nil {
			delete(e.readmes, key) // tried again the next time it opens
			return
		}
		r.blocks = parseReadme(text)
	})
}

// installDialog is what the user consents to: who publishes the plugin, where
// its data goes, what it can do and its README, with the hosts named (AGENTS.md).
func (e *extView) installDialog(c *ui.Context) {
	if e.detail == "" {
		return
	}
	en, ok := e.entry(e.detail)
	if !ok || !e.detailOpen {
		e.detail, e.detailOpen = "", false
		return
	}
	t, th := theme.Of(c), c.Theme()
	desc := ""
	if en.Publisher != "" {
		desc = i18n.Tf("settings.ext.dialog.publisher", en.Publisher)
	}
	kit.Dialog(c, &e.detailOpen, entryTitle(en), kit.DialogOpts{Desc: desc, Close: i18n.T("common.close")}, func() {
		ui.Column(c).FillWidth().Gap(th.Space(4)).Children(func() {
			ui.Row(c).FillWidth().Gap(th.Space(6)).Padding(th.Space(2), th.Space(3)).Radius(t.Radius.Md).Background(t.Card).Children(func() {
				fact := func(label, value string) {
					ui.Column(c).Grow(1).Basis(0).Gap(th.Space(0.5)).Children(func() {
						kit.Text(c, label, t.Text.Xs).TextColor(t.Muted)
						kit.Text(c, value, t.Text.Sm).LineHeight(theme.LineFor(value))
					})
				}
				fact(i18n.T("settings.ext.dialog.version"), en.Version)
				fact(i18n.T("settings.ext.dialog.kind"), entryKind(en))
				fact(i18n.T("settings.ext.dialog.source"), i18n.T("settings.ext.dialog.source.listed"))
			})
			dataFlowCard(c, en)
			entryPermissions(c, en)
			if en.Installed != "" {
				kit.Alert(c, kit.Warning, strings.TrimSpace(i18n.Tf("settings.ext.registry.confirm.replace", en.Name)), nil)
			}
			if en.Homepage != "" {
				kit.TextLink(c, i18n.T("settings.ext.dialog.home"), t.Muted, t.Text.Xs).AlignSelf(ui.Start).OnClick(func() {
					home, plat := en.Homepage, e.a.plat
					e.a.do(func(context.Context) error { return plat.OpenURL(home) }, e.a.toastErr)
				})
			}
			r := e.readmes[e.detail+"\x00"+i18n.Language()]
			switch {
			case r != nil && r.loading:
				kit.Text(c, i18n.T("settings.ext.dialog.readme.loading"), t.Text.Xs).TextColor(t.Muted)
			case r != nil && len(r.blocks) > 0:
				kit.Text(c, i18n.T("settings.ext.dialog.readme.note"), t.Text.Xs).TextColor(t.Muted)
				readmeView(c, r.blocks)
			}
		})
	}, func() {
		foot := i18n.T("settings.ext.dialog.foot.script")
		if en.Native {
			foot = i18n.T("settings.ext.dialog.foot.native")
		}
		kit.Text(c, foot, t.Text.Xs).TextColor(t.Muted).Grow(1).Shrink(1)
		kit.Button(c, i18n.T("common.cancel"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(func() { e.detailOpen = false })
		// A script's config comes first when it has any: its row opens pending.
		enable := en.Script && !en.Configurable
		label := i18n.T("settings.ext.registry.confirm.ok")
		switch {
		case en.Native:
			label = i18n.T("settings.ext.dialog.install_enable")
		case enable:
			label = i18n.T("settings.ext.dialog.allow_install")
		}
		kit.Button(c, label, kit.ButtonOpts{Disabled: e.installing != ""}).OnClick(func() {
			e.detailOpen = false
			var approved *core.RegistryEntry
			if enable {
				approved = &en
			}
			e.installFromRegistry(en.Name, approved)
		})
	})
}

// dataFlowCard is the reviewers' statement of where the plugin's data goes.
func dataFlowCard(c *ui.Context, en core.RegistryEntry) {
	t, th := theme.Of(c), c.Theme()
	kit.Block(c, ui.Column(c), kit.ToneCard).FillWidth().Gap(th.Space(1)).Padding(th.Space(3), th.Space(4)).Children(func() {
		ui.Row(c).Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
			kit.Text(c, i18n.T("settings.ext.dialog.flow"), t.Text.Sm).FontWeight(500)
			if l := flowLabel(en.DataFlow); l != "" {
				statusPill(c, l, false)
			}
		})
		if s := en.Summary.For(i18n.Language()); s != "" {
			kit.Text(c, s, t.Text.Sm).LineHeight(theme.LineFor(s))
		}
	})
}

// entryPermissions says what the release can do: a native plugin anything, a
// script all it declares, which is what the dialog's button grants.
func entryPermissions(c *ui.Context, en core.RegistryEntry) {
	t, th := theme.Of(c), c.Theme()
	kit.Block(c, ui.Column(c), kit.ToneCard).FillWidth().Gap(th.Space(1.5)).Padding(th.Space(3), th.Space(4)).Children(func() {
		kit.Text(c, i18n.T("settings.ext.dialog.perm"), t.Text.Sm).FontWeight(500)
		if en.Native {
			text := i18n.T("settings.ext.dialog.perm.native")
			kit.Text(c, text, t.Text.Sm).LineHeight(theme.LineFor(text))
			return
		}
		permissionList(c, en.Permissions, en.Capabilities)
	})
}

// readmeBlock is a README line reduced to plain text: untrusted text from
// outside the review, so no links to follow and no images to load.
type readmeBlock struct {
	kind       string // "h", "p", "li" or "code"
	mark, text string
}

var (
	readmeImage  = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	readmeLink   = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	readmeTag    = regexp.MustCompile(`</?[A-Za-z][^>]*>`)
	readmeMarks  = strings.NewReplacer("**", "", "__", "", "`", "")
	readmeHead   = regexp.MustCompile(`^#{1,6}\s+`)
	readmeBullet = regexp.MustCompile(`^\s*[-*+]\s+`)
	readmeNumber = regexp.MustCompile(`^\s*(\d+\.)\s+`)
)

// parseReadme keeps headings, paragraphs, list items and code. The first
// heading goes: the dialog's title already says it. So does a line that was
// only links (a language switcher, badges).
func parseReadme(md string) []readmeBlock {
	var out []readmeBlock
	var para []string
	flush := func() {
		if len(para) > 0 {
			out = append(out, readmeBlock{kind: "p", text: strings.Join(para, " ")})
			para = nil
		}
	}
	clean := func(s string) string {
		s = readmeImage.ReplaceAllString(s, "")
		s = readmeLink.ReplaceAllString(s, "$1")
		s = readmeTag.ReplaceAllString(s, "")
		return strings.TrimSpace(readmeMarks.Replace(s))
	}
	fence, titled := false, false
	for line := range strings.Lines(md) {
		line = strings.TrimRight(line, "\r\n")
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			flush()
			fence = !fence
			continue
		}
		if fence {
			out = append(out, readmeBlock{kind: "code", text: line})
			continue
		}
		switch {
		case trimmed == "" || trimmed == "---":
			flush()
		case readmeHead.MatchString(trimmed):
			flush()
			if !titled && len(out) == 0 {
				titled = true
				continue
			}
			out = append(out, readmeBlock{kind: "h", text: clean(readmeHead.ReplaceAllString(trimmed, ""))})
		case readmeBullet.MatchString(line):
			flush()
			out = append(out, readmeBlock{kind: "li", mark: "•", text: clean(readmeBullet.ReplaceAllString(line, ""))})
		case readmeNumber.MatchString(line):
			flush()
			m := readmeNumber.FindStringSubmatch(line)
			out = append(out, readmeBlock{kind: "li", mark: m[1], text: clean(readmeNumber.ReplaceAllString(line, ""))})
		default:
			s := readmeLink.ReplaceAllString(readmeImage.ReplaceAllString(trimmed, ""), "")
			if strings.Trim(s, " |·") == "" {
				continue
			}
			if s := clean(trimmed); s != "" {
				para = append(para, s)
			}
		}
	}
	flush()
	return out
}

func readmeView(c *ui.Context, blocks []readmeBlock) {
	t, th := theme.Of(c), c.Theme()
	ui.Column(c).FillWidth().Gap(th.Space(2)).Children(func() {
		for i, b := range blocks {
			cc := c.Key(i)
			switch b.kind {
			case "h":
				kit.Text(cc, b.text, t.Text.Sm).LineHeight(theme.LineFor(b.text)).FontWeight(600).Margin(th.Space(2), 0, 0, 0)
			case "li":
				ui.Row(cc).FillWidth().Gap(th.Space(2)).Children(func() {
					kit.Text(cc, b.mark, t.Text.Sm).TextColor(t.Muted).Shrink(0)
					kit.Text(cc, b.text, t.Text.Sm).LineHeight(theme.LineFor(b.text)).Shrink(1)
				})
			case "code":
				kit.Text(cc, b.text, t.Text.Xs).Font(theme.Mono).TextColor(t.Muted)
			default:
				kit.Text(cc, b.text, t.Text.Sm).LineHeight(theme.LineFor(b.text))
			}
		}
	})
}
