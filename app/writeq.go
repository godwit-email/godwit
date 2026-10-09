package app

import "context"

// writeQueue orders writes to one resource: a key's writes run one at a time,
// and while one runs only the newest submitted snapshot waits. All its state
// lives on the UI thread.
type writeQueue struct {
	a    *App
	keys map[string]*writeKey
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

func newWriteQueue(a *App) *writeQueue { return &writeQueue{a: a, keys: map[string]*writeKey{}} }

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
