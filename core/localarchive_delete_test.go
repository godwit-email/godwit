package core_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

// newServiceAt is newTestService over dataDir, so a test can see its files.
func newServiceAt(t *testing.T, dataDir string) *core.Service {
	t.Helper()
	keyring.MockInit()
	s, err := core.New(ctx, core.Options{DataDir: dataDir, AutoSync: false})
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}

// archiveFiles lists the files under the account's archive directory.
func archiveFiles(t *testing.T, dataDir string, accountID int64) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(filepath.Join(dataDir, "archive", strconv.FormatInt(accountID, 10)), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, p)
		}
		return err
	})
	if !os.IsNotExist(err) {
		require.NoError(t, err)
	}
	return out
}

func requireNoOps(t *testing.T, s *core.Service, accountID int64) {
	t.Helper()
	ops, err := s.PendingOps(ctx, accountID)
	require.NoError(t, err)
	require.Empty(t, ops)
}

func invoice(id string) []byte {
	return coretest.Msg("Bob <bob@example.com>", "me@example.com", "invoice "+id, "body", coretest.MessageID(id+"@example.com"))
}

func TestLocalArchiveTrashAndRestoreQueueNothing(t *testing.T) {
	s := newTestService(t)
	acc, inbox := newArchive(t, s)
	m := importOne(t, s, acc, inbox, invoice("a"))

	require.NoError(t, s.SetFlags(ctx, []int64{m.ID}, []string{`\Flagged`}, []string{`\Seen`}))
	require.NoError(t, s.Delete(ctx, []int64{m.ID}))
	trash, _ := folderNamed(t, s, acc.ID, "Trash")
	require.Equal(t, 1, trash.Total)
	require.NoError(t, s.Move(ctx, []int64{m.ID}, inbox.ID))
	in, _ := folderNamed(t, s, acc.ID, "INBOX")
	require.Equal(t, 1, in.Total)
	requireNoOps(t, s, acc.ID)
}

func TestLocalArchiveEmptyTrashDeletesOriginals(t *testing.T) {
	dir := t.TempDir()
	s := newServiceAt(t, dir)
	acc, inbox := newArchive(t, s)
	gone := importOne(t, s, acc, inbox, invoice("a"))
	kept, err := s.ImportEML(ctx, acc.ID, inbox.ID, []string{writeEML(t, t.TempDir(), "b.eml", coretest.Msg("c@example.com", "me@example.com", "kept", "x"))})
	require.NoError(t, err)
	require.Equal(t, 1, kept.Imported)
	// Files #555 extracts from an original live in parts/<content_hash>/ and go with it.
	sum := sha256.Sum256(invoice("a"))
	part := filepath.Join(dir, "archive", strconv.FormatInt(acc.ID, 10), "parts", hex.EncodeToString(sum[:]))
	require.NoError(t, os.MkdirAll(part, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(part, "a.pdf"), []byte("pdf"), 0o644))
	require.Len(t, archiveFiles(t, dir, acc.ID), 3)

	require.NoError(t, s.Delete(ctx, []int64{gone.ID}))
	trash, _ := folderNamed(t, s, acc.ID, "Trash")
	_, err = s.EmptyFolder(ctx, trash.ID)
	require.NoError(t, err)

	_, err = s.GetMessage(ctx, gone.ID, false)
	require.Error(t, err)
	hits, err := s.Search(ctx, acc.ID, "invoice", 10)
	require.NoError(t, err)
	require.Empty(t, hits)
	trash, _ = folderNamed(t, s, acc.ID, "Trash")
	require.Zero(t, trash.Total)
	require.Len(t, archiveFiles(t, dir, acc.ID), 1, "only the kept original")
	require.NoDirExists(t, part)
	requireNoOps(t, s, acc.ID)

	// The same file imports again.
	importOne(t, s, acc, inbox, invoice("a"))
}

func TestRemoveLocalArchiveDeletesItsOriginals(t *testing.T) {
	dir := t.TempDir()
	s := newServiceAt(t, dir)
	acc, inbox := newArchive(t, s)
	other, otherInbox := newArchive(t, s)
	importOne(t, s, acc, inbox, invoice("a"))
	importOne(t, s, other, otherInbox, invoice("a"))
	n, err := s.ArchiveUsage(ctx, acc.ID)
	require.NoError(t, err)
	require.Equal(t, int64(len(invoice("a"))), n)

	require.NoError(t, s.RemoveAccount(ctx, acc.ID))
	require.NoDirExists(t, filepath.Join(dir, "archive", strconv.FormatInt(acc.ID, 10)))
	require.Len(t, archiveFiles(t, dir, other.ID), 1, "other archives keep theirs")
	n, err = s.ArchiveUsage(ctx, acc.ID)
	require.NoError(t, err)
	require.Zero(t, n)
}

func TestLocalArchiveDeleteForeverOnlyFromTrash(t *testing.T) {
	dir := t.TempDir()
	s := newServiceAt(t, dir)
	acc, inbox := newArchive(t, s)
	m := importOne(t, s, acc, inbox, invoice("a"))

	require.NoError(t, s.DeleteForever(ctx, []int64{m.ID}))
	_, err := s.GetMessage(ctx, m.ID, false)
	require.NoError(t, err, "not in Trash: stays")
	require.Len(t, archiveFiles(t, dir, acc.ID), 1)

	require.NoError(t, s.Delete(ctx, []int64{m.ID}))
	require.NoError(t, s.DeleteForever(ctx, []int64{m.ID}))
	_, err = s.GetMessage(ctx, m.ID, false)
	require.Error(t, err)
	require.Empty(t, archiveFiles(t, dir, acc.ID))
	requireNoOps(t, s, acc.ID)
}

// Emptying the Trash while the same file is imported again must not leave the
// new row without its original. Racy by nature: it checks the invariant over
// several rounds.
func TestLocalArchiveEmptyTrashRacingReimportKeepsOriginal(t *testing.T) {
	s := newTestService(t)
	acc, inbox := newArchive(t, s)
	trash, _ := folderNamed(t, s, acc.ID, "Trash")
	path := writeEML(t, t.TempDir(), "a.eml", invoice("a"))
	for range 20 {
		_, err := s.ImportEML(ctx, acc.ID, inbox.ID, []string{path})
		require.NoError(t, err)
		hits, err := s.Search(ctx, acc.ID, "invoice", 10)
		require.NoError(t, err)
		require.Len(t, hits, 1)
		require.NoError(t, s.Delete(ctx, []int64{hits[0].ID}))

		done := make(chan error, 1)
		go func() { _, err := s.EmptyFolder(ctx, trash.ID); done <- err }()
		_, err = s.ImportEML(ctx, acc.ID, inbox.ID, []string{path})
		require.NoError(t, err)
		require.NoError(t, <-done)

		hits, err = s.Search(ctx, acc.ID, "invoice", 10)
		require.NoError(t, err)
		for _, h := range hits {
			_, err := s.GetRawMessage(ctx, h.ID)
			require.NoError(t, err, "a surviving row keeps its original")
			require.NoError(t, s.Delete(ctx, []int64{h.ID}))
		}
		_, err = s.EmptyFolder(ctx, trash.ID)
		require.NoError(t, err)
	}
}

// A Trash holding more mail than SQLite binds in one statement still empties.
func TestLocalArchiveEmptyLargeTrash(t *testing.T) {
	s := newTestService(t)
	acc, _ := newArchive(t, s)
	trash, _ := folderNamed(t, s, acc.ID, "Trash")
	const n = 33000 // over SQLite's default 32766 bound variables
	require.NoError(t, s.ExecForTest(fmt.Sprintf(`WITH RECURSIVE i(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM i WHERE x < %d)
		INSERT INTO messages(account_id, subject, date, content_hash) SELECT %d, 'old', x, printf('h%%d', x) FROM i`, n, acc.ID)))
	require.NoError(t, s.ExecForTest(fmt.Sprintf(`INSERT INTO message_locations(message_id, folder_id, shown, entered_at, date)
		SELECT id, %d, 1, 0, date FROM messages WHERE account_id = %d`, trash.ID, acc.ID)))
	trash, _ = folderNamed(t, s, acc.ID, "Trash")
	require.Equal(t, n, trash.Total)

	_, err := s.EmptyFolder(ctx, trash.ID)
	require.NoError(t, err)
	trash, _ = folderNamed(t, s, acc.ID, "Trash")
	require.Zero(t, trash.Total)
}
