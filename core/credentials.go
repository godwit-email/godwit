package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"time"

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

func keychainGet(user string) (string, error) {
	v, err := keyringGet(keyringService, user)
	return v, keychainErr(err)
}

func keychainSet(user, secret string) error {
	return keychainErr(keyringSet(keyringService, user, secret))
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
// token the server refused, or "": a token other than it is returned.
func (s *Service) credentials(ctx context.Context, acc Account, rejected string) (password, accessToken string, err error) {
	if acc.AuthType == "oauth" {
		tok, err := s.accessToken(ctx, acc, rejected)
		return "", tok, err
	}
	pw, err := s.secret(ctx, acc)
	return pw, "", err
}

// secret reads the account's keychain entry: a password or a refresh token.
func (s *Service) secret(ctx context.Context, acc Account) (string, error) {
	v, err := keychainGet(s.keyringUser(acc.ID))
	switch {
	case errors.Is(err, keyring.ErrNotFound):
		err = fmt.Errorf("credentials not found in keychain: %w", err)
		return "", errors.Join(err, s.setStatus(ctx, acc.ID, "reauth_required", "credentials_missing"))
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
func (s *Service) accessToken(ctx context.Context, acc Account, rejected string) (string, error) {
	s.tokMu.Lock()
	seen := s.tokens[acc.ID]
	s.tokMu.Unlock()
	sem := s.refreshLock(acc.ID)
	if seen.fresh() && seen.access != rejected {
		return seen.access, nil
	}
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	s.tokMu.Lock()
	cur := s.tokens[acc.ID]
	if cur.fresh() && cur.access != rejected {
		s.tokMu.Unlock()
		return cur.access, nil // refreshed by someone else
	}
	delete(s.tokens, acc.ID)
	s.tokMu.Unlock()

	rt, err := s.secret(ctx, acc)
	if err != nil {
		return "", err
	}
	cfg, err := oauthConfig(acc.Provider)
	if err != nil {
		return "", err
	}
	g, err := auth.Refresh(ctx, cfg, http.DefaultClient, rt)
	if errors.Is(err, auth.ErrInvalidGrant) {
		return "", errors.Join(err, s.setStatus(ctx, acc.ID, "reauth_required", "token_revoked"))
	}
	if err != nil {
		return "", err
	}
	if g.RefreshToken != "" {
		if err := keychainSet(s.keyringUser(acc.ID), g.RefreshToken); err != nil {
			err = fmt.Errorf("save rotated refresh token: %w", err)
			return "", errors.Join(err, s.setStatus(ctx, acc.ID, "error", "keychain_unavailable"))
		}
	}
	s.tokMu.Lock()
	if s.tokens == nil {
		s.tokens = map[int64]oauthToken{}
	}
	s.tokens[acc.ID] = oauthToken{g.AccessToken, g.Expiry}
	s.tokMu.Unlock()
	return g.AccessToken, nil
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
}
