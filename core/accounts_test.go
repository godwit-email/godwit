package core_test

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/internal/testmail"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

func hostPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	i := strings.LastIndex(addr, ":")
	p, err := strconv.Atoi(addr[i+1:])
	require.NoError(t, err)
	return addr[:i], p
}

func input(t *testing.T, imapAddr, smtpAddr, password string) core.AccountInput {
	t.Helper()
	ih, ip := hostPort(t, imapAddr)
	sh, sp := hostPort(t, smtpAddr)
	return core.AccountInput{
		Email: "alice@example.com", Name: "Alice", Username: "alice", Password: password,
		IMAPHost: ih, IMAPPort: ip, IMAPSecurity: core.SecurityNone,
		SMTPHost: sh, SMTPPort: sp, SMTPSecurity: core.SecurityNone,
	}
}

func servers(t *testing.T) (*testmail.IMAP, string) {
	t.Helper()
	smtpAddr, _ := testmail.StartSMTP(t)
	return testmail.StartIMAP(t), smtpAddr
}

func rowCount(t *testing.T, s *core.Service) int {
	t.Helper()
	a, err := s.ListAccounts(ctx)
	require.NoError(t, err)
	return len(a)
}

func TestAddAccount(t *testing.T) {
	s := newTestService(t)
	im, smtpAddr := servers(t)
	acc, err := s.AddAccount(ctx, input(t, im.Addr, smtpAddr, "secret"))
	require.NoError(t, err)
	assert.NotZero(t, acc.ID)
	assert.True(t, acc.IsDefault)
	assert.Equal(t, core.SyncRange{Limit: 2000}, acc.Sync)
	assert.Equal(t, "password", acc.AuthType)
	assert.Equal(t, "ok", acc.Status)
	require.NoError(t, s.SyncNow(ctx, acc.ID), "the password is in the keychain")

	list, err := s.ListAccounts(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, 4, list[0].Folders, "the sync mirrored the folders")
	acc.Folders = 4
	assert.Equal(t, acc, list[0])

	in2 := input(t, im.Addr, smtpAddr, "secret")
	in2.Email = "second@example.com"
	acc2, err := s.AddAccount(ctx, in2)
	require.NoError(t, err)
	assert.False(t, acc2.IsDefault)
}

func TestAddAccountWrongPassword(t *testing.T) {
	s := newTestService(t)
	im, smtpAddr := servers(t)
	_, err := s.AddAccount(ctx, input(t, im.Addr, smtpAddr, "hunter2-wrong"))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "hunter2-wrong")
	assert.Zero(t, rowCount(t, s))
	_, err = keyring.Get("godwit", "account:1")
	assert.ErrorIs(t, err, keyring.ErrNotFound)
}

func TestKeyringFailureRollsBack(t *testing.T) {
	s := newTestService(t)
	im, smtpAddr := servers(t)
	keyring.MockInitWithError(errors.New("boom"))
	t.Cleanup(keyring.MockInit)
	_, err := s.AddAccount(ctx, input(t, im.Addr, smtpAddr, "secret"))
	require.ErrorContains(t, err, "boom")
	assert.Zero(t, rowCount(t, s))
}

func TestPlaintextOnlyForLoopback(t *testing.T) {
	s := newTestService(t)
	in := input(t, "127.0.0.1:1", "127.0.0.1:1", "secret")
	in.IMAPHost = "mail.example.com"
	require.ErrorContains(t, s.TestAccount(ctx, in), "plaintext")
	in = input(t, "127.0.0.1:1", "127.0.0.1:1", "secret")
	in.SMTPHost = "mail.example.com"
	require.ErrorContains(t, s.TestAccount(ctx, in), "plaintext")
	in = input(t, "127.0.0.1:1", "127.0.0.1:1", "secret")
	in.Email = "not-an-email"
	require.Error(t, s.TestAccount(ctx, in))
}

func TestRemoveAccount(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()
	s, err := core.New(ctx, core.Options{DataDir: dir})
	require.NoError(t, err)
	defer s.Close()
	im, smtpAddr := servers(t)
	acc, err := s.AddAccount(ctx, input(t, im.Addr, smtpAddr, "secret"))
	require.NoError(t, err)
	id := strconv.FormatInt(acc.ID, 10)
	cache := filepath.Join(dir, "cache", id)
	require.NoError(t, os.MkdirAll(cache, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(cache, "x"), []byte("x"), 0o600))

	require.NoError(t, s.RemoveAccount(ctx, acc.ID))
	assert.Zero(t, rowCount(t, s))
	_, err = os.Stat(cache)
	assert.True(t, os.IsNotExist(err))
}

// Two data directories number their accounts independently; their keychain
// entries must not collide.
func TestCredentialsIsolatedPerDataDir(t *testing.T) {
	keyring.MockInit()
	im, smtpAddr := servers(t)
	dirA, dirB := t.TempDir(), t.TempDir()
	a, err := core.New(ctx, core.Options{DataDir: dirA})
	require.NoError(t, err)
	defer a.Close()
	b, err := core.New(ctx, core.Options{DataDir: dirB})
	require.NoError(t, err)
	accA, err := a.AddAccount(ctx, input(t, im.Addr, smtpAddr, "secret"))
	require.NoError(t, err)
	accB, err := b.AddAccount(ctx, input(t, im.Addr, smtpAddr, "secret"))
	require.NoError(t, err)
	require.Equal(t, accA.ID, accB.ID, "the same local id in both directories")

	require.NoError(t, a.RemoveAccount(ctx, accA.ID))
	require.NoError(t, b.SyncNow(ctx, accB.ID), "B's password survives removing A's account")
	require.NoError(t, b.Close())
	b, err = core.New(ctx, core.Options{DataDir: dirB})
	require.NoError(t, err)
	defer b.Close()
	require.NoError(t, b.SyncNow(ctx, accB.ID), "the namespace is stable across restarts")
}

func TestUpdateAccountIcon(t *testing.T) {
	keyring.MockInit()
	im, smtpAddr := servers(t)
	dir := t.TempDir()
	s, err := core.New(ctx, core.Options{DataDir: dir})
	require.NoError(t, err)
	defer s.Close()
	acc, err := s.AddAccount(ctx, input(t, im.Addr, smtpAddr, "secret"))
	require.NoError(t, err)
	assert.Empty(t, acc.Icon)

	icon := "label:shield-keyhole"
	updated, err := s.UpdateAccount(ctx, acc.ID, core.AccountPatch{Icon: &icon})
	require.NoError(t, err)
	assert.Equal(t, icon, updated.Icon)

	list, err := s.ListAccounts(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, icon, list[0].Icon)

	brand := "brand:gmail"
	withBrand, err := s.UpdateAccount(ctx, acc.ID, core.AccountPatch{Icon: &brand})
	require.NoError(t, err)
	assert.Equal(t, brand, withBrand.Icon)
	for _, bad := range []string{"brand:", "brand:Gmail!", "gmail"} {
		_, err = s.UpdateAccount(ctx, acc.ID, core.AccountPatch{Icon: &bad})
		require.Error(t, err, bad)
	}

	bad := "label:nope"
	_, err = s.UpdateAccount(ctx, acc.ID, core.AccountPatch{Icon: &bad})
	require.Error(t, err)
	_, err = s.UpdateAccount(ctx, acc.ID, core.AccountPatch{IconImage: []byte("not an image")})
	require.ErrorIs(t, err, core.ErrAccountIcon)

	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)
	withImage, err := s.UpdateAccount(ctx, acc.ID, core.AccountPatch{IconImage: svg})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(withImage.Icon, "image:"))
	img, err := s.AccountIcon(ctx, acc.ID)
	require.NoError(t, err)
	assert.Equal(t, core.AccountIcon{ContentType: "image/svg+xml", Data: svg}, img)

	empty := ""
	cleared, err := s.UpdateAccount(ctx, acc.ID, core.AccountPatch{Icon: &empty})
	require.NoError(t, err)
	assert.Empty(t, cleared.Icon)
	_, err = s.AccountIcon(ctx, acc.ID)
	require.ErrorIs(t, err, core.ErrAccountNotFound, "the image goes with it")
}
