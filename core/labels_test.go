package core

import (
	"sync"
	"testing"

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
	s := newLabelService(t, "", "")
	ls, err := s.ListLabels(t.Context())
	require.NoError(t, err)
	require.Equal(t, "Codes", ls[0].Name)
	require.Equal(t, "Bills", ls[1].Name)
	_, err = s.CreateLabel(t.Context(), LabelInput{Name: "BILLS", Color: "red", Icon: "bill"})
	require.ErrorIs(t, err, ErrLabelExists)
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
	for _, bad := range []LabelInput{{Name: "  ", Color: "red", Icon: "bill"}, {Name: "x", Color: "teal", Icon: "bill"}, {Name: "y", Color: "red", Icon: "nope"},
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
