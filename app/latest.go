package app

import "context"

// latest is a read whose newest run alone lands: Run drops the result of
// the run before it, Stop drops it and cancels its context. A screen keeps
// one per thing it reads instead of a counter; the zero value is ready. All
// its state lives on the UI thread.
type latest struct {
	loading bool // a run is in flight
	seq     uint64
	cancel  context.CancelFunc // the newest run's
}

// Run reads with fn off the UI thread, then calls done on the UI thread with
// fn's error, unless Run or Stop was called since: then done is dropped. A
// run that Run drops is left to finish; only Stop cancels fn's context.
func (l *latest) Run(a *App, fn func(ctx context.Context) error, done func(err error)) {
	l.seq++
	ctx, cancel := context.WithCancel(a.ctx)
	seq := l.seq
	l.loading, l.cancel = true, cancel
	a.do(func(context.Context) error { return fn(ctx) }, func(err error) {
		cancel()
		if seq != l.seq {
			return
		}
		l.loading, l.cancel = false, nil
		done(err)
	})
}

// Also reads with fn as part of what the last Run read (the next page of
// it, say): its done is dropped by the next Run or Stop like the run's, but
// it neither shows as loading nor is cancelled.
func (l *latest) Also(a *App, fn func(ctx context.Context) error, done func(err error)) {
	seq := l.seq
	a.do(fn, func(err error) {
		if seq == l.seq {
			done(err)
		}
	})
}

// Stop drops the run in flight, if any, and cancels its context.
func (l *latest) Stop() {
	l.seq++
	if l.cancel != nil {
		l.cancel()
	}
	l.loading, l.cancel = false, nil
}

// Gen numbers the runs: it rises with every Run and Stop, so a result
// stamped with it tells whether a later run has landed.
func (l *latest) Gen() uint64 { return l.seq }
