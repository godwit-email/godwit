package core

import (
	"context"
	"net"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/core/internal/testoauth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// oauthEnv is a Service whose "outlook" provider signs in at a testoauth server.
func oauthEnv(t *testing.T) (*Service, *testoauth.Server) {
	t.Helper()
	srv := testoauth.Start(t)
	setFor(t, &oauthConfig, func(id string) (auth.OAuthConfig, error) {
		require.Equal(t, "outlook", id)
		return srv.Config("openid", "email", "offline_access"), nil
	})
	s, err := New(context.Background(), Options{DataDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s, srv
}

type awaited struct {
	email string
	err   error
}

func await(s *Service, flowID string) <-chan awaited {
	ch := make(chan awaited, 1)
	go func() { e, err := s.AwaitOAuth(context.Background(), flowID); ch <- awaited{e, err} }()
	return ch
}

func recvAwait(t *testing.T, ch <-chan awaited) awaited {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("AwaitOAuth did not return")
		return awaited{}
	}
}

// requireLoopbackClosed fails unless authURL's loopback port stops accepting
// connections soon. Any dial error counts: Windows may time out instead of refusing.
func requireLoopbackClosed(t *testing.T, authURL string) {
	t.Helper()
	u, err := url.Parse(authURL)
	require.NoError(t, err)
	r, err := url.Parse(u.Query().Get("redirect_uri"))
	require.NoError(t, err)
	addr := net.JoinHostPort("127.0.0.1", r.Port())
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			return
		}
		c.Close()
	}
	t.Fatalf("%s still accepts connections", addr)
}

func TestOAuthServiceFlow(t *testing.T) {
	s, srv := oauthEnv(t)
	id, authURL, err := s.BeginOAuth(context.Background(), "outlook", "alice@outlook.com")
	require.NoError(t, err)
	require.NotEmpty(t, id)
	assert.Equal(t, "alice@outlook.com", must(url.Parse(authURL)).Query().Get("login_hint"))
	ch := await(s, id)
	srv.Browse(t, authURL, "Alice@Outlook.com")
	r := recvAwait(t, ch)
	require.NoError(t, r.err)
	assert.Equal(t, "alice@outlook.com", r.email)
	requireLoopbackClosed(t, authURL)

	g, ok := s.takeGrant(id)
	require.True(t, ok)
	assert.Equal(t, "alice@outlook.com", g.Email)
	assert.True(t, srv.ValidAccess(g.AccessToken))
	assert.NotEmpty(t, g.RefreshToken)
	_, ok = s.takeGrant(id)
	assert.False(t, ok, "a grant is taken once")
	_, err = s.AwaitOAuth(context.Background(), id)
	assert.ErrorIs(t, err, ErrUnknownFlow, "the flow is gone once awaited")
}

func TestOAuthGrantDiscarded(t *testing.T) {
	setFor(t, &grantTTL, 50*time.Millisecond)
	s, srv := oauthEnv(t)
	id, authURL, err := s.BeginOAuth(context.Background(), "outlook", "")
	require.NoError(t, err)
	ch := await(s, id)
	srv.Browse(t, authURL, "alice@outlook.com")
	require.NoError(t, recvAwait(t, ch).err)
	require.Eventually(t, func() bool {
		s.oauthMu.Lock()
		defer s.oauthMu.Unlock()
		return len(s.grants) == 0
	}, 2*time.Second, 10*time.Millisecond)
	_, ok := s.takeGrant(id)
	assert.False(t, ok)
}

func TestOAuthFlowExpires(t *testing.T) {
	setFor(t, &auth.FlowTimeout, 100*time.Millisecond)
	s, _ := oauthEnv(t)
	id, authURL, err := s.BeginOAuth(context.Background(), "outlook", "")
	require.NoError(t, err)
	require.ErrorIs(t, recvAwait(t, await(s, id)).err, auth.ErrFlowExpired)
	requireLoopbackClosed(t, authURL)
}

func TestCancelOAuth(t *testing.T) {
	s, srv := oauthEnv(t)
	id, authURL, err := s.BeginOAuth(context.Background(), "outlook", "")
	require.NoError(t, err)
	ch := await(s, id)
	time.Sleep(50 * time.Millisecond)
	s.CancelOAuth(id)
	require.Error(t, recvAwait(t, ch).err)
	requireLoopbackClosed(t, authURL)
	assert.Zero(t, srv.TokenRequests())
	s.CancelOAuth(id) // unknown now: no-op

	// A finished flow's grant is dropped by CancelOAuth too.
	id, authURL, err = s.BeginOAuth(context.Background(), "outlook", "")
	require.NoError(t, err)
	ch = await(s, id)
	srv.Browse(t, authURL, "alice@outlook.com")
	require.NoError(t, recvAwait(t, ch).err)
	s.CancelOAuth(id)
	_, ok := s.takeGrant(id)
	assert.False(t, ok)
}

func TestCloseEndsOAuthFlows(t *testing.T) {
	s, srv := oauthEnv(t)
	waiting, waitingURL, err := s.BeginOAuth(context.Background(), "outlook", "")
	require.NoError(t, err)
	_, idleURL, err := s.BeginOAuth(context.Background(), "outlook", "")
	require.NoError(t, err)
	done, doneURL, err := s.BeginOAuth(context.Background(), "outlook", "")
	require.NoError(t, err)
	dch := await(s, done)
	srv.Browse(t, doneURL, "alice@outlook.com")
	require.NoError(t, recvAwait(t, dch).err)

	ch := await(s, waiting)
	time.Sleep(50 * time.Millisecond)
	require.NoError(t, s.Close())
	require.Error(t, recvAwait(t, ch).err)
	requireLoopbackClosed(t, waitingURL)
	requireLoopbackClosed(t, idleURL)
	_, ok := s.takeGrant(done)
	assert.False(t, ok, "Close drops finished grants")
	_, _, err = s.BeginOAuth(context.Background(), "outlook", "")
	require.ErrorIs(t, err, ErrClosed)
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func TestCancelBetweenWaitAndCommit(t *testing.T) {
	s, srv := oauthEnv(t)
	id, authURL, err := s.BeginOAuth(context.Background(), "outlook", "")
	require.NoError(t, err)
	setFor(t, &afterOAuthWait, func(string) { s.CancelOAuth(id) })
	ch := await(s, id)
	srv.Browse(t, authURL, "alice@outlook.com")
	require.ErrorIs(t, recvAwait(t, ch).err, ErrUnknownFlow)
	_, ok := s.takeGrant(id)
	assert.False(t, ok, "a cancelled flow leaves no grant")
}

func TestConcurrentAwaitOneWins(t *testing.T) {
	s, srv := oauthEnv(t)
	id, authURL, err := s.BeginOAuth(context.Background(), "outlook", "")
	require.NoError(t, err)
	var both sync.WaitGroup
	both.Add(2)
	setFor(t, &afterOAuthWait, func(string) { both.Done(); both.Wait() }) // both Waits returned before either commits
	a, b := await(s, id), await(s, id)
	srv.Browse(t, authURL, "alice@outlook.com")
	ra, rb := recvAwait(t, a), recvAwait(t, b)
	if ra.err != nil {
		ra, rb = rb, ra
	}
	require.NoError(t, ra.err)
	assert.Equal(t, "alice@outlook.com", ra.email)
	require.ErrorIs(t, rb.err, ErrUnknownFlow)
	_, ok := s.takeGrant(id)
	assert.True(t, ok)
}

func TestUnclaimedFlowPurged(t *testing.T) {
	setFor(t, &auth.FlowTimeout, 300*time.Millisecond)
	s, srv := oauthEnv(t)
	id, authURL, err := s.BeginOAuth(context.Background(), "outlook", "")
	require.NoError(t, err)
	srv.Browse(t, authURL, "alice@outlook.com")
	requireLoopbackClosed(t, authURL) // the callback was handled: no Wait needed
	require.Eventually(t, func() bool {
		s.oauthMu.Lock()
		defer s.oauthMu.Unlock()
		return len(s.flows) == 0
	}, 3*time.Second, 10*time.Millisecond)
	_, err = s.AwaitOAuth(context.Background(), id)
	require.ErrorIs(t, err, ErrUnknownFlow)
	_, ok := s.takeGrant(id)
	assert.False(t, ok)
}
