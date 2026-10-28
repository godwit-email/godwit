package core

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSearchWithFilters(t *testing.T) {
	s := newMailContextService(t)
	_, err := s.db.Execute(t.Context(), "INSERT INTO accounts(id, email) VALUES (2, 'two@x')")
	require.NoError(t, err)
	_, err = s.db.Execute(t.Context(), "INSERT INTO folders(id, account_id, name, role) VALUES (2, 2, 'INBOX', 'inbox')")
	require.NoError(t, err)
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	old := addMessage(t, s, "budget plan", "Bob <bob@x>", "q3 numbers", "", day.Unix())
	recent := addMessage(t, s, "budget review", "Carol <carol_1@x>", "q4 numbers", "", day.AddDate(0, 0, 9).Unix())
	_, err = s.db.Execute(t.Context(), "UPDATE messages SET has_attachments = 1 WHERE id = ?", recent)
	require.NoError(t, err)
	other := addMessage(t, s, "budget elsewhere", "Bob <bob@x>", "", "", day.AddDate(0, 0, 5).Unix())
	_, err = s.db.Execute(t.Context(), "UPDATE messages SET account_id = 2, folder_id = 2 WHERE id = ?", other)
	require.NoError(t, err)

	ids := func(q SearchQuery) []int64 {
		t.Helper()
		got, err := s.SearchWith(t.Context(), q, 10)
		require.NoError(t, err)
		var out []int64
		for _, m := range got {
			out = append(out, m.ID)
		}
		return out
	}
	require.Equal(t, []int64{recent, other, old}, ids(SearchQuery{Text: "budget"}))
	require.Equal(t, []int64{recent, old}, ids(SearchQuery{Text: "budget", AccountID: 1}))
	require.Equal(t, []int64{other, old}, ids(SearchQuery{Text: "budget", From: "BOB@"}), "sender, any case")
	require.Equal(t, []int64{recent}, ids(SearchQuery{From: "carol_"}), "no text, a filter: _ is literal")
	require.Empty(t, ids(SearchQuery{From: "carol%1"}), "% is literal")
	notbob := addMessage(t, s, "budget", "notbob@x", "", "", day.AddDate(0, 0, 2).Unix())
	require.Equal(t, []int64{other, notbob, old}, ids(SearchQuery{From: "bob@x"}), "From is a substring")
	require.Equal(t, []int64{other, old}, ids(SearchQuery{FromAddr: "BOB@x"}), "FromAddr is the address exactly, any case")
	require.Equal(t, []int64{notbob}, ids(SearchQuery{FromAddr: "notbob@x"}), "a bare address too")
	quoted := addMessage(t, s, "budget", `Bob <"a b"@x>`, "", "", day.AddDate(0, 0, 3).Unix())
	require.Equal(t, []int64{quoted}, ids(SearchQuery{FromAddr: "a b@x"}), "a quoted local part, as a contact keys it (AddrKey)")
	_, err = s.db.Execute(t.Context(), "DELETE FROM messages WHERE id = ?", quoted)
	require.NoError(t, err)
	emile := addMessage(t, s, "budget", "Émile <Émile@x>", "", "", day.AddDate(0, 0, 3).Unix())
	require.Equal(t, []int64{emile}, ids(SearchQuery{FromAddr: "émile@x"}), "Unicode case folds as AddrKey folds it")
	_, err = s.db.Execute(t.Context(), "DELETE FROM messages WHERE id = ?", emile)
	require.NoError(t, err)
	_, err = s.db.Execute(t.Context(), "DELETE FROM messages WHERE id = ?", notbob) // the checks below count without it
	require.NoError(t, err)
	require.Equal(t, []int64{recent, other}, ids(SearchQuery{Since: day.AddDate(0, 0, 1)}))
	require.Equal(t, []int64{old}, ids(SearchQuery{Until: day.AddDate(0, 0, 5)}), "until is exclusive")
	require.Equal(t, []int64{recent}, ids(SearchQuery{HasAttachments: true}))
	require.Equal(t, []int64{old}, ids(SearchQuery{Text: "q3", From: "bob", AccountID: 1, Until: day.AddDate(0, 0, 1)}))
	require.Empty(t, ids(SearchQuery{Text: "budget", HasAttachments: true, From: "bob"}))
	require.Nil(t, ids(SearchQuery{AccountID: 1}), "an account alone is no query")
	require.Nil(t, ids(SearchQuery{Text: "  "}))
	require.True(t, SearchQuery{From: "x"}.Filtered())
	require.False(t, SearchQuery{AccountID: 1, Text: "x"}.Filtered())
	require.True(t, SearchQuery{LabelID: 1}.Filtered())
	l, err := s.CreateLabel(t.Context(), LabelInput{Name: "Codes", Color: "red", Icon: "shield-keyhole"})
	require.NoError(t, err)
	otp := l.ID
	require.NoError(t, s.SetLabel(t.Context(), []int64{recent}, otp, true))
	require.NoError(t, s.SetLabel(t.Context(), []int64{old}, otp, false))
	require.Equal(t, []int64{recent}, ids(SearchQuery{LabelID: otp}))
	require.Equal(t, []int64{recent}, ids(SearchQuery{Text: "budget", LabelID: otp}))
}
