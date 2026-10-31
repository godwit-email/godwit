package core

import (
	"testing"

	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/core/internal/testoauth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

// newOwnClientEnv stands in for Gmail: a provider without a client of
// Godwit's, whose token endpoint wants the user's client secret.
func newOwnClientEnv(t *testing.T) *oauthAccEnv {
	t.Helper()
	e := newOAuthEnv(t, false)
	setFor(t, &oauthConfig, func(string) (auth.OAuthConfig, error) {
		cfg := e.oa.Config("openid", "email")
		cfg.ClientID = ""
		return cfg, nil
	})
	e.oa.Secret("s3cret")
	return e
}

var ownClient = OAuthClient{ID: testoauth.ClientID, Secret: "s3cret"}

func (e *oauthAccEnv) signInWith(t *testing.T, email string, c OAuthClient) string {
	t.Helper()
	id, authURL, err := e.s.BeginOAuth(bg, "outlook", email, c)
	require.NoError(t, err)
	ch := await(e.s, id)
	e.oa.Browse(t, authURL, email)
	require.NoError(t, recvAwait(t, ch).err)
	return id
}

func TestOwnOAuthClient(t *testing.T) {
	e := newOwnClientEnv(t)
	_, _, err := e.s.BeginOAuth(bg, "outlook", "", OAuthClient{})
	require.ErrorIs(t, err, auth.ErrOAuthNotConfigured, "no client of Godwit's to fall back on")

	acc, err := e.s.AddAccount(bg, e.input(t, "alice@example.com", e.signInWith(t, "alice@example.com", ownClient)))
	require.NoError(t, err)
	e.acc = acc
	assert.Equal(t, testoauth.ClientID, acc.OAuthClientID)

	e.expire(t)
	n := e.oa.TokenRequests()
	e.sync(t) // refreshes with the account's client and its secret
	assert.Equal(t, n+1, e.oa.TokenRequests())

	e.oa.RevokeAll()
	require.Error(t, e.s.SyncNow(bg, acc.ID))
	flow := e.signInWith(t, acc.Email, OAuthClient{Account: acc.ID}) // the client it was added with
	require.NoError(t, e.s.Reauthorize(bg, acc.ID, flow))
	st, _ := e.status(t)
	assert.Equal(t, "ok", st)

	require.NoError(t, e.s.RemoveAccount(bg, acc.ID))
	_, err = keyring.Get(keyringService, e.s.oauthClientUser(acc.ID))
	assert.ErrorIs(t, err, keyring.ErrNotFound, "the client secret goes with the account")
}

// A rotated or lost client secret parks the account until it signs in again
// with the new one, which it then refreshes with.
func TestRotatedClientSecret(t *testing.T) {
	e := newOwnClientEnv(t)
	acc, err := e.s.AddAccount(bg, e.input(t, "alice@example.com", e.signInWith(t, "alice@example.com", ownClient)))
	require.NoError(t, err)
	e.acc = acc

	e.oa.Secret("n3w")
	e.expire(t)
	require.Error(t, e.s.SyncNow(bg, acc.ID))
	st, detail := e.status(t)
	assert.Equal(t, "reauth_required", st)
	assert.Equal(t, "client_rejected", detail)

	flow := e.signInWith(t, acc.Email, OAuthClient{ID: testoauth.ClientID, Secret: "n3w"})
	require.NoError(t, e.s.Reauthorize(bg, acc.ID, flow))
	e.expire(t)
	e.sync(t)
	st, _ = e.status(t)
	assert.Equal(t, "ok", st)

	require.NoError(t, keyring.Delete(keyringService, e.s.oauthClientUser(acc.ID)))
	e.expire(t)
	require.Error(t, e.s.SyncNow(bg, acc.ID))
	st, detail = e.status(t)
	assert.Equal(t, "reauth_required", st)
	assert.Equal(t, "credentials_missing", detail)
}

func TestReauthorizeWithGodwitsClient(t *testing.T) {
	e := newOwnClientEnv(t)
	acc, err := e.s.AddAccount(bg, e.input(t, "alice@example.com", e.signInWith(t, "alice@example.com", ownClient)))
	require.NoError(t, err)
	setFor(t, &oauthConfig, func(string) (auth.OAuthConfig, error) {
		return e.oa.Config("openid", "email"), nil
	})
	e.oa.Secret("")
	require.NoError(t, e.s.Reauthorize(bg, acc.ID, e.signInWith(t, acc.Email, OAuthClient{})))
	acc, err = e.s.account(bg, acc.ID)
	require.NoError(t, err)
	assert.Empty(t, acc.OAuthClientID)
	_, err = keyring.Get(keyringService, e.s.oauthClientUser(acc.ID))
	assert.ErrorIs(t, err, keyring.ErrNotFound, "no secret left behind")
}
