package auth_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/core/internal/testoauth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var ctx = context.Background()

type result struct {
	g   auth.Grant
	err error
}

func startFlow(t *testing.T, srv *testoauth.Server) *auth.Flow {
	t.Helper()
	f, err := auth.StartFlow(srv.Config("openid", "email"), "alice@example.com", http.DefaultClient)
	require.NoError(t, err)
	t.Cleanup(f.Close)
	return f
}

func wait(ctx context.Context, f *auth.Flow) <-chan result {
	ch := make(chan result, 1)
	go func() { g, err := f.Wait(ctx); ch <- result{g, err} }()
	return ch
}

func recv(t *testing.T, ch <-chan result) result {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("Wait did not return")
		return result{}
	}
}

// redirectURI is the flow's loopback redirect as the authorization URL names it.
func redirectURI(t *testing.T, f *auth.Flow) *url.URL {
	t.Helper()
	u, err := url.Parse(f.AuthURL())
	require.NoError(t, err)
	r, err := url.Parse(u.Query().Get("redirect_uri"))
	require.NoError(t, err)
	return r
}

// requirePortClosed fails unless the flow's loopback port stops accepting
// connections within a short window. Any dial error counts as closed: Windows
// answers a closed port slowly, so the dial may time out instead of being refused.
func requirePortClosed(t *testing.T, f *auth.Flow) {
	t.Helper()
	addr := net.JoinHostPort("127.0.0.1", redirectURI(t, f).Port())
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			return
		}
		c.Close()
	}
	t.Fatalf("%s still accepts connections", addr)
}

func TestFlowHappyPath(t *testing.T) {
	srv := testoauth.Start(t)
	f := startFlow(t, srv)
	q := must(url.Parse(f.AuthURL())).Query()
	assert.Equal(t, "select_account", q.Get("prompt"))
	assert.Equal(t, "alice@example.com", q.Get("login_hint"))
	assert.Equal(t, "S256", q.Get("code_challenge_method"))
	assert.Len(t, q.Get("state"), 43, "32 random bytes, base64url")
	ru := redirectURI(t, f)
	assert.Equal(t, "http", ru.Scheme)
	assert.Equal(t, "localhost", ru.Hostname())
	assert.Equal(t, "/callback", ru.Path)

	ch := wait(ctx, f)
	cb := srv.Browse(t, f.AuthURL(), "Alice@Example.com")
	assert.Equal(t, http.StatusOK, cb.Status)
	r := recv(t, ch)
	require.NoError(t, r.err)
	assert.Equal(t, "alice@example.com", r.g.Email)
	assert.True(t, srv.ValidAccess(r.g.AccessToken))
	assert.NotEmpty(t, r.g.RefreshToken)
	assert.WithinDuration(t, time.Now().Add(time.Hour), r.g.Expiry, time.Minute)
	assert.True(t, srv.S256Requested(), "authorization request carries code_challenge_method=S256")
	assert.True(t, srv.VerifierChecked(), "token request carries the matching code_verifier")
	requirePortClosed(t, f)
}

func TestFlowWrongStateIgnored(t *testing.T) {
	srv := testoauth.Start(t)
	f := startFlow(t, srv)
	ch := wait(ctx, f)
	ru := redirectURI(t, f)
	ru.RawQuery = url.Values{"state": {"forged-state"}, "code": {"forged-code"}}.Encode()
	bad := testoauth.Get(t, ru.String())
	assert.Equal(t, http.StatusBadRequest, bad.Status)
	assert.NotContains(t, bad.Body, "forged")
	select {
	case r := <-ch:
		t.Fatalf("Wait returned after a forged callback: %v", r.err)
	case <-time.After(100 * time.Millisecond):
	}
	assert.Zero(t, srv.TokenRequests())

	srv.Browse(t, f.AuthURL(), "alice@example.com")
	r := recv(t, ch)
	require.NoError(t, r.err)
	assert.Equal(t, "alice@example.com", r.g.Email)
}

func TestFlowAccessDenied(t *testing.T) {
	srv := testoauth.Start(t)
	srv.Deny(true)
	f := startFlow(t, srv)
	ch := wait(ctx, f)
	cb := srv.Browse(t, f.AuthURL(), "alice@example.com")
	assert.Equal(t, http.StatusBadRequest, cb.Status)
	r := recv(t, ch)
	require.Error(t, r.err)
	require.ErrorIs(t, r.err, auth.ErrAccessDenied)
	assert.Zero(t, srv.TokenRequests())
	requirePortClosed(t, f)
}

func TestFlowExpires(t *testing.T) {
	old := auth.FlowTimeout
	auth.FlowTimeout = 100 * time.Millisecond
	t.Cleanup(func() { auth.FlowTimeout = old })
	f := startFlow(t, testoauth.Start(t))
	r := recv(t, wait(ctx, f))
	require.ErrorIs(t, r.err, auth.ErrFlowExpired)
	requirePortClosed(t, f)
}

func TestFlowExpiresWithoutWait(t *testing.T) {
	old := auth.FlowTimeout
	auth.FlowTimeout = 100 * time.Millisecond
	t.Cleanup(func() { auth.FlowTimeout = old })
	f := startFlow(t, testoauth.Start(t))
	time.Sleep(150 * time.Millisecond)
	requirePortClosed(t, f)
	_, err := f.Wait(ctx)
	require.ErrorIs(t, err, auth.ErrFlowExpired)
}

func TestFlowCancelled(t *testing.T) {
	f := startFlow(t, testoauth.Start(t))
	c, cancel := context.WithCancel(ctx)
	ch := wait(c, f)
	time.Sleep(50 * time.Millisecond)
	cancel()
	require.ErrorIs(t, recv(t, ch).err, context.Canceled)
	requirePortClosed(t, f)
}

func TestFlowClose(t *testing.T) {
	f := startFlow(t, testoauth.Start(t))
	ch := wait(ctx, f)
	f.Close()
	require.Error(t, recv(t, ch).err)
	requirePortClosed(t, f)
}

func TestCallbackPageEchoesNothing(t *testing.T) {
	srv := testoauth.Start(t)
	f := startFlow(t, srv)
	ch := wait(ctx, f)
	cb := srv.Browse(t, f.AuthURL(), "alice@example.com")
	require.NoError(t, recv(t, ch).err)
	q := must(url.Parse(cb.URL)).Query()
	require.NotEmpty(t, q.Get("code"))
	assert.NotContains(t, cb.Body, q.Get("code"))
	assert.NotContains(t, cb.Body, q.Get("state"))
	assert.Contains(t, cb.Body, "登录完成")

	srv.Deny(true)
	f = startFlow(t, srv)
	ch = wait(ctx, f)
	cb = srv.Browse(t, f.AuthURL(), "alice@example.com")
	require.Error(t, recv(t, ch).err)
	assert.NotContains(t, cb.Body, "access_denied")
	assert.NotContains(t, cb.Body, must(url.Parse(cb.URL)).Query().Get("state"))
	assert.Contains(t, cb.Body, "登录没有完成")
}

func TestFlowNotConfigured(t *testing.T) {
	cfg := testoauth.Start(t).Config()
	cfg.ClientID = ""
	_, err := auth.StartFlow(cfg, "", nil)
	require.ErrorIs(t, err, auth.ErrOAuthNotConfigured)
}

func TestFlowErrorsCarryNoSecrets(t *testing.T) {
	srv := testoauth.Start(t)
	f := startFlow(t, srv)
	ch := wait(ctx, f)
	u, err := url.Parse(f.AuthURL())
	require.NoError(t, err)
	// Exchange a code the server never issued: the token endpoint answers invalid_grant.
	ru := redirectURI(t, f)
	ru.RawQuery = url.Values{"state": {u.Query().Get("state")}, "code": {"never-issued-code"}}.Encode()
	cb := testoauth.Get(t, ru.String())
	assert.Equal(t, http.StatusBadRequest, cb.Status)
	r := recv(t, ch)
	require.ErrorIs(t, r.err, auth.ErrInvalidGrant)
	assert.NotContains(t, r.err.Error(), "never-issued-code")
}

func TestRefresh(t *testing.T) {
	srv := testoauth.Start(t)
	f := startFlow(t, srv)
	ch := wait(ctx, f)
	srv.Browse(t, f.AuthURL(), "alice@example.com")
	first := recv(t, ch).g
	cfg := srv.Config("openid")

	g, err := auth.Refresh(ctx, cfg, http.DefaultClient, first.RefreshToken)
	require.NoError(t, err)
	assert.True(t, srv.ValidAccess(g.AccessToken))
	assert.NotEqual(t, first.AccessToken, g.AccessToken)
	assert.Empty(t, g.RefreshToken, "not rotated")
	assert.Equal(t, "alice@example.com", g.Email)

	srv.RotateRefresh(true)
	g, err = auth.Refresh(ctx, cfg, nil, first.RefreshToken)
	require.NoError(t, err)
	require.NotEmpty(t, g.RefreshToken)
	assert.NotEqual(t, first.RefreshToken, g.RefreshToken)

	srv.RevokeAll()
	_, err = auth.Refresh(ctx, cfg, http.DefaultClient, g.RefreshToken)
	require.ErrorIs(t, err, auth.ErrInvalidGrant)
	assert.NotContains(t, err.Error(), g.RefreshToken)
}

func TestRefreshNetworkError(t *testing.T) {
	cfg := testoauth.Start(t).Config()
	cfg.TokenURL = "http://127.0.0.1:1/token"
	_, err := auth.Refresh(ctx, cfg, nil, "rt-secret")
	require.Error(t, err)
	assert.False(t, errors.Is(err, auth.ErrInvalidGrant))
	assert.NotContains(t, err.Error(), "rt-secret")
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func TestTokenErrorsCarryNoSecrets(t *testing.T) {
	srv := testoauth.Start(t)
	cfg := srv.Config()
	for _, c := range []struct {
		status int
		body   string
	}{
		{200, `{"access_token":"at","token_type":"Bearer","expires_in":"SECRET-expires"}`},
		{400, `{"error":"weird_SECRET_code","error_description":"SECRET-description"}`},
		{400, `{"error":"invalid_client","error_description":"SECRET-description"}`},
		{500, `SECRET-not-json`},
	} {
		srv.RespondNext(c.status, c.body)
		_, err := auth.Refresh(ctx, cfg, nil, "rt")
		require.Error(t, err, c.body)
		assert.NotContains(t, err.Error(), "SECRET", c.body)
	}
	c, cancel := context.WithCancel(ctx)
	cancel()
	_, err := auth.Refresh(c, cfg, nil, "rt")
	require.ErrorIs(t, err, context.Canceled)
}

func TestCallbackClosesWithoutWait(t *testing.T) {
	srv := testoauth.Start(t)
	f := startFlow(t, srv)
	srv.Browse(t, f.AuthURL(), "alice@example.com")
	requirePortClosed(t, f)
	g, err := f.Wait(ctx)
	require.NoError(t, err)
	assert.Equal(t, "alice@example.com", g.Email)
}

func TestCallbackRejectsHEAD(t *testing.T) {
	srv := testoauth.Start(t)
	f := startFlow(t, srv)
	ch := wait(ctx, f)
	ru := redirectURI(t, f)
	ru.RawQuery = url.Values{"state": {must(url.Parse(f.AuthURL())).Query().Get("state")}, "code": {"some-code"}}.Encode()
	resp, err := http.Head(ru.String())
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
	assert.Zero(t, srv.TokenRequests())
	srv.Browse(t, f.AuthURL(), "alice@example.com")
	require.NoError(t, recv(t, ch).err)
}

func TestFlowDynamicClient(t *testing.T) {
	srv := testoauth.Start(t)
	srv.Dynamic(true)
	srv.Require("resource", "https://api.example/v1")
	srv.GrantScope("openid email plan.use")
	cfg := srv.Config("openid", "email")
	cfg.ClientID, cfg.DynamicClient, cfg.NoPrompt, cfg.Nonce = testoauth.DynamicClientID, true, true, true
	cfg.RedirectHost, cfg.RedirectPath = "127.0.0.1", "/auth/callback"
	cfg.AuthParams = map[string]string{"resource": "https://api.example/v1", "agent_name_hint": "Godwit"}
	cfg.TokenParams = map[string]string{"resource": "https://api.example/v1"}
	cfg.RequireScope = "plan.use"
	f, err := auth.StartFlow(cfg, "", http.DefaultClient)
	require.NoError(t, err)
	t.Cleanup(f.Close)
	u, _ := url.Parse(f.AuthURL())
	q := u.Query()
	assert.Equal(t, "Godwit", q.Get("agent_name_hint"))
	assert.NotEmpty(t, q.Get("nonce"))
	assert.Empty(t, q.Get("prompt"))
	r := redirectURI(t, f)
	assert.Equal(t, "127.0.0.1", r.Hostname())
	assert.Equal(t, "/auth/callback", r.Path)
	ch := wait(ctx, f)
	srv.Browse(t, f.AuthURL(), "alice@example.com")
	res := recv(t, ch)
	require.NoError(t, res.err)
	assert.Equal(t, testoauth.IssuedClientID, res.g.ClientID)
	assert.Equal(t, "openid email plan.use", res.g.Scope)
	assert.Equal(t, "alice@example.com", res.g.Email)
}

func TestFlowRequiresScope(t *testing.T) {
	srv := testoauth.Start(t)
	srv.GrantScope("openid email")
	cfg := srv.Config("openid", "email")
	cfg.RequireScope = "plan.use"
	f, err := auth.StartFlow(cfg, "", http.DefaultClient)
	require.NoError(t, err)
	t.Cleanup(f.Close)
	ch := wait(ctx, f)
	srv.Browse(t, f.AuthURL(), "alice@example.com")
	assert.ErrorIs(t, recv(t, ch).err, auth.ErrScopeMissing)
}

func TestFlowDynamicClientMissingID(t *testing.T) {
	srv := testoauth.Start(t) // Dynamic off: the callback carries no client_id
	cfg := srv.Config("openid")
	cfg.DynamicClient = true
	f, err := auth.StartFlow(cfg, "", http.DefaultClient)
	require.NoError(t, err)
	t.Cleanup(f.Close)
	ch := wait(ctx, f)
	srv.Browse(t, f.AuthURL(), "alice@example.com")
	require.Error(t, recv(t, ch).err)
}
