// Package core is Godwit's mail engine.
package core

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/godwitmail/godwit/core/conn"
	"github.com/godwitmail/godwit/core/events"
	"github.com/godwitmail/godwit/core/mailsync"
	"github.com/godwitmail/godwit/core/store"
	"github.com/zalando/go-keyring"
)

// Options configures a Service.
type Options struct {
	DataDir  string
	AutoSync bool // run a background sync worker per account
}

// Service owns the database and event bus of one Godwit data directory.
type Service struct {
	db        *store.DB
	bus       *events.Bus
	dataDir   string
	opts      Options
	installID string // namespaces this data directory's keychain entries

	locksMu sync.Mutex
	locks   map[int64]chan struct{} // per-account 1-slot semaphore: sync and op pushing never overlap

	editMu sync.Mutex // local changes (SetFlags, Move, Delete, drafts, Send) read then write

	workersMu sync.Mutex
	workers   map[int64]*worker
	closed    bool

	closeOnce sync.Once
	closeErr  error
}

// New creates DataDir if needed and opens DataDir/godwit.db.
func New(ctx context.Context, opts Options) (*Service, error) {
	if err := os.MkdirAll(opts.DataDir, 0o700); err != nil {
		return nil, err
	}
	db, err := store.Open(ctx, filepath.Join(opts.DataDir, "godwit.db"))
	if err != nil {
		return nil, err
	}
	if err := recoverOutbox(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	install, err := loadInstallID(ctx, db)
	if err != nil {
		db.Close()
		return nil, err
	}
	s := &Service{db: db, bus: events.New(), dataDir: opts.DataDir, opts: opts, installID: install}
	if err := s.cleanOrphans(ctx); err != nil {
		db.Close()
		return nil, err
	}
	accs, err := s.ListAccounts(ctx)
	if err != nil {
		db.Close()
		return nil, err
	}
	for _, a := range accs {
		s.startWorker(a.ID)
	}
	return s, nil
}

// Close stops the background workers, waits for them and releases the database.
// Concurrent and repeated calls all wait for the same shutdown.
func (s *Service) Close() error {
	s.closeOnce.Do(func() {
		s.workersMu.Lock()
		s.closed = true
		ws := slices.Collect(maps.Values(s.workers)) // includes workers a RemoveAccount is still stopping
		s.workersMu.Unlock()
		for _, w := range ws {
			w.cancel()
		}
		for _, w := range ws {
			<-w.done
		}
		s.workersMu.Lock()
		s.workers = nil
		s.workersMu.Unlock()
		s.closeErr = s.db.Close()
	})
	return s.closeErr
}

// cleanOrphans removes files under cache/, drafts/ and outbox/ that no row refers
// to: leftovers of crashes and failed removals. It runs before any worker starts.
func (s *Service) cleanOrphans(ctx context.Context) error {
	keep := map[string]bool{}
	for _, q := range []string{
		"SELECT cache_path FROM attachments WHERE cache_path IS NOT NULL",
		"SELECT raw_path FROM outbox WHERE state != 'done'",
		"SELECT path FROM draft_attachments WHERE path IS NOT NULL",
	} {
		rows, err := s.db.Query(ctx, q)
		if err != nil {
			return err
		}
		for _, r := range rows.Values {
			if p, err := filepath.Abs(store.Str(r[0])); err == nil {
				keep[p] = true
			}
		}
	}
	for _, sub := range []string{"cache", "drafts", "outbox"} {
		err := store.CleanOrphans(filepath.Join(s.dataDir, sub), func(p string) bool {
			p, err := filepath.Abs(p)
			return err != nil || keep[p]
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// Subscribe returns UI notifications and a func that unsubscribes.
func (s *Service) Subscribe() (<-chan events.Event, func()) { return s.bus.Subscribe() }

// lockAccount takes the account's sync lock, or gives up when ctx ends.
func (s *Service) lockAccount(ctx context.Context, id int64) (unlock func(), err error) {
	s.locksMu.Lock()
	if s.locks == nil {
		s.locks = map[int64]chan struct{}{}
	}
	if s.locks[id] == nil {
		s.locks[id] = make(chan struct{}, 1)
	}
	sem := s.locks[id]
	s.locksMu.Unlock()
	select {
	case sem <- struct{}{}:
		return func() { <-sem }, nil
	default:
	}
	if beforeLockWait != nil {
		beforeLockWait(id)
	}
	select {
	case sem <- struct{}{}:
		return func() { <-sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// SyncNow connects to the account's IMAP server, pushes queued local changes,
// sends and files the outbox, and fetches new mail. Calls for the same account
// run one at a time. Afterwards the background worker is woken; if it was parked
// on reauth_required and this call logged in, it resumes.
func (s *Service) SyncNow(ctx context.Context, accountID int64) error {
	err := s.syncNow(ctx, accountID, true)
	s.wakeWorker(accountID) // the worker re-reads the status itself, so no stale decision here
	return err
}

// syncNow is SyncNow for the worker (explicit false). A cancelled sync is a
// shutdown, not a failure.
func (s *Service) syncNow(ctx context.Context, accountID int64, explicit bool) error {
	unlock, err := s.lockAccount(ctx, accountID)
	if err != nil {
		return err
	}
	defer unlock()
	s.bus.Publish(events.Event{Type: events.SyncStarted, AccountID: accountID})
	if err := s.syncAccount(ctx, accountID, explicit); err != nil {
		if ctx.Err() == nil {
			// Safe to show: conn never includes server text or the password in login errors.
			s.bus.Publish(events.Event{Type: events.SyncFailed, AccountID: accountID, Error: err.Error()})
		}
		return err
	}
	s.bus.Publish(events.Event{Type: events.SyncFinished, AccountID: accountID})
	return nil
}

// Test hooks, nil in production: afterAccountRead runs in dialAccount after it read
// the account row, beforeLockWait when lockAccount finds the lock taken and is about to wait.
var (
	afterAccountRead func(id int64)
	beforeLockWait   func(id int64)
)

// dialAccount connects and logs in to the account's IMAP server. An auth failure
// also marks the account reauth_required. release closes the connection and must
// be called; cancelling ctx aborts blocked reads. h, if not nil, receives IDLE updates.
func (s *Service) dialAccount(ctx context.Context, accountID int64, h *imapclient.UnilateralDataHandler) (c *imapclient.Client, release, abort func(), err error) {
	acc, err := s.account(ctx, accountID)
	if err != nil {
		return nil, nil, nil, err
	}
	if afterAccountRead != nil {
		afterAccountRead(accountID)
	}
	pw, err := keyring.Get(keyringService, s.keyringUser(accountID))
	if err != nil {
		return nil, nil, nil, err
	}
	c, abort, err = conn.DialIMAPAbortable(ctx, conn.Endpoint{Host: acc.IMAPHost, Port: acc.IMAPPort, Security: acc.IMAPSecurity,
		Username: acc.Username, Password: pw}, h)
	if err != nil {
		if errors.Is(err, conn.ErrAuthFailed) {
			err = errors.Join(err, s.setStatus(ctx, accountID, "reauth_required"))
		}
		return nil, nil, nil, err
	}
	// The credentials work: clear reauth_required as it is now, not as it was read
	// above, since another login may have failed meanwhile. A parked worker resumes.
	// A refusal by SMTP stays until an SMTP login succeeds (checkSMTP).
	r, err := s.db.Execute(ctx, "UPDATE accounts SET status = 'ok' WHERE id = ? AND status = 'reauth_required' AND smtp_reauth = 0", accountID)
	if err != nil {
		c.Close()
		return nil, nil, nil, err
	}
	if r.Changes > 0 {
		s.wakeWorker(accountID)
	}
	stop := context.AfterFunc(ctx, func() { c.Close() })
	return c, func() { stop(); c.Close() }, abort, nil
}

// syncAccount runs one sync. An SMTP login failure skips the outbox but not the
// rest; it is returned at the end, so a worker round ends and parks.
func (s *Service) syncAccount(ctx context.Context, accountID int64, explicit bool) error {
	c, release, _, err := s.dialAccount(ctx, accountID, nil)
	if err != nil {
		return err
	}
	defer release()
	if err := s.pushOps(ctx, c, accountID, mailsync.PushOps); err != nil {
		return err
	}
	smtpErr := s.checkSMTP(ctx, accountID, explicit)
	if smtpErr == nil {
		smtpErr = s.processOutbox(ctx, c, accountID)
		if smtpErr != nil && !errors.Is(smtpErr, conn.ErrAuthFailed) {
			return smtpErr
		}
	}
	if err := mailsync.Fetch(ctx, s.db, c, accountID, s.bus); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	// Ops that waited for a move's claim can go now; ones already tried are not retried.
	if err := s.pushOps(ctx, c, accountID, mailsync.PushNewOps); err != nil {
		return err
	}
	return smtpErr
}

// checkSMTP returns nil when the outbox may use SMTP. After SMTP refused the
// password only an explicit SyncNow tries it again, with one login; success
// clears reauth_required (IMAP has just logged in too) and resumes the worker.
func (s *Service) checkSMTP(ctx context.Context, accountID int64, explicit bool) error {
	rows, err := s.db.Query(ctx, "SELECT smtp_reauth FROM accounts WHERE id = ?", accountID)
	if err != nil || len(rows.Values) == 0 || store.Int(rows.Values[0][0]) == 0 {
		return err
	}
	if !explicit {
		return fmt.Errorf("smtp: %w", conn.ErrAuthFailed)
	}
	ep, _, err := s.smtpEndpoint(ctx, accountID)
	if err != nil {
		return err
	}
	sc, err := conn.DialSMTP(ctx, ep)
	if err != nil {
		return err
	}
	sc.Close()
	if _, err := s.db.Execute(ctx, "UPDATE accounts SET status = 'ok', smtp_reauth = 0 WHERE id = ?", accountID); err != nil {
		return err
	}
	s.wakeWorker(accountID)
	return nil
}

// pushOps pushes queued local changes. Ops that became conflict or failed are
// announced with SyncFailed and the sync goes on.
func (s *Service) pushOps(ctx context.Context, c *imapclient.Client, accountID int64,
	push func(context.Context, *store.DB, *imapclient.Client, int64) error) error {
	err := push(ctx, s.db, c, accountID)
	var problems *mailsync.OpsError
	if errors.As(err, &problems) {
		if len(problems.Changed) > 0 {
			s.announce(ctx, accountID, problems.Changed)
		}
		if err == error(problems) {
			s.bus.Publish(events.Event{Type: events.SyncFailed, AccountID: accountID, Error: problems.Error()})
			return nil
		}
	}
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// announce publishes MessagesChanged for messages changed outside a local edit.
func (s *Service) announce(ctx context.Context, accountID int64, ids []int64) {
	ev := events.Event{Type: events.MessagesChanged, AccountID: accountID, MessageIDs: ids}
	for _, id := range ids {
		if r, err := s.db.Query(ctx, "SELECT thread_id FROM messages WHERE id = ?", id); err == nil && len(r.Values) > 0 && r.Values[0][0] != nil {
			ev.ThreadIDs = append(ev.ThreadIDs, store.Int(r.Values[0][0]))
		}
	}
	ev.ThreadIDs = slices.Compact(slices.Sorted(slices.Values(ev.ThreadIDs)))
	s.bus.Publish(ev)
}

func (s *Service) setStatus(ctx context.Context, accountID int64, status string) error {
	_, err := s.db.Execute(ctx, "UPDATE accounts SET status = ? WHERE id = ?", status, accountID)
	return err
}

// ListFolders returns the account's folders with their unread counts.
func (s *Service) ListFolders(ctx context.Context, accountID int64) ([]Folder, error) {
	rows, err := s.db.Query(ctx, `SELECT f.id, f.account_id, f.name, f.role,
		(SELECT count(*) FROM messages m WHERE m.folder_id = f.id AND instr(m.flags, '\Seen') = 0)
		FROM folders f WHERE f.account_id = ? ORDER BY f.id`, accountID)
	if err != nil {
		return nil, err
	}
	out := make([]Folder, 0, len(rows.Values))
	for _, r := range rows.Values {
		out = append(out, Folder{ID: store.Int(r[0]), AccountID: store.Int(r[1]), Name: store.Str(r[2]),
			Role: store.Str(r[3]), Unread: int(store.Int(r[4]))})
	}
	return out, nil
}
