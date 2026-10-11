package core

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"strconv"
	"strings"
	"testing"

	"github.com/godwit-email/godwit/core/mail"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// logo is a small PNG, different for each shade.
func logo(t *testing.T, shade uint8) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.Gray{Y: shade})
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, img))
	return b.Bytes()
}

func dataURL(ct string, data []byte) string {
	return "data:" + ct + ";base64," + base64.StdEncoding.EncodeToString(data)
}

// setSignature saves editor HTML as the account's signature, as the signature page does.
func setSignature(t *testing.T, s *Service, accountID int64, editor string) (Account, error) {
	t.Helper()
	sig := SignatureHTMLPrefix + editor
	return s.UpdateAccount(bg, accountID, AccountPatch{Signature: &sig})
}

func TestSignatureImagesAreStoredAndSentInline(t *testing.T) {
	e := newSendEnv(t, false)
	img := logo(t, 1)
	acc, err := setSignature(t, e.s, e.acc.ID, `<p>Alice</p><img src="`+dataURL("image/png", img)+`">`)
	require.NoError(t, err)
	assert.NotContains(t, acc.Signature, "data:")
	cids := mail.CIDs(strings.TrimPrefix(acc.Signature, SignatureHTMLPrefix))
	require.Len(t, cids, 1)
	assert.Regexp(t, `^sig-[0-9a-f]{32}@godwit$`, cids[0])
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM signature_images WHERE account_id = ?", e.acc.ID))

	// The editor gets data: back, and saving it again is the same signature.
	ed, err := e.s.SignatureEditor(bg, e.acc.ID)
	require.NoError(t, err)
	assert.Contains(t, ed, dataURL("image/png", img))
	again, err := setSignature(t, e.s, e.acc.ID, ed)
	require.NoError(t, err)
	assert.Equal(t, acc.Signature, again.Signature)
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM signature_images"))

	d, err := e.s.NewDraft(bg, e.acc.ID, "new", 0)
	require.NoError(t, err)
	assert.Contains(t, d.HTML, `src="cid:`+cids[0]+`"`)
	shown, err := e.s.InlineSignatureImages(bg, d.AccountID, d.HTML)
	require.NoError(t, err)
	assert.Contains(t, shown, dataURL("image/png", img), "the drafts pane shows it")

	d.To, d.HTML = []string{"bob@example.com"}, "<p>hi</p>"+d.HTML
	d, err = e.s.SaveDraft(bg, d)
	require.NoError(t, err)
	assert.Contains(t, d.HTML, "cid:"+cids[0], "kept on save and read")
	require.NoError(t, e.s.Send(bg, d.ID))
	e.sync(t)
	got := e.smtp.Messages()
	require.Len(t, got, 1)
	assert.Contains(t, string(got[0].Data), "multipart/related")
	p, err := mail.Parse(got[0].Data)
	require.NoError(t, err)
	assert.Contains(t, p.HTML, `src="cid:`+cids[0]+`"`)
	require.Len(t, p.Attachments, 1)
	assert.Equal(t, cids[0], p.Attachments[0].ContentID)
	assert.True(t, p.Attachments[0].Inline)
	assert.Equal(t, img, p.Attachments[0].Data)
	assert.NotContains(t, string(got[0].Data), "http", "never a remote URL")
}

func TestSignatureImageCaps(t *testing.T) {
	e := newSendEnv(t, false)
	big := append(logo(t, 1), make([]byte, SignatureImageMax)...)
	for name, html := range map[string]string{
		"too large": `<img src="` + dataURL("image/png", big) + `">`,
		"not image": `<img src="` + dataURL("image/png", []byte("<html><script>alert(1)</script>")) + `">`,
		"svg":       `<img src="` + dataURL("image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)) + `">`,
	} {
		_, err := setSignature(t, e.s, e.acc.ID, html)
		assert.ErrorIs(t, err, ErrSignatureImage, name)
	}
	var five strings.Builder
	for i := range SignatureImagesMax + 1 {
		five.WriteString(`<img src="` + dataURL("image/png", logo(t, uint8(i))) + `">`)
	}
	_, err := setSignature(t, e.s, e.acc.ID, five.String())
	assert.ErrorIs(t, err, ErrSignatureImageCount)
	assert.Zero(t, e.count(t, "SELECT count(*) FROM signature_images"), "nothing stored")

	u, err := SignatureImageURL(logo(t, 1))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(u, "data:image/png;base64,"), u)
	_, err = SignatureImageURL(big)
	assert.ErrorIs(t, err, ErrSignatureImage)
	_, err = SignatureImageURL([]byte("text in a file named logo.png"))
	assert.ErrorIs(t, err, ErrSignatureImage, "sniffed, not named")
}

func TestForeignSignatureImagesDropped(t *testing.T) {
	e := newSendEnv(t, false)
	bobIn := internalInput(t, e.im.Addr, e.acc.SMTPHost+":"+strconv.Itoa(e.acc.SMTPPort), "bob@example.com", false)
	bob, err := e.s.AddAccount(bg, bobIn)
	require.NoError(t, err)
	bobAcc, err := setSignature(t, e.s, bob.ID, `<img src="`+dataURL("image/png", logo(t, 9))+`">`)
	require.NoError(t, err)
	bobCID := mail.CIDs(strings.TrimPrefix(bobAcc.Signature, SignatureHTMLPrefix))[0]
	acc, err := setSignature(t, e.s, e.acc.ID, `<p>A</p><img src="`+dataURL("image/png", logo(t, 1))+`">`)
	require.NoError(t, err)
	mine := mail.CIDs(strings.TrimPrefix(acc.Signature, SignatureHTMLPrefix))[0]

	d, err := e.s.NewDraft(bg, e.acc.ID, "new", 0)
	require.NoError(t, err)
	d.To = []string{"x@example.com"}
	d.HTML = `<p><img src="cid:` + bobCID + `"><img src="cid:sig-00000000000000000000000000000000@godwit"></p>` + d.HTML
	d, err = e.s.SaveDraft(bg, d)
	require.NoError(t, err)
	assert.Equal(t, []string{mine}, mail.CIDs(d.HTML), "another account's and unknown cids are dropped")
	require.NoError(t, e.s.Send(bg, d.ID))
	e.sync(t)
	got := e.smtp.Messages()
	require.Len(t, got, 1)
	assert.NotContains(t, string(got[0].Data), bobCID)

	// A draft moved to Bob takes Bob's signature and image.
	d, err = e.s.NewDraft(bg, e.acc.ID, "new", 0)
	require.NoError(t, err)
	d, err = e.s.SetDraftAccount(bg, d.ID, bob.ID, d.Revision)
	require.NoError(t, err)
	assert.Equal(t, []string{bobCID}, mail.CIDs(d.HTML))
}

func TestSignatureImagesKeptWhileReferenced(t *testing.T) {
	e := newSendEnv(t, false)
	_, err := setSignature(t, e.s, e.acc.ID, `<img src="`+dataURL("image/png", logo(t, 1))+`">`)
	require.NoError(t, err)
	d, err := e.s.NewDraft(bg, e.acc.ID, "new", 0)
	require.NoError(t, err)
	_, err = setSignature(t, e.s, e.acc.ID, `<img src="`+dataURL("image/png", logo(t, 2))+`">`)
	require.NoError(t, err)
	assert.Equal(t, 2, e.count(t, "SELECT count(*) FROM signature_images"), "the draft still shows the old one")
	d, err = e.s.GetDraft(bg, d.ID)
	require.NoError(t, err)
	assert.Len(t, mail.CIDs(d.HTML), 1)

	require.NoError(t, e.s.DeleteDraft(bg, d.ID))
	_, err = setSignature(t, e.s, e.acc.ID, `<p>text only</p>`)
	require.NoError(t, err)
	assert.Zero(t, e.count(t, "SELECT count(*) FROM signature_images"))
}

func TestSignatureImagesCountTowardSizeLimit(t *testing.T) {
	e := newSendEnv(t, false)
	img := logo(t, 1)
	_, err := setSignature(t, e.s, e.acc.ID, `<img src="`+dataURL("image/png", img)+`">`)
	require.NoError(t, err)
	d, err := e.s.NewDraft(bg, e.acc.ID, "new", 0)
	require.NoError(t, err)
	d.To = []string{"bob@example.com"}
	d, err = e.s.SaveDraft(bg, d)
	require.NoError(t, err)
	old := attachLimit
	attachLimit = int64(len(d.HTML) + len(img) - 1)
	t.Cleanup(func() { attachLimit = old })
	assert.ErrorIs(t, e.s.Send(bg, d.ID), ErrTooLarge)
}
