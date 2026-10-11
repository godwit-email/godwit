package app

import (
	"cmp"
	"context"
	"reflect"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// changeSettings shows st at once and saves it as a full snapshot; a save
// still waiting is replaced. A failure of the newest save reverts to the
// last confirmed settings.
func (a *App) changeSettings(st core.Settings) {
	old := a.settings
	a.settings = st
	a.settingsGen++
	a.settingsVer++
	ver := a.settingsVer
	a.settingsEffects(old, st)
	save := a.saveSettings
	var saved core.Settings
	a.writes.Submit("settings", a.settingsGen, func(ctx context.Context) (err error) {
		saved, err = save(ctx, st)
		return err
	}, func(gen uint64, err error) {
		a.refreshTasks() // saved or not, ask again: it may name other models
		if err == nil {
			a.settingsOK, a.settingsSavedVer = saved, ver
		}
		if gen != a.settingsGen {
			return
		}
		if err != nil {
			cur := a.settings
			a.settings = a.settingsOK
			a.settingsVer++
			a.settingsSavedVer = a.settingsVer
			a.settingsEffects(cur, a.settings)
			a.toastErr(err)
			return
		}
		a.settings = saved
	})
}

// settingsEffects tells the platform and the catalog what changed from old to st.
func (a *App) settingsEffects(old, st core.Settings) {
	if st.Theme != old.Theme {
		a.plat.SetThemeSource(st.Theme)
	}
	if on := st.LaunchAtLogin; on != old.LaunchAtLogin {
		plat := a.plat
		a.writes.Chain("open-at-login", func(context.Context) error { return plat.SetOpenAtLogin(on) }, a.toastErr)
	}
	if tag := cmp.Or(st.UILanguage, a.sysLang); st.UILanguage != old.UILanguage && tag != "" {
		i18n.SetLanguage(tag)
	}
	a.syncLocalAPI()
}

// syncLocalAPI asks the platform to serve the Local API as a.settings say.
// Requests run in order and only the newest one's result counts: when it
// fails, the reason shows on the privacy page and the setting turns off.
func (a *App) syncLocalAPI() {
	on := a.settings.LocalAPI
	if on == a.localAPIWant {
		return
	}
	a.localAPIWant = on
	a.localAPIGen++
	gen := a.localAPIGen
	if on {
		a.localAPIErr = nil
	}
	plat := a.plat
	var path string
	a.writes.Chain("local-api", func(context.Context) (err error) {
		path, err = plat.SetLocalAPI(gen, on)
		return err
	}, func(err error) {
		if gen != a.localAPIGen { // a newer request runs after this one and decides
			return
		}
		a.localAPIPath = path
		if err != nil {
			a.localAPIWant = false // a failed request leaves the server off
			a.localAPIFailed(err)
		}
	})
}

// LocalAPIStopped tells the App that the Local API server that request gen
// asked for ended by itself with err. Safe from any goroutine.
func (a *App) LocalAPIStopped(gen uint64, err error) {
	a.onUI(func() {
		if gen != a.localAPIGen { // a newer request replaced or stopped that server
			return
		}
		a.localAPIPath = ""
		a.localAPIFailed(err)
	})
}

// localAPIFailed shows err on the privacy page and turns the setting off.
func (a *App) localAPIFailed(err error) {
	a.localAPIErr = err
	if a.settings.LocalAPI {
		st := a.settings
		st.LocalAPI = false
		a.changeSettings(st)
	}
}

func sameSettings(x, y core.Settings) bool {
	if !slices.Equal(x.NoTranslate, y.NoTranslate) {
		return false
	}
	x.NoTranslate, y.NoTranslate = nil, nil
	return reflect.DeepEqual(x, y)
}

// syncSettings saves what the controls changed since the last look, and
// then binds them to the App's settings again.
func (p *settingsPage) syncSettings() {
	if !sameSettings(p.st, p.shown) {
		st := p.st
		st.NoTranslate = slices.Clone(st.NoTranslate)
		p.a.changeSettings(st)
	}
	p.st = p.a.settings
	p.st.NoTranslate = slices.Clone(p.st.NoTranslate)
	p.shown = p.st
	p.shown.NoTranslate = slices.Clone(p.st.NoTranslate)
}

// languages are offered by their own names.
var languages = []struct{ tag, name string }{
	{"zh-CN", "简体中文"}, {"zh-TW", "繁體中文"}, {"en", "English"}, {"ja", "日本語"},
}

func langName(tag string) string {
	switch t := strings.ToLower(tag); {
	case strings.HasPrefix(t, "zh-tw"), strings.HasPrefix(t, "zh-hant"), strings.HasPrefix(t, "zh-hk"):
		return "繁體中文"
	case strings.HasPrefix(t, "zh"):
		return "简体中文"
	case strings.HasPrefix(t, "ja"):
		return "日本語"
	}
	return "English"
}

func sizeOptions(sizes []int, def, cur int) []kit.Option[int] {
	if !slices.Contains(sizes, cur) {
		sizes = append(slices.Clone(sizes), cur)
		slices.Sort(sizes)
	}
	opts := make([]kit.Option[int], len(sizes))
	for i, n := range sizes {
		key := "settings.px"
		if n == def {
			key = "settings.px_default"
		}
		opts[i] = kit.Option[int]{Value: n, Label: i18n.Tf(key, n)}
	}
	return opts
}

func (p *settingsPage) generalView(c *ui.Context) {
	p.syncSettings()
	defer p.syncSettings() // what handlers changed while building
	th := c.Theme()
	st := &p.st
	sel := func(label, help string, control func() ui.Element) {
		settingRow(c, label, help, func() { control().Width(theme.ControlWidth).Label(label) })
	}
	pageHeading(c, i18n.T("settings.general"), i18n.T("settings.general.sub"))
	ui.Column(c).Children(func() {
		settingsSection(c, i18n.T("settings.appearance"), "", func() {
			ui.Row(c).Gap(th.Space(3)).Children(func() {
				for _, th := range []string{"system", "light", "dark"} {
					p.themeTile(c, th)
				}
			})
			sel(i18n.T("settings.density"), "", func() ui.Element {
				return kit.Select(c, &st.Density, []kit.Option[string]{
					{Value: "compact", Label: i18n.T("settings.density.compact")},
					{Value: "comfortable", Label: i18n.T("settings.density.comfortable")},
				})
			})
			sel(i18n.T("settings.font_size"), "", func() ui.Element {
				return kit.Select(c, &st.FontSize, sizeOptions([]int{12, 13, 14, 15, 16}, 13, st.FontSize))
			})
			sel(i18n.T("settings.body_font_size"), i18n.T("settings.body_font_size.help"), func() ui.Element {
				return kit.Select(c, &st.BodyFontSize, sizeOptions([]int{13, 14, 16, 18}, 14, st.BodyFontSize))
			})
		})
		settingsSection(c, i18n.T("settings.language"), "", func() {
			sel(i18n.T("settings.ui_language"), "", func() ui.Element {
				return kit.Select(c, &st.UILanguage, []kit.Option[string]{
					{Value: "", Label: i18n.Tf("settings.ui_language.system", langName(cmp.Or(p.a.sysLang, i18n.Language())))},
					{Value: "zh-CN", Label: "简体中文"},
					{Value: "en", Label: "English"},
				})
			})
			sel(i18n.T("settings.reading_language"), i18n.T("settings.reading_language.help"), func() ui.Element {
				opts := []kit.Option[string]{{Value: "", Label: i18n.T("settings.reading_language.ui")}}
				for _, l := range languages {
					opts = append(opts, kit.Option[string]{Value: l.tag, Label: l.name})
				}
				return kit.Select(c, &st.ReadingLanguage, opts)
			})
			sel(i18n.T("settings.reply_language"), i18n.T("settings.reply_language.help"), func() ui.Element {
				return kit.Select(c, &st.ReplyLanguage, []kit.Option[string]{
					{Value: "match", Label: i18n.T("settings.reply_language.match")},
					{Value: "reading", Label: i18n.T("settings.reply_language.reading")},
				})
			})
			settingRow(c, i18n.T("settings.no_translate"), i18n.T("settings.no_translate.help"), func() { p.noTranslate(c) })
		})
		settingsSection(c, i18n.T("settings.startup"), "", func() {
			kit.SwitchRow(c, &st.LaunchAtLogin, i18n.T("settings.launch_at_login"), "")
			kit.SwitchRow(c, &st.KeepInBackground, i18n.T("settings.keep_in_background"), i18n.T("settings.keep_in_background.help"))
			kit.SwitchRow(c, &st.UnreadInMenuBar, i18n.T("settings.unread_in_menu_bar"), "")
		})
		settingsSection(c, i18n.T("settings.notifications"), "", func() {
			kit.SwitchRow(c, &st.NotifyNewMail, i18n.T("settings.notify_new_mail"), "")
			kit.SwitchRow(c, &st.NotifyPreview, i18n.T("settings.notify_preview"), i18n.T("settings.notify_preview.help"))
		})
		settingsSection(c, i18n.T("settings.ai"), "", func() {
			kit.SwitchRow(c, &st.Summary, i18n.T("settings.summary"), i18n.T("settings.summary.help"))
			kit.SwitchRow(c, &st.QuickReplies, i18n.T("settings.quick_replies"), i18n.T("settings.quick_replies.help"))
		})
		settingsSection(c, i18n.T("settings.mail"), "", func() {
			kit.SwitchRow(c, &st.ThreadView, i18n.T("settings.thread_view"), i18n.T("settings.thread_view.help"))
		})
	})
}

// themeTile is a small picture of the window in a theme; "system" shows
// both halves.
func (p *settingsPage) themeTile(c *ui.Context, th string) {
	t := theme.Of(c)
	on := p.st.Theme == th
	preview := func(tok theme.Tokens, w float32) {
		side, long, short := w/4, w/3, w/5
		ui.Row(c).Size(w, theme.ThemeTileHeight).AlignItems(ui.Stretch).Background(tok.Window).Children(func() {
			ui.Box(c).Width(min(theme.TileRail, side))
			ui.Box(c).Width(side).Margin(theme.TileInset, 0, 0, 0).Background(tok.Sidebar).Radius(t.Radius.Md, 0, 0, 0)
			ui.Column(c).Grow(1).Margin(theme.TileInset, 0, 0, 0).Padding(theme.TileInset).Gap(theme.TileGap).Background(tok.Background).Children(func() {
				ui.Box(c).Height(theme.TileLine).Width(long).Radius(t.Radius.Full).Background(tok.Border)
				ui.Box(c).Height(theme.TileLine).Width(short).Radius(t.Radius.Full).Background(tok.Border)
			})
		})
	}
	kit.Pressable(c, kit.PressableOpts{Bare: true, OnClick: func() { p.st.Theme = th }}).Gap(c.Theme().Space(1.5)).AlignItems(ui.Start).Children(func() {
		frame := ui.Row(c).Size(theme.ThemeTileWidth, theme.ThemeTileHeight).Radius(t.Radius.Lg).Clip().Border(theme.Hairline, t.Border)
		if on {
			frame.Border(theme.SelectedBorder, t.Primary)
		}
		frame.Children(func() {
			switch th {
			case "system":
				preview(theme.Light(), theme.ThemeTileWidth/2)
				preview(theme.Dark(), theme.ThemeTileWidth/2)
			case "dark":
				preview(theme.Dark(), theme.ThemeTileWidth)
			default:
				preview(theme.Light(), theme.ThemeTileWidth)
			}
		})
		ui.Text(c, i18n.T("settings.theme."+th)).FontSize(t.Text.Sm)
	})
}

// noTranslate picks the languages that need no translation.
func (p *settingsPage) noTranslate(c *ui.Context) {
	th := c.Theme()
	var names []string
	for _, l := range languages {
		if slices.Contains(p.st.NoTranslate, l.tag) {
			names = append(names, l.name)
		}
	}
	label := strings.Join(names, i18n.T("mail.list_sep"))
	if label == "" {
		label = i18n.T("settings.no_translate.none")
	}
	anchor := kit.Button(c, label, kit.ButtonOpts{Variant: kit.Outline}).Width(theme.ControlWidth).Label(i18n.T("settings.no_translate")).
		OnClick(func() { p.noTrans = !p.noTrans })
	kit.Popover(c, anchor, &p.noTrans, func() {
		ui.Column(c).Gap(th.Space(2)).Children(func() {
			for _, l := range languages {
				on := slices.Contains(p.st.NoTranslate, l.tag)
				if kit.Checkbox(c.Key(l.tag), &on, l.name).Changed() {
					if i := slices.Index(p.st.NoTranslate, l.tag); i >= 0 {
						p.st.NoTranslate = slices.Delete(p.st.NoTranslate, i, i+1)
					} else {
						p.st.NoTranslate = append(p.st.NoTranslate, l.tag)
					}
				}
			}
		})
	})
}
