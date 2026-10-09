package app

import (
	"context"
	"errors"
)

// writeQueue orders writes to one resource: a key's writes run one at a time,
// and while one runs only the newest submitted snapshot waits. All its state
// lives on the UI thread.
type writeQueue struct {
	a      *App
	keys   map[string]*writeKey
	chains map[string][]chainJob // Chain's writes per key, the running one first
	idle   []func(error)         // Settle's waiters
	errs   []error               // what failed while they wait
}

type writeKey struct {
	next    *writeJob // newest snapshot waiting behind the running one
	waiters []func(error)
}

type writeJob struct {
	gen  uint64
	fn   func(ctx context.Context) error
	done func(gen uint64, err error)
}

func newWriteQueue(a *App) *writeQueue {
	return &writeQueue{a: a, keys: map[string]*writeKey{}, chains: map[string][]chainJob{}}
}

// Submit runs fn for key now, or after the key's running write; a write still
// waiting is replaced and its done never called. done gets gen back.
func (q *writeQueue) Submit(key string, gen uint64, fn func(ctx context.Context) error, done func(gen uint64, err error)) {
	j := &writeJob{gen: gen, fn: fn, done: done}
	if k := q.keys[key]; k != nil {
		k.next = j
		return
	}
	k := &writeKey{}
	q.keys[key] = k
	q.run(key, k, j)
}

func (q *writeQueue) run(key string, k *writeKey, j *writeJob) {
	q.a.do(j.fn, func(err error) {
		defer q.settled(err)
		if j.done != nil {
			j.done(j.gen, err) // the key is still busy: a Submit from done waits as next
		}
		if n := k.next; n != nil {
			k.next = nil
			q.run(key, k, n)
			return
		}
		delete(q.keys, key)
		for _, w := range k.waiters {
			w(err)
		}
	})
}

// Flush calls done once the key's running and waiting writes have finished,
// with the last one's error; at once with nil when the key is idle.
func (q *writeQueue) Flush(key string, done func(err error)) {
	if k := q.keys[key]; k != nil {
		k.waiters = append(k.waiters, done)
		return
	}
	done(nil)
}

// Chain runs fn after the writes chained to key before it, in order; none
// is dropped or merged, unlike Submit's snapshots: for commands (mark read,
// move, resolve) where every one counts. done gets fn's error, before the
// next write of key starts.
func (q *writeQueue) Chain(key string, fn func(ctx context.Context) error, done func(err error)) {
	q.chains[key] = append(q.chains[key], chainJob{fn, done})
	if len(q.chains[key]) == 1 {
		q.runChain(key)
	}
}

type chainJob struct {
	fn   func(ctx context.Context) error
	done func(err error)
}

func (q *writeQueue) runChain(key string) {
	j := q.chains[key][0]
	q.a.do(j.fn, func(err error) {
		defer q.settled(err)
		if j.done != nil {
			j.done(err)
		}
		if rest := q.chains[key][1:]; len(rest) > 0 {
			q.chains[key] = rest
			q.runChain(key)
			return
		}
		delete(q.chains, key)
	})
}

// Settle calls done once no write of any key runs or waits, with the
// errors of the writes that finished meanwhile; at once with nil when idle.
func (q *writeQueue) Settle(done func(err error)) {
	q.idle = append(q.idle, done)
	q.settled(nil)
}

func (q *writeQueue) settled(err error) {
	if len(q.idle) == 0 {
		return
	}
	if err != nil {
		q.errs = append(q.errs, err)
	}
	if len(q.keys) > 0 || len(q.chains) > 0 {
		return
	}
	ws, err := q.idle, errors.Join(q.errs...)
	q.idle, q.errs = nil, nil
	for _, w := range ws {
		w(err)
	}
}
