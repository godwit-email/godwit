package ai

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	gai "github.com/firebase/genkit/go/ai"
)

// HistoryNote stands in for the turns TrimState omits.
const HistoryNote = "[Earlier turns of this conversation were omitted to fit the history budget.]"

// SummaryNote heads a Summary standing in for the turns it condensed;
// SummaryGapNote follows it when turns after it were omitted too.
const (
	SummaryNote    = "[Earlier turns of this conversation were condensed to fit the history budget. Their summary:]"
	SummaryGapNote = "[Some turns after this summary were omitted too.]"
)

// Summary is what compacting a conversation left: Text stands in for the
// state's messages after the first through Upto. The zero Summary is none; one
// that does not end before a user message of the state is none too.
type Summary struct {
	Text string
	Upto int
}

// cut is where the state's messages go on after sum: 0 when it stands for none.
func (sum Summary) cut(msgs []*gai.Message) int {
	if sum.Text == "" || sum.Upto < 1 || sum.Upto+1 >= len(msgs) || msgs[sum.Upto+1].Role != gai.RoleUser {
		return 0
	}
	return sum.Upto + 1
}

// note is the model message standing in for messages 1..cut-1: sum's, when it
// stands for some (from minCut), else HistoryNote.
func (sum Summary) note(cut, minCut int) *gai.Message {
	if minCut == 0 {
		return gai.NewModelTextMessage(HistoryNote)
	}
	text := SummaryNote + "\n\n" + sum.Text
	if cut > minCut {
		text += "\n\n" + SummaryGapNote
	}
	return gai.NewModelTextMessage(text)
}

// TrimState fits state into budget runes (of its JSON) for one run. sum, when
// it stands for some of state, does first; then whole turns go, oldest first:
// a turn runs from a user message to the next, so a tool request and its
// result always go together. The first user message and the last two turns
// always stay, even over budget; a model message with the summary or
// HistoryNote marks the gap. dropped is how many messages after the first
// were left out (0: state is returned as is), for RestoreState.
func TrimState(state []byte, budget int, sum Summary) (trimmed []byte, dropped int, err error) {
	if sum.Text == "" && utf8.RuneCount(state) <= budget {
		return state, 0, nil
	}
	msgs, err := decodeState(state)
	if err != nil {
		return nil, 0, err
	}
	minCut := sum.cut(msgs)
	if minCut == 0 && utf8.RuneCount(state) <= budget {
		return state, 0, nil
	}
	var cuts []int // where what is sent may go on after the first message
	if minCut > 0 {
		cuts = append(cuts, minCut)
	}
	var starts []int // where each turn after the first begins
	for i, m := range msgs {
		if i > 0 && m.Role == gai.RoleUser {
			starts = append(starts, i)
		}
	}
	for i, c := range starts {
		if c > minCut && i < len(starts)-1 { // the last two turns stay
			cuts = append(cuts, c)
		}
	}
	if len(cuts) == 0 {
		return state, 0, nil // at most two turns
	}
	var cut int
	// ponytail: re-encodes per candidate cut, O(turns²); fine for chat-sized histories
	for _, cut = range cuts {
		if trimmed, err = json.Marshal(append([]*gai.Message{msgs[0], sum.note(cut, minCut)}, msgs[cut:]...)); err != nil {
			return nil, 0, err
		}
		if utf8.RuneCount(trimmed) <= budget {
			break
		}
	}
	return trimmed, cut - 1, nil
}

// CompactedSize is the runes of state with sum standing in for what it
// condensed, as TrimState sends it before omitting any turn.
func CompactedSize(state []byte, sum Summary) (int, error) {
	if sum.Text == "" {
		return utf8.RuneCount(state), nil
	}
	msgs, err := decodeState(state)
	if err != nil {
		return 0, err
	}
	cut := sum.cut(msgs)
	if cut == 0 {
		return utf8.RuneCount(state), nil
	}
	b, err := json.Marshal(append([]*gai.Message{msgs[0], sum.note(cut, cut)}, msgs[cut:]...))
	return utf8.RuneCount(b), err
}

// CompactPoint is how far a summary of state may reach (its Upto): the
// message before its second-to-last turn, which stays with the last; 0 when
// that leaves nothing to condense.
func CompactPoint(state []byte) (int, error) {
	msgs, err := decodeState(state)
	if err != nil {
		return 0, err
	}
	var starts []int
	for i, m := range msgs {
		if i > 0 && m.Role == gai.RoleUser {
			starts = append(starts, i)
		}
	}
	if len(starts) < 2 {
		return 0, nil
	}
	return starts[len(starts)-2] - 1, nil
}

// maxTranscriptResult is the runes of a tool result a Transcript keeps.
const maxTranscriptResult = 2000

// Transcript is state's messages from through to as text to summarize: who
// said what, the tools called and what they returned, each result cut to
// maxTranscriptResult runes.
func Transcript(state []byte, from, to int) (string, error) {
	msgs, err := decodeState(state)
	if err != nil {
		return "", err
	}
	if from < 0 || to >= len(msgs) || from > to {
		return "", errors.New("ai: no such messages in the conversation state")
	}
	var b strings.Builder
	for _, m := range display(msgs[from : to+1]) {
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

// RestoreState puts the messages TrimState dropped back into result, the
// state a run on the trimmed state ended with: full's first message and the
// dropped ones, then result after its first message and the note.
func RestoreState(full, result []byte, dropped int) ([]byte, error) {
	if dropped == 0 {
		return result, nil
	}
	var f, r []json.RawMessage
	if err := errors.Join(json.Unmarshal(full, &f), json.Unmarshal(result, &r)); err != nil || len(f) < 1+dropped || len(r) < 2 {
		return nil, errors.New("ai: cannot restore the trimmed conversation state")
	}
	return json.Marshal(append(f[:1+dropped:1+dropped], r[2:]...))
}
