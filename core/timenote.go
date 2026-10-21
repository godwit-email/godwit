package core

import (
	"strings"
	"time"

	"github.com/godwit-email/godwit/core/ai"
)

// timeSystem tells the model how to read the time note and mail dates.
const timeSystem = `A user turn may begin with the current time; until another does, the last one given holds. ` +
	`Mail dates are in UTC: go by the user's local time for "today", "yesterday" and the times you tell the user.`

// timeNotePrefix starts the time note; timeNoteLayout is how it gives the time.
const (
	timeNotePrefix = "Current time: "
	timeNoteLayout = "2006-01-02 15:04 -07:00"
	// timeNoteGap is how long a time note holds on the same local day.
	timeNoteGap = 30 * time.Minute
)

// timeNote is now (the user's local time), for the model before a run's prompt, or
// "" when state (the history the run sends) has a note that still holds: from
// now's local day and under timeNoteGap old. A note only when the time moved
// keeps the turns short and leaves the cached history as it is.
func timeNote(state []byte, now time.Time) (string, error) {
	cs, err := ai.Contexts(state)
	if err != nil {
		return "", err
	}
	for i := len(cs) - 1; i >= 0; i-- {
		rest, ok := strings.CutPrefix(cs[i], timeNotePrefix)
		if !ok || len(rest) < len(timeNoteLayout) {
			continue
		}
		last, err := time.Parse(timeNoteLayout, rest[:len(timeNoteLayout)])
		if err != nil {
			continue
		}
		last = last.In(now.Location())
		if last.Format(time.DateOnly) == now.Format(time.DateOnly) && now.Sub(last) < timeNoteGap {
			return "", nil
		}
		break
	}
	zone, _ := now.Zone()
	return timeNotePrefix + now.Format(timeNoteLayout) + " (" + now.Weekday().String() + ", " + zone + "), the user's local time.", nil
}
