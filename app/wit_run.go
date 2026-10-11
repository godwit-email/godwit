package app

import (
	"context"
	"errors"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/internal/richtext"
)

// witMsg is a message of a Wit conversation as the panel shows it: a
// question, or the answer to it with the tools the model called. The live
// answer notes the messages its events cited; a saved one is
// Conversation.Messages[from:to] and finds them by the runs there (citedBy).
type witMsg struct {
	user     bool
	text     string
	doc      richtext.Doc // the answer converted from docOf
	docOf    string
	calls    []ai.ToolCall
	cited    []int64 // the live answer's
	from, to int
}

// witOptions are what the panel's owner decides. Start begins a run on
// input off the UI thread, creating the conversation first when the panel
// has none, and returns that conversation (with the error too, once it
// exists) and, for a subscription, what ends it. Changed, if set, hears on
// the UI thread that the panel took a conversation or a run ended, so lists
// of conversations read again. Gate, if set, comes before every run: it
// calls run to start it. Card, if set, draws an approval in place of the
// panel's card, reporting whether it did; Tools draws above the input,
// After below the conversation.
type witOptions struct {
	Placeholder, Label string  // Label "" names the input wit.input
	MaxWidth           float32 // 0 fills the width
	Start              func(ctx context.Context, input string) (conv int64, ch <-chan core.WitEvent, stop func(), err error)
	Changed            func()
	Gate               func(run func())
	Hold               func() bool // true refuses runs and decisions for now
	Pinned             bool        // Start is the turn's own: Retry runs it, not the Start of the moment
	Card               func(c *ui.Context, ap core.Approval) bool
	Tools, After       func(c *ui.Context)
}

func (o witOptions) held() bool { return o.Hold != nil && o.Hold() }

// witPanel shows a Wit conversation and follows its runs: the Ask page and
// the compose page each have one. What streams in shows at once; whenever
// a stream ends, what the conversation saved replaces it, as streams drop
// text under backpressure. Its writes (asking, deciding, stopping) chain on
// the conversation's key in the App's write queue. All state is the UI
// thread's.
type witPanel struct {
	a      *App
	conv   int64
	opened bool
	gen    uint64 // bumped per conversation opened: results for another are dropped
	reads  uint64 // bumped per read of the saved conversation: only the newest applies

	msgs      []witMsg        // as saved
	approvals []core.Approval // pending, in order
	cites     []core.Citation // every citation, saved then streamed, in the order they came
	runs      []core.RunSpan  // the saved messages each run added

	live     *witMsg // the answer streaming in; nil before any
	liveUser string  // the question just asked, until it is saved
	fresh    bool    // the run followed answers a new question, not the saved last one
	running  bool
	runID    int64
	err      error          // how the last run failed
	retry    string         // the question Retry asks again
	retryO   witOptions     // the options it was asked with, when pinned
	deciding map[int64]bool // approvals whose decision is on its way

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
	return &witPanel{a: a, resolve: a.svc.ResolveApproval, resolveAll: a.svc.ResolveAll, deciding: map[int64]bool{}, toolsOpen: map[int]bool{}}
}

// convKey is the write-queue key of a conversation's writes; "conv:new"
// for creating one.
func convKey(id int64) string {
	if id == 0 {
		return "conv:new"
	}
	return "conv:" + strconv.FormatInt(id, 10)
}

// open shows conv (0: a new conversation). Another conversation is read
// afresh after subscribing to it, so a run in flight is followed to its end
// (and can be stopped at once: the read tells its run); the one shown is
// read again.
func (p *witPanel) open(conv int64) {
	if p.opened && conv == p.conv {
		p.reconcile()
		return
	}
	p.stopFollow()
	*p = witPanel{a: p.a, resolve: p.resolve, resolveAll: p.resolveAll, gen: p.gen + 1, follows: p.follows,
		conv: conv, opened: true, input: p.input, deciding: map[int64]bool{}, toolsOpen: map[int]bool{}, fresh: true}
	p.scroll.Y = math.MaxFloat32
	if conv == 0 {
		return
	}
	svc := p.a.svc
	p.follow("", func(context.Context) (<-chan core.WitEvent, func(), error) {
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

// follow shows the events of the stream start opens (off the UI thread,
// chained on key in the write queue when key is set) until one ends the run
// or the stream closes, then reads what the conversation saved. stop, if
// start returns it, ends the stream: once it is over, or when another
// follow takes over. opened, if set, hears start's error on the UI thread
// first. A nil stream has nothing to follow.
func (p *witPanel) follow(key string, start func(ctx context.Context) (<-chan core.WitEvent, func(), error), opened func(err error)) {
	p.stopFollow()
	a, tok := p.a, p.follows
	var ch <-chan core.WitEvent
	var stop func()
	run := func(ctx context.Context) (err error) {
		ch, stop, err = start(ctx)
		return err
	}
	started := func(err error) {
		if tok != p.follows {
			switch {
			case stop != nil:
				stop()
			case ch != nil: // an Ask stream nobody shows: read it out, so its run is not held up
				a.do(func(context.Context) error {
					for range ch {
					}
					return nil
				}, nil)
			}
			return
		}
		p.unsub = stop
		if opened != nil {
			opened(err)
		}
		if ch == nil {
			p.ended()
			return
		}
		p.reconcile() // as saved now; the stream brings the rest
		ch, stop := ch, stop
		a.do(func(context.Context) error {
			for ev := range ch {
				a.onUI(func() {
					if tok == p.follows {
						p.apply(ev)
					}
				})
				if ends(ev) {
					break
				}
			}
			if stop != nil {
				stop()
			}
			return nil
		}, func(error) {
			if tok == p.follows {
				p.ended()
			}
		})
	}
	if key == "" {
		a.do(run, started)
	} else {
		a.writes.Chain(key, run, started)
	}
}

// ends reports whether ev ends the run followed: an interrupt whose
// approvals were all decided at once (a first generation) resumes it.
func ends(ev core.WitEvent) bool {
	switch ev.Kind {
	case "done", "error":
		return true
	case "interrupted":
		return len(ev.Approvals) == 0 || slices.ContainsFunc(ev.Approvals, func(a core.Approval) bool { return a.State == "pending" })
	}
	return false
}

// ended closes the current follow and reads what the run saved.
func (p *witPanel) ended() {
	p.unsub, p.over, p.running, p.cancel = nil, true, false, nil
	p.reconcile()
	if p.o.Changed != nil {
		p.o.Changed()
	}
}

// apply shows an event of the run followed.
func (p *witPanel) apply(ev core.WitEvent) {
	if ev.RunID != 0 {
		p.runID = ev.RunID
	}
	if p.live == nil && (len(ev.Citations) > 0 || ev.Kind == "text" || ev.Kind == "tool_call" || ev.Kind == "tool_result") {
		p.live = &witMsg{}
	}
	for _, c := range ev.Citations {
		p.cite(c)
		if !slices.Contains(p.live.cited, c.MessageID) {
			p.live.cited = append(p.live.cited, c.MessageID)
		}
	}
	switch ev.Kind {
	case "text", "tool_call", "tool_result":
		p.running = true
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
		p.running = !ends(ev)
	}
}

// cite adds a citation unless it is there: the same run, call and message.
func (p *witPanel) cite(c core.Citation) {
	if !slices.ContainsFunc(p.cites, func(x core.Citation) bool {
		return x.RunID == c.RunID && x.CallID == c.CallID && x.MessageID == c.MessageID
	}) {
		p.cites = append(p.cites, c)
	}
}

// cited is each cited message once, numbered from 1 as it first came;
// suspect once any citation of it is.
func (p *witPanel) cited() []core.Citation {
	var out []core.Citation
	for _, c := range p.cites {
		if i := slices.IndexFunc(out, func(x core.Citation) bool { return x.MessageID == c.MessageID }); i >= 0 {
			out[i].Suspect = out[i].Suspect || c.Suspect
			continue
		}
		out = append(out, c)
	}
	return out
}

// citedBy is the messages that the runs which added a saved answer cited,
// by their calls or their context.
func (p *witPanel) citedBy(m *witMsg) []int64 {
	var ids []int64
	for _, r := range p.runs {
		if r.From >= m.to || r.To <= m.from {
			continue
		}
		for _, c := range p.cites {
			if c.RunID == r.ID {
				ids = append(ids, c.MessageID)
			}
		}
	}
	return ids
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

// reconcile reads what the conversation saved: its messages, the mail its
// runs cited, its pending approvals and its active run. Once the follow is
// over the saved messages replace what streamed in.
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
		p.msgs, p.runs = msgs, c.Runs
		p.approvals = slices.DeleteFunc(as, func(x core.Approval) bool { return x.State != "pending" })
		live := p.cites
		p.cites = slices.Clone(c.Citations)
		for _, ct := range live { // cited since the read began
			p.cite(ct)
		}
		if tok != p.follows { // a run started since: its own events tell
			return
		}
		if c.ActiveRunID != 0 {
			p.runID = c.ActiveRunID
		}
		waits := slices.ContainsFunc(p.approvals, func(x core.Approval) bool { return x.RunID == c.ActiveRunID })
		p.running = c.ActiveRunID != 0 && !waits
		if over && p.over {
			p.live, p.liveUser = nil, ""
		}
	})
}

// witMsgs groups saved messages into questions and answers: an answer is
// everything the model said and called up to the next question.
func witMsgs(ms []ai.Message) []witMsg {
	var out []witMsg
	for i, m := range ms {
		if m.Role == "user" {
			out = append(out, witMsg{user: true, text: m.Text, from: i, to: i + 1})
			continue
		}
		if n := len(out); n == 0 || out[n-1].user {
			out = append(out, witMsg{from: i})
		}
		ans := &out[len(out)-1]
		ans.to = i + 1
		if m.Role != "tool" && strings.TrimSpace(m.Text) != "" {
			if ans.text != "" {
				ans.text += "\n\n"
			}
			ans.text += m.Text
		}
		ans.calls = append(ans.calls, m.Calls...)
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
	if in == "" || p.running || o.Start == nil || o.held() {
		return
	}
	p.input = ""
	p.ask(o, in)
}

// ask starts a run on in, past the owner's gate.
func (p *witPanel) ask(o witOptions, in string) {
	if o.held() {
		return
	}
	if o.Gate != nil {
		o.Gate(func() { p.run(o, in) })
		return
	}
	p.run(o, in)
}

// run starts a run on in and follows its stream.
func (p *witPanel) run(o witOptions, in string) {
	if o.held() {
		return
	}
	start, a := o.Start, p.a
	// Not under a.ctx: only Stop cancels the call, and the run outlives the
	// panel showing it. The Service's close ends it.
	ctx, cancel := context.WithCancel(context.WithoutCancel(a.ctx))
	var conv int64
	p.follow(convKey(p.conv), func(context.Context) (ch <-chan core.WitEvent, stop func(), err error) {
		conv, ch, stop, err = start(ctx, in)
		return ch, stop, err
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
	p.cancel, p.retryO = cancel, witOptions{}
	if o.Pinned {
		p.retryO = o
	}
	p.retry, p.err, p.liveUser, p.live, p.fresh, p.running, p.runID = in, nil, in, nil, true, true, 0
	p.scroll.Y = math.MaxFloat32
}

// stopRun cancels the run in flight.
func (p *witPanel) stopRun() {
	if p.cancel != nil {
		p.cancel() // the run of an Ask call before its first event
	}
	if id := p.runID; id != 0 {
		a := p.a
		a.writes.Chain(convKey(p.conv), func(ctx context.Context) error { return a.svc.CancelRun(ctx, id) }, func(err error) {
			if err != nil {
				a.toastErr(err)
			}
		})
	}
}

// decide accepts or rejects an approval, or with all every pending one of
// its run. It subscribes first: the run resumes once nothing of it is left
// open and may end before the decision returns.
func (p *witPanel) decide(ap core.Approval, accept, all bool) {
	resolve, resolveAll := p.resolve, p.resolveAll
	ids := []int64{ap.ID}
	if all {
		ids = nil
		for _, x := range p.approvals {
			if x.RunID == ap.RunID {
				ids = append(ids, x.ID)
			}
		}
	}
	p.settle(ap, ids, func(ctx context.Context) error {
		if all {
			return resolveAll(ctx, ap.RunID, accept)
		}
		return resolve(ctx, ap.ID, accept)
	})
}

// settle decides approvals ids of ap's run with decide, following the run
// it resumes.
func (p *witPanel) settle(ap core.Approval, ids []int64, decide func(ctx context.Context) error) {
	if p.o.held() {
		return
	}
	svc, conv := p.a.svc, p.conv
	for _, id := range ids {
		p.deciding[id] = true
	}
	p.err, p.fresh, p.live = nil, false, nil
	p.follow(convKey(conv), func(ctx context.Context) (<-chan core.WitEvent, func(), error) {
		ch, stop := svc.SubscribeConversation(conv)
		err := decide(ctx)
		as, lerr := svc.ListApprovals(ctx, conv)
		if lerr != nil || slices.ContainsFunc(as, func(x core.Approval) bool { return x.RunID == ap.RunID && x.State == "pending" }) {
			stop() // the run waits for the rest: nothing resumes
			return nil, nil, errors.Join(err, lerr)
		}
		return ch, stop, err
	}, func(err error) {
		if err != nil {
			p.a.toastErr(err)
		}
	})
	// A later decision's follow replaces this one's: the flags clear once
	// the chained write is done, whichever follow shows.
	p.a.writes.Chain(convKey(conv), func(context.Context) error { return nil }, func(error) {
		for _, id := range ids {
			delete(p.deciding, id)
		}
	})
}
