// Package testclock is a fake clock for tests: time moves only on Advance.
package testclock

import (
	"sync"
	"time"
)

// Clock is a fake core.Clock. Its timers fire in Advance, in deadline order, on
// the goroutine calling it.
type Clock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*timer
}

type timer struct {
	at time.Time
	f  func()
}

// New is a Clock reading now until advanced.
func New(now time.Time) *Clock { return &Clock{now: now} }

// Now is the clock's current time.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// AfterFunc calls f once the clock is advanced by d or more. As with
// time.AfterFunc, d <= 0 calls it at once in its own goroutine.
func (c *Clock) AfterFunc(d time.Duration, f func()) (stop func() bool) {
	if d <= 0 {
		go f()
		return func() bool { return false }
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &timer{at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		for i, u := range c.timers {
			if u == t {
				c.timers = append(c.timers[:i], c.timers[i+1:]...)
				return true
			}
		}
		return false
	}
}

// Advance moves the clock forward by d, firing each timer that comes due with
// Now at its deadline, including timers those callbacks set.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	end := c.now.Add(d)
	for {
		next := -1
		for i, t := range c.timers {
			if !t.at.After(end) && (next < 0 || t.at.Before(c.timers[next].at)) {
				next = i
			}
		}
		if next < 0 {
			break
		}
		t := c.timers[next]
		c.timers = append(c.timers[:next], c.timers[next+1:]...)
		c.now = t.at
		c.mu.Unlock()
		t.f()
		c.mu.Lock()
	}
	c.now = end
	c.mu.Unlock()
}
