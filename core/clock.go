package core

import "time"

// Clock is the Service's time source; tests swap in a fake one.
type Clock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) (stop func() bool)
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) AfterFunc(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }

// clk is the Service's clock.
func (s *Service) clk() Clock {
	if s.clock == nil {
		return realClock{}
	}
	return s.clock
}

// testClock, if set, is the clock of Services created afterwards (tests).
var testClock Clock

// SetClockForTest replaces the Service's clock. Tests only: with a fake clock
// they step past waits instead of sleeping. Call it right after New, before any
// account is added.
func (s *Service) SetClockForTest(c Clock) { s.clock = c }
