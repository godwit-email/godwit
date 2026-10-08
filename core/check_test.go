package core_test

import (
	"errors"
	"net"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/conn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

func TestCheckAllOK(t *testing.T) {
	s := newTestService(t)
	im, smtpAddr := servers(t)
	r := s.CheckAccount(ctx, input(t, im.Addr, smtpAddr, "secret"))
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
	r := s.CheckAccount(ctx, input(t, im.Addr, smtpAddr, "wrong"))
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
	r := s.CheckAccount(ctx, input(t, im.Addr, addr, "secret"))
	assert.True(t, r.Steps[1].OK)
	assert.Equal(t, conn.FailureConnect, r.Steps[2].Kind)
}

func TestCheckInvalidSettings(t *testing.T) {
	s := newTestService(t)
	im, smtpAddr := servers(t)
	in := input(t, im.Addr, smtpAddr, "secret")
	in.IMAPPort = 0
	r := s.CheckAccount(ctx, in)
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
	_, err = keyring.Get("godwit", "account:1")
	assert.ErrorIs(t, err, keyring.ErrNotFound)
}

func TestCheckDetailHasNoSecret(t *testing.T) {
	s := newTestService(t)
	im, smtpAddr := servers(t)
	for _, st := range s.CheckAccount(ctx, input(t, im.Addr, smtpAddr, "hunter2-secret")).Steps {
		assert.NotContains(t, st.Detail, "hunter2-secret")
	}
}
