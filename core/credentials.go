package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/auth"
	"github.com/zalando/go-keyring"
)

// ErrKeychainUnavailable means the system keychain cannot be used (locked, no
// secret service, access denied). Credentials never fall back to a file.
var ErrKeychainUnavailable = errors.New("system keychain is unavailable")

// Replaced in tests.
var (
	keyringGet    = keyring.Get
	keyringSet    = keyring.Set
	keyringDelete = keyring.Delete
)

// keychainErr passes nil and keyring.ErrNotFound through and wraps anything
// else in ErrKeychainUnavailable.
func keychainErr(err error) error {
	if err == nil || errors.Is(err, keyring.ErrNotFound) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrKeychainUnavailable, err)
}

// keychainValues holds every value read from or written to the keychain
// (passwords, refresh tokens, model keys, extension secrets) and every access
// token, for Redact. ponytail: process-wide, so one Service also redacts
// another's credentials; per-Service if several ever share a process for real.
var keychainValues secretSet

func keychainGet(user string) (string, error) {
	v, err := keyringGet(keyringService, user)
	if err == nil {
		keychainValues.Add(v)
	}
	return v, keychainErr(err)
}

func keychainSet(user, secret string) error {
	keychainValues.Add(secret)
	return keychainErr(keyringSet(keyringService, user, secret))
}

// Redact replaces every credential and extension secret this Service knows of
// in text. Not for RPC. Values under 8 characters are skipped (ai.Redact):
// replacing them would mangle ordinary text, and the ones that matter (keys,
// tokens) are longer; a short password is a known gap, not leaked by core's
// own errors, which never carry credentials.
func (s *Service) Redact(text string) string {
	return ai.Redact(text, append(s.secrets.Values(), keychainValues.Values()...))
}

func keychainDelete(user string) error {
	return keychainErr(keyringDelete(keyringService, user))
}

// KeychainStatus writes, reads back and deletes a fresh probe entry; any failure is
// ErrKeychainUnavailable. Onboarding calls it before showing the account form.
func (s *Service) KeychainStatus(ctx context.Context) error {
	b := make([]byte, 8)
	rand.Read(b)
	user := s.installID + "/probe/" + hex.EncodeToString(b) // concurrent probes must not delete each other's
	err := keychainSet(user, "probe")
	if err == nil {
		var v string
		v, err = keychainGet(user)
		if err == nil && v != "probe" {
			err = errors.New("probe read back wrong")
		}
		if derr := keychainDelete(user); err == nil {
			err = derr
		}
	}
	if err != nil && !errors.Is(err, ErrKeychainUnavailable) { // ErrNotFound right after a write: broken too
		err = fmt.Errorf("%w: %w", ErrKeychainUnavailable, err)
	}
	return err
}

// credentials returns the account's password, or for an OAuth account an access
// token, and records the outcome in its status: a missing keychain entry is
// reauth_required (the worker parks), an unusable keychain is error (the worker
// keeps retrying), and a successful read clears error. rejected is an access
// token the server refused, or "": a token other than it is returned. ver is the
// credential version they belong to; a refusal is recorded only through
// ifCurrent(ver), so a stale one cannot undo UpdatePassword, Reauthorize or a
// newer token refresh.
func (s *Service) credentials(ctx context.Context, acc Account, rejected string) (password, accessToken string, ver uint64, err error) {
	if acc.AuthType == "oauth" {
		tok, ver, err := s.accessToken(ctx, acc, rejected)
		return "", tok, ver, err
	}
	s.credMu.Lock()
	ver = s.credVer[acc.ID]
	s.credMu.Unlock()
	pw, err := s.secret(ctx, acc, ver)
	return pw, "", ver, err
}

// ifCurrent runs write, a record of a refusal of version ver's credentials,
// unless the account has newer credentials by now.
func (s *Service) ifCurrent(id int64, ver uint64, write func() error) error {
	s.credMu.Lock()
	defer s.credMu.Unlock()
	if s.credVer[id] != ver {
		return nil // the new credentials get their own try
	}
	return write()
}

// newCredentials makes refusals of the account's older credentials stale and
// caches g's access token, if any, as the current one. The caller holds tokMu
// and credMu (always taken in that order).
func (s *Service) newCredentials(id int64, g *auth.Grant) uint64 {
	if s.credVer == nil {
		s.credVer = map[int64]uint64{}
	}
	s.credVer[id]++
	if g != nil {
		if s.tokens == nil {
			s.tokens = map[int64]oauthToken{}
		}
		s.tokens[id] = oauthToken{g.AccessToken, g.Expiry, s.credVer[id]}
		keychainValues.Add(g.AccessToken)
	}
	return s.credVer[id]
}

// cacheToken caches g's access token as the account's current credentials.
func (s *Service) cacheToken(id int64, g auth.Grant) uint64 {
	s.tokMu.Lock()
	defer s.tokMu.Unlock()
	s.credMu.Lock()
	defer s.credMu.Unlock()
	return s.newCredentials(id, &g)
}

// credentialsUpdated is newCredentials for credentials the user just gave
// (a password in the keychain, or g): it also clears the account's credential
// problems and wakes its worker.
func (s *Service) credentialsUpdated(ctx context.Context, id int64, g *auth.Grant) error {
	s.tokMu.Lock()
	defer s.tokMu.Unlock()
	s.credMu.Lock()
	defer s.credMu.Unlock()
	s.newCredentials(id, g)
	_, err := s.db.Execute(context.WithoutCancel(ctx),
		"UPDATE accounts SET status = 'ok', status_detail = '', smtp_reauth = 0 WHERE id = ?", id)
	s.wakeWorker(id)
	return err
}

// secret reads the account's keychain entry: a password or a refresh token.
func (s *Service) secret(ctx context.Context, acc Account, ver uint64) (string, error) {
	v, err := keychainGet(s.keyringUser(acc.ID))
	switch {
	case errors.Is(err, keyring.ErrNotFound):
		err = fmt.Errorf("credentials not found in keychain: %w", err)
		return "", errors.Join(err, s.ifCurrent(acc.ID, ver, func() error {
			return s.setStatus(ctx, acc.ID, "reauth_required", "credentials_missing")
		}))
	case err != nil:
		return "", errors.Join(err, s.setStatus(ctx, acc.ID, "error", "keychain_unavailable"))
	}
	if _, err := s.db.Execute(ctx, "UPDATE accounts SET status = 'ok', status_detail = '' WHERE id = ? AND status = 'error'", acc.ID); err != nil {
		return "", err
	}
	return v, nil
}

// refreshLock returns the account's refresh semaphore: send to take it.
func (s *Service) refreshLock(id int64) chan struct{} {
	s.tokMu.Lock()
	defer s.tokMu.Unlock()
	if s.refreshing == nil {
		s.refreshing = map[int64]chan struct{}{}
	}
	if s.refreshing[id] == nil {
		s.refreshing[id] = make(chan struct{}, 1)
	}
	return s.refreshing[id]
}

// refreshTimeout bounds one OAuth refresh, so a token endpoint that never answers
// cannot hold the account's refresh lock. Tests lower it.
var refreshTimeout = 30 * time.Second

// tokenMargin: a cached access token closer than this to its expiry is refreshed.
const tokenMargin = 2 * time.Minute

func (t oauthToken) fresh() bool {
	return t.access != "" && time.Until(t.expiry) > tokenMargin
}

// accessToken returns a cached access token other than rejected, or refreshes
// it, one refresh per account at a time: callers that waited, or that were
// refused a token someone already replaced, use the cached one. A
// rotated refresh token is saved before the new access token is used; if that
// fails the old one stays in the keychain. A revoked grant is reauth_required
// (token_revoked); other failures change no status, so the worker backs off.
// Every new token is a new credential version, returned with it.
func (s *Service) accessToken(ctx context.Context, acc Account, rejected string) (string, uint64, error) {
	s.tokMu.Lock()
	seen := s.tokens[acc.ID]
	s.tokMu.Unlock()
	sem := s.refreshLock(acc.ID)
	if seen.fresh() && seen.access != rejected {
		return seen.access, seen.ver, nil
	}
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-ctx.Done():
		return "", 0, ctx.Err()
	}
	s.tokMu.Lock()
	cur := s.tokens[acc.ID]
	if cur.fresh() && cur.access != rejected {
		s.tokMu.Unlock()
		return cur.access, cur.ver, nil // refreshed by someone else
	}
	delete(s.tokens, acc.ID)
	s.tokMu.Unlock()
	s.credMu.Lock()
	ver := s.credVer[acc.ID] // for the statuses below; refreshes are serialised by sem
	s.credMu.Unlock()

	rt, err := s.secret(ctx, acc, ver)
	if err != nil {
		return "", 0, err
	}
	cfg, err := oauthConfig(acc.Provider)
	if err != nil {
		return "", 0, err
	}
	rctx, cancel := context.WithTimeout(ctx, refreshTimeout)
	g, err := auth.Refresh(rctx, cfg, http.DefaultClient, rt)
	cancel()
	if errors.Is(err, auth.ErrInvalidGrant) {
		return "", 0, errors.Join(err, s.ifCurrent(acc.ID, ver, func() error {
			return s.setStatus(ctx, acc.ID, "reauth_required", "token_revoked")
		}))
	}
	if err != nil {
		return "", 0, err
	}
	if g.RefreshToken != "" {
		if err := keychainSet(s.keyringUser(acc.ID), g.RefreshToken); err != nil {
			err = fmt.Errorf("save rotated refresh token: %w", err)
			return "", 0, errors.Join(err, s.setStatus(ctx, acc.ID, "error", "keychain_unavailable"))
		}
	}
	return g.AccessToken, s.cacheToken(acc.ID, g), nil
}

// ErrWrongAccount: the OAuth sign-in was for another address than the account's.
var ErrWrongAccount = errors.New("signed in with a different account")

// ErrProviderMismatch: AccountInput.Provider names another provider than the
// one the OAuth flow signed in with.
var ErrProviderMismatch = errors.New("signed in with a different provider")

// oauthToken is an access token held in memory only.
type oauthToken struct {
	access string
	expiry time.Time
	ver    uint64 // its credential version
}

var serializeKeyring sync.Once

// SerializeKeyringForTest makes keychain calls one at a time. Tests only:
// go-keyring's mock (keyring.MockInit) is not safe for concurrent use, and
// sync workers read credentials while a test writes them. Call it before any
// Service starts; later calls do nothing.
func SerializeKeyringForTest() {
	serializeKeyring.Do(func() {
		var mu sync.Mutex
		get, set, del := keyringGet, keyringSet, keyringDelete
		keyringGet = func(svc, user string) (string, error) { mu.Lock(); defer mu.Unlock(); return get(svc, user) }
		keyringSet = func(svc, user, v string) error { mu.Lock(); defer mu.Unlock(); return set(svc, user, v) }
		keyringDelete = func(svc, user string) error { mu.Lock(); defer mu.Unlock(); return del(svc, user) }
	})
}
