package core_test

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/conn"
	"github.com/godwit-email/godwit/internal/sysproxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckAllOK(t *testing.T) {
	s := newTestService(t)
	im, smtpAddr := servers(t)
	r, _ := s.CheckAccount(ctx, input(t, im.Addr, smtpAddr, "secret"))
	require.True(t, r.OK())
	require.Len(t, r.Steps, 3)
	for i, n := range []string{"settings", "imap", "smtp"} {
		assert.Equal(t, n, r.Steps[i].Name)
		assert.True(t, r.Steps[i].OK)
	}
}

func TestCheckIMAPAuthFails(t *testing.T) {
	s := newTestService(t)
	im, smtpAddr := servers(t)
	r, _ := s.CheckAccount(ctx, input(t, im.Addr, smtpAddr, "wrong"))
	assert.False(t, r.OK())
	assert.Equal(t, conn.FailureAuth, r.Steps[1].Kind)
	assert.True(t, r.Steps[2].Skipped)
}

func TestCheckSMTPDown(t *testing.T) {
	s := newTestService(t)
	im, _ := servers(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	l.Close()
	r, _ := s.CheckAccount(ctx, input(t, im.Addr, addr, "secret"))
	assert.True(t, r.Steps[1].OK)
	assert.Equal(t, conn.FailureConnect, r.Steps[2].Kind)
}

func TestCheckThroughRefusingProxy(t *testing.T) {
	s := newTestService(t)
	im, smtpAddr := servers(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				http.ReadRequest(bufio.NewReader(c))
				io.WriteString(c, "HTTP/1.1 403 Forbidden\r\n\r\n")
			}()
		}
	}()
	conn.SystemProxy = func(*url.URL) *url.URL { return &url.URL{Scheme: "http", Host: l.Addr().String()} }
	t.Cleanup(func() { conn.SystemProxy = sysproxy.For })
	in := input(t, im.Addr, smtpAddr, "secret")
	in.IMAPHost, in.IMAPSecurity = "imap.example", core.SecurityStartTLS // loopback skips the proxy
	_, err = s.AddAccount(ctx, in)
	var ce *core.CheckError
	require.True(t, errors.As(err, &ce), "%v", err)
	assert.Equal(t, conn.FailureProxyRefused, ce.Report.Steps[1].Kind)
	assert.Contains(t, ce.Report.Steps[1].Detail, "403")
	assert.NotErrorIs(t, err, conn.ErrAuthFailed)
}

func TestCheckInvalidSettings(t *testing.T) {
	s := newTestService(t)
	im, smtpAddr := servers(t)
	in := input(t, im.Addr, smtpAddr, "secret")
	in.IMAPPort = 0
	r, _ := s.CheckAccount(ctx, in)
	assert.Equal(t, core.KindInvalid, r.Steps[0].Kind)
	assert.NotEmpty(t, r.Steps[0].Detail)
	assert.True(t, r.Steps[1].Skipped)
	assert.True(t, r.Steps[2].Skipped)
	assert.Zero(t, im.Logins())
}

func TestAddAccountReturnsCheckError(t *testing.T) {
	s := newTestService(t)
	im, smtpAddr := servers(t)
	_, err := s.AddAccount(ctx, input(t, im.Addr, smtpAddr, "wrong"))
	var ce *core.CheckError
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, conn.FailureAuth, ce.Report.Steps[1].Kind)
	assert.ErrorIs(t, err, conn.ErrAuthFailed)
	assert.Zero(t, rowCount(t, s))
}

func TestCheckDetailHasNoSecret(t *testing.T) {
	s := newTestService(t)
	im, smtpAddr := servers(t)
	for _, st := range checkSteps(t, s, input(t, im.Addr, smtpAddr, "hunter2-secret")) {
		assert.NotContains(t, st.Detail, "hunter2-secret")
	}
}

func checkSteps(t *testing.T, s *core.Service, in core.AccountInput) []core.CheckStep {
	r, err := s.CheckAccount(ctx, in)
	require.NoError(t, err)
	return r.Steps
}
