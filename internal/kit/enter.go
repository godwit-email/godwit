package kit

import (
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/theme"
)

// entered is, for each overlay's open flag, whether the last frame saw it
// open and when it opened. Views run on the main thread only.
// ponytail: package state keyed by the callers' open pointers (their fields,
// so few); drop it once mygo's DialogBase and PopoverBase animate their layer.
var entered = map[*bool]struct {
	was bool
	at  time.Time
}{}

// clock is the frame time; tests freeze it, as a Tester frame can run longer
// than an overlay takes to come in.
var clock = (*ui.Context).Now

// enter returns how far the overlay that *open shows has come in: 0 in the
// frame it opens, 1 after d, eased out; 1 at once under reduced motion. mygo
// plays no Enter for an overlay, whose panel is born with its layer, so the
// overlays animate their own way in. Call it every frame, open or not.
func enter(c *ui.Context, open *bool, d time.Duration) float32 {
	s := entered[open]
	if *open && !s.was {
		s.at = clock(c)
	}
	s.was = *open
	entered[open] = s
	if !*open || c.Preferences().ReduceMotion {
		return 1
	}
	k := float32(clock(c).Sub(s.at)) / float32(d)
	if k >= 1 {
		return 1
	}
	c.AnimationFrame()
	return ui.EaseOut(k)
}

// leave fades an overlay out once it is no longer built: mygo shows a copy of
// what the last frame drew, so nothing of the caller's runs after it closed.
// Its other changes stay at once (they are enter's), and under reduced motion
// it goes at once.
func leave(c *ui.Context) ui.ElementTransition {
	if c.Preferences().ReduceMotion {
		return ui.ElementTransition{Duration: -1}
	}
	return ui.ElementTransition{Duration: -1, ExitDuration: theme.MotionFast, Exit: &ui.Motion{}}
}

// rise is how far below its place an overlay that has come in k is.
func rise(k float32) float32 { return theme.MotionOffset * (1 - k) }
