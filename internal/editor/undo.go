package editor

import (
	"time"

	"github.com/egoist/mygo/ui"
)

// typingPause ends a step of typing.
const typingPause = time.Second

type snapshot struct {
	doc Doc
	sel ui.TextInputSelection
}

// history is the undo and redo stacks. Typing joins the step before it
// while it goes on where that step left the caret, within typingPause.
type history struct {
	undo, redo []snapshot
	typing     bool      // the last step is typing
	at         time.Time // when it last typed
	caret      int       // where it left the caret
}

// push records s before a change; typing at caret joins the typing step
// before it.
func (h *history) push(s snapshot, typing bool, now time.Time, caret int) {
	join := typing && h.typing && now.Sub(h.at) < typingPause && caret == h.caret && len(h.undo) > 0
	if !join {
		h.undo = append(h.undo, s)
	}
	h.redo = nil
	h.typing, h.at = typing, now
}

// typed records where typing left the caret.
func (h *history) typed(caret int) { h.caret = caret }

// step moves cur from one stack to the other and returns the state there.
func (h *history) step(cur snapshot, redo bool) (snapshot, bool) {
	from, to := &h.undo, &h.redo
	if redo {
		from, to = to, from
	}
	if len(*from) == 0 {
		return snapshot{}, false
	}
	s := (*from)[len(*from)-1]
	*from = (*from)[:len(*from)-1]
	*to = append(*to, cur)
	h.typing = false
	return s, true
}
