package core_test

import (
	"fmt"
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

// archiveMessage puts one message in the local archive's folder, the way an
// import would leave it: a location with no server uid.
func archiveMessage(t *testing.T, s *core.Service, acc core.Account, folderID int64) int64 {
	t.Helper()
	require.NoError(t, s.ExecForTest(fmt.Sprintf(`INSERT INTO messages(account_id, subject, from_addr, date) VALUES (%d, 'old', 'a@example.com', 1)`, acc.ID)))
	require.NoError(t, s.ExecForTest(fmt.Sprintf(`INSERT INTO message_locations(message_id, folder_id, shown, entered_at, date)
		VALUES ((SELECT max(id) FROM messages), %d, 1, 1, 1)`, folderID)))
	page, err := s.ListMessages(ctx, core.View{FolderID: folderID}, "", 10)
	require.NoError(t, err)
	require.NotEmpty(t, page.Items)
	return page.Items[0].ID
}

func folderNamed(t *testing.T, s *core.Service, accountID int64, name string) (core.Folder, bool) {
	t.Helper()
	folders, err := s.ListFolders(ctx, accountID)
	require.NoError(t, err)
	for _, f := range folders {
		if f.Name == name {
			return f, true
		}
	}
	return core.Folder{}, false
}

func TestLocalFolderIsAMoveTarget(t *testing.T) {
	s := newTestService(t)
	acc, err := s.AddLocalArchive(ctx, "旧邮件")
	require.NoError(t, err)
	f, err := s.CreateLocalFolder(ctx, acc.ID, " 2019 ")
	require.NoError(t, err)
	require.Equal(t, "2019", f.Name)
	require.Equal(t, acc.ID, f.AccountID)
	_, err = s.CreateLocalFolder(ctx, acc.ID, "2019")
	require.ErrorIs(t, err, core.ErrFolderExists, "same name twice")
	_, err = s.CreateLocalFolder(ctx, acc.ID, "  ")
	require.ErrorIs(t, err, core.ErrFolderNameEmpty)

	inbox, _ := folderNamed(t, s, acc.ID, "INBOX")
	id := archiveMessage(t, s, acc, inbox.ID)
	require.NoError(t, s.Move(ctx, []int64{id}, f.ID))
	got, ok := folderNamed(t, s, acc.ID, "2019")
	require.True(t, ok)
	require.Equal(t, 1, got.Total)
}

func TestRenameLocalFolder(t *testing.T) {
	s := newTestService(t)
	acc, err := s.AddLocalArchive(ctx, "旧邮件")
	require.NoError(t, err)
	f, err := s.CreateLocalFolder(ctx, acc.ID, "2019")
	require.NoError(t, err)
	require.NoError(t, s.RenameLocalFolder(ctx, f.ID, "2019 年"))
	got, ok := folderNamed(t, s, acc.ID, "2019 年")
	require.True(t, ok)
	require.Equal(t, f.ID, got.ID)
	_, ok = folderNamed(t, s, acc.ID, "2019")
	require.False(t, ok)
	require.ErrorIs(t, s.RenameLocalFolder(ctx, f.ID, "INBOX"), core.ErrFolderExists)
	require.ErrorIs(t, s.RenameLocalFolder(ctx, f.ID, " "), core.ErrFolderNameEmpty)

	for _, name := range []string{"INBOX", "Trash"} {
		role, _ := folderNamed(t, s, acc.ID, name)
		require.ErrorIs(t, s.RenameLocalFolder(ctx, role.ID, "x"), core.ErrRoleFolder, name)
		require.ErrorIs(t, s.DeleteLocalFolder(ctx, role.ID), core.ErrRoleFolder, name)
	}
}

func TestDeleteLocalFolder(t *testing.T) {
	s := newTestService(t)
	acc, err := s.AddLocalArchive(ctx, "旧邮件")
	require.NoError(t, err)
	f, err := s.CreateLocalFolder(ctx, acc.ID, "2019")
	require.NoError(t, err)
	id := archiveMessage(t, s, acc, f.ID)
	require.ErrorIs(t, s.DeleteLocalFolder(ctx, f.ID), core.ErrFolderNotEmpty)

	require.NoError(t, s.Delete(ctx, []int64{id})) // to Trash
	require.NoError(t, s.DeleteLocalFolder(ctx, f.ID))
	_, ok := folderNamed(t, s, acc.ID, "2019")
	require.False(t, ok)
}

func TestLocalFoldersOnlyInALocalArchive(t *testing.T) {
	s := newTestService(t)
	im, smtpAddr := servers(t)
	acc, err := s.AddAccount(ctx, input(t, im.Addr, smtpAddr, "secret"))
	require.NoError(t, err)
	require.NoError(t, s.ExecForTest(fmt.Sprintf(`INSERT INTO folders(account_id, name) VALUES (%d, 'Projects')`, acc.ID)))
	_, err = s.CreateLocalFolder(ctx, acc.ID, "2019")
	require.ErrorIs(t, err, core.ErrNotLocalArchive)
	folders, err := s.ListFolders(ctx, acc.ID)
	require.NoError(t, err)
	require.NotEmpty(t, folders)
	require.ErrorIs(t, s.RenameLocalFolder(ctx, folders[0].ID, "x"), core.ErrNotLocalArchive)
	require.ErrorIs(t, s.DeleteLocalFolder(ctx, folders[0].ID), core.ErrNotLocalArchive)
}
