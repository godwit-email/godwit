package webui

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/godwit-email/godwit/core/coretest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// selectionCase is a ⌘K case of testdata/selection.json, which the page's
// tests share: html as the editor saves it, its plain text, and selected,
// [start, end) of it in UTF-16 offsets; range is the stored HTML it maps to.
type selectionCase struct {
	Name, HTML, Text, Selected, Range string
	Start, End                        int
}

func selectionCases(t *testing.T) []selectionCase {
	t.Helper()
	b, err := os.ReadFile("testdata/selection.json")
	require.NoError(t, err)
	var cs []selectionCase
	require.NoError(t, json.Unmarshal(b, &cs))
	return cs
}

func TestComposeRewriteSelection(t *testing.T) {
	f, d, llm := witFixture(t, "<p>x</p>")
	for _, c := range selectionCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			u := utf16.Encode([]rune(c.Text))
			require.Equal(t, c.Selected, string(utf16.Decode(u[c.Start:c.End])), "the case itself")
			res, err := f.page.Save(f.ctx(1), ComposeEdit{Body: c.HTML, Revision: f.stored(t, d.ID).Revision})
			require.NoError(t, err)
			st, err := f.page.Load(f.ctx(1))
			require.NoError(t, err)
			text, offs := plainText(st.Body)
			require.Equal(t, c.Text, text)
			assert.Equal(t, c.Range, st.Body[offs[c.Start]:offs[c.End]])

			llm.Script(coretest.Turn{Text: []string{"ok"}})
			n := len(f.events(1))
			rs, err := f.page.Rewrite(f.ctx(1), res.Revision, c.Text, c.Start, c.End, "更具体一点")
			require.NoError(t, err, "core's checkSelection")
			require.False(t, rs.Conflict)
			require.NotZero(t, rs.Run)
			require.Eventually(t, func() bool {
				for _, ev := range f.events(1)[n:] {
					if ev.Kind == "done" && ev.Run == rs.Run {
						return true
					}
				}
				return false
			}, 10*time.Second, 5*time.Millisecond)
		})
	}
}

func TestComposeRewriteSelectionStale(t *testing.T) {
	f, d, llm := witFixture(t, "<p>First para.</p><p>Second para.</p>")
	rev := f.stored(t, d.ID).Revision
	rs, err := f.page.Rewrite(f.ctx(1), rev, "First para.\nSecond para!", 0, 5, "更具体一点")
	require.NoError(t, err)
	assert.Equal(t, RunStart{Revision: rev, Conflict: true}, rs, "another text")
	rs, err = f.page.Rewrite(f.ctx(1), rev-1, "First para.\nSecond para.", 0, 5, "更具体一点")
	require.NoError(t, err)
	assert.True(t, rs.Conflict, "another revision")
	_, err = f.page.Rewrite(f.ctx(1), rev, "First para.\nSecond para.", 5, 5, "更具体一点")
	assert.ErrorIs(t, err, ErrBadSelection)
	_, err = f.page.Rewrite(f.ctx(1), rev, "First para.\nSecond para.", 5, 99, "更具体一点")
	assert.ErrorIs(t, err, ErrBadSelection)
	assert.Zero(t, llm.Requests(), "no run")
	cs, err := draftConversations(t.Context(), f.e.Svc, d.ID)
	require.NoError(t, err)
	assert.Empty(t, cs)
}

func TestPlainTextOffsets(t *testing.T) {
	text, offs := plainText(`<p>a😀</p>`)
	assert.Equal(t, "a😀", text)
	assert.Equal(t, []int{3, 4, -1, 8}, offs, "inside the pair: no offset")
}

func TestComposeRewriteSelectionReviewed(t *testing.T) {
	f, d, llm := witFixture(t, "<p>First para.</p><p>Second para.</p>")
	propose := coretest.Turn{Calls: []coretest.Call{{ID: "c1", Name: "update_selection", Args: `{"html":"Second paragraph, rewritten."}`}}}
	llm.Script(propose, coretest.Turn{Text: []string{"改好了。"}})
	rs, err := f.page.Rewrite(f.ctx(1), f.stored(t, d.ID).Revision, "First para.\nSecond para.", 12, 24, "更具体一点")
	require.NoError(t, err)
	require.False(t, rs.Conflict)
	ev := f.waitKind(t, 1, "interrupted")
	assert.Equal(t, rs.Run, ev.Run)
	require.Len(t, ev.Approvals, 1)
	r, err := f.page.Review(f.ctx(1), ev.Approvals[0].ID)
	require.NoError(t, err)
	assert.Equal(t, "update_selection", r.Tool)
	n := len(f.events(1))
	bc, err := f.page.Finish(f.ctx(1), r.Approval, slices.Repeat([]int{1}, r.Changes))
	require.NoError(t, err)
	assert.Equal(t, "<p>First para.</p><p>Second paragraph, rewritten.</p>", bc.Body)
	f.doneAfter(t, n, rs.Run)
}
