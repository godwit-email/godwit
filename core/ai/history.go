package ai

import (
	"encoding/json"
	"errors"
	"unicode/utf8"

	gai "github.com/firebase/genkit/go/ai"
)

// HistoryNote stands in for the turns TrimState omits.
const HistoryNote = "[Earlier turns of this conversation were omitted to fit the history budget.]"

// TrimState fits state into budget runes (of its JSON) for one run by
// omitting whole turns, oldest first: a turn runs from a user message to the
// next, so a tool request and its result always go together. The first user
// message and the last two turns always stay, even over budget; a model
// message with HistoryNote marks the gap. dropped is how many messages after
// the first were omitted (0: state is returned as is), for RestoreState.
func TrimState(state []byte, budget int) (trimmed []byte, dropped int, err error) {
	if utf8.RuneCount(state) <= budget {
		return state, 0, nil
	}
	msgs, err := decodeState(state)
	if err != nil {
		return nil, 0, err
	}
	var starts []int // where each turn after the first begins
	for i, m := range msgs {
		if i > 0 && m.Role == gai.RoleUser {
			starts = append(starts, i)
		}
	}
	if len(starts) < 2 {
		return state, 0, nil // at most two turns
	}
	note := gai.NewModelTextMessage(HistoryNote)
	var cut int
	// ponytail: re-encodes per candidate cut, O(turns²); fine for chat-sized histories
	for _, cut = range starts[:len(starts)-1] {
		if trimmed, err = json.Marshal(append([]*gai.Message{msgs[0], note}, msgs[cut:]...)); err != nil {
			return nil, 0, err
		}
		if utf8.RuneCount(trimmed) <= budget {
			break
		}
	}
	return trimmed, cut - 1, nil
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
