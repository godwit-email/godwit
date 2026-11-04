package kit

import (
	"regexp"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/theme"
)

// TemplateToken is a token a TemplateField offers: stored as {ID}, shown as Label.
type TemplateToken struct{ ID, Label string }

// TemplateOpts name a TemplateField and what it offers.
type TemplateOpts struct {
	Label        string          // the field's name, for its input
	Tokens       []TemplateToken // the tokens, in the order their buttons show
	Insert       string          // the heading before the token buttons
	Remove       func(label string) string
	Placeholder  string
	Error        string // in place of Preview; the box turns red
	Preview      string // what the value gives, as an example
	PreviewLabel string // before Preview, as 示例
}

// TemplateField edits *value, a template of literal text and {id} tokens
// (a 命名模板): tokens show as chips that a button takes out, then an
// input for the text after them; the buttons below add a token at the end,
// and Backspace in the empty input takes out the last one. Text between
// tokens stays as typed. An {id} not in Tokens is text.
func TemplateField(c *ui.Context, value *string, o TemplateOpts) ui.Element {
	t, th := theme.Of(c), c.Theme()
	segs := parseTemplate(*value, o.Tokens)
	tail := "" // the text after the last token, which the input edits
	if n := len(segs); n > 0 && segs[n-1].id == "" {
		tail, segs = segs[n-1].text, segs[:n-1]
	}
	head := formatTemplate(segs)
	label := func(id string) string {
		i := slices.IndexFunc(o.Tokens, func(k TemplateToken) bool { return k.ID == id })
		return o.Tokens[i].Label
	}
	return ui.Column(c).FillWidth().Gap(th.Space(1.5)).Children(func() {
		// A line of tokens is 24 high, so one line (with the border) makes the box an Input's 32.
		box := ui.Row(c).FillWidth().Wrap().AlignItems(ui.Center).Gap(th.Space(1)).MinHeight(th.Space(8)).Padding(th.Space(1)-theme.Stroke, th.Space(2))
		inputFrame(c, box, InputOpts{Invalid: o.Error != "", Flat: true}).Children(func() {
			for i, s := range segs {
				if s.id == "" {
					Text(c.Key(i), s.text, t.Text.Sm).Font(theme.Mono)
					continue
				}
				l := label(s.id)
				ui.Row(c.Key(i)).Height(th.Space(6)).AlignItems(ui.Center).Gap(th.Space(0.5)).Padding(0, th.Space(1), 0, th.Space(2)).
					Radius(t.Radius.Sm).Background(t.Hover).Children(func() {
					Text(c, l, t.Text.Sm).TextColor(t.Foreground)
					IconButton(c, "x", o.Remove(l), ButtonOpts{Variant: Ghost, Size: SizeXs}).
						OnClick(func() { *value = formatTemplate(slices.Delete(slices.Clone(segs), i, i+1)) + tail })
				})
			}
			ph := ""
			if len(segs) == 0 {
				ph = o.Placeholder
			}
			ui.TextInputBase(c.Key("input"), &tail).Label(o.Label).Placeholder(ph).FocusRing(false).Grow(1).MinWidth(th.Space(16)).Height(th.Space(6)).
				Font(theme.Mono).FontSize(t.Text.Sm).
				OnChange(func() { *value = head + tail }).
				OnTextKey(func(k ui.TextKey) bool {
					if k.Key != ui.KeyBackspace || k.Mods != 0 || k.Text() != "" || len(segs) == 0 {
						return false
					}
					*value = formatTemplate(segs[:len(segs)-1])
					return true
				})
		})
		ui.Row(c).Wrap().AlignItems(ui.Center).Gap(th.Space(1)).Children(func() {
			Text(c, o.Insert, t.Text.Sm).TextColor(t.Muted)
			for _, k := range o.Tokens {
				Button(c.Key(k.ID), k.Label, ButtonOpts{Variant: Outline, Size: SizeSm, Icon: "plus"}).
					OnClick(func() { *value += "{" + k.ID + "}" })
			}
		})
		switch {
		case o.Error != "":
			Text(c, o.Error, t.Text.Sm).TextColor(t.Danger)
		case o.Preview != "":
			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(th.Space(1.5)).Children(func() {
				if o.PreviewLabel != "" {
					Text(c, o.PreviewLabel, t.Text.Sm).TextColor(t.Muted).Shrink(0)
				}
				Text(c, o.Preview, t.Text.Sm).TextColor(t.Muted).Font(theme.Mono).SingleLine().Shrink(1)
			})
		}
	})
}

// templateSeg is a token (id) or literal text.
type templateSeg struct{ id, text string }

var templateToken = regexp.MustCompile(`\{([a-z]+)\}`)

func parseTemplate(v string, tokens []TemplateToken) []templateSeg {
	var out []templateSeg
	text := func(s string) {
		if s == "" {
			return
		}
		if n := len(out); n > 0 && out[n-1].id == "" {
			out[n-1].text += s
			return
		}
		out = append(out, templateSeg{text: s})
	}
	last := 0
	for _, m := range templateToken.FindAllStringSubmatchIndex(v, -1) {
		id := v[m[2]:m[3]]
		if !slices.ContainsFunc(tokens, func(k TemplateToken) bool { return k.ID == id }) {
			continue
		}
		text(v[last:m[0]])
		out = append(out, templateSeg{id: id})
		last = m[1]
	}
	text(v[last:])
	return out
}

func formatTemplate(segs []templateSeg) string {
	var b strings.Builder
	for _, s := range segs {
		if s.id != "" {
			b.WriteString("{" + s.id + "}")
		} else {
			b.WriteString(s.text)
		}
	}
	return b.String()
}
