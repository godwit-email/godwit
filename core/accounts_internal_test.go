package core

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/godwitmail/godwit/core/internal/testmail"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

func internalInput(t *testing.T, imapAddr, smtpAddr, email string, def bool) AccountInput {
	t.Helper()
	split := func(a string) (string, int) {
		i := strings.LastIndex(a, ":")
		p, err := strconv.Atoi(a[i+1:])
		require.NoError(t, err)
		return a[:i], p
	}
	ih, ip := split(imapAddr)
	sh, sp := split(smtpAddr)
	return AccountInput{Email: email, Username: "alice", Password: "secret", IsDefault: def,
		IMAPHost: ih, IMAPPort: ip, IMAPSecurity: SecurityNone, SMTPHost: sh, SMTPPort: sp, SMTPSecurity: SecurityNone}
}

func TestKeyringFailureKeepsPreviousDefault(t *testing.T) {
	keyring.MockInit()
	s, err := New(context.Background(), Options{DataDir: t.TempDir()})
	require.NoError(t, err)
	defer s.Close()
	smtpAddr, _ := testmail.StartSMTP(t)
	im := testmail.StartIMAP(t)
	first, err := s.AddAccount(context.Background(), internalInput(t, im.Addr, smtpAddr, "a@example.com", false))
	require.NoError(t, err)
	require.True(t, first.IsDefault)

	keyringSet = func(string, string, string) error { return errors.New("boom") }
	t.Cleanup(func() { keyringSet = keyring.Set })
	_, err = s.AddAccount(context.Background(), internalInput(t, im.Addr, smtpAddr, "b@example.com", true))
	require.ErrorContains(t, err, "boom")

	list, err := s.ListAccounts(context.Background())
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.True(t, list[0].IsDefault)
}

func TestKeyringFailureWithCancelledContextLeavesNoRow(t *testing.T) {
	keyring.MockInit()
	s, err := New(context.Background(), Options{DataDir: t.TempDir()})
	require.NoError(t, err)
	defer s.Close()
	smtpAddr, _ := testmail.StartSMTP(t)
	im := testmail.StartIMAP(t)

	ctx, cancel := context.WithCancel(context.Background())
	keyringSet = func(string, string, string) error { cancel(); return errors.New("boom") }
	t.Cleanup(func() { keyringSet = keyring.Set })
	_, err = s.AddAccount(ctx, internalInput(t, im.Addr, smtpAddr, "a@example.com", false))
	require.ErrorContains(t, err, "boom")

	list, err := s.ListAccounts(context.Background())
	require.NoError(t, err)
	assert.Empty(t, list)
}
