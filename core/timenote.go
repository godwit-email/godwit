package core

import (
	"strings"
	"time"
)

// timeSystem tells the model how to read the time note and mail dates, and write them.
const timeSystem = `A user turn may begin with the current time; until another does, the last one given holds. ` +
	`Mail dates are in UTC: go by the user's local time for "today", "yesterday" and the times you tell the user, ` +
	`and write dates as the language of your answer does, not as ISO YYYY-MM-DD.`

// timeNotePrefix starts the time note; timeNoteLayout is how it gives the time.
const (
	timeNotePrefix = "Current time: "
	timeNoteLayout = "2006-01-02 15:04 -07:00"
	// timeNoteGap is how long a time note holds on the same local day.
	timeNoteGap = 30 * time.Minute
)

// timeNote is now (the user's local time), for the model before a run's
// prompt, or "" when cs (the contexts of the history the run sends) has a note
// that still holds: with now's UTC offset, from now's local day and under
// timeNoteGap old. A note only when the time moved keeps the turns short and
// leaves the cached history as it is.
func timeNote(cs []string, now time.Time) string {
	for i := len(cs) - 1; i >= 0; i-- {
		rest, ok := strings.CutPrefix(cs[i], timeNotePrefix)
		if !ok || len(rest) < len(timeNoteLayout) {
			continue
		}
		last, err := time.Parse(timeNoteLayout, rest[:len(timeNoteLayout)])
		if err != nil {
			continue
		}
		_, was := last.Zone()
		_, is := now.Zone()
		last = last.In(now.Location())
		if was == is && last.Format(time.DateOnly) == now.Format(time.DateOnly) && now.Sub(last) >= 0 && now.Sub(last) < timeNoteGap {
			return ""
		}
		break
	}
	zone, _ := now.Zone()
	return timeNotePrefix + now.Format(timeNoteLayout) + " (" + now.Weekday().String() + ", " + zone + "), the user's local time."
}
