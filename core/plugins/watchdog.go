package plugins

import (
	"runtime"
	"sync"
	"time"
)

// Watchdog is the soft heap limit: goja has no per-runtime memory cap, so
// while a call runs it samples a process-wide meter and interrupts that call
// with ErrMemoryLimit when the meter has grown by more than Threshold since the
// call began. ponytail: process-wide, so it cannot tell the plugin's
// allocations from the app's; a child process with GOMEMLIMIT is the hard cap
// if one is ever needed.
//
// Zero fields take defaults: Meter reads runtime.MemStats.HeapAlloc, Clock is
// the VM's, Interval 10 ms, Threshold 256 MB.
type Watchdog struct {
	Meter     func() uint64
	Clock     Clock
	Interval  time.Duration
	Threshold uint64
}

func heapAlloc() uint64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// Watch samples until release and can only ever abort callID: a tick after
// release, or while another call runs, does nothing.
func (w *Watchdog) Watch(v *VM, callID uint64) (release func()) {
	meter, clock, interval, threshold := w.Meter, w.Clock, w.Interval, w.Threshold
	if meter == nil {
		meter = heapAlloc
	}
	if clock == nil {
		clock = v.clock
	}
	if interval == 0 {
		interval = 10 * time.Millisecond
	}
	if threshold == 0 {
		threshold = 256 << 20
	}
	base := meter()

	var mu sync.Mutex
	released := false
	var stop func() bool
	var tick func()
	tick = func() {
		mu.Lock()
		defer mu.Unlock()
		if released {
			return
		}
		if m := meter(); m > base && m-base > threshold {
			v.abort(callID, ErrMemoryLimit)
			return
		}
		stop = clock.AfterFunc(interval, tick)
	}
	mu.Lock() // the first tick may fire before stop is assigned
	stop = clock.AfterFunc(interval, tick)
	mu.Unlock()
	return func() {
		mu.Lock()
		defer mu.Unlock()
		released = true
		stop()
	}
}
