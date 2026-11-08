package core

import (
	"testing"

	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/require"
)

// Tags written on the mail server as IMAP keywords (by an agent, a script or
// another client) show up in Godwit as labels. The seam is the Service: flags as
// a sync stored them in, labels as the reader asks for them.

// newKeywordService is a label service whose account 1 syncs labels as keywords.
func newKeywordService(t *testing.T) *Service {
	t.Helper()
	s := newLabelService(t, "", "en")
	on := true
	_, err := s.UpdateAccount(t.Context(), 1, AccountPatch{LabelKeywords: &on})
	require.NoError(t, err)
	return s
}

func addMailWithFlags(t *testing.T, s *Service, mid, flags string) int64 {
	t.Helper()
	res, err := s.db.Execute(t.Context(), `INSERT INTO messages(account_id, message_id, subject, from_addr, date, label_fp, flags)
		VALUES (1, ?, 's', 'a@x', 1, ?, ?)`, mid, "fp:"+mid, flags)
	require.NoError(t, err)
	return res.LastInsertID
}

func appliedSlugs(t *testing.T, s *Service, msg int64) []string {
	t.Helper()
	applied, err := s.MessageLabels(t.Context(), []int64{msg})
	require.NoError(t, err)
	ls, err := s.ListLabels(t.Context())
	require.NoError(t, err)
	bySlugID := map[int64]string{}
	for _, l := range ls {
		bySlugID[l.ID] = l.Slug
	}
	var out []string
	for _, a := range applied[msg] {
		out = append(out, bySlugID[a.LabelID])
	}
	return out
}

func TestKeywordOnTheServerBecomesALabel(t *testing.T) {
	s := newKeywordService(t)
	msg := addMailWithFlags(t, s, "a@x", `\Seen cliente-acme`)

	require.NoError(t, s.importKeywordLabels(t.Context(), 1))

	require.Equal(t, []string{"cliente-acme"}, appliedSlugs(t, s, msg))
}

func TestKeywordMatchingAnExistingLabelUsesIt(t *testing.T) {
	s := newKeywordService(t)
	before, err := s.ListLabels(t.Context())
	require.NoError(t, err)
	msg := addMailWithFlags(t, s, "b@x", `newsletter`)

	require.NoError(t, s.importKeywordLabels(t.Context(), 1))

	require.Equal(t, []string{"newsletter"}, appliedSlugs(t, s, msg))
	after, err := s.ListLabels(t.Context())
	require.NoError(t, err)
	require.Len(t, after, len(before), "the preset is reused, not duplicated")
}

func TestSystemFlagsAreNotLabels(t *testing.T) {
	s := newKeywordService(t)
	msg := addMailWithFlags(t, s, "c@x", `\Seen \Flagged $Forwarded $MDNSent NonJunk`)

	require.NoError(t, s.importKeywordLabels(t.Context(), 1))

	require.Empty(t, appliedSlugs(t, s, msg))
}

func TestKeywordRemovedOnTheServerTakesTheLabelOff(t *testing.T) {
	s := newKeywordService(t)
	msg := addMailWithFlags(t, s, "d@x", `cliente-acme`)
	require.NoError(t, s.importKeywordLabels(t.Context(), 1))
	require.Equal(t, []string{"cliente-acme"}, appliedSlugs(t, s, msg))

	_, err := s.db.Execute(t.Context(), "UPDATE messages SET flags = '\\Seen' WHERE id = ?", msg)
	require.NoError(t, err)
	require.NoError(t, s.importKeywordLabels(t.Context(), 1))

	require.Empty(t, appliedSlugs(t, s, msg))
}

func TestHandMadeChoiceBeatsTheServerKeyword(t *testing.T) {
	s := newKeywordService(t)
	msg := addMailWithFlags(t, s, "e@x", `newsletter`)
	news := labelIDs(t, s)["newsletter"]
	require.NoError(t, s.SetLabel(t.Context(), []int64{msg}, news, false)) // taken off by hand
	// The server puts the keyword back (another client), before or after our STORE.
	_, err := s.db.Execute(t.Context(), "UPDATE messages SET flags = 'newsletter' WHERE id = ?", msg)
	require.NoError(t, err)

	require.NoError(t, s.importKeywordLabels(t.Context(), 1))

	require.Empty(t, appliedSlugs(t, s, msg), "a label removed by hand is not put back by the import")
}

// The other way round: a label put on by hand is written to the server as a keyword.

func pendingFlagOps(t *testing.T, s *Service, msg int64) []string {
	t.Helper()
	rows, err := s.db.Query(t.Context(), "SELECT payload FROM ops WHERE message_id = ? AND kind = 'flags' ORDER BY id", msg)
	require.NoError(t, err)
	var out []string
	for _, v := range rows.Values {
		out = append(out, store.Str(v[0]))
	}
	return out
}

func TestLabelPutOnByHandIsQueuedAsAServerKeyword(t *testing.T) {
	s := newKeywordService(t)
	msg := addMailWithFlags(t, s, "f@x", `\Seen`)
	news := labelIDs(t, s)["newsletter"]

	require.NoError(t, s.SetLabel(t.Context(), []int64{msg}, news, true))

	require.Equal(t, []string{`{"add":["newsletter"]}`}, pendingFlagOps(t, s, msg))
	rows, err := s.db.Query(t.Context(), "SELECT flags FROM messages WHERE id = ?", msg)
	require.NoError(t, err)
	require.Contains(t, store.Str(rows.Values[0][0]), "newsletter")
}

func TestLabelTakenOffByHandRemovesTheServerKeyword(t *testing.T) {
	s := newKeywordService(t)
	msg := addMailWithFlags(t, s, "g@x", `newsletter`)
	news := labelIDs(t, s)["newsletter"]

	require.NoError(t, s.SetLabel(t.Context(), []int64{msg}, news, false))

	require.Equal(t, []string{`{"remove":["newsletter"]}`}, pendingFlagOps(t, s, msg))
}

func TestLabelMadeHereIsNotAServerKeyword(t *testing.T) {
	s := newKeywordService(t)
	msg := addMailWithFlags(t, s, "h@x", `\Seen u1`) // another device's own label
	l, err := s.CreateLabel(t.Context(), LabelInput{Name: "Clients", Color: "gray", Icon: "folder-3"})
	require.NoError(t, err)

	require.NoError(t, s.SetLabel(t.Context(), []int64{msg}, l.ID, true))
	require.NoError(t, s.importKeywordLabels(t.Context(), 1))

	require.Empty(t, pendingFlagOps(t, s, msg), "u<id> differs from device to device")
	require.Equal(t, []string{l.Slug}, appliedSlugs(t, s, msg))
}

func TestDeletedLabelDoesNotComeBackFromItsKeyword(t *testing.T) {
	s := newKeywordService(t)
	msg := addMailWithFlags(t, s, "i@x", `newsletter`)
	require.NoError(t, s.importKeywordLabels(t.Context(), 1))

	require.NoError(t, s.DeleteLabel(t.Context(), labelIDs(t, s)["newsletter"]))
	require.NoError(t, s.importKeywordLabels(t.Context(), 1))

	require.NotContains(t, labelIDs(t, s), "newsletter")
	require.Equal(t, []string{`{"remove":["newsletter"]}`}, pendingFlagOps(t, s, msg))
}

func TestAccountThatHasNotTurnedItOnKeepsLabelsLocal(t *testing.T) {
	s := newLabelService(t, "", "en")
	msg := addMailWithFlags(t, s, "j@x", `cliente-acme`)

	require.NoError(t, s.importKeywordLabels(t.Context(), 1))
	require.NoError(t, s.SetLabel(t.Context(), []int64{msg}, labelIDs(t, s)["newsletter"], true))

	require.Equal(t, []string{"newsletter"}, appliedSlugs(t, s, msg), "only the label set by hand")
	require.Empty(t, pendingFlagOps(t, s, msg))
}

func TestTurningItOnImportsTheKeywordsAtOnce(t *testing.T) {
	s := newLabelService(t, "", "en")
	msg := addMailWithFlags(t, s, "k@x", `cliente-acme`)

	on := true
	acc, err := s.UpdateAccount(t.Context(), 1, AccountPatch{LabelKeywords: &on})
	require.NoError(t, err)

	require.True(t, acc.LabelKeywords)
	require.Equal(t, []string{"cliente-acme"}, appliedSlugs(t, s, msg))
}
