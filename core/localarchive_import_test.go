package core_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/events"
	"github.com/stretchr/testify/require"
)

// newArchive is a service with a local archive and its inbox.
func newArchive(t *testing.T, s *core.Service) (core.Account, core.Folder) {
	t.Helper()
	acc, err := s.AddLocalArchive(ctx, "旧邮件")
	require.NoError(t, err)
	folders, err := s.ListFolders(ctx, acc.ID)
	require.NoError(t, err)
	for _, f := range folders {
		if f.Role == "inbox" {
			return acc, f
		}
	}
	t.Fatal("no inbox")
	return acc, core.Folder{}
}

// writeEML writes raw as dir/name and returns its path.
func writeEML(t *testing.T, dir, name string, raw []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, raw, 0o644))
	return p
}

func TestImportEMLShowsInListThreadAndSearch(t *testing.T) {
	s := newTestService(t)
	acc, inbox := newArchive(t, s)
	raw := coretest.Msg("Bob <bob@example.com>", "me@example.com", "Quarterly report", "the zanzibar figures", coretest.MessageID("q1@example.com"))
	dir := t.TempDir()
	path := writeEML(t, dir, "q1.eml", raw)

	sum, err := s.ImportEML(ctx, acc.ID, inbox.ID, []string{path})
	require.NoError(t, err)
	require.Equal(t, core.ImportSummary{Imported: 1}, sum)

	page, err := s.ListMessages(ctx, core.View{FolderID: inbox.ID}, "", 50)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	m := page.Items[0]
	require.Equal(t, "Quarterly report", m.Subject)
	require.Contains(t, m.From, "bob@example.com")

	th, err := s.GetThread(ctx, m.ThreadID)
	require.NoError(t, err)
	require.Len(t, th.Messages, 1)

	hits, err := s.Search(ctx, 0, "zanzibar", 10)
	require.NoError(t, err)
	require.Len(t, hits, 1)
	require.Equal(t, m.ID, hits[0].ID)

	got, err := s.GetRawMessage(ctx, m.ID)
	require.NoError(t, err)
	require.Equal(t, raw, got)

	// The original is the service's copy: the user's file may go.
	require.NoError(t, os.Remove(path))
	got, err = s.GetRawMessage(ctx, m.ID)
	require.NoError(t, err)
	require.Equal(t, raw, got)
}

func TestImportEMLSkipsDuplicateContentNotMessageID(t *testing.T) {
	s := newTestService(t)
	acc, inbox := newArchive(t, s)
	dir := t.TempDir()
	rawA := coretest.Msg("bob@example.com", "me@example.com", "Hi", "first", coretest.MessageID("same@example.com"))
	a := writeEML(t, dir, "a.eml", rawA)
	copyOfA := writeEML(t, dir, "sub/a copy.eml", rawA)
	b := writeEML(t, dir, "b.eml", coretest.Msg("bob@example.com", "me@example.com", "Hi", "second", coretest.MessageID("same@example.com")))

	sum, err := s.ImportEML(ctx, acc.ID, inbox.ID, []string{a, copyOfA, b})
	require.NoError(t, err)
	require.Equal(t, core.ImportSummary{Imported: 2, Duplicates: 1}, sum)

	// Again, as a folder: all three are there already.
	sum, err = s.ImportEML(ctx, acc.ID, inbox.ID, []string{dir})
	require.NoError(t, err)
	require.Equal(t, core.ImportSummary{Duplicates: 3}, sum)

	page, err := s.ListMessages(ctx, core.View{FolderID: inbox.ID}, "", 50)
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
}

func TestImportEMLFolderFindsEMLAndBadFilesFailAlone(t *testing.T) {
	s := newTestService(t)
	acc, inbox := newArchive(t, s)
	dir := t.TempDir()
	writeEML(t, dir, "deep/er/good.EML", coretest.Msg("bob@example.com", "me@example.com", "Good", "fine"))
	writeEML(t, dir, "notes.txt", []byte("not an eml, not looked at"))
	junk := writeEML(t, dir, "junk.eml", []byte{0x00, 0xff, 0x13, 0x37, '\n', 0x00})
	empty := writeEML(t, dir, "empty.eml", nil)
	missing := filepath.Join(dir, "missing.eml")

	sum, err := s.ImportEML(ctx, acc.ID, inbox.ID, []string{dir, missing})
	require.NoError(t, err)
	require.Equal(t, 1, sum.Imported)
	var failed []string
	for _, f := range sum.Failed {
		require.NotEmpty(t, f.Reason, f.Path)
		failed = append(failed, f.Path)
	}
	require.ElementsMatch(t, []string{junk, empty, missing}, failed)
}

func TestImportEMLDecodesGBK(t *testing.T) {
	s := newTestService(t)
	acc, inbox := newArchive(t, s)
	// 张三, 会议纪要 and 明天下午开会 in GBK.
	body := []byte{0xc3, 0xf7, 0xcc, 0xec, 0xcf, 0xc2, 0xce, 0xe7, 0xbf, 0xaa, 0xbb, 0xe1}
	raw := append([]byte("Message-ID: <gbk@example.com>\r\nFrom: =?GBK?B?1cXI/Q==?= <zs@example.com>\r\nTo: me@example.com\r\nSubject: =?GBK?B?u+HS6bzN0qo=?="+
		"\r\nDate: Mon, 02 Jan 2006 15:04:05 +0800\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=GBK\r\nContent-Transfer-Encoding: 8bit\r\n\r\n"), body...)
	path := writeEML(t, t.TempDir(), "gbk.eml", raw)

	sum, err := s.ImportEML(ctx, acc.ID, inbox.ID, []string{path})
	require.NoError(t, err)
	require.Equal(t, 1, sum.Imported)
	page, err := s.ListMessages(ctx, core.View{FolderID: inbox.ID}, "", 50)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Equal(t, "会议纪要", page.Items[0].Subject)
	require.Contains(t, page.Items[0].From, "张三")
	require.Equal(t, 2006, page.Items[0].Date.Year())
	m, err := s.GetMessage(ctx, page.Items[0].ID, false)
	require.NoError(t, err)
	require.Contains(t, m.Text, "明天下午开会")
}

func TestImportEMLCanceledKeepsWhatWasImported(t *testing.T) {
	s := newTestService(t)
	acc, inbox := newArchive(t, s)
	dir := t.TempDir()
	for i := range 120 {
		writeEML(t, dir, fmt.Sprintf("m%03d.eml", i), coretest.Msg("bob@example.com", "me@example.com", fmt.Sprint("n", i), "b"))
	}
	ch, stop := s.Subscribe()
	defer stop()
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	progress := make(chan events.Event, 1)
	go func() { // cancel at the first progress after some mail went in
		for ev := range ch {
			if ev.Type == events.ImportProgress && ev.Done > 0 {
				cancel()
				progress <- ev
				return
			}
		}
	}()

	sum, err := s.ImportEML(cctx, acc.ID, inbox.ID, []string{dir})
	require.NoError(t, err)
	ev := <-progress
	require.Equal(t, 120, ev.Total)
	require.Equal(t, acc.ID, ev.AccountID)
	require.True(t, sum.Canceled)
	require.Positive(t, sum.Imported)
	require.Less(t, sum.Imported, 120)
	require.Empty(t, sum.Failed)

	page, err := s.ListMessages(ctx, core.View{FolderID: inbox.ID}, "", 200)
	require.NoError(t, err)
	require.Len(t, page.Items, sum.Imported)
}

func TestImportEMLOnlyIntoALocalArchive(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	mailAcc := e.AddAccount(t, "me@example.com")
	path := writeEML(t, t.TempDir(), "a.eml", coretest.Msg("bob@example.com", "me@example.com", "Hi", "x"))
	acc, inbox := newArchive(t, e.Svc)
	_, err := e.Svc.ImportEML(ctx, mailAcc.ID, inbox.ID, []string{path})
	require.Error(t, err, "a mail account")

	other, _ := newArchive(t, e.Svc)
	_, err = e.Svc.ImportEML(ctx, other.ID, inbox.ID, []string{path})
	require.Error(t, err, "a folder of another account")
	_, err = e.Svc.ImportEML(ctx, acc.ID, inbox.ID, []string{path})
	require.NoError(t, err)
}

// importOne imports raw, whose subject has "invoice", into the local archive
// and returns its message.
func importOne(t *testing.T, s *core.Service, acc core.Account, inbox core.Folder, raw []byte) core.MessageSummary {
	t.Helper()
	sum, err := s.ImportEML(ctx, acc.ID, inbox.ID, []string{writeEML(t, t.TempDir(), "m.eml", raw)})
	require.NoError(t, err)
	require.Equal(t, 1, sum.Imported, sum)
	hits, err := s.Search(ctx, acc.ID, "invoice", 10)
	require.NoError(t, err)
	require.Len(t, hits, 1)
	return hits[0]
}

// billsRule saves a rule on mail whose subject has "invoice", its label
// actions putting on a new label Bills.
func billsRule(t *testing.T, s *core.Service, actions ...core.RuleAction) (core.Label, int64) {
	t.Helper()
	l, err := s.CreateLabel(ctx, core.LabelInput{Name: "Bills", Color: "gray", Icon: "folder-3"})
	require.NoError(t, err)
	for i := range actions {
		if actions[i].Kind == core.ActionLabel {
			actions[i].Label = l.Slug
		}
	}
	id, err := s.SaveRule(ctx, core.Rule{Enabled: true, Doc: core.RuleDoc{Version: core.RuleVersion, Name: "Bills", Trigger: core.TriggerReceive, Match: core.MatchAll,
		Conditions: []core.RuleCondition{{Field: core.FieldSubject, Op: core.OpContains, Value: "invoice"}}, Actions: actions}})
	require.NoError(t, err)
	return l, id
}

func labeled(t *testing.T, s *core.Service, id, labelID int64) bool {
	t.Helper()
	got, err := s.MessageLabels(ctx, []int64{id})
	require.NoError(t, err)
	for _, a := range got[id] {
		if a.LabelID == labelID {
			return true
		}
	}
	return false
}

func TestImportEMLRunsNoReceiveRule(t *testing.T) {
	e := coretest.New(t)
	mailAcc := e.AddAccount(t, "me@example.com")
	l, _ := billsRule(t, e.Svc, core.RuleAction{Kind: core.ActionLabel})
	acc, inbox := newArchive(t, e.Svc)
	old := importOne(t, e.Svc, acc, inbox, coretest.Msg("billing@example.com", "me@example.com", "Old invoice", "b"))

	// New mail after it gets the label: the rule ran past the import.
	e.Deliver(t, mailAcc.ID, "INBOX", coretest.Msg("billing@example.com", "me@example.com", "New invoice", "b"))
	require.Eventually(t, func() bool {
		hits, err := e.Svc.Search(ctx, mailAcc.ID, "invoice", 10)
		return err == nil && len(hits) == 1 && labeled(t, e.Svc, hits[0].ID, l.ID)
	}, 5*time.Second, 20*time.Millisecond)
	require.False(t, labeled(t, e.Svc, old.ID, l.ID))
}

func TestImportEMLRunRuleOnExistingSkipsOutbound(t *testing.T) {
	e := coretest.New(t)
	mailAcc := e.AddAccount(t, "me@example.com")
	acc, inbox := newArchive(t, e.Svc)
	old := importOne(t, e.Svc, acc, inbox, coretest.Msg("billing@example.com", "me@example.com", "Old invoice", "b"))
	l, rule := billsRule(t, e.Svc, core.RuleAction{Kind: core.ActionLabel}, core.RuleAction{Kind: core.ActionForward, To: "boss@example.com"})

	n, err := e.Svc.RunRuleOnExisting(ctx, rule)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.True(t, labeled(t, e.Svc, old.ID, l.ID))
	out, err := e.Svc.ListOutbox(ctx, mailAcc.ID)
	require.NoError(t, err)
	require.Empty(t, out)
	require.Empty(t, e.Sent(mailAcc.ID))
}

func TestLocalArchiveMailIsNotArchivedBySenderOrUnsubscribed(t *testing.T) {
	s := newTestService(t)
	acc, inbox := newArchive(t, s)
	m := importOne(t, s, acc, inbox, coretest.Msg("news@example.com", "me@example.com", "Weekly invoice digest", "b",
		coretest.Header("List-Unsubscribe: <https://example.com/u>, <mailto:u@example.com>"), coretest.Header("List-Unsubscribe-Post: List-Unsubscribe=One-Click")))

	res, err := s.ArchiveSender(ctx, "news@example.com")
	require.NoError(t, err, "a local archive, with no archive folder, does not fail it")
	require.Empty(t, res.Moved)
	got, err := s.GetMessage(ctx, m.ID, false)
	require.NoError(t, err)
	require.Equal(t, inbox.ID, got.FolderID)

	u, err := s.UnsubscribeMethod(ctx, m.ID)
	require.NoError(t, err)
	require.Equal(t, core.Unsubscribe{}, u)
}
