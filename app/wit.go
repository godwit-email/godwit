package app

import (
	"context"
	"encoding/json"
	"errors"
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

// witMsg is a message of a Wit conversation as the panel shows it: a
// question, or the answer to it with the tools the model called.
type witMsg struct {
	user  bool
	text  string
	doc   richtext.Doc // the answer converted from docOf
	docOf string
	calls []ai.ToolCall
}

// witOptions are what the panel's owner decides. Start begins a run on
// input off the UI thread, creating the conversation first when the panel
// has none, and returns that conversation (with the error too, once it
// exists). Changed, if set, hears on the UI thread that the panel took a
// conversation or a run ended, so lists of conversations read again.
type witOptions struct {
	Placeholder string
	MaxWidth    float32 // 0 fills the width
	Start       func(ctx context.Context, input string) (conv int64, ch <-chan core.WitEvent, err error)
	Changed     func()
}

// witPanel shows a Wit conversation and follows its runs: the Ask page and
// the compose page each have one. What streams in shows at once; whenever
// a stream ends, what the conversation saved replaces it, as streams drop
// text under backpressure. All state is the UI thread's.
type witPanel struct {
	a      *App
	conv   int64
	opened bool
	gen    uint64 // bumped per conversation opened: results for another are dropped
	reads  uint64 // bumped per read of the saved conversation: only the newest applies

	msgs      []witMsg        // as saved
	approvals []core.Approval // pending, in order
	cites     []core.Citation // numbered from 1 as they came
	notes     map[int][]int   // per turn (its question's 1-based number), the suspect citations' numbers

	live     *witMsg // the answer streaming in; nil before any
	liveUser string  // the question just asked, until it is saved
	fresh    bool    // the run followed answers a new question, not the saved last one
	running  bool
	runID    int64
	err      error  // how the last run failed
	retry    string // the question Retry asks again
	deciding bool   // an approval's decision is on its way

	follows uint64             // bumped per follow: an older one's events are dropped
	unsub   func()             // ends the subscription followed; nil for none
	over    bool               // the follow has ended: the next read replaces what streamed
	cancel  context.CancelFunc // cancels the Ask call in flight

	input     string
	composing bool
	toolsOpen map[int]bool // tool records opened, by turn: they stay open as the saved answer replaces the live one
	scroll    ui.ScrollState
	o         witOptions // as of the last frame

	resolve    func(ctx context.Context, id int64, accept bool) error // tests swap them
	resolveAll func(ctx context.Context, runID int64, accept bool) error
}

func newWitPanel(a *App) *witPanel {
	return &witPanel{a: a, resolve: a.svc.ResolveApproval, resolveAll: a.svc.ResolveAll, notes: map[int][]int{}, toolsOpen: map[int]bool{}}
}

// open shows conv (0: a new conversation). Another conversation is read
// afresh after subscribing to it, so a run in flight is followed to its end;
// the one shown is read again.
func (p *witPanel) open(conv int64) {
	if p.opened && conv == p.conv {
		p.reconcile()
		return
	}
	p.stopFollow()
	*p = witPanel{a: p.a, resolve: p.resolve, resolveAll: p.resolveAll, gen: p.gen + 1, follows: p.follows,
		conv: conv, opened: true, input: p.input, notes: map[int][]int{}, toolsOpen: map[int]bool{}, fresh: true}
	p.scroll.Y = math.MaxFloat32
	if conv == 0 {
		return
	}
	svc := p.a.svc
	p.follow(func(context.Context) (<-chan core.WitEvent, func(), error) {
		ch, stop := svc.SubscribeConversation(conv)
		return ch, stop, nil
	}, nil)
}

// stopFollow drops the follow in flight and ends its subscription; a run
// it followed goes on.
func (p *witPanel) stopFollow() {
	p.follows++
	if p.unsub != nil {
		p.unsub()
	}
	p.unsub, p.over, p.cancel = nil, false, nil
}

// follow shows the events of the stream start opens (off the UI thread)
// until one ends the run or the stream closes, then reads what the
// conversation saved. stop, if start returns it, ends the stream: once it
// is over, or when another follow takes over. opened, if set, hears start's
// error on the UI thread first. A nil stream has nothing to follow.
func (p *witPanel) follow(start func(ctx context.Context) (<-chan core.WitEvent, func(), error), opened func(err error)) {
	p.stopFollow()
	a, tok := p.a, p.follows
	a.do(func(ctx context.Context) error {
		ch, stop, err := start(ctx)
		a.onUI(func() {
			if tok != p.follows {
				if stop != nil {
					stop()
				}
				return
			}
			p.unsub = stop
			if opened != nil {
				opened(err)
			}
			if ch != nil {
				p.reconcile() // as saved now; the stream brings the rest
			}
		})
		if ch == nil {
			return nil
		}
		for ev := range ch {
			a.onUI(func() {
				if tok == p.follows {
					p.apply(ev)
				}
			})
			if ev.Kind == "done" || ev.Kind == "error" || ev.Kind == "interrupted" {
				break
			}
		}
		if stop != nil {
			stop()
		}
		return nil
	}, func(error) {
		if tok != p.follows {
			return
		}
		p.unsub, p.over, p.running, p.cancel = nil, true, false, nil
		p.reconcile()
		if p.o.Changed != nil {
			p.o.Changed()
		}
	})
}

// apply shows an event of the run followed.
func (p *witPanel) apply(ev core.WitEvent) {
	if ev.RunID != 0 {
		p.runID = ev.RunID
	}
	for _, c := range ev.Citations {
		p.cite(c)
	}
	switch ev.Kind {
	case "text", "tool_call", "tool_result":
		p.running = true
		if p.live == nil {
			p.live = &witMsg{}
		}
		if ev.Kind == "text" {
			p.live.text += ev.Text
		}
		if ev.Kind == "tool_call" && ev.Call != nil {
			p.live.calls = append(p.live.calls, *ev.Call)
		}
	case "error":
		p.running = false
		if ev.Err != nil && humanize(ev.Err) != "" { // a stop is no failure
			p.err = ev.Err
		}
	case "done", "interrupted":
		p.running = false
	}
}

// cite numbers a citation once; a suspect one gets a note on the turn.
func (p *witPanel) cite(c core.Citation) {
	i := slices.IndexFunc(p.cites, func(x core.Citation) bool { return x.MessageID == c.MessageID })
	if i < 0 {
		p.cites, i = append(p.cites, c), len(p.cites)
	}
	if turn := p.liveTurn(); c.Suspect && !slices.Contains(p.notes[turn], i+1) {
		p.notes[turn] = append(p.notes[turn], i+1)
	}
}

func (p *witPanel) users() int {
	n := 0
	for _, m := range p.msgs {
		if m.user {
			n++
		}
	}
	return n
}

// liveTurn is the turn the run followed answers.
func (p *witPanel) liveTurn() int {
	if p.fresh {
		return p.users() + 1
	}
	return p.users()
}

// reconcile reads what the conversation saved and its pending approvals;
// once the follow is over the saved messages replace what streamed in.
func (p *witPanel) reconcile() {
	if p.conv == 0 {
		return
	}
	p.reads++
	a, gen, read, conv, tok, over := p.a, p.gen, p.reads, p.conv, p.follows, p.over
	var c core.Conversation
	var as []core.Approval
	var msgs []witMsg
	a.do(func(ctx context.Context) (err error) {
		if c, err = a.svc.GetConversation(ctx, conv); err != nil {
			return err
		}
		as, err = a.svc.ListApprovals(ctx, conv)
		msgs = witMsgs(c.Messages)
		return err
	}, func(err error) {
		if gen != p.gen || read != p.reads {
			return
		}
		if err != nil {
			a.toastErr(err)
			return
		}
		p.msgs = msgs
		p.approvals = slices.DeleteFunc(as, func(x core.Approval) bool { return x.State != "pending" })
		if over && tok == p.follows && p.over {
			p.live, p.liveUser = nil, ""
		}
	})
}

// witMsgs groups saved messages into questions and answers: an answer is
// everything the model said and called up to the next question.
func witMsgs(ms []ai.Message) []witMsg {
	var out []witMsg
	for _, m := range ms {
		switch m.Role {
		case "user":
			out = append(out, witMsg{user: true, text: m.Text})
		case "tool":
		default:
			if n := len(out); n == 0 || out[n-1].user {
				out = append(out, witMsg{})
			}
			ans := &out[len(out)-1]
			if strings.TrimSpace(m.Text) != "" {
				if ans.text != "" {
					ans.text += "\n\n"
				}
				ans.text += m.Text
			}
			ans.calls = append(ans.calls, m.Calls...)
		}
	}
	for i := range out {
		if !out[i].user {
			out[i].doc, out[i].docOf = richtext.FromMarkdown(out[i].text), out[i].text
		}
	}
	return out
}

// send asks what the input holds.
func (p *witPanel) send(o witOptions) {
	in := strings.TrimSpace(p.input)
	if in == "" || p.running || o.Start == nil {
		return
	}
	p.input = ""
	p.ask(o, in)
}

// ask starts a run on in and follows its stream.
func (p *witPanel) ask(o witOptions, in string) {
	start, a := o.Start, p.a
	// Not under a.ctx: only Stop cancels the call, and the run outlives the
	// panel showing it. The Service's close ends it.
	ctx, cancel := context.WithCancel(context.WithoutCancel(a.ctx))
	var conv int64
	p.follow(func(context.Context) (ch <-chan core.WitEvent, _ func(), err error) {
		conv, ch, err = start(ctx, in)
		return ch, nil, err
	}, func(err error) {
		if conv != 0 && conv != p.conv {
			p.conv = conv
			if p.o.Changed != nil {
				p.o.Changed()
			}
		}
		if err != nil {
			p.err, p.liveUser, p.running = err, "", false
		}
	})
	p.cancel = cancel
	p.retry, p.err, p.liveUser, p.live, p.fresh, p.running, p.runID = in, nil, in, nil, true, true, 0
	p.scroll.Y = math.MaxFloat32
}

// stopRun cancels the run in flight.
func (p *witPanel) stopRun() {
	if p.cancel != nil {
		p.cancel() // the run of an Ask call before its first event
	}
	if id := p.runID; id != 0 {
		p.a.do(func(ctx context.Context) error { return p.a.svc.CancelRun(ctx, id) }, func(err error) {
			if err != nil {
				p.a.toastErr(err)
			}
		})
	}
}

// decide accepts or rejects an approval, or with all every pending one of
// its run. It subscribes first: the run resumes once nothing of it is left
// open and may end before the decision returns.
func (p *witPanel) decide(ap core.Approval, accept, all bool) {
	svc, conv := p.a.svc, p.conv
	resolve, resolveAll := p.resolve, p.resolveAll
	p.deciding, p.err, p.fresh, p.live = true, nil, false, nil
	p.follow(func(ctx context.Context) (<-chan core.WitEvent, func(), error) {
		ch, stop := svc.SubscribeConversation(conv)
		var err error
		if all {
			err = resolveAll(ctx, ap.RunID, accept)
		} else {
			err = resolve(ctx, ap.ID, accept)
		}
		as, lerr := svc.ListApprovals(ctx, conv)
		if lerr != nil || slices.ContainsFunc(as, func(x core.Approval) bool { return x.RunID == ap.RunID && x.State == "pending" }) {
			stop() // the run waits for the rest: nothing resumes
			return nil, nil, errors.Join(err, lerr)
		}
		return ch, stop, err
	}, func(err error) {
		p.deciding = false
		if err != nil {
			p.a.toastErr(err)
		}
	})
}

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
	noted := map[int]bool{}
	turn := 0
	for i, m := range p.msgs {
		if m.user {
			turn++
			p.bubble(c, m.text)
			continue
		}
		p.answer(c, false, &p.msgs[i], turn, noted)
	}
	if p.liveUser != "" {
		p.bubble(c, p.liveUser)
	}
	if p.live != nil || p.running {
		live := p.live
		if live == nil {
			live = &witMsg{}
		}
		p.answer(c, true, live, p.liveTurn(), noted)
	}
	for _, ap := range p.approvals {
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
				kit.Button(c, i18n.T("common.retry"), kit.ButtonOpts{Variant: kit.Ghost, Small: true}).OnClick(func() { p.ask(o, p.retry) })
			}
		})
	}
}

// bubble is a question: right-aligned on the primary color.
func (p *witPanel) bubble(c *ui.Context, text string) {
	t, th := theme.Of(c), c.Theme()
	ui.Box(c).AlignSelf(ui.End).MaxWidthPercent(80).Padding(th.Space(2.5), th.Space(3.5)).Radius(t.Radius.Panel).
		Background(t.Primary).TextColor(t.PrimaryText).Children(func() { ui.Text(c, text).Selectable() })
}

// answer is the model's: its tool calls folded, the suspect-mail notes of
// its turn (once per turn), its text as Markdown.
func (p *witPanel) answer(c *ui.Context, live bool, m *witMsg, turn int, noted map[int]bool) {
	t, th := theme.Of(c), c.Theme()
	if m.docOf != m.text { // the live answer: converted at most once a frame
		m.doc, m.docOf = richtext.FromMarkdown(m.text), m.text
	}
	ui.Column(c).Gap(th.Space(2.5)).Children(func() {
		if len(m.calls) > 0 {
			p.tools(c, turn, m.calls)
		}
		if !noted[turn] {
			noted[turn] = true
			for _, n := range p.notes[turn] {
				ui.Row(c).Gap(th.Space(2.5)).AlignItems(ui.Start).Padding(th.Space(2.5), th.Space(3)).Radius(t.Radius.Md).
					Background(t.WarningBg).TextColor(t.WarningText).FontSize(t.Text.Sm).Role(ui.RoleStatus).Children(func() {
					ui.Icon(c, icons.Get("triangle-alert")).Size(theme.Icon, theme.Icon)
					ui.Text(c, i18n.Tf("wit.suspect", citeRef(n))).Grow(1).Shrink(1)
				})
			}
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
	open := p.toolsOpen[turn]
	chevron := "chevron-right"
	if open {
		chevron = "chevron-down"
	}
	ui.ButtonBase(c).Label(label).Justify(ui.Start).Gap(th.Space(1)).FontSize(t.Text.Sm).TextColor(t.Muted).Children(func() {
		ui.Icon(c, icons.Get(chevron)).Size(theme.IconXs, theme.IconXs)
		ui.Text(c, label).Shrink(1)
	}).OnClick(func() { p.toolsOpen[turn] = !open })
	if !open {
		return
	}
	ui.Column(c).Gap(th.Space(1)).TextColor(t.Muted).Children(func() {
		for _, call := range calls {
			ui.Text(c, call.Name+" "+clip(compactJSON(call.Args), argsRunes)).Font("Geist Mono").FontSize(t.Text.Xs).Selectable()
		}
	})
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
	ui.Column(c).Radius(t.Radius.Lg).Border(theme.Hairline, t.Primary).Background(t.Background).Clip().Children(func() {
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
			busy := p.deciding
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
// which opens the model settings.
func (p *witPanel) inputBox(c *ui.Context, o witOptions) {
	t, th := theme.Of(c), c.Theme()
	ui.Column(c).Gap(th.Space(2)).Padding(th.Space(2.5), th.Space(3)).Radius(t.Radius.Panel).Border(theme.Hairline, t.Border).Background(t.Background).Children(func() {
		area := ui.TextAreaBase(c, &p.input).Lines(2, 8).Placeholder(o.Placeholder).Label(i18n.T("wit.input")).
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
				kit.Button(c, m, kit.ButtonOpts{Variant: kit.Outline, Small: true}).Tooltip(i18n.T("wit.model")).
					OnClick(func() { p.a.navigate(Route{Page: PageSettings, Settings: "models"}) })
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
