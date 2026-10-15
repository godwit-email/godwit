package core

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/godwit-email/godwit/core/internal/testmail"
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

func TestAccountPasswordWhitespacePreserved(t *testing.T) {
	for _, tc := range []struct {
		provider, password string
	}{
		{"icloud", "\r\n abcd-efgh ijkl \r\n"},
		{"gmail", "\n abcd efgh ijkl \n"},
		{"other", "\r secret \r"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			keyring.MockInit()
			ctx := context.Background()
			s, err := New(ctx, Options{DataDir: t.TempDir()})
			require.NoError(t, err)
			defer s.Close()
			smtpAddr, smtp := testmail.StartSMTP(t)
			im := testmail.StartIMAP(t)
			im.SetPassword(tc.password)
			smtp.SetPassword(tc.password)
			in := internalInput(t, im.Addr, smtpAddr, "a@example.com", false)
			in.Provider, in.Password = tc.provider, tc.password
			acc, err := s.AddAccount(ctx, in)
			require.NoError(t, err)

			assert.NoError(t, s.TestAccount(ctx, in))
			report, err := s.CheckAccount(ctx, in)
			require.NoError(t, err)
			assert.True(t, report.OK(), "%+v", report)
			assert.NoError(t, s.UpdatePassword(ctx, acc.ID, tc.password))
			assert.Equal(t, tc.password, storedSecret(t, s, acc.ID))

			in.Email = "b@example.com"
			added, err := s.AddAccount(ctx, in)
			require.NoError(t, err)
			assert.Equal(t, tc.password, storedSecret(t, s, added.ID))
			require.NoError(t, s.SyncNow(ctx, added.ID))
		})
	}
}

// failAccountWrites makes saving an account's password fail with "boom" after
// calling before; the KeychainStatus probe still works.
func failAccountWrites(t *testing.T, before func()) {
	setFor(t, &keyringSet, func(svc, user, pw string) error {
		if strings.Contains(user, "/probe/") {
			return keyring.Set(svc, user, pw)
		}
		before()
		return errors.New("boom")
	})
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

	failAccountWrites(t, func() {})
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
	failAccountWrites(t, cancel)
	_, err = s.AddAccount(ctx, internalInput(t, im.Addr, smtpAddr, "a@example.com", false))
	require.ErrorContains(t, err, "boom")

	list, err := s.ListAccounts(context.Background())
	require.NoError(t, err)
	assert.Empty(t, list)
}

// The keychain entry is "godwit" / "<install id>/account:<id>" and goes with the account.
func TestKeyringEntryNamespaced(t *testing.T) {
	keyring.MockInit()
	s, err := New(context.Background(), Options{DataDir: t.TempDir()})
	require.NoError(t, err)
	defer s.Close()
	smtpAddr, _ := testmail.StartSMTP(t)
	im := testmail.StartIMAP(t)
	acc, err := s.AddAccount(context.Background(), internalInput(t, im.Addr, smtpAddr, "a@example.com", false))
	require.NoError(t, err)
	assert.Len(t, s.installID, 32)
	assert.Equal(t, s.installID+"/account:"+strconv.FormatInt(acc.ID, 10), s.keyringUser(acc.ID))
	pw, err := keyring.Get(keyringService, s.keyringUser(acc.ID))
	require.NoError(t, err)
	assert.Equal(t, "secret", pw)
	require.NoError(t, s.RemoveAccount(context.Background(), acc.ID))
	_, err = keyring.Get(keyringService, s.keyringUser(acc.ID))
	assert.ErrorIs(t, err, keyring.ErrNotFound)
}
