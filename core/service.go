// Package core is Godwit's mail engine.
package core

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/conn"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/mailsync"
	"github.com/godwit-email/godwit/core/store"
)

// ErrClosed is returned by calls made after Close.
var ErrClosed = errors.New("service is closed")

// Options configures a Service.
type Options struct {
	DataDir  string
	AutoSync bool // run a background sync worker per account
	// SystemLanguage is the operating system language (BCP 47) the GUI passes in; empty means "en".
	SystemLanguage string
}

// Service owns the database and event bus of one Godwit data directory.
type Service struct {
	skillsMu  sync.Mutex
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

	callsMu     sync.Mutex
	modelMu     sync.Mutex   // serializes model-service updates (ponytail: global, per-id if contended)
	modelHTTP   *http.Client // model-service HTTP; nil = ai.NoRedirectClient; tests swap it
	aiMu        sync.Mutex
	ai          *ai.Engine         // created on first use; see aiEngine
	calls       map[*call]struct{} // explicit network calls in flight (SyncNow, downloads, ...)
	callsClosed bool
	removing    map[int64]bool // accounts RemoveAccount is deleting

	oauthMu sync.Mutex
	flows   map[string]oauthFlow  // OAuth flows begun and not yet awaited or cancelled
	grants  map[string]oauthGrant // finished flows' grants, kept for grantTTL

	tokMu      sync.Mutex
	tokens     map[int64]oauthToken    // OAuth accounts' access tokens; never stored
	refreshing map[int64]chan struct{} // per-account 1-slot semaphore around a token refresh

	credMu  sync.Mutex
	credVer map[int64]uint64 // bumped when the user gives an account new credentials

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

// Close refuses new calls with ErrClosed, cancels the explicit network calls and
// background workers, waits for them (each records its outcome) and releases the
// database. Concurrent and repeated calls all wait for the same shutdown.
func (s *Service) Close() error {
	s.closeOnce.Do(func() {
		s.callsMu.Lock()
		s.callsClosed = true
		s.callsMu.Unlock()
		s.stopCalls(func(int64) bool { return true })
		s.closeOAuth()
		s.aiMu.Lock()
		if s.ai != nil {
			s.ai.Close() // every call using it has ended
		}
		s.aiMu.Unlock()
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

// call is an explicit network call in flight.
type call struct {
	accountID int64 // 0 before the account exists (AddAccount)
	cancel    context.CancelFunc
	done      chan struct{}
}

type callKey struct{}

// begin registers an explicit call on accountID (0 until bind names it) before it
// touches the database or network. Its context also ends when the Service closes
// or the account is removed, which then wait for end; end must be called once
// the call has recorded its outcome.
func (s *Service) begin(ctx context.Context, accountID int64) (context.Context, func(), error) {
	s.callsMu.Lock()
	defer s.callsMu.Unlock()
	if s.callsClosed {
		return nil, nil, ErrClosed
	}
	if s.removing[accountID] {
		return nil, nil, fmt.Errorf("account %d is being removed", accountID)
	}
	ctx, cancel := context.WithCancel(ctx)
	c := &call{accountID: accountID, cancel: cancel, done: make(chan struct{})}
	ctx = context.WithValue(ctx, callKey{}, c)
	if s.calls == nil {
		s.calls = map[*call]struct{}{}
	}
	s.calls[c] = struct{}{}
	return ctx, func() {
		cancel()
		s.callsMu.Lock()
		delete(s.calls, c)
		s.callsMu.Unlock()
		close(c.done)
	}, nil
}

// bind ties the call begun with ctx to accountID once the call has read it, so
// removing that account stops it.
func (s *Service) bind(ctx context.Context, accountID int64) error {
	s.callsMu.Lock()
	defer s.callsMu.Unlock()
	if s.removing[accountID] {
		return fmt.Errorf("account %d is being removed", accountID)
	}
	ctx.Value(callKey{}).(*call).accountID = accountID
	return nil
}

// stopCalls cancels the calls whose account matches and waits for them to end.
func (s *Service) stopCalls(match func(accountID int64) bool) {
	s.callsMu.Lock()
	var stop []*call
	for c := range s.calls {
		if match(c.accountID) {
			c.cancel()
			stop = append(stop, c)
		}
	}
	s.callsMu.Unlock()
	for _, c := range stop {
		<-c.done
	}
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
	ctx, end, err := s.begin(ctx, accountID)
	if err != nil {
		return err
	}
	defer end()
	err = s.syncNow(ctx, accountID, true)
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
	var ver uint64
	for rejected := ""; ; {
		var pw, tok string
		if pw, tok, ver, err = s.credentials(ctx, acc, rejected); err != nil {
			return nil, nil, nil, err
		}
		c, abort, err = conn.DialIMAPAbortable(ctx, conn.Endpoint{Host: acc.IMAPHost, Port: acc.IMAPPort, Security: acc.IMAPSecurity,
			Username: acc.Username, Password: pw, AccessToken: tok}, h)
		// A refused access token may just be stale: refresh it and try once more.
		if rejected != "" || tok == "" || !errors.Is(err, conn.ErrAuthFailed) {
			break
		}
		rejected = tok
	}
	if err != nil {
		if errors.Is(err, conn.ErrAuthFailed) {
			err = errors.Join(err, s.ifCurrent(accountID, ver, func() error {
				return s.setStatus(ctx, accountID, "reauth_required", "auth_rejected")
			}))
		}
		return nil, nil, nil, err
	}
	// The credentials work: clear reauth_required as it is now, not as it was read
	// above, since another login may have failed meanwhile. A parked worker resumes.
	// SMTP's refusal (smtp_reauth) is separate and stays until checkSMTP clears it.
	r, err := s.db.Execute(ctx, "UPDATE accounts SET status = 'ok', status_detail = '' WHERE id = ? AND status = 'reauth_required'", accountID)
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
	useSMTP, smtpErr := s.checkSMTP(ctx, accountID, explicit)
	if useSMTP {
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

// checkSMTP reports whether the outbox may use SMTP. After SMTP refused the
// password the outbox pauses: a worker sync skips it without logging in, and an
// explicit SyncNow tries one SMTP login, which on success clears smtp_reauth.
func (s *Service) checkSMTP(ctx context.Context, accountID int64, explicit bool) (bool, error) {
	rows, err := s.db.Query(ctx, "SELECT smtp_reauth FROM accounts WHERE id = ?", accountID)
	if err != nil || len(rows.Values) == 0 || store.Int(rows.Values[0][0]) == 0 {
		return err == nil, err
	}
	if !explicit {
		return false, nil
	}
	ep, _, _, err := s.smtpEndpoint(ctx, accountID, "")
	if err != nil {
		return false, err
	}
	sc, err := conn.DialSMTP(ctx, ep)
	if errors.Is(err, conn.ErrAuthFailed) && ep.AccessToken != "" {
		if ep, _, _, err = s.smtpEndpoint(ctx, accountID, ep.AccessToken); err == nil {
			sc, err = conn.DialSMTP(ctx, ep)
		}
	}
	if err != nil {
		return false, err
	}
	sc.Close()
	_, err = s.db.Execute(ctx, "UPDATE accounts SET smtp_reauth = 0 WHERE id = ?", accountID)
	return err == nil, err
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

func (s *Service) setStatus(ctx context.Context, accountID int64, status, detail string) error {
	_, err := s.db.Execute(ctx, "UPDATE accounts SET status = ?, status_detail = ? WHERE id = ?", status, detail, accountID)
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
