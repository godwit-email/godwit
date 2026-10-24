package core

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/internal/testmail"
	"github.com/godwit-email/godwit/core/internal/testoauth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

// oauthAccEnv is a Service with mail servers that accept the access tokens a
// testoauth server issues, standing in for "outlook".
type oauthAccEnv struct {
	*sendEnv
	oa     *testoauth.Server
	smtpAt string
	parked chan struct{}
	ch     <-chan events.Event
	smtpNo atomic.Value // string: an access token the SMTP server alone refuses
}

func newOAuthEnv(t *testing.T, autoSync bool) *oauthAccEnv {
	t.Helper()
	oa := testoauth.Start(t)
	setFor(t, &oauthConfig, func(string) (auth.OAuthConfig, error) {
		return oa.Config("openid", "email", "offline_access"), nil
	})
	parked := make(chan struct{}, 64)
	setFor(t, &onParked, func(int64) {
		select {
		case parked <- struct{}{}:
		default:
		}
	})
	setFor(t, &backoffMin, 20*time.Millisecond)
	setFor(t, &backoffMax, 200*time.Millisecond)
	keyring.MockInit()
	clock := useTestClock(t)
	dir := t.TempDir()
	s, err := New(bg, Options{DataDir: dir, AutoSync: autoSync, ImageClient: loopbackImageClient()})
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	ch, unsub := s.Subscribe()
	t.Cleanup(unsub)
	e := &oauthAccEnv{oa: oa, parked: parked, ch: ch}
	e.smtpNo.Store("")
	im := testmail.StartIMAP(t, testmail.WithXOAuth2Func(oa.ValidAccess))
	smtpAddr, in := testmail.StartSMTP(t, testmail.WithXOAuth2Func(func(tok string) bool {
		return oa.ValidAccess(tok) && tok != e.smtpNo.Load()
	}))
	e.sendEnv, e.smtpAt = &sendEnv{syncEnv: &syncEnv{s: s, im: im, dir: dir}, smtp: in, clock: clock}, smtpAddr
	return e
}

// signIn runs a flow to the end as email and returns its ID.
func (e *oauthAccEnv) signIn(t *testing.T, email string) string {
	t.Helper()
	id, authURL, err := e.s.BeginOAuth(bg, "outlook", email)
	require.NoError(t, err)
	ch := await(e.s, id)
	e.oa.Browse(t, authURL, email)
	require.NoError(t, recvAwait(t, ch).err)
	return id
}

func (e *oauthAccEnv) input(t *testing.T, email, flowID string) AccountInput {
	in := internalInput(t, e.im.Addr, e.smtpAt, email, true)
	in.Username, in.Password, in.OAuthFlowID, in.Provider = "", "", flowID, "outlook"
	return in
}

// add signs in as alice and adds the account.
func (e *oauthAccEnv) add(t *testing.T) {
	t.Helper()
	acc, err := e.s.AddAccount(bg, e.input(t, "alice@example.com", e.signIn(t, "alice@example.com")))
	require.NoError(t, err)
	e.acc = acc
}

func (e *oauthAccEnv) keychain(t *testing.T) string {
	t.Helper()
	v, err := keyring.Get(keyringService, e.s.keyringUser(e.acc.ID))
	require.NoError(t, err)
	return v
}

// cached returns the account's in-memory access token.
func (e *oauthAccEnv) cached() (oauthToken, bool) {
	e.s.tokMu.Lock()
	defer e.s.tokMu.Unlock()
	tok, ok := e.s.tokens[e.acc.ID]
	return tok, ok
}

// expire makes the cached access token look expired; the server still accepts it.
func (e *oauthAccEnv) expire(t *testing.T) {
	t.Helper()
	e.s.tokMu.Lock()
	defer e.s.tokMu.Unlock()
	tok, ok := e.s.tokens[e.acc.ID]
	require.True(t, ok, "no cached access token")
	tok.expiry = time.Now()
	e.s.tokens[e.acc.ID] = tok
}

func TestAddOAuthAccount(t *testing.T) {
	e := newOAuthEnv(t, false)
	flow := e.signIn(t, "alice@example.com")
	e.s.oauthMu.Lock()
	g := e.s.grants[flow]
	e.s.oauthMu.Unlock()
	require.NotEmpty(t, g.RefreshToken)

	in := e.input(t, "Alice@Example.com", flow) // the address matches whatever its case
	r, err := e.s.CheckAccount(bg, in)
	require.NoError(t, err)
	require.True(t, r.OK(), "%+v", r.Steps)
	acc, err := e.s.AddAccount(bg, in) // the check did not use the grant up
	require.NoError(t, err)
	e.acc = acc
	assert.Equal(t, "oauth", acc.AuthType)
	assert.Equal(t, "alice@example.com", acc.Username)
	assert.Equal(t, "outlook", acc.Provider)
	_, err = e.s.AddAccount(bg, in)
	assert.ErrorIs(t, err, ErrUnknownFlow, "a grant adds one account")

	rt := e.keychain(t)
	assert.Equal(t, g.RefreshToken, rt, "the keychain holds the refresh token only")
	assert.Less(t, len(rt), 2560)
	for _, row := range e.rows(t, "SELECT * FROM accounts") {
		for _, v := range row {
			if s, ok := v.(string); ok {
				assert.NotContains(t, s, rt)
				assert.NotContains(t, s, g.AccessToken)
			}
		}
	}

	e.put(t, "INBOX", msg("o@x"))
	logins := e.im.Logins()
	e.sync(t)
	assert.Greater(t, e.im.Logins(), logins)
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM messages WHERE message_id = 'o@x'"))
	assert.Equal(t, 1, e.oa.TokenRequests(), "the grant's access token is used; no refresh")

	require.NoError(t, e.s.RemoveAccount(bg, acc.ID))
	_, ok := e.cached()
	assert.False(t, ok, "RemoveAccount drops the access token")
}

func TestAddOAuthAccountEmailMismatch(t *testing.T) {
	e := newOAuthEnv(t, false)
	flow := e.signIn(t, "bob@example.com")
	_, err := e.s.AddAccount(bg, e.input(t, "alice@example.com", flow))
	require.ErrorIs(t, err, ErrWrongAccount)
	assert.Zero(t, e.im.Logins())
	accs, err := e.s.ListAccounts(bg)
	require.NoError(t, err)
	assert.Empty(t, accs)
	_, err = keyring.Get(keyringService, e.s.keyringUser(1))
	assert.ErrorIs(t, err, keyring.ErrNotFound)
}

func TestRefreshIsSingleFlight(t *testing.T) {
	e := newOAuthEnv(t, false)
	e.add(t)
	e.put(t, "INBOX", withAttachments("a@x", "files", 1))
	e.sync(t)
	att := e.rows(t, "SELECT id FROM attachments")[0][0].(int64)
	e.expire(t)
	n := e.oa.TokenRequests()
	e.s.tokMu.Lock()
	sem := e.s.refreshing[e.acc.ID]
	e.s.tokMu.Unlock()
	sem <- struct{}{} // hold the refresh until all ten are waiting for it

	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := range 10 {
		wg.Go(func() {
			switch i % 3 {
			case 0:
				errs <- e.s.SyncNow(bg, e.acc.ID)
			case 1:
				_, _, err := e.s.GetAttachment(bg, att)
				errs <- err
			default: // like the worker's IDLE dial, which does not take the sync lock
				_, _, _, err := e.s.credentials(bg, e.acc, "")
				errs <- err
			}
		})
	}
	time.Sleep(200 * time.Millisecond)
	<-sem
	wg.Wait()
	close(errs)
	for err := range errs {
		assert.NoError(t, err)
	}
	assert.Equal(t, n+1, e.oa.TokenRequests())
}

func TestRefreshRotationPersisted(t *testing.T) {
	e := newOAuthEnv(t, false)
	e.add(t)
	e.oa.RotateRefresh(true)
	first := e.keychain(t)
	e.expire(t)
	e.sync(t)
	second := e.keychain(t)
	assert.NotEqual(t, first, second, "the rotated refresh token is saved")

	var fail atomic.Bool
	setFor(t, &keyringSet, func(svc, user, pw string) error {
		if fail.Load() && strings.HasSuffix(user, "/account:1") {
			return errors.New("keychain is locked")
		}
		return keyring.Set(svc, user, pw)
	})
	fail.Store(true)
	e.expire(t)
	n := e.oa.TokenRequests()
	err := e.s.SyncNow(bg, e.acc.ID)
	require.ErrorIs(t, err, ErrKeychainUnavailable)
	assert.Equal(t, n+1, e.oa.TokenRequests(), "the server rotated")
	assert.Equal(t, second, e.keychain(t), "the old refresh token is kept")
	tok, _ := e.cached()
	assert.False(t, tok.fresh(), "the new access token is not cached")

	fail.Store(false)
	e.sync(t) // refreshes with the old refresh token
	assert.NotEqual(t, second, e.keychain(t))
	a, err := e.s.account(bg, e.acc.ID)
	require.NoError(t, err)
	assert.Equal(t, "ok", a.Status)
}

func TestRevokedTokenNeedsReauth(t *testing.T) {
	e := newOAuthEnv(t, true)
	e.add(t)
	waitEvent(t, e.ch, events.SyncFinished, 5*time.Second)
	e.oa.RevokeAll()
	e.s.wakeWorker(e.acc.ID)
	waitSignal(t, e.parked, "worker parking", 5*time.Second)
	a, err := e.s.account(bg, e.acc.ID)
	require.NoError(t, err)
	assert.Equal(t, "reauth_required", a.Status)
	assert.Equal(t, "token_revoked", a.StatusDetail)
	_, ok := e.cached()
	assert.False(t, ok, "the access token is dropped")

	n := e.oa.TokenRequests()
	e.s.wakeWorker(e.acc.ID)
	time.Sleep(500 * time.Millisecond) // backoff alone would retry several times
	assert.Equal(t, n, e.oa.TokenRequests(), "no refresh while parked")
	assert.ErrorIs(t, e.s.SyncNow(bg, e.acc.ID), auth.ErrInvalidGrant)
}

func TestRejectedAccessTokenRefreshesOnce(t *testing.T) {
	e := newOAuthEnv(t, false)
	e.add(t)
	e.oa.RevokeAccess() // well before its expiry
	n := e.oa.TokenRequests()
	e.put(t, "INBOX", msg("r@x"))
	e.sync(t)
	assert.Equal(t, n+1, e.oa.TokenRequests())
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM messages WHERE message_id = 'r@x'"))
	a, err := e.s.account(bg, e.acc.ID)
	require.NoError(t, err)
	assert.Equal(t, "ok", a.Status)

	tok, _ := e.cached()
	e.smtpNo.Store(tok.access) // SMTP alone refuses it: the outbox refreshes and sends
	e.sendNew(t)
	e.sync(t)
	assert.Len(t, e.smtp.Messages(), 1)
	assert.Equal(t, n+2, e.oa.TokenRequests())
}

func TestSendWithOAuth(t *testing.T) {
	e := newOAuthEnv(t, true)
	e.add(t)
	waitEvent(t, e.ch, events.SyncFinished, 5*time.Second)
	e.sendNew(t)
	require.Eventually(t, func() bool { return len(e.smtp.Messages()) == 1 }, 5*time.Second, 20*time.Millisecond)
	assert.Equal(t, []string{"bob@example.com"}, e.smtp.Messages()[0].To)
}

// Two callers were refused the same token; the second must not refresh again.
func TestForcedRefreshReusesNewerToken(t *testing.T) {
	e := newOAuthEnv(t, false)
	e.add(t)
	t0, _ := e.cached()
	n := e.oa.TokenRequests()
	_, t1, _, err := e.s.credentials(bg, e.acc, t0.access) // caller B refreshes
	require.NoError(t, err)
	require.NotEqual(t, t0.access, t1)
	_, got, _, err := e.s.credentials(bg, e.acc, t0.access) // caller A, refused T0 earlier
	require.NoError(t, err)
	assert.Equal(t, t1, got)
	assert.Equal(t, n+1, e.oa.TokenRequests())
}

func TestAddOAuthAccountProviderMismatch(t *testing.T) {
	e := newOAuthEnv(t, false)
	flow := e.signIn(t, "alice@example.com") // begun for outlook
	in := e.input(t, "alice@example.com", flow)
	in.Provider = "gmail"
	_, err := e.s.AddAccount(bg, in)
	require.ErrorIs(t, err, ErrProviderMismatch)
	accs, err := e.s.ListAccounts(bg)
	require.NoError(t, err)
	assert.Empty(t, accs)
	_, err = keyring.Get(keyringService, e.s.keyringUser(1))
	assert.ErrorIs(t, err, keyring.ErrNotFound)

	in.Provider = "" // the grant is still there; the flow names the provider
	acc, err := e.s.AddAccount(bg, in)
	require.NoError(t, err)
	assert.Equal(t, "outlook", acc.Provider)
}

func TestZeroExpiryLasts30Minutes(t *testing.T) {
	e := newOAuthEnv(t, false)
	e.add(t)
	e.expire(t)
	e.oa.RespondNext(200, `{"access_token":"no-expiry","token_type":"Bearer"}`)
	_, tok, _, err := e.s.credentials(bg, e.acc, "")
	require.NoError(t, err)
	require.Equal(t, "no-expiry", tok)
	c, _ := e.cached()
	assert.WithinDuration(t, time.Now().Add(30*time.Minute), c.expiry, time.Minute)
}

// The 30 minutes for a token without expires_in run from its receipt, not from
// when AddAccount caches it.
func TestZeroExpiryCountsFromReceipt(t *testing.T) {
	e := newOAuthEnv(t, false)
	e.oa.OmitExpiry(true)
	before := time.Now()
	flow := e.signIn(t, "alice@example.com")
	after := time.Now()
	time.Sleep(500 * time.Millisecond)
	acc, err := e.s.AddAccount(bg, e.input(t, "alice@example.com", flow))
	require.NoError(t, err)
	e.acc = acc
	c, ok := e.cached()
	require.True(t, ok)
	assert.False(t, c.expiry.Before(before.Add(30*time.Minute)), "expiry %v", c.expiry)
	assert.False(t, c.expiry.After(after.Add(30*time.Minute)), "expiry %v is later than receipt + 30m", c.expiry)
}
