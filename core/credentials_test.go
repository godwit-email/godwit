package core

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

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

func TestKeychainStatusLeavesNoProbe(t *testing.T) {
	keyring.MockInit()
	s, err := New(bg, Options{DataDir: t.TempDir()})
	require.NoError(t, err)
	defer s.Close()
	require.NoError(t, s.KeychainStatus(bg))
	_, err = keyring.Get(keyringService, s.installID+"/probe")
	assert.ErrorIs(t, err, keyring.ErrNotFound)

	keyring.MockInitWithError(errors.New("locked"))
	t.Cleanup(keyring.MockInit)
	assert.ErrorIs(t, s.KeychainStatus(bg), ErrKeychainUnavailable)
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
