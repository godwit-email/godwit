package app

import (
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/richtext"
	"github.com/godwit-email/godwit/internal/theme"
)

// View draws the conversation and the input below it.
func (p *witPanel) View(c *ui.Context, o witOptions) {
	p.o = o
	th := c.Theme()
	if p.scroll.Y >= p.scroll.MaxY-1 { // at the end: stay there as answers grow
		p.scroll.Y = math.MaxFloat32
	}
	column := func(body func()) {
		ui.Column(c).FillWidth().AlignItems(ui.Center).Padding(0, th.Space(6)).Children(func() {
			col := ui.Column(c).FillWidth().Gap(th.Space(5))
			if o.MaxWidth > 0 {
				col.MaxWidth(o.MaxWidth)
			}
			col.Children(body)
		})
	}
	ui.Column(c).Grow(1).Children(func() {
		ui.Scroll(c).Grow(1).TrackScroll(&p.scroll).Children(func() {
			ui.Column(c).Padding(th.Space(6), 0).Children(func() { column(func() { p.messages(c, o) }) })
		})
		ui.Column(c).Padding(0, 0, th.Space(6), 0).Children(func() { column(func() { p.inputBox(c, o) }) })
	})
}

func (p *witPanel) messages(c *ui.Context, o witOptions) {
	turn := 0
	for i, m := range p.msgs {
		if m.user {
			turn++
			p.bubble(c, m.text)
			continue
		}
		p.answer(c, false, &p.msgs[i], turn)
	}
	if p.liveUser != "" {
		p.bubble(c, p.liveUser)
	}
	if p.live != nil || p.running {
		live := p.live
		if live == nil {
			live = &witMsg{}
		}
		p.answer(c, true, live, p.liveTurn())
	}
	if o.After != nil {
		o.After(c)
	}
	for _, ap := range p.approvals {
		if o.Card != nil && o.Card(c, ap) {
			continue
		}
		n := 0
		for _, x := range p.approvals {
			if x.RunID == ap.RunID {
				n++
			}
		}
		p.card(c, ap, n > 1)
	}
	if msg := humanize(p.err); msg != "" {
		kit.Alert(c, kit.Danger, msg, func() {
			if p.retry != "" && !p.running {
				kit.Button(c, i18n.T("common.retry"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).OnClick(func() {
					ro := o
					if p.retryO.Start != nil {
						ro = p.retryO
					}
					p.ask(ro, p.retry)
				})
			}
		})
	}
}

// bubble is a question: right-aligned on the primary color.
func (p *witPanel) bubble(c *ui.Context, text string) {
	t, th := theme.Of(c), c.Theme()
	ui.Box(c).AlignSelf(ui.End).MaxWidthPercent(80).Padding(th.Space(2.5), th.Space(3.5)).Radius(t.Radius.Xl).
		Background(t.Primary).TextColor(t.PrimaryText).Children(func() { ui.Text(c, text).Selectable() })
}

// answer is the model's: its tool calls folded, a note on each suspect
// message its tools showed, its text as Markdown.
func (p *witPanel) answer(c *ui.Context, live bool, m *witMsg, turn int) {
	t, th := theme.Of(c), c.Theme()
	if m.docOf != m.text { // the live answer: converted at most once a frame
		m.doc, m.docOf = richtext.FromMarkdown(m.text), m.text
	}
	ui.Column(c).Gap(th.Space(2.5)).Children(func() {
		if len(m.calls) > 0 {
			p.tools(c, turn, m.calls)
		}
		cited := m.cited
		if !live {
			cited = p.citedBy(m)
		}
		for i, ct := range p.cited() {
			if !ct.Suspect || !slices.Contains(cited, ct.MessageID) {
				continue
			}
			n := i + 1
			ui.Row(c).Gap(th.Space(2.5)).AlignItems(ui.Start).Padding(th.Space(2.5), th.Space(3)).Radius(t.Radius.Lg).
				Background(t.WarningBg).TextColor(t.WarningText).FontSize(t.Text.Sm).Role(ui.RoleStatus).Children(func() {
				ui.Icon(c, icons.Get("triangle-alert")).Size(theme.Icon, theme.Icon)
				ui.Text(c, i18n.Tf("wit.suspect", citeRef(n))).Grow(1).Shrink(1)
			})
		}
		if strings.TrimSpace(m.text) != "" {
			richtext.Render(c, m.doc, richtext.RenderOptions{FontSize: t.Text.Base, OnLink: p.a.openLink})
		} else if live && p.running {
			kit.Spinner(c)
		}
	})
}

func citeRef(n int) string { return "[" + strconv.Itoa(n) + "]" }

// tools is the folded record of an answer's tool calls: how many tools and
// which; open, each call with its arguments.
func (p *witPanel) tools(c *ui.Context, turn int, calls []ai.ToolCall) {
	t, th := theme.Of(c), c.Theme()
	var names []string
	count := map[string]int{}
	for _, call := range calls {
		if count[call.Name]++; count[call.Name] == 1 {
			names = append(names, call.Name)
		}
	}
	parts := make([]string, 0, len(names))
	for _, n := range names {
		if k := count[n]; k > 1 {
			n += " ×" + strconv.Itoa(k)
		}
		parts = append(parts, n)
	}
	label := i18n.Plural("wit.tools", len(names)) + " · " + strings.Join(parts, i18n.T("mail.list_sep"))
	kit.Collapsible(c, boolAt(p.toolsOpen, turn, false), func() { ui.Text(c, label).Shrink(1) }, func() {
		ui.Column(c).Gap(th.Space(1)).TextColor(t.Muted).Children(func() {
			for _, call := range calls {
				ui.Text(c, call.Name+" "+clip(compactJSON(call.Args), argsRunes)).Font("Geist Mono").FontSize(t.Text.Xs).Selectable()
			}
		})
	}).Label(label)
}

const (
	argsRunes = 160 // a tool call's arguments in its record
	bodyRunes = 240 // a draft's body in its approval card
)

func compactJSON(b json.RawMessage) string {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return string(b)
	}
	out, _ := json.Marshal(v)
	return string(out)
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// card asks about an approval: what Wit wants, the call's recipients,
// subject and body, where it sends data; Allow, Allow all (with others
// of its run pending) and Reject.
func (p *witPanel) card(c *ui.Context, ap core.Approval, many bool) {
	t, th := theme.Of(c), c.Theme()
	var args map[string]any
	_ = json.Unmarshal(ap.Args, &args)
	action := ap.Tool
	if key := "wit.tool." + ap.Tool; i18n.T(key) != key {
		action = i18n.T(key)
	}
	ui.Column(c).Radius(t.Radius.Xl).Border(theme.Hairline, t.Primary).Background(t.Background).Clip().Children(func() {
		ui.Row(c).Wrap().Gap(th.Space(2)).Padding(th.Space(3), th.Space(3.5)).Background(t.Hover).Children(func() {
			ui.Text(c, i18n.Tf("wit.approval.wants", action)).FontWeight(600)
			ui.Text(c, ap.Tool).Font("Geist Mono").FontSize(t.Text.Xs)
		})
		ui.Column(c).Gap(th.Space(1.5)).Padding(th.Space(3), th.Space(3.5)).Children(func() {
			if ap.Summary != "" {
				ui.Text(c, ap.Summary).TextColor(t.Muted)
			}
			field := func(label, value string) {
				if value == "" {
					return
				}
				ui.Row(c).Gap(th.Space(3)).AlignItems(ui.Start).Children(func() {
					ui.Text(c, label).TextColor(t.Muted).Shrink(0)
					ui.Text(c, value).Grow(1).Shrink(1)
				})
			}
			field(i18n.T("wit.approval.to"), strings.Join(strList(args["to"]), ", "))
			field(i18n.T("wit.approval.cc"), strings.Join(strList(args["cc"]), ", "))
			subject, _ := args["subject"].(string)
			field(i18n.T("wit.approval.subject"), subject)
			for _, k := range []string{"html", "body", "text"} {
				if s, ok := args[k].(string); ok && strings.TrimSpace(s) != "" {
					if k == "html" {
						s = mail.HTMLToText(s)
					}
					ui.Text(c, clip(strings.TrimSpace(s), bodyRunes)).TextColor(t.Muted)
					break
				}
			}
			if len(ap.Destinations) > 0 {
				ui.Text(c, i18n.Tf("wit.approval.destinations", strings.Join(ap.Destinations, ", "))).TextColor(t.WarningText)
			}
		})
		ui.Row(c).Wrap().Gap(th.Space(2)).Padding(th.Space(2.5), th.Space(3.5)).BorderWidth(theme.Hairline, 0, 0, 0).BorderColor(t.Border).Children(func() {
			busy := p.deciding[ap.ID]
			kit.Button(c, i18n.T("wit.approval.allow"), kit.ButtonOpts{Disabled: busy}).OnClick(func() { p.decide(ap, true, false) })
			if many {
				kit.Button(c, i18n.T("wit.approval.allow_all"), kit.ButtonOpts{Variant: kit.Outline, Disabled: busy}).OnClick(func() { p.decide(ap, true, true) })
			}
			kit.Button(c, i18n.T("wit.approval.reject"), kit.ButtonOpts{Variant: kit.Outline, Disabled: busy}).OnClick(func() { p.decide(ap, false, false) })
			if ap.Tool == "create_draft" {
				ui.Spacer(c)
				ui.Text(c, i18n.T("wit.approval.send_note")).FontSize(t.Text.Sm).TextColor(t.Muted)
			}
		})
	})
}

func strList(v any) []string {
	switch v := v.(type) {
	case string:
		return []string{v}
	case []any:
		var out []string
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// inputBox is the question's text area: Enter sends, Shift+Enter starts a
// new line; while a run goes on Send is Stop. Below it the default model,
// which opens the model settings. The box floats (kit.Float).
func (p *witPanel) inputBox(c *ui.Context, o witOptions) {
	t, th := theme.Of(c), c.Theme()
	if o.Tools != nil {
		o.Tools(c)
	}
	label := o.Label
	if label == "" {
		label = i18n.T("wit.input")
	}
	box := ui.Column(c).Gap(th.Space(2)).Padding(th.Space(2.5), th.Space(3)).Radius(t.Radius.X2l).Background(t.Background)
	kit.Float(c, box).Children(func() {
		area := kit.TextArea(c, &p.input, kit.InputOpts{Bare: true, Placeholder: o.Placeholder}).Lines(2, 8).Label(label).
			HandleInput(func(ev ui.InputEvent) bool { // before the text area's own Enter, a new line
				if ev.Kind != ui.InputKeyDown || ev.Key != ui.KeyEnter || ev.Mods != 0 || p.composing {
					return false
				}
				p.send(o)
				return true
			})
		p.composing = area.Composing()
		ui.Row(c).Gap(th.Space(2)).Children(func() {
			if m := modelOf(p.a.settings.DefaultModel); m != "" {
				b := kit.ButtonFrame(c, kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeSm}).Label(m).
					OnClick(func() { p.a.navigate(Route{Page: PageSettings, Settings: "models"}) })
				kit.Tooltip(c, b, i18n.T("wit.model"))
				b.Children(func() {
					ui.Text(c, m)
					ui.Icon(c, icons.Get("chevron-down")).Size(theme.IconXs, theme.IconXs).TextColor(t.Muted)
				})
			}
			ui.Spacer(c)
			if p.running {
				kit.Button(c, i18n.T("wit.stop"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(p.stopRun)
			} else {
				kit.Button(c, i18n.T("wit.send"), kit.ButtonOpts{}).OnClick(func() { p.send(o) })
			}
		})
	})
}
