package core

import (
	"context"
	"strings"
	"time"
	_ "time/tzdata" // Windows has no IANA zoneinfo for WithTimeZone's LoadLocation
)

type zoneKey struct{}

// WithTimeZone is ctx with the user's IANA time zone (the UI's, sent with each
// call) for the local time Wit's runs give the model. Go loads time.Local
// once per process, so it goes stale when the OS zone changes while the app
// runs. An empty or unknown name leaves ctx as it is: time.Local holds.
func WithTimeZone(ctx context.Context, name string) context.Context {
	if name == "" {
		return ctx
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return ctx
	}
	return context.WithValue(ctx, zoneKey{}, loc)
}

// userNow is the clock's time in the zone WithTimeZone put on ctx, else in
// time.Local.
func (s *Service) userNow(ctx context.Context) time.Time {
	loc, ok := ctx.Value(zoneKey{}).(*time.Location)
	if !ok {
		loc = time.Local
	}
	return s.clk().Now().In(loc)
}

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
