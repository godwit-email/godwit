package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/core/internal/testoauth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeviceFlowHappyPath(t *testing.T) {
	t.Parallel()
	srv := testoauth.Start(t)
	srv.Require("referrer", "godwit")
	cfg := srv.DeviceConfig("openid", "email")
	cfg.AuthParams = map[string]string{"referrer": "godwit"}
	f, err := auth.StartDevice(ctx, cfg, http.DefaultClient)
	require.NoError(t, err)
	t.Cleanup(f.Close)
	assert.NotEmpty(t, f.UserCode())
	assert.Contains(t, f.AuthURL(), "user_code="+f.UserCode())
	ch := make(chan result, 1)
	go func() { g, err := f.Wait(ctx); ch <- result{g, err} }()
	srv.Approve("alice@example.com")
	res := recv(t, ch)
	require.NoError(t, res.err)
	assert.Equal(t, "alice@example.com", res.g.Email)
	assert.NotEmpty(t, res.g.RefreshToken)
	assert.True(t, srv.ValidAccess(res.g.AccessToken))
}

func TestDeviceFlowDenied(t *testing.T) {
	t.Parallel()
	srv := testoauth.Start(t)
	f, err := auth.StartDevice(ctx, srv.DeviceConfig("openid", "email"), http.DefaultClient)
	require.NoError(t, err)
	t.Cleanup(f.Close)
	srv.DenyDevice()
	_, err = f.Wait(ctx)
	require.ErrorIs(t, err, auth.ErrAccessDenied)

	srv2 := testoauth.Start(t)
	f2, err := auth.StartDevice(ctx, srv2.DeviceConfig("openid", "email"), nil)
	require.NoError(t, err)
	t.Cleanup(f2.Close)
	srv2.RespondNext(http.StatusBadRequest, `{"error":"authorization_denied","error_description":"SECRET"}`)
	_, err = f2.Wait(ctx)
	require.ErrorIs(t, err, auth.ErrAccessDenied)
	assert.NotContains(t, err.Error(), "SECRET")
}

func TestDeviceFlowExpires(t *testing.T) {
	t.Parallel()
	srv := testoauth.Start(t)
	f, err := auth.StartDevice(ctx, srv.DeviceConfig("openid", "email"), http.DefaultClient)
	require.NoError(t, err)
	t.Cleanup(f.Close)
	srv.DeviceExpired()
	_, err = f.Wait(ctx)
	require.ErrorIs(t, err, auth.ErrFlowExpired)

	expSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/device" {
			w.Write([]byte(`{"device_code":"dc","user_code":"uc","verification_uri":"https://auth.example/verify","verification_uri_complete":"https://auth.example/verify?c=uc","interval":1,"expires_in":1}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"authorization_pending"}`))
	}))
	t.Cleanup(expSrv.Close)
	f2, err := auth.StartDevice(ctx, auth.OAuthConfig{ClientID: "id", DeviceURL: expSrv.URL + "/device", TokenURL: expSrv.URL + "/token"}, nil)
	require.NoError(t, err)
	t.Cleanup(f2.Close)
	assert.Equal(t, "https://auth.example/verify?c=uc", f2.AuthURL())
	_, err = f2.Wait(ctx)
	require.ErrorIs(t, err, auth.ErrFlowExpired)
}

func TestDeviceFlowSlowDown(t *testing.T) {
	t.Parallel()
	srv := testoauth.Start(t)
	f, err := auth.StartDevice(ctx, srv.DeviceConfig("openid", "email"), http.DefaultClient)
	require.NoError(t, err)
	t.Cleanup(f.Close)
	srv.SlowDownOnce()
	srv.Approve("alice@example.com")
	g, err := f.Wait(ctx)
	require.NoError(t, err)
	assert.Equal(t, "alice@example.com", g.Email)
	assert.GreaterOrEqual(t, srv.TokenRequests(), 2)
}

func TestDeviceFlowCancelled(t *testing.T) {
	t.Parallel()
	srv := testoauth.Start(t)
	f, err := auth.StartDevice(ctx, srv.DeviceConfig("openid", "email"), http.DefaultClient)
	require.NoError(t, err)
	t.Cleanup(f.Close)
	c, cancel := context.WithCancel(ctx)
	ch := make(chan result, 1)
	go func() { g, err := f.Wait(c); ch <- result{g, err} }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	require.ErrorIs(t, recv(t, ch).err, context.Canceled)
	f.Close()
	f.Close()

	_, err = auth.StartDevice(ctx, auth.OAuthConfig{ClientID: "id"}, nil)
	require.ErrorIs(t, err, auth.ErrOAuthNotConfigured)
	_, err = auth.StartDevice(ctx, auth.OAuthConfig{DeviceURL: srv.DeviceConfig().DeviceURL}, nil)
	require.ErrorIs(t, err, auth.ErrOAuthNotConfigured)

	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"device_code":"dc","user_code":"uc","verification_uri":"http://evil.example/verify"}`))
	}))
	t.Cleanup(badSrv.Close)
	_, err = auth.StartDevice(ctx, auth.OAuthConfig{ClientID: "id", DeviceURL: badSrv.URL, TokenURL: badSrv.URL}, nil)
	require.Error(t, err)

	bareSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"device_code":"dc","user_code":"uc","verification_uri":"https://auth.example/verify"}`))
	}))
	t.Cleanup(bareSrv.Close)
	_, err = auth.StartDevice(ctx, auth.OAuthConfig{ClientID: "id", DeviceURL: bareSrv.URL, TokenURL: bareSrv.URL}, nil)
	require.Error(t, err, "a code the user would have to type, which Godwit never shows")
}
