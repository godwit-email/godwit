// Package core is Godwit's mail engine.
package core

import (
	"context"
	"errors"
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
	AutoSync bool // background workers arrive with the worker task; unused for now
}

// Service owns the database and event bus of one Godwit data directory.
type Service struct {
	db      *store.DB
	bus     *events.Bus
	dataDir string
	opts    Options

	locksMu sync.Mutex
	locks   map[int64]*sync.Mutex // per-account: sync and op pushing never overlap

	editMu sync.Mutex // local changes (SetFlags, Move, Delete, drafts, Send) read then write
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
	return &Service{db: db, bus: events.New(), dataDir: opts.DataDir, opts: opts}, nil
}

// Close releases the database.
func (s *Service) Close() error { return s.db.Close() }

// Subscribe returns UI notifications and a func that unsubscribes.
func (s *Service) Subscribe() (<-chan events.Event, func()) { return s.bus.Subscribe() }

// accountLock returns the account's sync mutex, creating it on first use.
func (s *Service) accountLock(id int64) *sync.Mutex {
	s.locksMu.Lock()
	defer s.locksMu.Unlock()
	if s.locks == nil {
		s.locks = map[int64]*sync.Mutex{}
	}
	if s.locks[id] == nil {
		s.locks[id] = new(sync.Mutex)
	}
	return s.locks[id]
}

// SyncNow connects to the account's IMAP server, pushes queued local changes,
// sends and files the outbox, and fetches new mail. Calls for the same account run one at a time.
func (s *Service) SyncNow(ctx context.Context, accountID int64) error {
	mu := s.accountLock(accountID)
	mu.Lock()
	defer mu.Unlock()
	s.bus.Publish(events.Event{Type: events.SyncStarted, AccountID: accountID})
	if err := s.syncAccount(ctx, accountID); err != nil {
		// Safe to show: conn never includes server text or the password in login errors.
		s.bus.Publish(events.Event{Type: events.SyncFailed, AccountID: accountID, Error: err.Error()})
		return err
	}
	s.bus.Publish(events.Event{Type: events.SyncFinished, AccountID: accountID})
	return nil
}

// dialAccount connects and logs in to the account's IMAP server. An auth failure
// also marks the account reauth_required. release closes the connection and must
// be called; cancelling ctx aborts blocked reads.
func (s *Service) dialAccount(ctx context.Context, accountID int64) (c *imapclient.Client, release, abort func(), err error) {
	acc, err := s.account(ctx, accountID)
	if err != nil {
		return nil, nil, nil, err
	}
	pw, err := keyring.Get(keyringService, keyringUser(accountID))
	if err != nil {
		return nil, nil, nil, err
	}
	c, abort, err = conn.DialIMAPAbortable(ctx, conn.Endpoint{Host: acc.IMAPHost, Port: acc.IMAPPort, Security: acc.IMAPSecurity,
		Username: acc.Username, Password: pw})
	if err != nil {
		if errors.Is(err, conn.ErrAuthFailed) {
			err = errors.Join(err, s.setStatus(ctx, accountID, "reauth_required"))
		}
		return nil, nil, nil, err
	}
	stop := context.AfterFunc(ctx, func() { c.Close() })
	return c, func() { stop(); c.Close() }, abort, nil
}

func (s *Service) syncAccount(ctx context.Context, accountID int64) error {
	c, release, _, err := s.dialAccount(ctx, accountID)
	if err != nil {
		return err
	}
	defer release()
	if err := s.pushOps(ctx, c, accountID, mailsync.PushOps); err != nil {
		return err
	}
	if err := s.processOutbox(ctx, c, accountID); err != nil {
		return err
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
	return s.setStatus(ctx, accountID, "ok")
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
