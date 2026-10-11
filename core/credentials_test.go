package core

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/conn"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/internal/testmail"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

func TestAddAccountKeychainUnavailable(t *testing.T) {
	keyring.MockInit()
	s, err := New(bg, Options{DataDir: t.TempDir()})
	require.NoError(t, err)
	defer s.Close()
	smtpAddr, _ := testmail.StartSMTP(t)
	im := testmail.StartIMAP(t)
	keyring.MockInitWithError(errors.New("locked"))
	t.Cleanup(keyring.MockInit)

	_, err = s.AddAccount(bg, internalInput(t, im.Addr, smtpAddr, "a@example.com", false))
	assert.ErrorIs(t, err, ErrKeychainUnavailable)
	assert.Zero(t, im.Logins(), "no connection check without a keychain")
	list, err := s.ListAccounts(bg)
	require.NoError(t, err)
	assert.Empty(t, list)
}

// The keychain locks after the account was added: syncs fail with status error,
// the worker keeps retrying on its backoff (no parking) and recovers by itself.
func TestKeychainLockedAtRuntime(t *testing.T) {
	var locked atomic.Bool
	// Swapping keyring's mock provider would drop the stored password and race
	// with the worker, so the read itself is made to fail.
	setFor(t, &keyringGet, func(svc, user string) (string, error) {
		if locked.Load() {
			return "", errors.New("keychain is locked")
		}
		return keyring.Get(svc, user)
	})
	e, ch := newWorkerEnv(t)
	locked.Store(true)
	err := e.s.SyncNow(bg, e.acc.ID)
	assert.ErrorIs(t, err, ErrKeychainUnavailable)
	a, err := e.s.account(bg, e.acc.ID)
	require.NoError(t, err)
	assert.Equal(t, "error", a.Status)
	assert.Equal(t, "keychain_unavailable", a.StatusDetail)

	n := len(ofType(collect(ch), events.SyncFailed))
	time.Sleep(500 * time.Millisecond) // the woken worker fails and backs off, again and again
	assert.Greater(t, len(ofType(collect(ch), events.SyncFailed))+n, 1, "the worker retries")
	select {
	case <-e.parked:
		t.Fatal("an unavailable keychain parked the worker")
	default:
	}

	locked.Store(false)
	waitEvent(t, ch, events.SyncFinished, 5*time.Second) // no SyncNow: the backoff retry succeeds
	a, err = e.s.account(bg, e.acc.ID)
	require.NoError(t, err)
	assert.Equal(t, "ok", a.Status)
	assert.Empty(t, a.StatusDetail)
	workerResumed(t, e, "back@x")
}

func TestCredentialsMissing(t *testing.T) {
	e, _ := newWorkerEnv(t)
	require.NoError(t, keyring.Delete(keyringService, e.s.keyringUser(e.acc.ID)))
	e.put(t, "INBOX", msg("m@x")) // the next round finds no password
	waitSignal(t, e.parked, "worker parking", 5*time.Second)
	a, err := e.s.account(bg, e.acc.ID)
	require.NoError(t, err)
	assert.Equal(t, "reauth_required", a.Status)
	assert.Equal(t, "credentials_missing", a.StatusDetail)

	n := e.im.Logins()
	e.s.wakeWorker(e.acc.ID)
	time.Sleep(time.Second) // backoff alone would retry about 7 times
	assert.Equal(t, n, e.im.Logins(), "no logins while parked")
	assert.ErrorIs(t, e.s.SyncNow(bg, e.acc.ID), keyring.ErrNotFound)
}

func TestAuthRejectedDetail(t *testing.T) {
	e, ch := newWorkerEnv(t)
	parkWorker(t, e, ch)
	a, err := e.s.account(bg, e.acc.ID)
	require.NoError(t, err)
	assert.Equal(t, "auth_rejected", a.StatusDetail)
	require.NoError(t, keyring.Set(keyringService, e.s.keyringUser(e.acc.ID), "secret"))
	require.NoError(t, e.s.SyncNow(bg, e.acc.ID))
	a, err = e.s.account(bg, e.acc.ID)
	require.NoError(t, err)
	assert.Equal(t, "ok", a.Status)
	assert.Empty(t, a.StatusDetail)
}

// probeWrites records the keychain entries KeychainStatus writes.
func probeWrites(t *testing.T) *[]string {
	var users []string
	setFor(t, &keyringSet, func(svc, user, pw string) error {
		users = append(users, user)
		return keyring.Set(svc, user, pw)
	})
	return &users
}

func assertNoProbes(t *testing.T, users []string) {
	t.Helper()
	require.NotEmpty(t, users)
	for _, u := range users {
		_, err := keyring.Get(keyringService, u)
		assert.ErrorIs(t, err, keyring.ErrNotFound, u)
	}
}

func TestKeychainStatusLeavesNoProbe(t *testing.T) {
	keyring.MockInit()
	s, err := New(bg, Options{DataDir: t.TempDir()})
	require.NoError(t, err)
	defer s.Close()
	users := probeWrites(t)
	require.NoError(t, s.KeychainStatus(bg))
	assertNoProbes(t, *users)

	keyring.MockInitWithError(errors.New("locked"))
	t.Cleanup(keyring.MockInit)
	assert.ErrorIs(t, s.KeychainStatus(bg), ErrKeychainUnavailable)
}

// Each failing step of the probe is ErrKeychainUnavailable, and the probe is
// still cleaned up when deleting works.
func TestKeychainStatusFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		get       func(svc, user string) (string, error)
		del       func(svc, user string) error
		cleanedUp bool
	}{
		"read error":   {get: func(string, string) (string, error) { return "", errors.New("denied") }, cleanedUp: true},
		"wrong value":  {get: func(string, string) (string, error) { return "other", nil }, cleanedUp: true},
		"delete error": {del: func(string, string) error { return errors.New("denied") }},
	} {
		t.Run(name, func(t *testing.T) {
			keyring.MockInit()
			s, err := New(bg, Options{DataDir: t.TempDir()})
			require.NoError(t, err)
			defer s.Close()
			users := probeWrites(t)
			if tc.get != nil {
				setFor(t, &keyringGet, tc.get)
			}
			if tc.del != nil {
				setFor(t, &keyringDelete, tc.del)
			}
			assert.ErrorIs(t, s.KeychainStatus(bg), ErrKeychainUnavailable)
			if tc.cleanedUp {
				assertNoProbes(t, *users)
			}
		})
	}
}

// Two probes at once (two onboarding windows) must not delete each other's entry.
func TestConcurrentKeychainStatus(t *testing.T) {
	keyring.MockInit()
	s, err := New(bg, Options{DataDir: t.TempDir()})
	require.NoError(t, err)
	defer s.Close()
	users := probeWrites(t)
	firstWrote, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	setFor(t, &keyringGet, func(svc, user string) (string, error) {
		if calls.Add(1) == 1 { // the first probe reads only after the second finished
			close(firstWrote)
			<-release
		}
		return keyring.Get(svc, user)
	})
	first := make(chan error, 1)
	go func() { first <- s.KeychainStatus(bg) }()
	<-firstWrote
	require.NoError(t, s.KeychainStatus(bg))
	close(release)
	require.NoError(t, <-first)
	assertNoProbes(t, *users)
}

// A keychain failure is shown over a stale SMTP refusal, which shows again once
// the keychain works and SMTP still refuses.
func TestKeychainErrorOutranksSMTPRefusal(t *testing.T) {
	var locked atomic.Bool
	setFor(t, &keyringGet, func(svc, user string) (string, error) {
		if locked.Load() {
			return "", errors.New("keychain is locked")
		}
		return keyring.Get(svc, user)
	})
	e := newSendEnv(t, false)
	status := func() (string, string) {
		a, err := e.s.account(bg, e.acc.ID)
		require.NoError(t, err)
		return a.Status, a.StatusDetail
	}
	e.smtp.RejectAuth(true)
	e.sendNew(t)
	require.ErrorIs(t, e.s.SyncNow(bg, e.acc.ID), conn.ErrAuthFailed)
	st, d := status()
	assert.Equal(t, []string{"reauth_required", "auth_rejected"}, []string{st, d})

	locked.Store(true)
	require.ErrorIs(t, e.s.SyncNow(bg, e.acc.ID), ErrKeychainUnavailable)
	st, d = status()
	assert.Equal(t, []string{"error", "keychain_unavailable"}, []string{st, d})

	locked.Store(false)
	require.ErrorIs(t, e.s.SyncNow(bg, e.acc.ID), conn.ErrAuthFailed)
	st, d = status()
	assert.Equal(t, []string{"reauth_required", "auth_rejected"}, []string{st, d})
}

func TestAddAccountStoresProvider(t *testing.T) {
	keyring.MockInit()
	s, err := New(bg, Options{DataDir: t.TempDir()})
	require.NoError(t, err)
	defer s.Close()
	smtpAddr, _ := testmail.StartSMTP(t)
	im := testmail.StartIMAP(t)
	a, err := s.AddAccount(bg, internalInput(t, im.Addr, smtpAddr, "a@example.com", false))
	require.NoError(t, err)
	assert.Equal(t, "other", a.Provider)
	in := internalInput(t, im.Addr, smtpAddr, "b@example.com", false)
	in.Provider = "gmail"
	b, err := s.AddAccount(bg, in)
	require.NoError(t, err)
	assert.Equal(t, "gmail", b.Provider)
}
