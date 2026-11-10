package core

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/conn"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/store"
)

// Background sync timing; tests lower the backoff.
var (
	idleMax    = 5 * time.Minute // longest IDLE on INBOX before a sync of every folder
	backoffMin = 2 * time.Second // first wait after a failure, doubled per failure
	backoffMax = 5 * time.Minute
	// chatterWithin: a server update that ends IDLE sooner than this after it
	// began is chatter. A run of chatter waits between rounds as failures do, so a
	// server that keeps reporting changes (or one the sync itself sets off) cannot
	// spin the worker through back-to-back full syncs (#453).
	chatterWithin = 10 * time.Second
)

// Test hooks, nil in production: beforeWorkerExit runs as a worker exits (tests
// make exits slow), onParked each time a worker parks on reauth_required.
var (
	beforeWorkerExit func()
	onParked         func(id int64)
	// afterWorkerSync 报告成功后台同步的请求范围，供测试等待模式切换完成。
	afterWorkerSync func(id int64, only []backend.FolderRef)
)

// wakeSource is what a time set with wakeWorkerAt is for. Each source keeps its
// own time, so one does not drop another's.
type wakeSource int

const (
	wakeOutbox wakeSource = iota // the next queued outbox item comes due
	wakeOps                      // the next purge or archive undo window ends
	wakeSources
)

// worker is one account's background sync goroutine.
type worker struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	wake   chan struct{} // buffered 1: a local change asks for a sync
	full   atomic.Bool   // 全量请求不随普通唤醒被节流消费而丢失。
	// due is when each source wants a sync, zero for none; stopTimer stops the
	// timer set for the earliest, nil if none. Under workersMu.
	due       [wakeSources]time.Time
	stopTimer func() bool
}

// startWorker starts the account's worker if AutoSync is on, the service is open
// and the account has none yet.
func (s *Service) startWorker(id int64) {
	if !s.opts.AutoSync {
		return
	}
	s.workersMu.Lock()
	defer s.workersMu.Unlock()
	if s.closed || s.workers[id] != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := &worker{ctx: ctx, cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1)}
	if s.workers == nil {
		s.workers = map[int64]*worker{}
	}
	s.workers[id] = w
	go func() {
		defer close(w.done)
		s.runWorker(ctx, id, w)
		s.workersMu.Lock()
		if w.stopTimer != nil {
			w.stopTimer() // ctx is done: none is set again
		}
		s.workersMu.Unlock()
		if beforeWorkerExit != nil {
			beforeWorkerExit()
		}
	}()
}

// stopWorker stops the account's worker, if any, and waits for it to exit. The
// worker stays registered until then, so a concurrent Close waits for it too.
func (s *Service) stopWorker(id int64) {
	s.workersMu.Lock()
	w := s.workers[id]
	s.workersMu.Unlock()
	if w == nil {
		return
	}
	w.cancel()
	<-w.done
	s.workersMu.Lock()
	if s.workers[id] == w {
		delete(s.workers, id)
	}
	s.workersMu.Unlock()
}

// wakeWorker asks the account's worker for a sync now.
func (s *Service) wakeWorker(id int64) {
	s.workersMu.Lock()
	w := s.workers[id]
	s.workersMu.Unlock()
	if w != nil {
		select {
		case w.wake <- struct{}{}:
		default: // one is already pending
		}
	}
}

// wakeWorkerAt has the account's worker sync at t (on the Service's clock) for
// src, in place of the time src set before; a zero t sets none. The worker
// wakes at the earliest time of any source.
func (s *Service) wakeWorkerAt(id int64, src wakeSource, t time.Time) {
	s.workersMu.Lock()
	defer s.workersMu.Unlock()
	w := s.workers[id]
	if w == nil || w.ctx.Err() != nil {
		return
	}
	w.due[src] = t
	s.armWorker(id, w)
}

// armWorker sets w's timer for the earliest time a source wants. When it fires,
// the times it reached are spent, the timer is set for the rest and the worker
// syncs. Under workersMu.
func (s *Service) armWorker(id int64, w *worker) {
	if w.stopTimer != nil {
		w.stopTimer()
		w.stopTimer = nil
	}
	var next time.Time
	for _, t := range w.due {
		if !t.IsZero() && (next.IsZero() || t.Before(next)) {
			next = t
		}
	}
	if next.IsZero() {
		return
	}
	c := s.clk()
	w.stopTimer = c.AfterFunc(next.Sub(c.Now()), func() {
		s.workersMu.Lock()
		if w.ctx.Err() == nil {
			now := c.Now()
			for i, t := range w.due {
				if !t.After(now) {
					w.due[i] = time.Time{}
				}
			}
			s.armWorker(id, w)
		}
		s.workersMu.Unlock()
		s.wakeWorker(id)
	})
}

func (s *Service) runWorker(ctx context.Context, id int64, w *worker) {
	wake := w.wake
	var delay, pace time.Duration
	var only []backend.FolderRef // what the last server change touched; nil for a full round
	var fullBy time.Time         // the next full round is due; rounds of only some folders keep it
	for {
		if !s.awaitCredentials(ctx, id, wake) {
			return
		}
		full := w.full.Swap(false)
		if full || !time.Now().Before(fullBy) {
			only = nil
		}
		if only == nil {
			fullBy = time.Now().Add(idleMax)
		}
		if s.light.Load() && !full {
			if refs := s.inboxOnly(ctx, id); len(refs) > 0 {
				only = refs
				fullBy = time.Now().Add(idleMax) // a full round waits for the window
			}
		}
		chatter, changed, err := s.syncRound(ctx, id, wake, only, fullBy)
		only = changed
		if err != nil && full {
			w.full.Store(true)
		}
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			delay = 0
			if !chatter {
				pace = 0
				continue
			}
			// The first chatter goes on at once (it may be real mail); a run of it
			// waits 2 s, 4 s, ... up to backoffMax. A local change cuts the wait short.
			p := pace
			pace = min(max(2*pace, backoffMin), backoffMax)
			if p > 0 && !waitOrWake(ctx, p, wake) {
				return
			}
			continue
		}
		delay = min(max(2*delay, backoffMin), backoffMax)
		if !waitOrWake(ctx, delay, nil) { // a wake stays pending until the backoff is over
			return
		}
	}
}

// inboxOnly 返回支持文件夹限定的账户的收件箱；没有引用时保持完整同步。
// JMAP 使用账户级游标，必须保留实际完整同步所需的本地收尾。
func (s *Service) inboxOnly(ctx context.Context, accountID int64) []backend.FolderRef {
	rows, err := s.db.Query(ctx, `SELECT f.server_ref FROM folders f JOIN accounts a ON a.id = f.account_id
		WHERE f.account_id = ? AND f.role = 'inbox' AND f.server_ref IS NOT NULL AND a.recv_protocol = 'imap'`, accountID)
	if err != nil || len(rows.Values) == 0 {
		return nil
	}
	refs := make([]backend.FolderRef, 0, len(rows.Values))
	for _, v := range rows.Values {
		refs = append(refs, backend.FolderRef(store.Str(v[0])))
	}
	return refs
}

// waitOrWake waits d, or until wake fires (a nil wake never does). It reports
// false when ctx ends.
func waitOrWake(ctx context.Context, d time.Duration, wake <-chan struct{}) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-wake:
	case <-ctx.Done():
		return false
	}
	return true
}

// awaitCredentials parks the worker while IMAP refuses the password or the
// keychain has none (status reauth_required; SMTP refusing it only pauses the
// outbox, and an unavailable keychain, status error, backs off instead), so it
// makes no login attempts. Any LOGIN that succeeds, as SyncNow's, clears the
// status and wakes it; so does any local change, which only re-reads the status. It reports
// false when ctx ends.
func (s *Service) awaitCredentials(ctx context.Context, id int64, wake chan struct{}) bool {
	for {
		r, err := s.db.Query(ctx, "SELECT status FROM accounts WHERE id = ?", id) // IMAP's; SMTP's never parks
		if err != nil || len(r.Values) == 0 || store.Str(r.Values[0][0]) != "reauth_required" {
			return ctx.Err() == nil // on a read error the round reports it
		}
		if onParked != nil {
			onParked(id)
		}
		select {
		case <-wake:
		case <-ctx.Done():
			return false
		}
	}
}

// syncRound runs SyncNow, fetching only the folders in only (nil: all), and
// then waits for a reason to sync again, at the latest until fullBy. A second session Watches the server,
// started before SyncNow, so a change that lands during or after the sync
// ends the wait that follows; changed is where it landed, nil for anywhere or
// when no server change ended the wait. chatter reports that a server change
// ended the wait within chatterWithin of its start.
func (s *Service) syncRound(ctx context.Context, id int64, wake chan struct{}, only []backend.FolderRef, fullBy time.Time) (chatter bool, changed []backend.FolderRef, err error) {
	recv, release, err := s.dialAccount(ctx, id)
	if err != nil {
		return false, nil, s.workerFailed(ctx, id, err)
	}
	updates := make(chan struct{}, 1)
	watched := make(chan error, 1)
	var mu sync.Mutex
	var seen []backend.FolderRef // where Watch said changes landed
	anywhere := false
	wctx, stop := context.WithCancel(ctx)
	go func() {
		watched <- recv.Watch(wctx, func(fs []backend.FolderRef) {
			mu.Lock()
			anywhere = anywhere || len(fs) == 0
			for _, f := range fs {
				if !slices.Contains(seen, f) {
					seen = append(seen, f)
				}
			}
			mu.Unlock()
			select {
			case updates <- struct{}{}:
			default:
			}
		})
	}()
	// touched is where the changes since the last call landed, nil for anywhere.
	touched := func() []backend.FolderRef {
		mu.Lock()
		defer mu.Unlock()
		out := seen
		if anywhere {
			out = nil
		}
		anywhere, seen = false, nil
		return out
	}
	defer func() { stop(); release(); <-watched }() // closing unblocks a Watch mid-command
	// Watch wakes once as it starts watching; one that cannot push returns,
	// and the account is polled as often as its backend declares.
	var poll time.Duration
	select {
	case <-updates:
		touched() // the first wake is for what changed before: this round syncs it
	case err := <-watched:
		watched <- err
		if !errors.Is(err, backend.ErrUnsupported) {
			return false, nil, s.workerFailed(ctx, id, err)
		}
		d, err := recv.Describe(ctx)
		if err != nil {
			return false, nil, s.workerFailed(ctx, id, err)
		}
		poll = d.PollInterval // the backend contract holds it above 0
		release()             // only needed to watch; a second release is harmless
	case <-ctx.Done():
		return false, nil, ctx.Err()
	}
	if err := s.syncNow(ctx, id, false, only); err != nil {
		return false, nil, err // syncNow announced it
	}
	if afterWorkerSync != nil {
		afterWorkerSync(id, only)
	}
	if poll == 0 {
		start := time.Now()
		t := time.NewTimer(time.Until(fullBy))
		defer t.Stop()
		select {
		case <-updates:
			return time.Since(start) < chatterWithin, touched(), nil
		case err := <-watched:
			watched <- err
			return false, nil, s.workerFailed(ctx, id, err)
		case <-wake:
		case <-t.C:
		case <-ctx.Done():
			return false, nil, ctx.Err()
		}
		return false, nil, nil
	}
	t := time.NewTimer(poll)
	defer t.Stop()
	select {
	case <-t.C:
	case <-wake:
	case <-ctx.Done():
	}
	return false, nil, nil
}

// workerFailed announces a failure outside SyncNow, unless the worker is stopping.
func (s *Service) workerFailed(ctx context.Context, id int64, err error) error {
	if ctx.Err() == nil {
		s.bus.Publish(events.Event{Type: events.SyncFailed, AccountID: id, Error: err.Error(), Kind: string(conn.Classify(err))})
	}
	return err
}
