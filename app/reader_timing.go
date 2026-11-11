package app

import (
	"fmt"
	"log"
	"os"
	"time"
)

// readerTiming turns on bodyTiming's log: GODWIT_READER_TIMING=1. Off by
// default, as a line per opened message would push what an error report
// needs out of RecentLog's last lines.
var readerTiming = os.Getenv("GODWIT_READER_TIMING") == "1"

// bodyTiming is how long a message's body took to show (#667), from its
// native read's start: the read back (read), its page made (view), the body
// built for the page (built) and the page's first height (shown). It is
// logged once, at shown; a page made again later (refolding) starts anew
// from its making.
type bodyTiming struct {
	start, read, viewStart, view, built time.Time
	logged                              bool
}

// begin starts timing at the native read.
func (t *bodyTiming) begin() { *t = bodyTiming{start: time.Now()} }

// readDone marks the native read back.
func (t *bodyTiming) readDone() {
	if t.read.IsZero() {
		t.read = time.Now()
	}
}

// viewMade marks the page made, NewBodyView having started at from. A page
// made again (refolding, shown or not yet) is timed from its making, so
// nothing of the page before carries over.
func (t *bodyTiming) viewMade(from time.Time) {
	if t.logged || t.start.IsZero() || !t.view.IsZero() {
		*t = bodyTiming{start: from, read: from}
	}
	t.viewStart, t.view = from, time.Now()
}

// builtDone marks the page's first body built.
func (t *bodyTiming) builtDone() {
	if t.built.IsZero() && !t.view.IsZero() {
		t.built = time.Now()
	}
}

// shown marks the page's first height and returns the line to log, once;
// "" before the page was made, after the line was given, or while off.
func (t *bodyTiming) shown(message int64) string {
	if !readerTiming || t.logged || t.view.IsZero() {
		return ""
	}
	t.logged = true
	now := time.Now()
	stage := func(from, to time.Time) string {
		if from.IsZero() || to.IsZero() {
			return "-"
		}
		return to.Sub(from).Round(time.Millisecond).String()
	}
	built := t.built
	if built.IsZero() { // the height came first: the build is in the paint
		built = now
	}
	return fmt.Sprintf("reader timing: message %d shown in %s (read %s, wait %s, view %s, build %s, paint %s)",
		message, stage(t.start, now), stage(t.start, t.read), stage(t.read, t.viewStart),
		stage(t.viewStart, t.view), stage(t.view, built), stage(built, now))
}

func logBodyTiming(t *bodyTiming, message int64) {
	if line := t.shown(message); line != "" {
		log.Print(line)
	}
}
