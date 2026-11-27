package core

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// imageFile writes data to a file named name and returns its path.
func imageFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(p, data, 0o600))
	return p
}

// An image put in the body (#812) is stored with the draft, kept by its
// sanitizing, read back as cid:, and sent inline in multipart/related.
func TestInlineImageSavedAndSentRelated(t *testing.T) {
	const gps = "GPSLatitude 52.37"
	e := newSendEnv(t, false)
	d, err := e.s.NewDraft(bg, e.acc.ID, "new", 0)
	require.NoError(t, err)
	shot := withChunk(logo(t, 3), "tEXt", "Comment\x00"+gps)
	// Sniffed, never named: a .txt name does not stop a png.
	a, err := e.s.AddAttachment(bg, d.ID, imageFile(t, "shot.txt", shot), true)
	require.NoError(t, err)
	assert.Regexp(t, `^img-[0-9a-f]{32}@godwit$`, a.ContentID)
	assert.Equal(t, "image/png", a.ContentType)
	again, err := e.s.AddAttachment(bg, d.ID, imageFile(t, "copy.png", shot), true)
	require.NoError(t, err)
	assert.Equal(t, a.ID, again.ID, "the same image is the same attachment")
	unshown, err := e.s.AddAttachment(bg, d.ID, imageFile(t, "other.png", logo(t, 4)), true)
	require.NoError(t, err)
	_, err = e.s.AddAttachment(bg, d.ID, imageFile(t, "notes.txt", []byte("notes")), false)
	require.NoError(t, err)

	d, err = e.s.GetDraft(bg, d.ID)
	require.NoError(t, err)
	d.To = []string{"bob@example.com"}
	d.HTML = `<p>As in the screenshot below:</p><img src="cid:` + a.ContentID + `" alt="the button">` +
		`<img src="cid:img-00000000000000000000000000000000@godwit">` +
		`<img src="` + dataURL("image/png", shot) + `"><img src="https://example.com/x.png">` + d.HTML
	d, err = e.s.SaveDraft(bg, d)
	require.NoError(t, err)
	assert.Equal(t, []string{a.ContentID}, mail.CIDs(d.HTML), "only the draft's own image stays")
	assert.NotContains(t, d.HTML, "data:")
	assert.NotContains(t, d.HTML, "example.com")

	// Reopening keeps the body CID. Only its file is needed for sending.
	d, err = e.s.GetDraft(bg, d.ID)
	require.NoError(t, err)
	assert.Contains(t, d.HTML, `src="cid:`+a.ContentID+`"`)
	orphan, err := e.s.DraftImageFiles(bg, d.ID, []string{unshown.ContentID})
	require.NoError(t, err)
	require.Len(t, orphan, 1)
	// The orphan is still listed, but sending must never open it.
	require.NoError(t, os.Remove(orphan[0].Path))
	stored, err := stripImageMetadata("image/png", shot)
	require.NoError(t, err)

	require.NoError(t, e.s.Send(bg, d.ID))
	e.clock.Advance(UndoSendWindow)
	e.sync(t)
	got := e.smtp.Messages()
	require.Len(t, got, 1)
	raw := string(got[0].Data)
	assert.Contains(t, raw, "multipart/mixed")
	assert.Contains(t, raw, "multipart/related")
	assert.NotContains(t, raw, gps, "metadata never leaves")
	p, err := mail.Parse(got[0].Data)
	require.NoError(t, err)
	assert.Contains(t, p.HTML, `src="cid:`+a.ContentID+`"`)
	var inline, files []mail.Attachment
	for _, at := range p.Attachments {
		if at.Inline {
			inline = append(inline, at)
		} else {
			files = append(files, at)
		}
	}
	require.Len(t, inline, 1, "an image the body no longer shows is not sent")
	assert.Equal(t, a.ContentID, inline[0].ContentID)
	assert.Equal(t, stored, inline[0].Data)
	require.Len(t, files, 1)
	assert.Equal(t, "notes.txt", files[0].Filename)
}

func TestInlineImageRefusedUnlessImage(t *testing.T) {
	e := newSendEnv(t, false)
	d, err := e.s.NewDraft(bg, e.acc.ID, "new", 0)
	require.NoError(t, err)
	_, err = e.s.AddAttachment(bg, d.ID, os.DevNull, true)
	assert.ErrorContains(t, err, "not a regular file", "a device is never read")
	for name, data := range map[string][]byte{
		"not image": []byte("<html><script>alert(1)</script>"),
		"svg":       []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`),
		"broken":    []byte("\x89PNG\r\n\x1a\nbroken"),
	} {
		_, err := e.s.AddAttachment(bg, d.ID, imageFile(t, "x.png", data), true)
		assert.ErrorIs(t, err, ErrInlineImage, name)
	}
	assert.Zero(t, e.count(t, "SELECT count(*) FROM draft_attachments"))
	now, err := e.s.GetDraft(bg, d.ID)
	require.NoError(t, err)
	assert.Equal(t, d.Revision, now.Revision, "nothing changed")
}

// A draft shows only its own images: another draft's cid is dropped.
func TestInlineImageBelongsToItsDraft(t *testing.T) {
	e := newSendEnv(t, false)
	d1, err := e.s.NewDraft(bg, e.acc.ID, "new", 0)
	require.NoError(t, err)
	a, err := e.s.AddAttachment(bg, d1.ID, imageFile(t, "a.png", logo(t, 5)), true)
	require.NoError(t, err)
	d2, err := e.s.NewDraft(bg, e.acc.ID, "new", 0)
	require.NoError(t, err)
	d2.HTML = `<img src="cid:` + a.ContentID + `">` + d2.HTML
	d2, err = e.s.SaveDraft(bg, d2)
	require.NoError(t, err)
	assert.Empty(t, mail.CIDs(d2.HTML))
	assert.NotContains(t, d2.HTML, a.ContentID)
	files, err := e.s.DraftImageFiles(bg, d2.ID, []string{a.ContentID})
	require.NoError(t, err)
	assert.Empty(t, files)
}

// A shown image counts toward the message's limit, as an attachment does.
func TestInlineImageCountsTowardSizeLimit(t *testing.T) {
	e := newSendEnv(t, false)
	d, err := e.s.NewDraft(bg, e.acc.ID, "new", 0)
	require.NoError(t, err)
	img := logo(t, 6)
	a, err := e.s.AddAttachment(bg, d.ID, imageFile(t, "a.png", img), true)
	require.NoError(t, err)
	d, err = e.s.GetDraft(bg, d.ID)
	require.NoError(t, err)
	d.To, d.HTML = []string{"bob@example.com"}, `<img src="cid:`+a.ContentID+`">`+d.HTML
	d, err = e.s.SaveDraft(bg, d)
	require.NoError(t, err)
	files, err := e.s.DraftImageFiles(bg, d.ID, []string{a.ContentID})
	require.NoError(t, err)
	require.Len(t, files, 1)
	require.NoError(t, os.Remove(files[0].Path)) // an over-limit draft is refused before reading this image
	old := attachLimit
	t.Cleanup(func() { attachLimit = old })
	attachLimit = int64(len(d.HTML) + a.Size - 1)
	assert.ErrorIs(t, e.s.Send(bg, d.ID), ErrTooLarge)
}

// Wit's rewrites keep the draft's images: a whole-body update and a selection
// rewrite elsewhere in a body that shows one (#907 review).
func TestInlineImageKeptThroughWitRewrite(t *testing.T) {
	e := newAssistEnv(t)
	d := e.replyDraft(t, "<p>See the form below.</p>")
	a, err := e.s.AddAttachment(t.Context(), d.ID, imageFile(t, "form.png", logo(t, 8)), true)
	require.NoError(t, err)
	img := `<img src="cid:` + a.ContentID + `"/>`
	d = e.getDraft(t, d.ID)
	_, q, sig := parts(t, d.HTML)
	d.HTML = mail.JoinDraft("<p>See the form below.</p>"+img, q, sig)
	_, err = e.s.SaveDraft(t.Context(), d)
	require.NoError(t, err)

	e.accept(t, e.propose(t, d.ID, "<p>Please see the signed form below.</p>"+img).Approvals[0])
	ed, _, _ := parts(t, e.getDraft(t, d.ID).HTML)
	assert.Equal(t, "<p>Please see the signed form below.</p>"+img, ed)

	d = e.getDraft(t, d.ID)
	sel := selectionOf(t, d, "signed form")
	e.srv.Script(testllm.Turn{Calls: []testllm.Call{{Name: "update_selection", Args: `{"html":"completed form"}`}}},
		testllm.Turn{Text: []string{"Done."}})
	ch, err := e.s.RewriteSelection(t.Context(), d.ID, sel, "say completed")
	require.NoError(t, err)
	evs := drainWit(t, ch)
	last := evs[len(evs)-1]
	require.Equal(t, "interrupted", last.Kind, "%+v", last.Err)
	require.Len(t, last.Approvals, 1)
	e.accept(t, last.Approvals[0])
	ed, _, _ = parts(t, e.getDraft(t, d.ID).HTML)
	assert.Equal(t, "<p>Please see the completed form below.</p>"+img, ed)
}

// An image removed by Wit, the text unchanged, is a change, not no_change.
func TestInlineImageRemovalIsAChange(t *testing.T) {
	e := newAssistEnv(t)
	d := e.replyDraft(t, "<p>See below.</p>")
	a, err := e.s.AddAttachment(t.Context(), d.ID, imageFile(t, "x.png", logo(t, 9)), true)
	require.NoError(t, err)
	d = e.getDraft(t, d.ID)
	_, q, sig := parts(t, d.HTML)
	d.HTML = mail.JoinDraft(`<p>See below.</p><img src="cid:`+a.ContentID+`"/>`, q, sig)
	_, err = e.s.SaveDraft(t.Context(), d)
	require.NoError(t, err)

	p := e.propose(t, d.ID, "<p>See below.</p>").Approvals[0]
	assert.NotEqual(t, kindNoChange, p.Summary)
	e.accept(t, p)
	ed, _, _ := parts(t, e.getDraft(t, d.ID).HTML)
	assert.Equal(t, "<p>See below.</p>", ed)
}
