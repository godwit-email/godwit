package core

import (
	"context"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/godwitmail/godwit/core/events"
	"github.com/godwitmail/godwit/core/mailsync"
)

// Background sync timing; tests lower the backoff.
var (
	idleMax      = 5 * time.Minute // longest IDLE on INBOX before a sync of every folder
	pollInterval = 5 * time.Minute // wait between syncs when the server lacks IDLE
	backoffMin   = 2 * time.Second // first wait after a failure, doubled per failure
	backoffMax   = 5 * time.Minute
)

// worker is one account's background sync goroutine.
type worker struct {
	cancel context.CancelFunc
	done   chan struct{}
	wake   chan struct{} // buffered 1: a local change asks for a sync
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
	w := &worker{cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1)}
	if s.workers == nil {
		s.workers = map[int64]*worker{}
	}
	s.workers[id] = w
	go func() {
		defer close(w.done)
		s.runWorker(ctx, id, w.wake)
	}()
}

// stopWorker stops the account's worker, if any, and waits for it to exit.
func (s *Service) stopWorker(id int64) {
	s.workersMu.Lock()
	w := s.workers[id]
	delete(s.workers, id)
	s.workersMu.Unlock()
	if w != nil {
		w.cancel()
		<-w.done
	}
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

func (s *Service) runWorker(ctx context.Context, id int64, wake chan struct{}) {
	var delay time.Duration
	for {
		err := s.syncRound(ctx, id, wake)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			delay = 0
			continue
		}
		delay = min(max(2*delay, backoffMin), backoffMax)
		t := time.NewTimer(delay) // a wake stays pending until the backoff is over
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return
		}
	}
}

// syncRound runs SyncNow and then waits for a reason to sync again. With IDLE,
// INBOX is selected on a second connection before SyncNow, so mail that lands
// during or after the sync ends the IDLE that follows.
func (s *Service) syncRound(ctx context.Context, id int64, wake chan struct{}) error {
	updates := make(chan struct{}, 1)
	notify := func() {
		select {
		case updates <- struct{}{}:
		default:
		}
	}
	c, release, _, err := s.dialAccount(ctx, id, &imapclient.UnilateralDataHandler{
		Expunge: func(uint32) { notify() },
		Mailbox: func(*imapclient.UnilateralDataMailbox) { notify() },
		Fetch:   func(*imapclient.FetchMessageData) { notify() },
	})
	if err != nil {
		return s.workerFailed(ctx, id, err)
	}
	defer release()
	idle := c.Caps().Has(imap.CapIdle)
	if idle {
		if _, err := c.Select("INBOX", nil).Wait(); err != nil {
			return s.workerFailed(ctx, id, err)
		}
	} else {
		release() // only needed for IDLE; a second release is harmless
	}
	if err := s.SyncNow(ctx, id); err != nil {
		return err // SyncNow announced it
	}
	if idle {
		if err := mailsync.Idle(ctx, c, updates, wake, idleMax); err != nil {
			return s.workerFailed(ctx, id, err)
		}
		return nil
	}
	t := time.NewTimer(pollInterval)
	defer t.Stop()
	select {
	case <-t.C:
	case <-wake:
	case <-ctx.Done():
	}
	return nil
}

// workerFailed announces a failure outside SyncNow, unless the worker is stopping.
func (s *Service) workerFailed(ctx context.Context, id int64, err error) error {
	if ctx.Err() == nil {
		s.bus.Publish(events.Event{Type: events.SyncFailed, AccountID: id, Error: err.Error()})
	}
	return err
}
