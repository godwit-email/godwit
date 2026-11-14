package core_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A program's draft (ADR 0042) is marked whichever Local API role wrote it, and
// the mark outlives the user's edits; the user's own drafts are not marked.
func TestProgramDraftMarked(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	acc := e.AddAccount(t, "alice@example.com")
	ui := core.WithUISession(ctx, e.Svc)

	own, err := e.Svc.SaveDraft(ui, core.Draft{AccountID: acc.ID, Mode: "new", HTML: "<p>mine</p>"})
	require.NoError(t, err)
	assert.False(t, own.FromProgram, "the interface's")
	own, err = e.Svc.NewDraft(ctx, acc.ID, "new", 0)
	require.NoError(t, err)
	assert.False(t, own.FromProgram, "the engine's")

	for _, src := range []string{core.AuditLocalAPI, core.AuditMCP} {
		api := core.WithExternalSource(ctx, src)
		d, err := e.Svc.NewDraft(api, acc.ID, "new", 0)
		require.NoError(t, err)
		assert.True(t, d.FromProgram, src)

		d.HTML = "<p>the user edits</p>"
		d, err = e.Svc.SaveDraft(ui, d)
		require.NoError(t, err)
		assert.True(t, d.FromProgram, "the user's edit keeps it")
	}

	// A program's change to the user's draft marks it, an attachment's too.
	d, err := e.Svc.SaveDraft(ui, core.Draft{AccountID: acc.ID, Mode: "new", HTML: "<p>mine</p>"})
	require.NoError(t, err)
	attachFile(t, e.Svc, d.ID, "a.txt", "a") // the engine's: no mark
	d, err = e.Svc.GetDraft(ctx, d.ID)
	require.NoError(t, err)
	assert.False(t, d.FromProgram)
	_, _, err = e.Svc.AddAttachmentAt(core.WithExternalSource(ctx, core.AuditLocalAPI), d.ID, d.Revision, attachSrc(t, "b.txt"))
	require.NoError(t, err)
	d, err = e.Svc.GetDraft(ctx, d.ID)
	require.NoError(t, err)
	assert.True(t, d.FromProgram)
}

// 批准并发送 sends exactly the revision approved, and only a program's draft;
// the thread lists the program's reply until it is queued.
func TestApproveDraft(t *testing.T) {
	e := coretest.New(t)
	acc := e.AddAccount(t, "alice@example.com")
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("Bob <bob@example.com>", "alice@example.com", "plan", "the plan"))
	page, err := e.Svc.ListMessages(ctx, core.View{Unified: "inbox"}, "", 10)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	src, thread := page.Items[0].ID, page.Items[0].ThreadID
	ui := core.WithUISession(ctx, e.Svc)
	api := core.WithExternalSource(ctx, core.AuditMCP)

	own, err := e.Svc.NewDraft(ui, acc.ID, "reply", src)
	require.NoError(t, err)
	assert.ErrorIs(t, e.Svc.ApproveDraft(ui, own.ID, own.Revision), core.ErrNotProgramDraft)

	d, err := e.Svc.NewDraft(api, acc.ID, "reply", src)
	require.NoError(t, err)
	th, err := e.Svc.GetThread(ctx, thread)
	require.NoError(t, err)
	require.Len(t, th.Drafts, 1, "only the program's draft")
	assert.Equal(t, d.ID, th.Drafts[0].ID)

	shown := d.Revision
	d.HTML = "<p>swapped after the user looked</p>"
	d, err = e.Svc.SaveDraft(api, d)
	require.NoError(t, err)
	assert.ErrorIs(t, e.Svc.ApproveDraft(ui, d.ID, shown), core.ErrStaleDraft, "a change after the look voids it")
	ob, err := e.Svc.ListOutbox(ctx, acc.ID)
	require.NoError(t, err)
	assert.Empty(t, ob)

	require.NoError(t, e.Svc.ApproveDraft(ui, d.ID, d.Revision))
	ob, err = e.Svc.ListOutbox(ctx, acc.ID)
	require.NoError(t, err)
	assert.Len(t, ob, 1)
	th, err = e.Svc.GetThread(ctx, thread)
	require.NoError(t, err)
	assert.Empty(t, th.Drafts, "queued: not offered again")
	assert.ErrorIs(t, e.Svc.ApproveDraft(ui, d.ID, d.Revision), core.ErrStaleDraft, "queued already")
}

func attachSrc(t *testing.T, name string) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(src, []byte("x"), 0o600))
	return src
}
