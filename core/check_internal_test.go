package core

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/godwit-email/godwit/core/internal/testmail"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

func TestAddAccountFailedCheckWritesNoKeychain(t *testing.T) {
	keyring.MockInit()
	s, err := New(context.Background(), Options{DataDir: t.TempDir()})
	require.NoError(t, err)
	defer s.Close()
	smtpAddr, _ := testmail.StartSMTP(t)
	im := testmail.StartIMAP(t)
	writes := 0 // of the password; the KeychainStatus probe comes first
	setFor(t, &keyringSet, func(svc, user, pw string) error {
		if !strings.HasSuffix(user, "/probe") {
			writes++
		}
		return keyring.Set(svc, user, pw)
	})
	in := internalInput(t, im.Addr, smtpAddr, "a@example.com", false)
	in.Password = "wrong"
	_, err = s.AddAccount(context.Background(), in)
	var ce *CheckError
	require.True(t, errors.As(err, &ce))
	assert.Zero(t, writes)
	list, _ := s.ListAccounts(context.Background())
	assert.Empty(t, list)
}

func TestCheckAccountClosedAndCancelled(t *testing.T) {
	keyring.MockInit()
	s, err := New(context.Background(), Options{DataDir: t.TempDir()})
	require.NoError(t, err)
	in := internalInput(t, "127.0.0.1:1", "127.0.0.1:1", "a@example.com", false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = s.CheckAccount(ctx, in)
	assert.ErrorIs(t, err, context.Canceled)
	require.NoError(t, s.Close())
	_, err = s.CheckAccount(context.Background(), in)
	assert.ErrorIs(t, err, ErrClosed)
}

func TestCheckDetailCapped(t *testing.T) {
	keyring.MockInit()
	s, err := New(context.Background(), Options{DataDir: t.TempDir()})
	require.NoError(t, err)
	defer s.Close()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Write([]byte("* BYE " + strings.Repeat("é", 500) + "\r\n"))
			c.Close()
		}
	}()
	smtpAddr, _ := testmail.StartSMTP(t)
	r, err := s.CheckAccount(context.Background(), internalInput(t, l.Addr().String(), smtpAddr, "a@example.com", false))
	require.NoError(t, err)
	d := r.Steps[1].Detail
	require.NotEmpty(t, d)
	assert.LessOrEqual(t, len(d), 200)
	assert.True(t, utf8.ValidString(d))
}
