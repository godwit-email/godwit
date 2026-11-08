package core_test

import (
	"testing"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

func TestLocalArchiveHasInboxAndTrash(t *testing.T) {
	s := newTestService(t)
	acc, err := s.AddLocalArchive(ctx, "旧邮件")
	require.NoError(t, err)
	require.Equal(t, core.KindLocalArchive, acc.Kind)
	require.Equal(t, "旧邮件", acc.Name)
	require.False(t, acc.IsDefault)

	accs, err := s.ListAccounts(ctx)
	require.NoError(t, err)
	require.Len(t, accs, 1)
	require.Equal(t, core.KindLocalArchive, accs[0].Kind)

	folders, err := s.ListFolders(ctx, acc.ID)
	require.NoError(t, err)
	var roles []string
	for _, f := range folders {
		roles = append(roles, f.Role)
	}
	require.ElementsMatch(t, []string{"inbox", "trash"}, roles)
}

func TestLocalArchiveNeverConnects(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()
	s, err := core.New(ctx, core.Options{DataDir: dir, AutoSync: true})
	require.NoError(t, err)
	acc, err := s.AddLocalArchive(ctx, "旧邮件")
	require.NoError(t, err)
	s.Close()

	// Reopened with sync on: New starts a worker for every account but this one.
	s, err = core.New(ctx, core.Options{DataDir: dir, AutoSync: true})
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	ch, stop := s.Subscribe()
	defer stop()

	require.NoError(t, s.SyncNow(ctx, acc.ID))
	folders, err := s.ListFolders(ctx, acc.ID)
	require.NoError(t, err)
	more, err := s.LoadOlder(ctx, folders[0].ID)
	require.NoError(t, err)
	require.False(t, more)

	timeout := time.After(300 * time.Millisecond)
	for done := false; !done; {
		select {
		case ev := <-ch:
			require.NotEqual(t, acc.ID, ev.AccountID, "event %s for the local archive", ev.Type)
		case <-timeout:
			done = true
		}
	}
	got, err := s.ListAccounts(ctx)
	require.NoError(t, err)
	require.Equal(t, "ok", got[0].Status)
}

func TestLocalArchiveIsNoSender(t *testing.T) {
	s := newTestService(t)
	acc, err := s.AddLocalArchive(ctx, "旧邮件")
	require.NoError(t, err)

	_, err = s.NewDraft(ctx, acc.ID, "new", 0)
	require.ErrorIs(t, err, core.ErrLocalArchive)
	_, err = s.SaveDraft(ctx, core.Draft{AccountID: acc.ID, Mode: "new"})
	require.ErrorIs(t, err, core.ErrLocalArchive)
}

func TestLocalArchiveRefusesServerSettings(t *testing.T) {
	s := newTestService(t)
	acc, err := s.AddLocalArchive(ctx, "旧邮件")
	require.NoError(t, err)

	yes := true
	for name, p := range map[string]core.AccountPatch{
		"servers": {Servers: &core.ServerSettings{IMAPHost: "imap.example.com", IMAPPort: 993}},
		"sync":    {Sync: &core.SyncRange{Limit: 100}},
		"default": {IsDefault: &yes},
	} {
		_, err := s.UpdateAccount(ctx, acc.ID, p)
		require.ErrorIs(t, err, core.ErrLocalArchive, name)
	}
	require.ErrorIs(t, s.SetPurge(ctx, acc.ID, "trash", 30), core.ErrLocalArchive)

	name := "新名字"
	got, err := s.UpdateAccount(ctx, acc.ID, core.AccountPatch{Name: &name})
	require.NoError(t, err)
	require.Equal(t, name, got.Name)
	require.NoError(t, s.RemoveAccount(ctx, acc.ID))
}
