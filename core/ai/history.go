package ai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	gai "github.com/firebase/genkit/go/ai"
)

// HistoryNote stands in for the turns a sent view omits.
const HistoryNote = "[Earlier turns of this conversation were omitted to fit the history budget.]"

// SummaryNote heads a Summary standing in for the turns it condensed;
// SummaryGapNote follows it when turns after it were omitted too.
const (
	SummaryNote    = "[Earlier turns of this conversation were condensed to fit the history budget. Their summary:]"
	SummaryGapNote = "[Some turns after this summary were omitted too.]"
)

// contextKey marks a question's context part (Sent.Ask); commandKey marks a
// command, a question the model never sees (History.AddCommand).
const (
	contextKey = "godwit_context"
	commandKey = "godwit_command"
)

// Summary is what compacting a conversation left: Text stands in for its
// messages after the first through the one with seq Upto. The zero Summary is
// none; one that does not end before a question of the history is none too.
type Summary struct {
	Text string
	Upto int64
}

// Command is a question that runs a skill on a draft (翻译, 调整格式): the
// model never sees it. Lang is the target language of a translation.
type Command struct {
	Skill string `json:"skill"`
	Lang  string `json:"lang,omitempty"`
}

// StoredMessage is one message of a History as saved: Seq orders a
// conversation's messages and is never reused, Data is its encoding.
type StoredMessage struct {
	Seq  int64
	Data []byte
}

// HistoryChanges is what saving a changed History writes: the messages from
// seq DropFrom on go (0: none), then Put are saved, new or rewritten; Next is
// the seq the next new message gets. DropSummary: a rewind went back into what
// the conversation's summary condensed, so the summary goes too.
type HistoryChanges struct {
	DropFrom    int64
	Put         []StoredMessage
	Next        int64
	DropSummary bool
}

// History is a conversation's messages, each with its seq. It is never
// changed in place: what changes it returns a new History and the changes
// that save it.
type History struct {
	msgs []*gai.Message
	seqs []int64
	next int64
}

// Turn is one question of a History (a command included) and its answer:
// everything up to the next question. Seq is the question's, To the seq after
// the turn's last message.
type Turn struct {
	Seq      int64
	To       int64
	Question Message
	Answer   []Message
}

// NewHistory is an empty conversation's history.
func NewHistory() *History { return &History{next: 1} }

// LoadHistory reads a conversation's saved messages, in seq order; next is
// the seq its next message gets.
func LoadHistory(rows []StoredMessage, next int64) (*History, error) {
	h := &History{next: next}
	for _, r := range rows {
		m, err := decodeMessage(r.Data)
		if err != nil {
			return nil, err
		}
		if n := len(h.seqs); r.Seq < 1 || r.Seq >= next || n > 0 && r.Seq <= h.seqs[n-1] {
			return nil, errors.New("ai: conversation messages out of order")
		}
		h.msgs, h.seqs = append(h.msgs, m), append(h.seqs, r.Seq)
	}
	return h, nil
}

// LegacyHistory reads a conversation state saved as one array (before its
// messages were saved one by one), placing cmds before the state's message at
// their At (len(state's messages): after the last). Messages take seqs from
// 1 in order; seqOf is the seq of the old message at an index (its length:
// Next), cmdSeqs the seq of each of cmds.
func LegacyHistory(state []byte, cmds []PlacedCommand) (h *History, seqOf func(int) int64, cmdSeqs []int64, err error) {
	msgs, err := decodeState(state)
	if err != nil {
		return nil, nil, nil, err
	}
	order := make([]int, len(cmds))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { return cmds[a].At - cmds[b].At })
	h = &History{next: 1}
	old := make([]int64, len(msgs)+1)
	cmdSeqs = make([]int64, len(cmds))
	add := func(m *gai.Message) {
		h.msgs, h.seqs = append(h.msgs, m), append(h.seqs, h.next)
		h.next++
	}
	c := 0
	for i := 0; i <= len(msgs); i++ {
		for ; c < len(order) && cmds[order[c]].At <= i; c++ {
			cmdSeqs[order[c]] = h.next
			add(commandMessage(cmds[order[c]].Command))
		}
		old[i] = h.next
		if i < len(msgs) {
			add(msgs[i])
		}
	}
	return h, func(i int) int64 { return old[min(max(i, 0), len(msgs))] }, cmdSeqs, nil
}

// PlacedCommand is a command LegacyHistory puts before the old message At.
type PlacedCommand struct {
	At      int
	Command Command
}

// Rows are h's messages as saved.
func (h *History) Rows() ([]StoredMessage, error) {
	out := make([]StoredMessage, len(h.msgs))
	for i, m := range h.msgs {
		b, err := json.Marshal(m)
		if err != nil {
			return nil, err
		}
		out[i] = StoredMessage{Seq: h.seqs[i], Data: b}
	}
	return out, nil
}

// Next is the seq h's next message gets.
func (h *History) Next() int64 { return h.next }

// Len is how many messages h has.
func (h *History) Len() int { return len(h.msgs) }

// Messages are h's messages for display: no context parts, a command as its
// Command.
func (h *History) Messages() []Message {
	out := display(h.msgs)
	for i := range out {
		out[i].Seq = h.seqs[i]
	}
	return out
}

// Turns are h's questions with their answers, in order. Messages before the
// first question (none in a history Wit wrote) are left out.
func (h *History) Turns() []Turn {
	var out []Turn
	for _, m := range h.Messages() {
		switch {
		case m.Role == string(gai.RoleUser):
			if n := len(out); n > 0 {
				out[n-1].To = m.Seq
			}
			out = append(out, Turn{Seq: m.Seq, To: h.next, Question: m})
		case len(out) > 0:
			out[len(out)-1].Answer = append(out[len(out)-1].Answer, m)
		}
	}
	return out
}

// AddCommand is h with cmd asked after its last message.
func (h *History) AddCommand(cmd Command) (*History, HistoryChanges, error) {
	return h.with(nil, []*gai.Message{commandMessage(cmd)})
}

func commandMessage(cmd Command) *gai.Message {
	p := gai.NewTextPart("/" + cmd.Skill)
	p.Metadata = map[string]any{commandKey: map[string]any{"skill": cmd.Skill, "lang": cmd.Lang}}
	return gai.NewUserMessage(p)
}

func isCommand(m *gai.Message) bool {
	return m.Role == gai.RoleUser && slices.ContainsFunc(m.Content, func(p *gai.Part) bool { return p.Metadata[commandKey] != nil })
}

// commandOf is the command m asks, if it is one.
func commandOf(m *gai.Message) *Command {
	for _, p := range m.Content {
		if v, ok := p.Metadata[commandKey].(map[string]any); ok {
			c := &Command{}
			c.Skill, _ = v["skill"].(string)
			c.Lang, _ = v["lang"].(string)
			return c
		}
	}
	return nil
}

// with is h with the messages at the indexes of put replaced and add appended.
func (h *History) with(put map[int]*gai.Message, add []*gai.Message) (*History, HistoryChanges, error) {
	n := &History{msgs: slices.Clone(h.msgs), seqs: slices.Clone(h.seqs), next: h.next}
	var ch HistoryChanges
	idx := make([]int, 0, len(put))
	for i := range put {
		idx = append(idx, i)
	}
	slices.Sort(idx)
	for _, i := range idx {
		n.msgs[i] = put[i]
		b, err := json.Marshal(put[i])
		if err != nil {
			return nil, HistoryChanges{}, err
		}
		ch.Put = append(ch.Put, StoredMessage{Seq: n.seqs[i], Data: b})
	}
	for _, m := range add {
		b, err := json.Marshal(m)
		if err != nil {
			return nil, HistoryChanges{}, err
		}
		n.msgs, n.seqs = append(n.msgs, m), append(n.seqs, n.next)
		ch.Put = append(ch.Put, StoredMessage{Seq: n.next, Data: b})
		n.next++
	}
	ch.Next = n.next
	return n, ch, nil
}

// Rewind is h before its question with seq at: that question and everything
// after it go. sum is the conversation's summary: a rewind into what it
// condensed drops it (DropSummary).
func (h *History) Rewind(at int64, sum Summary) (*History, HistoryChanges, error) {
	i := slices.Index(h.seqs, at)
	if i < 0 || h.msgs[i].Role != gai.RoleUser || isCommand(h.msgs[i]) {
		return nil, HistoryChanges{}, fmt.Errorf("no question at %d", at)
	}
	n := &History{msgs: slices.Clone(h.msgs[:i]), seqs: slices.Clone(h.seqs[:i]), next: h.next}
	return n, HistoryChanges{DropFrom: at, Next: h.next, DropSummary: sum.Text != "" && sum.Upto >= at}, nil
}

// Close answers every open call of h's last message, without asking the
// model: calls that already ran keep their result, the others take theirs
// from responses, which must match them one to one by ID and name. A
// cancelled run uses it so the conversation can go on.
func (h *History) Close(responses []ToolResult) (*History, HistoryChanges, error) {
	if len(h.msgs) == 0 {
		return nil, HistoryChanges{}, errors.New("ai: no open tool calls")
	}
	last, err := clone(h.msgs[len(h.msgs)-1])
	if err != nil {
		return nil, HistoryChanges{}, err
	}
	calls, byID, err := matchResults([]*gai.Message{last}, responses)
	if err != nil {
		return nil, HistoryChanges{}, err
	}
	toolMsg := &gai.Message{Role: gai.RoleTool}
	for _, p := range calls {
		out, ran := p.Metadata["pendingOutput"]
		if !ran {
			if out, err = resultOutput(byID[p.ToolRequest.Ref]); err != nil {
				return nil, HistoryChanges{}, err
			}
		}
		for _, k := range []string{"pendingOutput", "pendingMetadata", "pendingContent", "interrupt"} {
			delete(p.Metadata, k)
		}
		toolMsg.Content = append(toolMsg.Content, gai.NewResponseForToolRequest(p, out))
	}
	return h.with(map[int]*gai.Message{len(h.msgs) - 1: last}, []*gai.Message{toolMsg})
}

// clone is a deep copy of m, through its encoding.
func clone(m *gai.Message) (*gai.Message, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return decodeMessage(b)
}

// visible are the indexes of h's messages the model sees: all but commands.
func (h *History) visible() []int {
	out := make([]int, 0, len(h.msgs))
	for i, m := range h.msgs {
		if !isCommand(m) {
			out = append(out, i)
		}
	}
	return out
}

// cut is where the visible messages vis go on after sum: 0 when it stands
// for none.
func (h *History) cut(vis []int, sum Summary) int {
	if sum.Text == "" {
		return 0
	}
	i := slices.IndexFunc(vis, func(j int) bool { return h.seqs[j] == sum.Upto })
	if i < 1 || i+1 >= len(vis) || h.msgs[vis[i+1]].Role != gai.RoleUser {
		return 0
	}
	return i + 1
}

// note is the model message standing in for visible messages 1..cut-1: sum's,
// when it stands for some (from minCut), else HistoryNote.
func note(sum Summary, cut, minCut int) *gai.Message {
	if minCut == 0 {
		return gai.NewModelTextMessage(HistoryNote)
	}
	text := SummaryNote + "\n\n" + sum.Text
	if cut > minCut {
		text += "\n\n" + SummaryGapNote
	}
	return gai.NewModelTextMessage(text)
}

// Sent is what one run sends of a History (History.Send): the model's view,
// which may stand a summary or a note in for its older turns and leaves out
// its commands. Restore turns what the run ended with back into the whole
// history.
type Sent struct {
	h    *History
	msgs []*gai.Message
	src  []int // the index in h of each of msgs: -1 the note, -2 the question asked
}

const (
	srcNote     = -1
	srcQuestion = -2
)

// Send fits h into budget runes (of its JSON) for one run. sum, when it
// stands for some of h, does first; then whole turns go, oldest first: a turn
// runs from a question to the next, so a tool request and its result always
// go together. The first message and the last two turns always stay, even
// over budget; a model message with the summary or HistoryNote marks the gap.
func (h *History) Send(budget int, sum Summary) (*Sent, error) {
	vis := h.visible()
	whole := func() (*Sent, error) {
		s := &Sent{h: h}
		for _, i := range vis {
			s.msgs, s.src = append(s.msgs, h.msgs[i]), append(s.src, i)
		}
		return s, nil
	}
	size := func(msgs []*gai.Message) (int, error) {
		b, err := json.Marshal(msgs)
		return utf8.RuneCount(b), err
	}
	all := make([]*gai.Message, len(vis))
	for k, i := range vis {
		all[k] = h.msgs[i]
	}
	n, err := size(all)
	if err != nil {
		return nil, err
	}
	minCut := h.cut(vis, sum)
	if minCut == 0 && n <= budget {
		return whole()
	}
	var cuts []int // where what is sent may go on after the first message
	if minCut > 0 {
		cuts = append(cuts, minCut)
	}
	var starts []int // where each turn after the first begins
	for k, i := range vis {
		if k > 0 && h.msgs[i].Role == gai.RoleUser {
			starts = append(starts, k)
		}
	}
	for k, c := range starts {
		if c > minCut && k < len(starts)-1 { // the last two turns stay
			cuts = append(cuts, c)
		}
	}
	if len(cuts) == 0 {
		return whole() // at most two turns
	}
	var s *Sent
	// shortcut: re-encodes per candidate cut, O(turns²); fine for chat-sized histories
	for _, cut := range cuts {
		s = &Sent{h: h, msgs: []*gai.Message{all[0], note(sum, cut, minCut)}, src: []int{vis[0], srcNote}}
		for _, i := range vis[cut:] {
			s.msgs, s.src = append(s.msgs, h.msgs[i]), append(s.src, i)
		}
		if n, err = size(s.msgs); err != nil {
			return nil, err
		}
		if n <= budget {
			break
		}
	}
	return s, nil
}

// Contexts are the context texts of what s sends, oldest first.
func (s *Sent) Contexts() []string {
	var out []string
	for _, m := range s.msgs {
		for _, p := range m.Content {
			if p.IsText() && p.Metadata[contextKey] != nil {
				out = append(out, p.Text)
			}
		}
	}
	return out
}

// Ask is s with a question: context, if any, goes to the model before
// prompt in the same message and stays in the history; Messages never show
// it (quoted mail, for example).
func (s *Sent) Ask(prompt, context string) *Sent {
	var parts []*gai.Part
	if context != "" {
		p := gai.NewTextPart(context)
		p.Metadata = map[string]any{contextKey: true}
		parts = append(parts, p)
	}
	parts = append(parts, gai.NewTextPart(prompt))
	return &Sent{h: s.h, msgs: append(slices.Clone(s.msgs), gai.NewUserMessage(parts...)), src: append(slices.Clone(s.src), srcQuestion)}
}

// asked reports whether s ends with a question it asks.
func (s *Sent) asked() bool { return len(s.src) > 0 && s.src[len(s.src)-1] == srcQuestion }

// Restore is the whole history res, the successful end of a run that sent s,
// leaves, and the changes that save it: the messages res rewrote of what s
// sent and the ones it added, after the history s was sent from.
func (s *Sent) Restore(res Result) (*History, HistoryChanges, error) {
	out := res.out
	bad := errors.New("ai: cannot restore the conversation history")
	if len(out) < len(s.msgs) {
		return nil, HistoryChanges{}, bad
	}
	put := map[int]*gai.Message{}
	var add []*gai.Message
	for k, src := range s.src {
		if out[k].Role != s.msgs[k].Role {
			return nil, HistoryChanges{}, bad
		}
		switch src {
		case srcNote:
		case srcQuestion:
			add = append(add, out[k])
		default:
			same, err := sameMessage(out[k], s.h.msgs[src])
			if err != nil {
				return nil, HistoryChanges{}, err
			}
			if !same {
				put[src] = out[k]
			}
		}
	}
	add = append(add, out[len(s.msgs):]...)
	return s.h.with(put, add)
}

func sameMessage(a, b *gai.Message) (bool, error) {
	x, err := json.Marshal(a)
	if err != nil {
		return false, err
	}
	y, err := json.Marshal(b)
	return bytes.Equal(x, y), err
}

// CompactedSize is the runes of h with sum standing in for what it condensed,
// as Send sends it before omitting any turn.
func (h *History) CompactedSize(sum Summary) (int, error) {
	vis := h.visible()
	msgs := make([]*gai.Message, 0, len(vis)+1)
	if cut := h.cut(vis, sum); cut > 0 {
		msgs = append(msgs, h.msgs[vis[0]], note(sum, cut, cut))
		vis = vis[cut:]
	}
	for _, i := range vis {
		msgs = append(msgs, h.msgs[i])
	}
	b, err := json.Marshal(msgs)
	return utf8.RuneCount(b), err
}

// CompactPoint is how far a summary of h may reach (its Upto): the message
// before its second-to-last turn, which stays with the last; 0 when that
// leaves nothing to condense.
func (h *History) CompactPoint() int64 {
	if ps := h.CompactPoints(); len(ps) > 0 {
		return ps[len(ps)-1]
	}
	return 0
}

// CompactPoints are the Uptos a summary of h may have, ascending: the message
// before each turn after the first, through CompactPoint.
func (h *History) CompactPoints() []int64 {
	vis := h.visible()
	var out []int64
	for k, i := range vis {
		if k > 0 && h.msgs[i].Role == gai.RoleUser {
			out = append(out, h.seqs[vis[k-1]])
		}
	}
	if len(out) < 2 {
		return nil
	}
	return out[:len(out)-1]
}

// Compactable reports whether a compaction of h has turns to condense that sum
// does not: CompactPoint is past it.
func (h *History) Compactable(sum Summary) bool { return h.CompactPoint() > sum.Upto }

// maxTranscriptResult is the runes of a tool result a Transcript keeps.
const maxTranscriptResult = 2000

// Transcript is h's messages after seq after through upto as text to
// summarize: who said what, the tools called and what they returned, each
// result cut to maxTranscriptResult runes. Commands are left out.
func (h *History) Transcript(after, upto int64) (string, error) {
	var msgs []*gai.Message
	for _, i := range h.visible() {
		if h.seqs[i] > after && h.seqs[i] <= upto {
			msgs = append(msgs, h.msgs[i])
		}
	}
	if len(msgs) == 0 {
		return "", errors.New("ai: no such messages in the conversation history")
	}
	var b strings.Builder
	for _, m := range display(msgs) {
		who := "Assistant"
		if m.Role == string(gai.RoleUser) {
			who = "User"
		}
		if m.Text != "" {
			fmt.Fprintf(&b, "%s: %s\n\n", who, m.Text)
		}
		for _, c := range m.Calls {
			fmt.Fprintf(&b, "Assistant called %s with %s\n\n", c.Name, c.Args)
		}
		for _, r := range m.Results {
			out := string(r.Output)
			if rs := []rune(out); len(rs) > maxTranscriptResult {
				out = string(rs[:maxTranscriptResult]) + " [...]"
			}
			fmt.Fprintf(&b, "%s returned: %s\n\n", r.Name, out)
		}
	}
	return b.String(), nil
}

func decodeMessage(b []byte) (*gai.Message, error) {
	msgs, err := decodeState(append(append([]byte{'['}, b...), ']'))
	if err != nil {
		return nil, err
	}
	if len(msgs) != 1 {
		return nil, errors.New("ai: unreadable conversation state")
	}
	return msgs[0], nil
}
