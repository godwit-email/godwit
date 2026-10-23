package webui

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const inlineOld = `<p>Hello <b>Lena</b>, see <a href="https://cal.example/x">the calendar</a> for Monday.</p><p>Best regards</p>`
const inlineNew = `<p>Hello <b>Lena</b>, see <a href="https://cal.example/x">the calendar</a> for Tuesday.</p><p>Warm regards</p>`

// propose has Wit propose html for window 1's draft and returns the
// approval waiting, with the model's closing words scripted.
func (f *fixture) propose(t *testing.T, llm *coretest.LLM, html string) Approval {
	t.Helper()
	llm.Script(proposeTurn("c1", html), coretest.Turn{Text: []string{"改好了。"}})
	_, err := f.page.Ask(f.ctx(1), "改一下", nil, "", "")
	require.NoError(t, err)
	ev := f.waitKind(t, 1, "interrupted")
	require.Len(t, ev.Approvals, 1)
	require.Equal(t, "pending", ev.Approvals[0].State)
	return ev.Approvals[0]
}

func (f *fixture) editable(t *testing.T, id int64) string {
	t.Helper()
	e, _, _ := mail.SplitDraft(f.stored(t, id).HTML)
	return e
}

// doneAfter waits for window 1 to get a done of run after its first n events.
func (f *fixture) doneAfter(t *testing.T, n int, run int64) {
	t.Helper()
	require.Eventually(t, func() bool {
		return slices.ContainsFunc(f.events(1)[n:], func(ev WitEvent) bool { return ev.Kind == "done" && ev.Run == run })
	}, 10*time.Second, 5*time.Millisecond, "events %v", kinds(f.events(1)))
}

func TestComposeReviewInlineAcceptAll(t *testing.T) {
	f, d, llm := witFixture(t, inlineOld)
	a := f.propose(t, llm, inlineNew)
	r, err := f.page.Review(f.ctx(1), a.ID)
	require.NoError(t, err)
	assert.True(t, r.Inline)
	assert.True(t, r.Exact)
	assert.Equal(t, 2, r.Changes)
	assert.Equal(t, "update_draft", r.Tool)
	bc, err := f.page.Finish(f.ctx(1), a.ID, []int{1, 1})
	require.NoError(t, err)
	e := f.editable(t, d.ID)
	assert.Contains(t, e, "Tuesday")
	assert.Contains(t, e, "Warm regards")
	assert.Equal(t, e, bc.Body)
	assert.Equal(t, f.stored(t, d.ID).Revision, bc.Revision)
	assert.False(t, bc.Conflict)
}

func TestComposeReviewInlinePartial(t *testing.T) {
	f, d, llm := witFixture(t, inlineOld)
	a := f.propose(t, llm, inlineNew)
	r, err := f.page.Review(f.ctx(1), a.ID)
	require.NoError(t, err)
	var ins []string // each change's insertion, in change order
	for _, b := range r.Blocks {
		for _, op := range b.Ops {
			if op.Kind == "ins" {
				ins = append(ins, op.Tokens[0].Text)
				assert.Equal(t, len(ins)-1, op.Change)
			}
		}
	}
	assert.Equal(t, []string{"Tuesday", "Warm"}, ins)
	bc, err := f.page.Finish(f.ctx(1), a.ID, []int{1, -1})
	require.NoError(t, err)
	e := f.editable(t, d.ID)
	for _, want := range []string{"Tuesday", "Best regards", "<b>Lena</b>", `<a href="https://cal.example/x">the calendar</a>`} {
		assert.Contains(t, e, want)
	}
	assert.NotContains(t, e, "Warm")
	assert.NotContains(t, e, "Monday")
	assert.Equal(t, e, bc.Body)
}

func TestComposeReviewLinkOnlyChangeShown(t *testing.T) {
	f, _, llm := witFixture(t, `<p>Read <a href="https://a.example/x">the doc</a> today.</p>`)
	a := f.propose(t, llm, `<p>Read <a href="https://b.example/y">the doc</a> today.</p>`)
	r, err := f.page.Review(f.ctx(1), a.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, r.Changes)
	var links []string
	for _, op := range r.Blocks[0].Ops {
		if op.Link != "" {
			links = append(links, op.Link)
		}
		for _, tk := range op.Tokens {
			if op.Kind == "ins" {
				assert.Equal(t, "https://b.example/y", tk.Href)
			}
		}
	}
	assert.Equal(t, []string{"https://b.example/y"}, links)
}

func TestComposeReviewRewriteCompare(t *testing.T) {
	f, d, llm := witFixture(t, "<p>One two three four.</p>")
	a := f.propose(t, llm, "<p>Completely different words appear here.</p>")
	r, err := f.page.Review(f.ctx(1), a.ID)
	require.NoError(t, err)
	assert.False(t, r.Inline)
	assert.True(t, r.Exact)
	assert.Greater(t, r.Ratio, rewriteRatio)
	assert.Equal(t, "<p>One two three four.</p>", r.Old)
	assert.Equal(t, "<p>Completely different words appear here.</p>", r.New)
	_, err = f.page.Finish(f.ctx(1), a.ID, make([]int, r.Changes))
	require.ErrorIs(t, err, ErrBadDecisions, "a rewrite is decided as a whole")
	bc, err := f.page.Decide(f.ctx(1), a.ID, false)
	require.NoError(t, err)
	assert.Equal(t, "<p>One two three four.</p>", f.editable(t, d.ID))
	assert.Equal(t, "<p>One two three four.</p>", bc.Body)
}

// A rewrite that changes a block's shape still says how much changed.
func TestComposeReviewRewriteShapeRatio(t *testing.T) {
	f, _, llm := witFixture(t, "<p>One two three four.</p>")
	a := f.propose(t, llm, "<ul><li>One two three five.</li></ul>")
	r, err := f.page.Review(f.ctx(1), a.ID)
	require.NoError(t, err)
	assert.False(t, r.Inline)
	assert.Empty(t, r.Blocks, "a shape change is not reviewed word by word")
	assert.True(t, r.Exact)
	assert.InDelta(t, 0.2, r.Ratio, 0.01)
}

func TestComposeFinishPartialStale(t *testing.T) {
	f, d, llm := witFixture(t, inlineOld)
	a := f.propose(t, llm, inlineNew)
	f.setEditable(t, f.stored(t, d.ID), "<p>Changed elsewhere</p>")
	bc, err := f.page.Finish(f.ctx(1), a.ID, []int{1, -1})
	require.NoError(t, err)
	assert.True(t, bc.Conflict)
	h, err := f.page.Follow(f.ctx(1))
	require.NoError(t, err)
	i := slices.IndexFunc(h.Approvals, func(x Approval) bool { return x.ID == a.ID })
	require.GreaterOrEqual(t, i, 0)
	assert.Equal(t, "stale", h.Approvals[i].State)
	assert.Equal(t, "<p>Changed elsewhere</p>", f.editable(t, d.ID))
}

func TestComposeUndo(t *testing.T) {
	f, d, llm := witFixture(t, "")
	llm.Script(proposeTurn("", "<p>Thanks, Lena!</p>"), coretest.Turn{Text: []string{"写好了"}})
	_, err := f.page.Ask(f.ctx(1), "写一封感谢信", nil, "", "")
	require.NoError(t, err)
	f.waitKind(t, 1, "done")
	before := f.stored(t, d.ID)
	require.Contains(t, f.editable(t, d.ID), "Thanks, Lena!")
	bc, err := f.page.Undo(f.ctx(1))
	require.NoError(t, err)
	assert.True(t, mail.Blank(bc.Body), "body %q", bc.Body)
	assert.True(t, mail.Blank(f.editable(t, d.ID)))
	assert.Greater(t, bc.Revision, before.Revision)
	_, err = f.page.Undo(f.ctx(1))
	assert.ErrorIs(t, err, core.ErrInvalidState, "nothing left to undo")
}

func TestComposeDecideResumes(t *testing.T) {
	for _, accept := range []bool{true, false} {
		t.Run(map[bool]string{true: "accept", false: "reject"}[accept], func(t *testing.T) { decideResumes(t, accept) })
	}
}

func decideResumes(t *testing.T, accept bool) {
	f, d, llm := witFixture(t, inlineOld)
	a := f.propose(t, llm, inlineNew)
	f.c.unfollow(1) // as after a reload: the decision subscribes
	n := len(f.events(1))
	bc, err := f.page.Decide(f.ctx(1), a.ID, accept)
	require.NoError(t, err)
	assert.Equal(t, f.editable(t, d.ID), bc.Body)
	assert.Equal(t, accept, strings.Contains(bc.Body, "Tuesday"))
	f.doneAfter(t, n, a.Run)
	assert.Contains(t, kinds(f.events(1)[n:]), "text", "the resumed run streams")
}

func TestComposeFinishValidates(t *testing.T) {
	f, d, llm := witFixture(t, inlineOld)
	a := f.propose(t, llm, inlineNew)
	for _, ds := range [][]int{nil, {1}, {1, 1, 1}, {1, 0}, {2, -1}} {
		_, err := f.page.Finish(f.ctx(1), a.ID, ds)
		assert.ErrorIs(t, err, ErrBadDecisions, "%v", ds)
	}
	assert.Equal(t, inlineOld, f.editable(t, d.ID))
	_, err := f.page.Review(f.ctx(1), a.ID)
	assert.NoError(t, err, "still pending")
}

func TestComposeReviewEmptyBlock(t *testing.T) {
	f, d, llm := witFixture(t, "<p>Hello there</p>")
	a := f.propose(t, llm, "<p>Hello there</p><p><br></p><p>Bye</p>")
	r, err := f.page.Review(f.ctx(1), a.ID)
	require.NoError(t, err)
	require.True(t, r.Inline)
	require.Len(t, r.Blocks, 3)
	empty := r.Blocks[1]
	assert.True(t, empty.Added)
	assert.Empty(t, empty.Ops)
	assert.Equal(t, 0, empty.Whole)
	assert.Equal(t, 1, r.Blocks[2].Whole)
	assert.Equal(t, -1, r.Blocks[0].Whole)
	assert.Equal(t, 2, r.Changes)
	_, err = f.page.Finish(f.ctx(1), a.ID, []int{-1, 1})
	require.NoError(t, err)
	assert.Equal(t, "<p>Hello there</p><p>Bye</p>", f.editable(t, d.ID))
}

func TestComposeReviewOtherDraftRefused(t *testing.T) {
	f, _, llm := witFixture(t, inlineOld)
	a := f.propose(t, llm, inlineNew)
	f.c.Attach(2, f.draft(t, "new", 0).ID)
	_, err := f.page.Review(f.ctx(2), a.ID)
	assert.ErrorIs(t, err, core.ErrInvalidState)
	_, err = f.page.Decide(f.ctx(2), a.ID, true)
	assert.ErrorIs(t, err, core.ErrInvalidState)
	_, err = f.page.Finish(f.ctx(2), a.ID, []int{1, 1})
	assert.ErrorIs(t, err, core.ErrInvalidState)
	_, err = f.page.Review(f.ctx(1), a.ID)
	assert.NoError(t, err, "still pending")
}

func TestComposeOpenLink(t *testing.T) {
	f, _, _ := witFixture(t, "")
	var urls []string
	var opened []int64
	f.page.d.OpenURL = func(u string) error { urls = append(urls, u); return nil }
	f.page.d.OpenCompose = func(id int64) { opened = append(opened, id) }
	require.NoError(t, f.page.OpenLink(f.ctx(1), "https://x.org"))
	assert.Equal(t, []string{"https://x.org"}, urls)
	assert.ErrorIs(t, f.page.OpenLink(f.ctx(1), "javascript:x"), ErrBadLink)
	assert.ErrorIs(t, f.page.OpenLink(f.ctx(1), "file:///etc/passwd"), ErrBadLink)
	assert.Len(t, urls, 1)
	require.NoError(t, f.page.OpenLink(f.ctx(1), "mailto:a@b.c"))
	require.Len(t, opened, 1)
	d := f.stored(t, opened[0])
	assert.Equal(t, []string{"a@b.c"}, d.To)
	assert.Equal(t, f.acc.ID, d.AccountID)
	assert.ErrorIs(t, f.page.OpenLink(context.Background(), "https://x.org"), ErrNoDraft, "a window with no draft")
}

func TestReviewSubjectOps(t *testing.T) {
	ops := func(old, subject string) []ReviewOp {
		args := `{"old":"<p>a</p>","html":"<p>a</p>","old_subject":` + strconv.Quote(old) + `,"subject":` + strconv.Quote(subject) + `}`
		return newProposal(core.Approval{Args: json.RawMessage(args)}).review().SubjectOps
	}
	assert.Equal(t, []ReviewOp{
		{Kind: "same", Change: -1, Tokens: []ReviewToken{{Text: "Meet on "}}},
		{Kind: "del", Change: -1, Tokens: []ReviewToken{{Text: "Monday"}}},
		{Kind: "ins", Change: -1, Tokens: []ReviewToken{{Text: "Tuesday"}}},
	}, ops("Meet on Monday", "Meet on Tuesday"))
	assert.Equal(t, []ReviewOp{{Kind: "ins", Change: -1, Tokens: []ReviewToken{{Text: "Hello"}}}}, ops("", "Hello"), "no subject before")
	assert.Empty(t, ops("Same", "Same"))
}
