package core_test

import (
	"bytes"
	netmail "net/mail"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func attachFile(t *testing.T, s *core.Service, draftID int64, name, body string) {
	t.Helper()
	src := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(src, []byte(body), 0o600))
	_, err := s.AddAttachment(ctx, draftID, src)
	require.NoError(t, err)
}

func TestDeleteDraft(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	acc := e.AddAccount(t, "alice@example.com")
	d, err := e.Svc.NewDraft(ctx, acc.ID, "new", 0)
	require.NoError(t, err)
	attachFile(t, e.Svc, d.ID, "notes.txt", "n")
	keep, err := e.Svc.NewDraft(ctx, acc.ID, "new", 0)
	require.NoError(t, err)
	files, _ := filepath.Glob(filepath.Join(e.DataDir, "drafts", "*", "*"))
	require.Len(t, files, 1)

	require.NoError(t, e.Svc.DeleteDraft(ctx, d.ID))
	_, err = e.Svc.GetDraft(ctx, d.ID)
	assert.Error(t, err)
	assert.NoFileExists(t, files[0])
	_, err = e.Svc.GetDraft(ctx, keep.ID)
	assert.NoError(t, err, "other drafts stay")
	assert.ErrorContains(t, e.Svc.DeleteDraft(ctx, d.ID), "not found")
}

func TestSetDraftAccount(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	a := e.AddAccount(t, "alice@example.com")
	b := e.AddAccount(t, "bob@example.com")
	for id, sig := range map[int64]string{a.ID: "Alice A.", b.ID: "Bob B."} {
		_, err := e.Svc.UpdateAccount(ctx, id, core.AccountPatch{Signature: &sig})
		require.NoError(t, err)
	}
	d, err := e.Svc.NewDraft(ctx, a.ID, "new", 0)
	require.NoError(t, err)
	d.To, d.Subject = []string{"carol@example.com"}, "hello"
	d.HTML = "<p>body text</p>" + d.HTML
	d, err = e.Svc.SaveDraft(ctx, d)
	require.NoError(t, err)
	attachFile(t, e.Svc, d.ID, "notes.txt", "n")
	d, err = e.Svc.GetDraft(ctx, d.ID)
	require.NoError(t, err)

	got, err := e.Svc.SetDraftAccount(ctx, d.ID, b.ID, d.Revision)
	require.NoError(t, err)
	assert.Equal(t, b.ID, got.AccountID)
	assert.Equal(t, d.Revision+1, got.Revision)
	ed, _, sig := mail.SplitDraft(got.HTML)
	assert.Contains(t, ed, "body text")
	assert.Contains(t, sig, "Bob B.")
	assert.NotContains(t, got.HTML, "Alice A.")
	require.Len(t, got.Attachments, 1)
	assert.Equal(t, "notes.txt", got.Attachments[0].Filename)

	_, err = e.Svc.SetDraftAccount(ctx, d.ID, a.ID, d.Revision)
	assert.ErrorIs(t, err, core.ErrStaleDraft)
	_, err = e.Svc.SetDraftAccount(ctx, d.ID, 999, got.Revision)
	assert.Error(t, err)

	require.NoError(t, e.Svc.Send(ctx, d.ID))
	e.AdvanceClock(core.UndoSendWindow)
	require.NoError(t, e.Svc.SyncNow(ctx, b.ID))
	msgs := e.SMTP(b.ID).Messages()
	require.Len(t, msgs, 1)
	assert.Equal(t, "bob@example.com", msgs[0].From)
	hdr, err := netmail.ReadMessage(bytes.NewReader(msgs[0].Data))
	require.NoError(t, err)
	from, err := hdr.Header.AddressList("From")
	require.NoError(t, err)
	assert.Equal(t, "bob@example.com", from[0].Address)
	assert.Contains(t, string(msgs[0].Data), "notes.txt")
	assert.Empty(t, e.Sent(a.ID))
}

func TestSendRevisionStale(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	acc := e.AddAccount(t, "alice@example.com")
	d, err := e.Svc.SaveDraft(ctx, core.Draft{AccountID: acc.ID, Mode: "new", To: []string{"bob@example.com"}, HTML: "<p>one</p>"})
	require.NoError(t, err)
	old := d.Revision
	d.HTML = "<p>two</p>"
	d, err = e.Svc.SaveDraft(ctx, d)
	require.NoError(t, err)
	assert.ErrorIs(t, e.Svc.SendRevision(ctx, d.ID, old), core.ErrStaleDraft)
	assert.ErrorIs(t, e.Svc.SendAcknowledgedRevision(ctx, d.ID, old, ""), core.ErrStaleDraft)
	_, err = e.Svc.GetDraft(ctx, d.ID)
	require.NoError(t, err, "nothing sent")
	require.NoError(t, e.Svc.SendRevision(ctx, d.ID, d.Revision))
	assert.ErrorIs(t, e.Svc.SendRevision(ctx, d.ID, d.Revision), core.ErrStaleDraft, "queued already")
}

func TestDeleteDraftIfEmptyRevision(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	acc := e.AddAccount(t, "alice@example.com")
	d, err := e.Svc.NewDraft(ctx, acc.ID, "new", 0)
	require.NoError(t, err)
	checked := d.Revision
	d.HTML = "<p>typed after the check</p>"
	_, err = e.Svc.SaveDraft(ctx, d)
	require.NoError(t, err)
	deleted, err := e.Svc.DeleteDraftIfEmpty(ctx, d.ID, checked)
	require.NoError(t, err)
	assert.False(t, deleted)
	_, err = e.Svc.GetDraft(ctx, d.ID)
	require.NoError(t, err, "a save after the check keeps the draft")

	empty, err := e.Svc.NewDraft(ctx, acc.ID, "new", 0)
	require.NoError(t, err)
	deleted, err = e.Svc.DeleteDraftIfEmpty(ctx, empty.ID, empty.Revision)
	require.NoError(t, err)
	assert.True(t, deleted)
	_, err = e.Svc.GetDraft(ctx, empty.ID)
	assert.Error(t, err)
}

func TestDeleteDraftIfEmptyKeepsActiveRun(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	acc := e.AddAccount(t, "alice@example.com")
	llm := e.LLM(t)
	t.Cleanup(llm.Release)
	emptyIfDeleted := func(d core.Draft) bool {
		d, err := e.Svc.GetDraft(ctx, d.ID)
		require.NoError(t, err)
		deleted, err := e.Svc.DeleteDraftIfEmpty(ctx, d.ID, d.Revision)
		require.NoError(t, err)
		return deleted
	}
	untilEnd := func(ch <-chan core.WitEvent) (last core.WitEvent) {
		for ev := range ch {
			last = ev
		}
		return last
	}

	running, err := e.Svc.NewDraft(ctx, acc.ID, "new", 0)
	require.NoError(t, err)
	llm.Script(coretest.Turn{Text: []string{"thinking"}, Hang: true})
	ch, err := e.Svc.Assist(ctx, running.ID, "hi")
	require.NoError(t, err)
	<-ch // the run has started
	assert.False(t, emptyIfDeleted(running), "a running run keeps the draft")
	llm.Release()
	require.Equal(t, "done", untilEnd(ch).Kind)
	assert.True(t, emptyIfDeleted(running), "once the run is over it is empty again")

	pending, err := e.Svc.NewDraft(ctx, acc.ID, "new", 0)
	require.NoError(t, err)
	_, q, sig := mail.SplitDraft(pending.HTML)
	pending.HTML = mail.JoinDraft("<p>Hello</p>", q, sig)
	pending, err = e.Svc.SaveDraft(ctx, pending)
	require.NoError(t, err)
	llm.Script(coretest.Turn{Calls: []coretest.Call{{Name: "update_draft", Args: `{"html":"<p>Hi</p>"}`}}})
	ch, err = e.Svc.Assist(ctx, pending.ID, "edit")
	require.NoError(t, err)
	require.Equal(t, "interrupted", untilEnd(ch).Kind)
	pending.HTML = mail.JoinDraft("", q, sig) // emptied while the edit waits
	_, err = e.Svc.SaveDraft(ctx, pending)
	require.NoError(t, err)
	assert.False(t, emptyIfDeleted(pending), "a pending approval keeps the draft")
}

func TestDeleteOrSendDraftCancelsRun(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	acc := e.AddAccount(t, "alice@example.com")
	llm := e.LLM(t)
	t.Cleanup(llm.Release)
	for _, name := range []string{"delete", "send"} {
		t.Run(name, func(t *testing.T) {
			d, err := e.Svc.NewDraft(ctx, acc.ID, "new", 0)
			require.NoError(t, err)
			d.To = []string{"bob@example.com"}
			_, err = e.Svc.SaveDraft(ctx, d)
			require.NoError(t, err)
			llm.Script(coretest.Turn{Text: []string{"thinking"}, Hang: true})
			ch, err := e.Svc.Assist(ctx, d.ID, "hi")
			require.NoError(t, err)
			<-ch // the run has started
			if name == "delete" {
				require.NoError(t, e.Svc.DeleteDraft(ctx, d.ID))
			} else {
				require.NoError(t, e.Svc.Send(ctx, d.ID))
			}
			ended := make(chan struct{})
			go func() {
				for range ch {
				}
				close(ended)
			}()
			select {
			case <-ended: // cancelled and settled before the draft went
			case <-time.After(5 * time.Second):
				t.Fatal("the run still goes on after its draft is gone")
			}
			_, err = e.Svc.GetDraft(ctx, d.ID)
			if name == "delete" {
				require.Error(t, err)
			} else {
				require.NoError(t, err, "kept in the outbox until it may have gone out")
			}
		})
	}
}

func TestAttachmentAtRevision(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	acc := e.AddAccount(t, "alice@example.com")
	d, err := e.Svc.NewDraft(ctx, acc.ID, "new", 0)
	require.NoError(t, err)
	src := filepath.Join(t.TempDir(), "a.txt")
	require.NoError(t, os.WriteFile(src, []byte("a"), 0o600))

	_, _, err = e.Svc.AddAttachmentAt(ctx, d.ID, d.Revision-1, src)
	assert.ErrorIs(t, err, core.ErrStaleDraft)
	a, rev, err := e.Svc.AddAttachmentAt(ctx, d.ID, d.Revision, src)
	require.NoError(t, err)
	assert.Equal(t, d.Revision+1, rev)

	_, err = e.Svc.RemoveAttachmentAt(ctx, d.ID, a.ID, d.Revision)
	assert.ErrorIs(t, err, core.ErrStaleDraft)
	rev, err = e.Svc.RemoveAttachmentAt(ctx, d.ID, a.ID, rev)
	require.NoError(t, err)
	assert.Equal(t, d.Revision+2, rev)
	stored, err := e.Svc.GetDraft(ctx, d.ID)
	require.NoError(t, err)
	assert.Empty(t, stored.Attachments)
	assert.Equal(t, rev, stored.Revision)
}

func TestDeleteDraftIfEmptyForwardAndOrphanReply(t *testing.T) {
	e := coretest.New(t)
	acc := e.AddAccount(t, "alice@example.com")
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("Bob <bob@example.com>", "alice@example.com", "plan", "the plan",
		coretest.Attachment("plan.txt", "text/plain", []byte("p"))))
	page, err := e.Svc.ListMessages(ctx, core.View{Unified: "inbox"}, "", 10)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	src := page.Items[0].ID

	withFiles, err := e.Svc.NewDraft(ctx, acc.ID, "forward", src)
	require.NoError(t, err)
	require.Len(t, withFiles.Attachments, 1)
	deleted, err := e.Svc.DeleteDraftIfEmpty(ctx, withFiles.ID, withFiles.Revision)
	require.NoError(t, err)
	assert.False(t, deleted, "a forward keeps the attachments it carries")

	bare, err := e.Svc.NewDraft(ctx, acc.ID, "forward", src)
	require.NoError(t, err)
	rev, err := e.Svc.RemoveAttachmentAt(ctx, bare.ID, bare.Attachments[0].ID, bare.Revision)
	require.NoError(t, err)
	deleted, err = e.Svc.DeleteDraftIfEmpty(ctx, bare.ID, rev)
	require.NoError(t, err)
	assert.True(t, deleted, "a forward without its attachments holds nothing")

	// A reply whose source was deleted: the row's source_message_id is set
	// NULL (ON DELETE SET NULL), so it is a reply without a source.
	orphan, err := e.Svc.SaveDraft(ctx, core.Draft{AccountID: acc.ID, Mode: "reply", HTML: "<p></p>"})
	require.NoError(t, err)
	deleted, err = e.Svc.DeleteDraftIfEmpty(ctx, orphan.ID, orphan.Revision)
	require.NoError(t, err)
	assert.False(t, deleted, "its fresh headers are unknown: kept")
}
