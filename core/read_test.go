package core

import (
	"os"
	"strings"
	"testing"

	"github.com/godwitmail/godwit/core/internal/testmail"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (e *syncEnv) inboxView(t *testing.T) View {
	t.Helper()
	fs, err := e.s.ListFolders(bg, e.acc.ID)
	require.NoError(t, err)
	for _, f := range fs {
		if f.Role == "inbox" {
			return View{FolderID: f.ID}
		}
	}
	t.Fatal("no inbox")
	return View{}
}

func (e *syncEnv) second(t *testing.T) *syncEnv {
	t.Helper()
	smtpAddr, _ := testmail.StartSMTP(t)
	im := testmail.StartIMAP(t)
	acc, err := e.s.AddAccount(bg, internalInput(t, im.Addr, smtpAddr, "carol@example.com", false))
	require.NoError(t, err)
	return &syncEnv{s: e.s, im: im, acc: acc, dir: e.dir}
}

func TestListThreadsPaging(t *testing.T) {
	e := newSyncEnv(t)
	for i := 1; i <= 5; i++ {
		id := string(rune('a' + i))
		e.put(t, "INBOX", eml(id+"@x", "Bob <bob@example.com>", "alice@example.com", "s"+id, "Mon, 0"+string(rune('0'+i))+" Oct 2026 09:00:00 +0000", "b"))
	}
	e.put(t, "INBOX", eml("re@x", "Dan <dan@example.com>", "alice@example.com", "Re: sb", "Tue, 06 Oct 2026 09:00:00 +0000", "b", "In-Reply-To: <b@x>"))
	e.sync(t)
	v := e.inboxView(t)
	var got []string
	cur := ""
	sizes := []int{}
	for {
		p, err := e.s.ListThreads(bg, v, cur, 2)
		require.NoError(t, err)
		sizes = append(sizes, len(p.Items))
		for _, it := range p.Items {
			got = append(got, it.Subject)
		}
		if p.NextCursor == "" {
			break
		}
		cur = p.NextCursor
	}
	assert.Equal(t, []int{2, 2, 1}, sizes)
	assert.Len(t, got, 5)
	assert.Equal(t, "sb", got[0]) // reply on Oct 6 bumps its thread
	p, _ := e.s.ListThreads(bg, v, "", 50)
	assert.Equal(t, 2, p.Items[0].Count)
	assert.Equal(t, 2, p.Items[0].Unread)
	assert.Equal(t, []string{"Dan", "Bob"}, p.Items[0].Participants)

	e2 := e.second(t)
	e2.put(t, "INBOX", eml("c1@x", "Eve <eve@example.com>", "carol@example.com", "other", "Mon, 05 Oct 2026 09:00:00 +0000", "b"))
	e2.sync(t)
	u, err := e.s.ListThreads(bg, View{Unified: "inbox"}, "", 50)
	require.NoError(t, err)
	assert.Len(t, u.Items, 6)
	_, err = e.s.ListThreads(bg, v, "!!bad", 5)
	assert.Error(t, err)
	_, err = e.s.ListThreads(bg, View{}, "", 5)
	assert.Error(t, err)
}

func TestGetThreadOrder(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", eml("o@x", "Bob <bob@example.com>", "alice@example.com", "plan", "Mon, 05 Oct 2026 09:00:00 +0000", "q"))
	e.put(t, "Sent", eml("r@x", "alice@example.com", "bob@example.com", "Re: plan", "Mon, 05 Oct 2026 10:00:00 +0000", "a", "In-Reply-To: <o@x>"))
	e.put(t, "INBOX", eml("r2@x", "Bob <bob@example.com>", "alice@example.com", "Re: plan", "Mon, 05 Oct 2026 08:00:00 +0000", "early", "In-Reply-To: <o@x>"))
	e.sync(t)
	th, err := e.s.GetThread(bg, e.threadOf(t, "o@x"))
	require.NoError(t, err)
	require.Len(t, th.Messages, 3)
	assert.Equal(t, "plan", th.Subject)
	assert.Equal(t, "early", th.Messages[0].Snippet)
	assert.Equal(t, "a", th.Messages[2].Snippet)
	assert.Equal(t, []string{"bob@example.com"}, th.Messages[2].To)
}

func TestGetMessageSanitized(t *testing.T) {
	e := newSyncEnv(t)
	raw, err := os.ReadFile("mail/testdata/hostile.eml")
	require.NoError(t, err)
	e.put(t, "INBOX", string(raw))
	e.sync(t)
	r := e.rows(t, "SELECT id FROM messages")
	m, err := e.s.GetMessage(bg, r[0][0].(int64), false)
	require.NoError(t, err)
	assert.Equal(t, 4, m.RemoteImagesBlocked)
	for _, bad := range []string{"<script", "javascript:", "onerror", "<iframe", " href="} {
		assert.NotContains(t, strings.ToLower(m.HTML), bad)
	}
}

func TestGetMessageInlineImage(t *testing.T) {
	e := newSyncEnv(t)
	raw, err := os.ReadFile("mail/testdata/inline-image.eml")
	require.NoError(t, err)
	e.put(t, "INBOX", string(raw))
	e.sync(t)
	id := e.rows(t, "SELECT id FROM messages")[0][0].(int64)
	m, err := e.s.GetMessage(bg, id, false)
	require.NoError(t, err)
	assert.Contains(t, m.HTML, "data:image/")
	assert.NotZero(t, e.count(t, "SELECT count(*) FROM attachments WHERE cache_path IS NOT NULL"))
}

func TestSearchChineseBodyOnly(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", eml("z@x", "finance@x", "alice@example.com", "通知", "Mon, 05 Oct 2026 09:00:00 +0000", "请查收本月发票和报销单"))
	e.sync(t)
	for q, n := range map[string]int{"发票": 1, "报销单": 1, "不存在": 0, "a%": 0} {
		got, err := e.s.Search(bg, e.acc.ID, q, 10)
		require.NoError(t, err)
		assert.Len(t, got, n, q)
	}
}

func TestSearchQueryIsNotFTSSyntax(t *testing.T) {
	e := newSyncEnv(t)
	_, err := e.s.Search(bg, 0, `"unbalanced AND (`, 10)
	assert.NoError(t, err)
}

func TestSearchAllAccounts(t *testing.T) {
	e := newSyncEnv(t)
	e2 := e.second(t)
	e.put(t, "INBOX", eml("s1@x", "a@x", "alice@example.com", "n", "Mon, 05 Oct 2026 09:00:00 +0000", "发票"))
	e2.put(t, "INBOX", eml("s2@x", "a@x", "carol@example.com", "n", "Mon, 05 Oct 2026 09:00:00 +0000", "发票"))
	e.sync(t)
	e2.sync(t)
	got, err := e.s.Search(bg, 0, "发票", 10)
	require.NoError(t, err)
	assert.Len(t, got, 2)
	got, _ = e.s.Search(bg, e.acc.ID, "发票", 10)
	assert.Len(t, got, 1)
}

func TestListMessagesFlat(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", eml("m1@x", "bob@example.com", "alice@example.com", "t", "Mon, 05 Oct 2026 09:00:00 +0000", "1"))
	e.put(t, "INBOX", eml("m2@x", "bob@example.com", "alice@example.com", "Re: t", "Mon, 05 Oct 2026 10:00:00 +0000", "2", "In-Reply-To: <m1@x>"))
	e.put(t, "INBOX", eml("m3@x", "bob@example.com", "alice@example.com", "Re: t", "Mon, 05 Oct 2026 11:00:00 +0000", "3", "In-Reply-To: <m2@x>"))
	e.sync(t)
	v := e.inboxView(t)
	p1, err := e.s.ListMessages(bg, v, "", 2)
	require.NoError(t, err)
	require.Len(t, p1.Items, 2)
	assert.Equal(t, "3", p1.Items[0].Snippet)
	p2, err := e.s.ListMessages(bg, v, p1.NextCursor, 2)
	require.NoError(t, err)
	require.Len(t, p2.Items, 1)
	assert.Equal(t, "1", p2.Items[0].Snippet)
	assert.Empty(t, p2.NextCursor)
}

func TestGetAttachmentLazy(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", withAttachments("a1@x", "hi", 1))
	e.sync(t)
	aid := e.rows(t, "SELECT id FROM attachments")[0][0].(int64)
	require.Zero(t, e.count(t, "SELECT count(*) FROM attachments WHERE cache_path IS NOT NULL"))
	path, info, err := e.s.GetAttachment(bg, aid)
	require.NoError(t, err)
	assert.Equal(t, "f0.pdf", info.Filename)
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "%PDF-1.4\n", string(b))
	assert.Equal(t, path, e.rows(t, "SELECT cache_path FROM attachments")[0][0])
	e.im.Stop()
	path2, _, err := e.s.GetAttachment(bg, aid)
	require.NoError(t, err)
	assert.Equal(t, path, path2)
}

func TestGetAttachmentNotOnServerAndEvict(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", withAttachments("a1@x", "hi", 2))
	e.sync(t)
	ids := e.rows(t, "SELECT id FROM attachments ORDER BY id")
	p1, _, err := e.s.GetAttachment(bg, ids[0][0].(int64))
	require.NoError(t, err)
	p2, _, err := e.s.GetAttachment(bg, ids[1][0].(int64))
	require.NoError(t, err)
	e.s.evict(bg, 9, ids[1][0].(int64)) // each file is 9 bytes; room for one
	_, err = os.Stat(p1)
	assert.True(t, os.IsNotExist(err))
	_, err = os.Stat(p2)
	assert.NoError(t, err)
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM attachments WHERE cache_path IS NULL"))

	e.rows(t, "SELECT 1")
	_, err = e.s.db.Execute(bg, "UPDATE messages SET server_folder_id = NULL, server_uid = NULL")
	require.NoError(t, err)
	_, _, err = e.s.GetAttachment(bg, ids[0][0].(int64))
	assert.ErrorIs(t, err, ErrNotOnServerYet)
}
