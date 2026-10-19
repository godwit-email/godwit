package core

import (
	"strings"
	"sync"
	"testing"

	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func labelIDs(t *testing.T, s *Service) map[string]int64 {
	t.Helper()
	ls, err := s.ListLabels(t.Context())
	require.NoError(t, err)
	m := map[string]int64{}
	for _, l := range ls {
		m[l.Slug] = l.ID
	}
	return m
}

func labelSlugs(ls []Label) []string {
	var out []string
	for _, l := range ls {
		out = append(out, l.Slug)
	}
	return out
}

func TestSeedLabelsChinese(t *testing.T) {
	dir := t.TempDir()
	s := newLabelService(t, dir, "zh-CN")
	ls, err := s.ListLabels(t.Context())
	require.NoError(t, err)
	require.Equal(t, []string{"otp", "bill", "order", "travel", "calendar", "notice", "newsletter"}, labelSlugs(ls))
	require.Equal(t, "验证码", ls[0].Name)
	require.Equal(t, "red", ls[0].Color)
	require.Equal(t, "shield-keyhole", ls[0].Icon)
	require.True(t, ls[0].Builtin)
	require.NotEmpty(t, ls[0].Description)
	require.NoError(t, s.DeleteLabel(t.Context(), ls[0].ID))
	s.Close()

	s = newLabelService(t, dir, "zh-CN")
	ls, err = s.ListLabels(t.Context())
	require.NoError(t, err)
	require.Len(t, ls, 6, "a deleted preset is not seeded again")
}

func TestSeedLabelsEnglish(t *testing.T) {
	s := newLabelService(t, "", "en")
	ls, err := s.ListLabels(t.Context())
	require.NoError(t, err)
	require.Equal(t, "Codes", ls[0].Name)
	require.Equal(t, "Bills", ls[1].Name)
	_, err = s.CreateLabel(t.Context(), LabelInput{Name: "BILLS", Color: "red", Icon: "bill"})
	require.ErrorIs(t, err, ErrLabelExists)

	for locale, want := range map[string]string{
		"zh-TW": "驗證碼",
		"de-DE": "Codes",
		"ja-JP": "認証コード",
		"ko-KR": "인증번호",
		"fr-FR": "Codes",
		"es-ES": "Códigos",
		"ru-RU": "Коды",
	} {
		s := newLabelService(t, "", locale)
		ls, err := s.ListLabels(t.Context())
		require.NoError(t, err)
		require.Equal(t, want, ls[0].Name, locale)
	}
}

func TestLabelCRUD(t *testing.T) {
	s := newLabelService(t, "", "zh-CN")
	ctx := t.Context()
	l, err := s.CreateLabel(ctx, LabelInput{Name: " 家人 ", Color: "pink", Icon: "heart-3"})
	require.NoError(t, err)
	require.Equal(t, "家人", l.Name)
	require.Equal(t, "u8", l.Slug)
	require.Equal(t, 7, l.Position)
	require.False(t, l.Builtin)

	l, err = s.UpdateLabel(ctx, l.ID, LabelInput{Name: "家庭", Description: "家里人", Color: "pink", Icon: "home-4"})
	require.NoError(t, err)
	require.Equal(t, "u8", l.Slug)
	require.Equal(t, "家庭", l.Name)
	require.Equal(t, "home-4", l.Icon)

	_, err = s.CreateLabel(ctx, LabelInput{Name: "账单 ", Color: "red", Icon: "bill"})
	require.ErrorIs(t, err, ErrLabelExists)
	_, err = s.UpdateLabel(ctx, l.ID, LabelInput{Name: "账单", Color: "red", Icon: "bill"})
	require.ErrorIs(t, err, ErrLabelExists)
	_, err = s.CreateLabel(ctx, LabelInput{Name: "Équipe", Color: "red", Icon: "team"})
	require.NoError(t, err)
	_, err = s.CreateLabel(ctx, LabelInput{Name: "équipe", Color: "red", Icon: "team"})
	require.ErrorIs(t, err, ErrLabelExists)
	for _, bad := range []LabelInput{{Name: "  ", Color: "red", Icon: "bill"}, {Name: "x", Color: "rose", Icon: "bill"}, {Name: "y", Color: "red", Icon: "nope"},
		{Name: "一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十一二三", Color: "red", Icon: "bill"}} {
		_, err = s.CreateLabel(ctx, bad)
		require.Error(t, err, "%+v", bad)
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Go(func() { _, errs[i] = s.CreateLabel(ctx, LabelInput{Name: "旅行", Color: "blue", Icon: "plane"}) })
	}
	wg.Wait()
	ok := 0
	for _, e := range errs {
		if e == nil {
			ok++
		} else {
			require.ErrorIs(t, e, ErrLabelExists)
		}
	}
	require.Equal(t, 1, ok, "exactly one of two concurrent creates wins")

	ls, err := s.ListLabels(ctx)
	require.NoError(t, err)
	ids := make([]int64, len(ls))
	for i, x := range ls {
		ids[len(ls)-1-i] = x.ID
	}
	require.NoError(t, s.ReorderLabels(ctx, ids))
	got, err := s.ListLabels(ctx)
	require.NoError(t, err)
	require.Equal(t, ls[len(ls)-1].ID, got[0].ID)
	require.Equal(t, ls[0].ID, got[len(got)-1].ID)
	require.Error(t, s.ReorderLabels(ctx, ids[1:]), "every label must be named")

	m := addMessage(t, s, "s", "a@x", "", "", 1)
	_, err = s.db.Execute(ctx, "INSERT INTO message_labels(message_id, label_id, source, active) VALUES (?, ?, 'auto', 1)", m, l.ID)
	require.NoError(t, err)
	require.NoError(t, s.DeleteLabel(ctx, l.ID))
	rows, err := s.db.Query(ctx, "SELECT count(*) FROM message_labels WHERE label_id = ?", l.ID)
	require.NoError(t, err)
	require.EqualValues(t, 0, rows.Values[0][0])
}

func TestLabelCounts(t *testing.T) {
	s := newLabelService(t, "", "zh-CN")
	ctx := t.Context()
	otp := labelIDs(t, s)["otp"]
	a := addMessage(t, s, "a", "a@x", "", "", 1)
	b := addMessage(t, s, "b", "b@x", "", "", 2)
	c := addMessage(t, s, "c", "c@x", "", "", 3)
	_, err := s.db.Execute(ctx, `UPDATE messages SET flags = '\Seen' WHERE id = ?`, b)
	require.NoError(t, err)
	for id, active := range map[int64]int{a: 1, b: 1, c: 0} {
		_, err = s.db.Execute(ctx, "INSERT INTO message_labels(message_id, label_id, source, active) VALUES (?, ?, 'manual', ?)", id, otp, active)
		require.NoError(t, err)
	}
	ls, err := s.ListLabels(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, ls[0].Count)
	require.Equal(t, 1, ls[0].Unread)
}

// labelRow is a message_labels row as (source, active), or ("", -1) for none.
func labelRow(t *testing.T, s *Service, msg, label int64) (string, int) {
	t.Helper()
	rows, err := s.db.Query(t.Context(), "SELECT source, active FROM message_labels WHERE message_id = ? AND label_id = ?", msg, label)
	require.NoError(t, err)
	if len(rows.Values) == 0 {
		return "", -1
	}
	return store.Str(rows.Values[0][0]), int(store.Int(rows.Values[0][1]))
}

func setRow(t *testing.T, s *Service, msg, label int64, source string, active int) {
	t.Helper()
	_, err := s.db.Execute(t.Context(), `INSERT INTO message_labels(message_id, label_id, source, active) VALUES (?, ?, ?, ?)
		ON CONFLICT(message_id, label_id) DO UPDATE SET source = excluded.source, active = excluded.active`, msg, label, source, active)
	require.NoError(t, err)
}

func TestSetLabelManual(t *testing.T) {
	s := newLabelService(t, "", "zh-CN")
	ctx := t.Context()
	l := labelIDs(t, s)
	a := addMessage(t, s, "a", "a@x", "", "", 1)
	b := addMessage(t, s, "b", "b@x", "", "", 2)
	setRow(t, s, a, l["otp"], "auto", 1)
	require.NoError(t, s.SetLabel(ctx, []int64{a}, l["otp"], false))
	src, on := labelRow(t, s, a, l["otp"])
	require.Equal(t, "manual", src)
	require.Equal(t, 0, on)
	got, err := s.MessageLabels(ctx, []int64{a})
	require.NoError(t, err)
	require.Empty(t, got[a])

	require.NoError(t, s.SetLabel(ctx, []int64{a, b}, l["bill"], true))
	got, err = s.MessageLabels(ctx, []int64{a, b})
	require.NoError(t, err)
	require.Equal(t, []AppliedLabel{{LabelID: l["bill"]}}, got[a])
	require.Equal(t, []AppliedLabel{{LabelID: l["bill"]}}, got[b])
}

func TestSetLabelAtomic(t *testing.T) {
	s := newLabelService(t, "", "zh-CN")
	a := addMessage(t, s, "a", "a@x", "", "", 1)
	otp := labelIDs(t, s)["otp"]
	require.Error(t, s.SetLabel(t.Context(), []int64{a, 999999}, otp, true))
	_, on := labelRow(t, s, a, otp)
	require.Equal(t, -1, on, "nothing written when one message is missing")
}

func TestThreadLabels(t *testing.T) {
	s := newLabelService(t, "", "zh-CN")
	ctx := t.Context()
	l := labelIDs(t, s)
	_, err := s.db.Execute(ctx, "INSERT INTO threads(id, account_id, subject, last_date) VALUES (1, 1, 't', 2), (2, 1, 'u', 3)")
	require.NoError(t, err)
	a := addMessage(t, s, "a", "a@x", "", "", 1)
	b := addMessage(t, s, "b", "b@x", "", "", 2)
	c := addMessage(t, s, "c", "c@x", "", "", 3)
	_, err = s.db.Execute(ctx, "UPDATE messages SET thread_id = CASE id WHEN ? THEN 2 ELSE 1 END", c)
	require.NoError(t, err)
	setRow(t, s, a, l["otp"], "auto", 1)
	setRow(t, s, b, l["otp"], "manual", 1)
	setRow(t, s, c, l["bill"], "auto", 1)
	setRow(t, s, c, l["otp"], "auto", 1)
	setRow(t, s, a, l["notice"], "auto", 0)
	got, err := s.ThreadLabels(ctx, []int64{1, 2})
	require.NoError(t, err)
	require.Equal(t, []AppliedLabel{{LabelID: l["otp"]}}, got[1], "one manual row makes the thread's label manual")
	require.Equal(t, []AppliedLabel{{LabelID: l["otp"], Auto: true}, {LabelID: l["bill"], Auto: true}}, got[2], "in the labels' order")
}

func TestLabelView(t *testing.T) {
	s := newLabelService(t, "", "zh-CN")
	ctx := t.Context()
	otp := labelIDs(t, s)["otp"]
	a := addMessage(t, s, "a", "a@x", "", "", 1)
	b := addMessage(t, s, "b", "b@x", "", "", 2)
	setRow(t, s, a, otp, "auto", 1)
	setRow(t, s, b, otp, "manual", 0)
	p, err := s.ListMessages(ctx, View{LabelID: otp}, "", 10)
	require.NoError(t, err)
	require.Len(t, p.Items, 1)
	require.Equal(t, a, p.Items[0].ID)
	_, err = s.ListMessages(ctx, View{LabelID: otp, Unified: "inbox"}, "", 10)
	require.Error(t, err)
	_, err = s.ListMessages(ctx, View{LabelID: otp, FolderID: 1}, "", 10)
	require.Error(t, err)
}

// TestSeedLabelsWaitsForLanguage: a first start that does not know the
// language (headless) seeds nothing, so the GUI seeds in the user's.
func TestSeedLabelsWaitsForLanguage(t *testing.T) {
	dir := t.TempDir()
	s := newLabelService(t, dir, "")
	ls, err := s.ListLabels(t.Context())
	require.NoError(t, err)
	require.Empty(t, ls)
	s.Close()
	s = newLabelService(t, dir, "zh-CN")
	ls, err = s.ListLabels(t.Context())
	require.NoError(t, err)
	require.Len(t, ls, 7)
	require.Equal(t, "验证码", ls[0].Name)
}

// TestLabelsSurviveUIDValidityReset: a folder's UIDVALIDITY change drops its
// cached messages; the labels set by hand, a removal too, come back with the
// refetched copies (by Message-ID).
func TestLabelsSurviveUIDValidityReset(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "Archive", eml("kept@x", "bob@example.com", "alice@example.com", "s", day, "body"))
	e.sync(t)
	on, err := e.s.CreateLabel(bg, LabelInput{Name: "Family", Color: "pink", Icon: "heart-3"})
	require.NoError(t, err)
	off, err := e.s.CreateLabel(bg, LabelInput{Name: "Work", Color: "blue", Icon: "briefcase-4"})
	require.NoError(t, err)
	old := store.Int(e.rows(t, "SELECT id FROM messages WHERE message_id = 'kept@x'")[0][0])
	require.NoError(t, e.s.SetLabel(bg, []int64{old}, on.ID, true))
	require.NoError(t, e.s.SetLabel(bg, []int64{old}, off.ID, false))

	require.NoError(t, e.im.User.Delete("Archive"))
	require.NoError(t, e.im.User.Create("Archive", nil))
	e.put(t, "Archive", eml("kept@x", "bob@example.com", "alice@example.com", "s", day, "body"))
	e.sync(t)

	id := store.Int(e.rows(t, "SELECT id FROM messages WHERE message_id = 'kept@x'")[0][0])
	require.NotEqual(t, old, id, "the message was refetched")
	assert.Equal(t, [][]any{{on.ID, "manual", int64(1)}, {off.ID, "manual", int64(0)}},
		e.rows(t, "SELECT label_id, source, active FROM message_labels WHERE message_id = ? ORDER BY label_id", id))
}

// TestLabelsSurviveResetForEachCopy: two copies of one message (one
// Message-ID) in a folder both get their hand-set label back.
func TestLabelsSurviveResetForEachCopy(t *testing.T) {
	e := newSyncEnv(t)
	for range 2 {
		e.put(t, "Archive", eml("twin@x", "bob@example.com", "alice@example.com", "s", day, "body"))
	}
	e.sync(t)
	l, err := e.s.CreateLabel(bg, LabelInput{Name: "Family", Color: "pink", Icon: "heart-3"})
	require.NoError(t, err)
	var ids []int64
	for _, r := range e.rows(t, "SELECT id FROM messages WHERE message_id = 'twin@x'") {
		ids = append(ids, store.Int(r[0]))
	}
	require.Len(t, ids, 2)
	require.NoError(t, e.s.SetLabel(bg, ids, l.ID, true))

	require.NoError(t, e.im.User.Delete("Archive"))
	require.NoError(t, e.im.User.Create("Archive", nil))
	for range 2 {
		e.put(t, "Archive", eml("twin@x", "bob@example.com", "alice@example.com", "s", day, "body"))
	}
	e.sync(t)

	assert.Equal(t, 2, e.count(t, "SELECT count(*) FROM messages WHERE message_id = 'twin@x'"))
	assert.Equal(t, 2, e.count(t, `SELECT count(*) FROM message_labels ml JOIN messages m ON m.id = ml.message_id
		WHERE m.message_id = 'twin@x' AND ml.label_id = ? AND ml.source = 'manual' AND ml.active = 1`, l.ID))
}

// TestLabelsSurviveResetWithoutMessageID: mail with no Message-ID keeps its
// hand-set labels across a reset too (by sender, date and subject).
func TestLabelsSurviveResetWithoutMessageID(t *testing.T) {
	e := newSyncEnv(t)
	raw := "From: bob@example.com\r\nTo: alice@example.com\r\nSubject: no id\r\nDate: " + day + "\r\nContent-Type: text/plain\r\n\r\nbody\r\n"
	e.put(t, "Archive", raw)
	e.sync(t)
	l, err := e.s.CreateLabel(bg, LabelInput{Name: "Family", Color: "pink", Icon: "heart-3"})
	require.NoError(t, err)
	old := store.Int(e.rows(t, "SELECT id FROM messages WHERE subject = 'no id'")[0][0])
	require.NoError(t, e.s.SetLabel(bg, []int64{old}, l.ID, false))

	require.NoError(t, e.im.User.Delete("Archive"))
	require.NoError(t, e.im.User.Create("Archive", nil))
	e.put(t, "Archive", raw)
	e.sync(t)

	id := store.Int(e.rows(t, "SELECT id FROM messages WHERE subject = 'no id'")[0][0])
	require.NotEqual(t, old, id)
	assert.Equal(t, [][]any{{"manual", int64(0)}}, e.rows(t, "SELECT source, active FROM message_labels WHERE message_id = ?", id))
}

// addCopy inserts a cached copy of a message with a Message-ID, as an import does.
func addCopy(t *testing.T, s *Service, mid string) int64 {
	t.Helper()
	res, err := s.db.Execute(t.Context(), `INSERT INTO messages(account_id, folder_id, server_folder_id, message_id, subject, from_addr, date, label_fp)
		VALUES (1, 1, 1, ?, 's', 'a@x', 1, ?)`, mid, "fp:"+mid)
	require.NoError(t, err)
	return res.LastInsertID
}

// TestLabelsMoveToTheOtherCopy: a cached row dropped while another copy of the
// message exists (a move the server did not report) hands it its labels.
func TestLabelsMoveToTheOtherCopy(t *testing.T) {
	s := newLabelService(t, "", "zh-CN")
	otp := labelIDs(t, s)["otp"]
	a := addCopy(t, s, "m@x")
	b := addCopy(t, s, "m@x")
	require.NoError(t, s.SetLabel(t.Context(), []int64{a}, otp, false))
	_, err := s.db.Execute(t.Context(), "DELETE FROM messages WHERE id = ?", a)
	require.NoError(t, err)
	src, on := labelRow(t, s, b, otp)
	require.Equal(t, "manual", src)
	require.Equal(t, 0, on)
}

// TestKeptLabelsFollowLaterEdits: a copy arriving after the user changed a
// restored label takes the current choice, not the kept old one.
func TestKeptLabelsFollowLaterEdits(t *testing.T) {
	s := newLabelService(t, "", "zh-CN")
	otp := labelIDs(t, s)["otp"]
	a := addCopy(t, s, "m@x")
	require.NoError(t, s.SetLabel(t.Context(), []int64{a}, otp, true))
	_, err := s.db.Execute(t.Context(), "DELETE FROM messages WHERE id = ?", a)
	require.NoError(t, err)
	b := addCopy(t, s, "m@x") // refetched: restored on
	_, on := labelRow(t, s, b, otp)
	require.Equal(t, 1, on)
	require.NoError(t, s.SetLabel(t.Context(), []int64{b}, otp, false))
	c := addCopy(t, s, "m@x") // a later copy
	_, on = labelRow(t, s, c, otp)
	require.Equal(t, 0, on, "the later copy follows the user's last choice")
}

// TestKeptLabelsTakeNewChoices: a hand-set choice on a label the stash did not
// hold yet is kept too, for copies that turn up later.
func TestKeptLabelsTakeNewChoices(t *testing.T) {
	s := newLabelService(t, "", "zh-CN")
	l := labelIDs(t, s)
	a := addCopy(t, s, "m@x")
	require.NoError(t, s.SetLabel(t.Context(), []int64{a}, l["otp"], true))
	_, err := s.db.Execute(t.Context(), "DELETE FROM messages WHERE id = ?", a)
	require.NoError(t, err)
	b := addCopy(t, s, "m@x")
	require.NoError(t, s.SetLabel(t.Context(), []int64{b}, l["bill"], false)) // not in the stash
	c := addCopy(t, s, "m@x")
	src, on := labelRow(t, s, c, l["bill"])
	require.Equal(t, "manual", src)
	require.Equal(t, 0, on)
}

// TestFingerprintTellsBodiesApart: two messages with no Message-ID and the
// same sender, date and subject but different bodies are not one message.
func TestFingerprintTellsBodiesApart(t *testing.T) {
	e := newSyncEnv(t)
	raw := func(body string) string {
		return "From: bob@example.com\r\nTo: alice@example.com\r\nSubject: same\r\nDate: " + day + "\r\nContent-Type: text/plain\r\n\r\n" + body + "\r\n"
	}
	e.put(t, "Archive", raw("first body, padded to one size"))
	e.put(t, "Archive", raw("other body, padded to one size"))
	e.sync(t)
	l, err := e.s.CreateLabel(bg, LabelInput{Name: "Family", Color: "pink", Icon: "heart-3"})
	require.NoError(t, err)
	ids := e.rows(t, "SELECT id FROM messages WHERE subject = 'same' ORDER BY id")
	require.Len(t, ids, 2)
	a, b := store.Int(ids[0][0]), store.Int(ids[1][0])
	require.NoError(t, e.s.SetLabel(bg, []int64{a}, l.ID, true))
	_, err = e.s.db.Execute(bg, "DELETE FROM messages WHERE id = ?", a)
	require.NoError(t, err)
	assert.Zero(t, e.count(t, "SELECT count(*) FROM message_labels WHERE message_id = ?", b), "the other message is not this one")
}

// TestLabelKeepHoldsNoMailText: the stash keys a dropped message by a hash,
// never by its headers or text.
func TestLabelKeepHoldsNoMailText(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "Archive", "From: bob@example.com\r\nTo: alice@example.com\r\nSubject: secret plans\r\nDate: "+day+"\r\nContent-Type: text/plain\r\n\r\nbody\r\n")
	e.sync(t)
	l, err := e.s.CreateLabel(bg, LabelInput{Name: "Family", Color: "pink", Icon: "heart-3"})
	require.NoError(t, err)
	id := store.Int(e.rows(t, "SELECT id FROM messages WHERE subject = 'secret plans'")[0][0])
	require.NoError(t, e.s.SetLabel(bg, []int64{id}, l.ID, true))
	_, err = e.s.db.Execute(bg, "DELETE FROM messages WHERE id = ?", id)
	require.NoError(t, err)
	fps := e.rows(t, "SELECT fp FROM label_keep")
	require.Len(t, fps, 1)
	assert.Regexp(t, `^[0-9a-f]{64}$`, store.Str(fps[0][0]))
}

// TestSeedSkipsTakenNames: a label made before the presets (headless, through
// the Local API) keeps its name; the preset of that name is not added.
func TestSeedSkipsTakenNames(t *testing.T) {
	dir := t.TempDir()
	s := newLabelService(t, dir, "")
	_, err := s.CreateLabel(t.Context(), LabelInput{Name: "验证码", Color: "blue", Icon: "key-2"})
	require.NoError(t, err)
	s.Close()
	s = newLabelService(t, dir, "zh-CN")
	ls, err := s.ListLabels(t.Context())
	require.NoError(t, err)
	n := 0
	for _, l := range ls {
		if l.Name == "验证码" {
			n++
			require.Equal(t, "blue", l.Color, "the user's own label stays")
		}
	}
	require.Equal(t, 1, n)
	require.Len(t, ls, 7, "six presets and the user's label")
}

// TestLabelDescriptionLimit: a description goes into every judge request, so
// it is bounded.
func TestLabelDescriptionLimit(t *testing.T) {
	s := newLabelService(t, "", "zh-CN")
	_, err := s.CreateLabel(t.Context(), LabelInput{Name: "长", Description: strings.Repeat("字", MaxLabelDescription), Color: "red", Icon: "bill"})
	require.NoError(t, err)
	_, err = s.CreateLabel(t.Context(), LabelInput{Name: "更长", Description: strings.Repeat("字", MaxLabelDescription+1), Color: "red", Icon: "bill"})
	require.Error(t, err)
}

// TestNoFingerprintForPartialContent: mail with no Message-ID whose content
// we hold only in part (attachments are not fetched) gets no fingerprint, so
// its labels are never handed to another message.
func TestNoFingerprintForPartialContent(t *testing.T) {
	e := newSyncEnv(t)
	_, withFiles, _ := strings.Cut(withAttachments("x@x", "body", 1), "\r\n") // drop the Message-ID line
	e.put(t, "Archive", withFiles)
	e.put(t, "Archive", "From: bob@example.com\r\nTo: alice@example.com\r\nSubject: plain\r\nDate: "+day+"\r\nContent-Type: text/plain\r\n\r\nbody\r\n")
	e.sync(t)
	assert.Equal(t, [][]any{{nil}}, e.rows(t, "SELECT label_fp FROM messages WHERE has_attachments = 1"))
	assert.Len(t, store.Str(e.rows(t, "SELECT label_fp FROM messages WHERE subject = 'plain'")[0][0]), 64)
}
