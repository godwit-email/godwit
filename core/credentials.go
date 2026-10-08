package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

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

// credentials reads the account's secret from the keychain and records the
// outcome in its status: a missing entry is reauth_required (the worker parks),
// an unusable keychain is error (the worker keeps retrying), and a successful
// read clears error. Password accounts only; forceRefresh is for OAuth.
func (s *Service) credentials(ctx context.Context, acc Account, forceRefresh bool) (password, accessToken string, err error) {
	pw, err := keychainGet(s.keyringUser(acc.ID))
	switch {
	case errors.Is(err, keyring.ErrNotFound):
		err = fmt.Errorf("password not found in keychain: %w", err)
		return "", "", errors.Join(err, s.setStatus(ctx, acc.ID, "reauth_required", "credentials_missing"))
	case err != nil:
		return "", "", errors.Join(err, s.setStatus(ctx, acc.ID, "error", "keychain_unavailable"))
	}
	if _, err := s.db.Execute(ctx, "UPDATE accounts SET status = 'ok', status_detail = '' WHERE id = ? AND status = 'error'", acc.ID); err != nil {
		return "", "", err
	}
	return pw, "", nil
}
