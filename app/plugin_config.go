package app

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// pluginForm is a plugin's settings form, generated from its config fields.
type pluginForm struct {
	name string
	vals map[string]*cfgVal
	err  string // a failure that belongs to no one field
	busy bool
}

// cfgVal is one field: s for text, number, select and secret (typed only,
// never the stored one), on for boolean; orig* as loaded.
type cfgVal struct {
	s, orig   string
	on, origO bool
	set       bool // stored (a secret: configured)
	err       string
}

func newPluginForm(name string, vals []core.ConfigValue) *pluginForm {
	f := &pluginForm{name: name, vals: map[string]*cfgVal{}}
	for _, v := range vals {
		cv := &cfgVal{set: v.Set}
		var s string
		switch {
		case json.Unmarshal(v.Value, &cv.on) == nil:
		case json.Unmarshal(v.Value, &s) == nil:
		case len(v.Value) > 0 && string(v.Value) != "null":
			s = string(v.Value) // a number
		}
		cv.s, cv.orig, cv.origO = s, s, cv.on
		f.vals[v.Key] = cv
	}
	return f
}

// raw is the field's value as the form holds it now; nil when empty.
func (f *pluginForm) raw(fd plugins.ConfigField) json.RawMessage {
	v := f.vals[fd.Key]
	if v == nil {
		return nil
	}
	if fd.Type == "boolean" {
		return json.RawMessage(strconv.FormatBool(v.on))
	}
	if fd.Type == "number" {
		if n := strings.TrimSpace(v.s); n == "" {
			return nil
		} else if _, err := strconv.ParseFloat(n, 64); err == nil {
			return json.RawMessage(n)
		}
	}
	if v.s == "" {
		return nil
	}
	b, _ := json.Marshal(v.s) // verbatim: spaces may belong to the value
	return b
}

// shown says which fields their when shows for the form's values now, as
// the Service decides it: an empty field counts as its default, a hidden
// one as unset.
func (f *pluginForm) shown(fields []plugins.ConfigField) map[string]bool {
	shown := map[string]bool{}
	byKey := map[string]plugins.ConfigField{}
	for _, fd := range fields {
		var ref json.RawMessage
		if w := fd.When; w != nil && shown[w.Key] {
			if ref = f.raw(byKey[w.Key]); ref == nil {
				ref = byKey[w.Key].Default
			}
		}
		shown[fd.Key] = fd.When == nil || fd.When.Match(ref)
		byKey[fd.Key] = fd
	}
	return shown
}

// values are what Save sends: the shown fields that changed or are stored,
// and only the secrets typed in. A stored field is resent unchanged because
// SetPluginConfig unsets every non-secret field it is not given; hidden
// fields are left out, and the Service keeps what they had.
func (f *pluginForm) values(fields []plugins.ConfigField) map[string]json.RawMessage {
	shown := f.shown(fields)
	out := map[string]json.RawMessage{}
	for _, fd := range fields {
		v := f.vals[fd.Key]
		if v == nil || !shown[fd.Key] {
			continue
		}
		changed := v.s != v.orig || v.on != v.origO
		switch {
		case fd.Type == "secret":
			if v.s != "" {
				out[fd.Key] = f.raw(fd)
			}
		case changed || v.set:
			out[fd.Key] = f.raw(fd)
		}
	}
	return out
}

func (e *extView) saveConfig(f *pluginForm, fields []plugins.ConfigField) {
	a, name := e.a, f.name
	vals := f.values(fields)
	sent := f.snapshot()
	f.busy, f.err = true, ""
	for _, v := range f.vals {
		v.err = ""
	}
	var got []core.ConfigValue
	a.writes.Chain("plugins", func(ctx context.Context) error {
		if err := a.svc.SetPluginConfig(ctx, name, vals); err != nil {
			return err
		}
		var err error
		got, err = a.svc.GetPluginConfig(ctx, name)
		return err
	}, func(err error) {
		f.busy = false
		if e.cfgs[name] != f {
			a.toastErr(err)
			return
		}
		if err != nil {
			f.fieldError(err, fields)
			return
		}
		nf := newPluginForm(name, got)
		e.cfgs[name] = nf
		for k, v := range f.vals { // edits made while it saved stay
			if nv := nf.vals[k]; nv != nil && [2]any{v.s, v.on} != sent[k] {
				nv.s, nv.on = v.s, v.on
			}
		}
		a.toastAs(kit.ToastSuccess, i18n.T("settings.saved"))
	})
}

// snapshot is each field's value now.
func (f *pluginForm) snapshot() map[string][2]any {
	out := map[string][2]any{}
	for k, v := range f.vals {
		out[k] = [2]any{v.s, v.on}
	}
	return out
}

// fieldError shows err under the field it names ("config <key>…"), or
// above the buttons.
func (f *pluginForm) fieldError(err error, fields []plugins.ConfigField) {
	msg := err.Error()
	for _, fd := range fields {
		rest, ok := strings.CutPrefix(msg, "config "+fd.Key)
		if v := f.vals[fd.Key]; ok && v != nil && (strings.HasPrefix(rest, " ") || strings.HasPrefix(rest, ":")) {
			if strings.TrimSpace(rest) == "is required" {
				v.err = i18n.T("settings.ext.config.required")
			} else {
				v.err = humanize(err)
			}
			return
		}
	}
	f.err = humanize(err)
}

// configView is the plugin's settings form; save adds its 保存 button (a
// pending plugin's form is saved by 允许并启用).
func (e *extView) configView(c *ui.Context, f *pluginForm, fields []plugins.ConfigField, save bool) {
	th := c.Theme()
	ui.Column(c).FillWidth().Gap(th.Space(3)).Children(func() { e.configFields(c, f, fields, save) })
}

func (e *extView) configFields(c *ui.Context, f *pluginForm, fields []plugins.ConfigField, save bool) {
	t, th := theme.Of(c), c.Theme()
	lang := i18n.Language()
	ui.Row(c).Gap(th.Space(1)).AlignItems(ui.Center).Children(func() {
		kit.Text(c, i18n.T("settings.ext.config"), t.Text.Body).LineHeight(theme.LineFor(i18n.T("settings.ext.config"))).FontWeight(500)
		kit.Text(c, i18n.T("settings.ext.config.sub"), t.Text.Sm).TextColor(t.Muted)
	})
	shown := f.shown(fields)
	for _, fd := range fields {
		v := f.vals[fd.Key]
		if v == nil || !shown[fd.Key] {
			continue
		}
		label := fd.Label.For(lang)
		cc := c.Key(fd.Key)
		if fd.Type == "boolean" {
			kit.SwitchRow(cc, &v.on, label, v.err).Padding(0)
			continue
		}
		ui.Field(cc, label, func() {
			switch {
			case fd.Type == "secret":
				ph := ""
				if v.set {
					ph = i18n.T("settings.secret_kept")
				}
				kit.Input(c, &v.s, kit.InputOpts{Password: true, Placeholder: ph, Invalid: v.err != ""})
			case fd.Type == "select" && fd.AllowCustom:
				var sugg []string
				for _, o := range fd.Options {
					sugg = append(sugg, o.Value)
				}
				box := kit.InputGroup(c, kit.InputOpts{Invalid: v.err != "", Flat: true})
				box.Children(func() {
					ui.Autocomplete(c, &v.s, sugg).Label(label).Grow(1).MinWidth(0).
						Height(th.Space(8)-theme.Hairline-theme.Hairline).Padding(0, th.Space(2.5)).Border(0, t.Input).FocusRing(false)
				})
			case fd.Type == "select":
				opts := make([]kit.Option[string], len(fd.Options))
				for i, o := range fd.Options {
					opts[i] = kit.Option[string]{Value: o.Value, Label: o.Label.For(lang)}
				}
				kit.Select(c, &v.s, opts).Label(label)
			default:
				kit.Input(c, &v.s, kit.InputOpts{Invalid: v.err != ""})
			}
		}).Error(v.err).LineHeight(theme.LineFor(label)) // the label's line, as LineFor says
	}
	if f.err != "" {
		kit.Alert(c, kit.Danger, f.err, nil)
	}
	if save {
		kit.Button(c, i18n.T("common.save"), kit.ButtonOpts{Disabled: f.busy}).AlignSelf(ui.Start).OnClick(func() { e.saveConfig(f, fields) })
	}
}
