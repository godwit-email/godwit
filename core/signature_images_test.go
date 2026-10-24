package core

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
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
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM signature_images WHERE signature_id = ?", acc.SignatureID))

	// The editor gets data: back, and saving it again is the same signature.
	ed, err := e.s.SignatureEditor(bg, acc.SignatureID)
	require.NoError(t, err)
	assert.Contains(t, ed, dataURL("image/png", img))
	again, err := setSignature(t, e.s, e.acc.ID, ed)
	require.NoError(t, err)
	assert.Equal(t, acc.Signature, again.Signature)
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM signature_images"))

	d, err := e.s.NewDraft(bg, e.acc.ID, "new", 0)
	require.NoError(t, err)
	assert.Contains(t, d.HTML, `src="cid:`+cids[0]+`"`)
	shown, err := e.s.InlineSignatureImages(bg, d.HTML)
	require.NoError(t, err)
	assert.Contains(t, shown, dataURL("image/png", img), "the drafts pane shows it")

	d.To, d.HTML = []string{"bob@example.com"}, "<p>hi</p>"+d.HTML
	d, err = e.s.SaveDraft(bg, d)
	require.NoError(t, err)
	assert.Contains(t, d.HTML, "cid:"+cids[0], "kept on save and read")
	require.NoError(t, e.s.Send(bg, d.ID))
	e.clock.Advance(UndoSendWindow)
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

// Accounts share signatures, so a draft may show any stored image; a cid
// nothing stores is dropped.
func TestUnknownSignatureImagesDropped(t *testing.T) {
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
	assert.Equal(t, []string{bobCID, mine}, mail.CIDs(d.HTML), "an unknown cid is dropped")
	require.NoError(t, e.s.Send(bg, d.ID))
	e.clock.Advance(UndoSendWindow)
	e.sync(t)
	got := e.smtp.Messages()
	require.Len(t, got, 1)
	p, err := mail.Parse(got[0].Data)
	require.NoError(t, err)
	assert.Len(t, p.Attachments, 2)

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

// withChunk inserts a PNG chunk after IHDR (its CRC is not checked here).
func withChunk(p []byte, typ, body string) []byte {
	at := 8 + 12 + 13 // signature, IHDR
	c := binary.BigEndian.AppendUint32(nil, uint32(len(body)))
	c = append(append(append(c, typ...), body...), 0, 0, 0, 0)
	return append(append(append([]byte(nil), p[:at]...), c...), p[at:]...)
}

// The stored images carry no EXIF, XMP or text: GPS never leaves with a mail.
func TestSignatureImageMetadataStripped(t *testing.T) {
	const gps = "GPSLatitude 52.37"
	var j bytes.Buffer
	require.NoError(t, jpeg.Encode(&j, image.NewGray(image.Rect(0, 0, 2, 2)), nil))
	app1 := append([]byte{0xFF, 0xE1, 0, byte(2 + len("Exif\x00\x00"+gps))}, "Exif\x00\x00"+gps...)
	jp := append(append([]byte{0xFF, 0xD8}, app1...), j.Bytes()[2:]...)
	vp8x := append([]byte("VP8X\x0a\x00\x00\x00"), 0x08|0x04, 0, 0, 0, 1, 0, 0, 1, 0, 0)
	exif := append([]byte("EXIF"), byte(len(gps)), 0, 0, 0)
	exif = append(append(exif, gps...), 0) // odd: padded
	xmp := append([]byte("XMP \x04\x00\x00\x00"), "<x/>"...)
	body := append(append(append([]byte("WEBP"), vp8x...), exif...), xmp...)
	body = append(body, "VP8L\x05\x00\x00\x00\x2f\x00\x00\x00\x00\x00"...) // a stand-in bitstream, padded
	wp := append(binary.LittleEndian.AppendUint32([]byte("RIFF"), uint32(len(body))), body...)
	pn := withChunk(withChunk(logo(t, 9), "eXIf", gps), "tEXt", "Comment\x00"+gps)

	for ct, data := range map[string][]byte{"image/jpeg": jp, "image/png": pn, "image/webp": wp} {
		got, err := stripImageMetadata(ct, data)
		require.NoError(t, err, ct)
		assert.NotContains(t, string(got), gps, ct)
		assert.NotContains(t, string(got), "<x/>", ct)
		again, err := stripImageMetadata(ct, got)
		require.NoError(t, err, ct)
		assert.Equal(t, got, again, "%s: stripping is stable, so the cid is", ct)
		if ct == "image/webp" {
			assert.Equal(t, uint32(len(got)-8), binary.LittleEndian.Uint32(got[4:]), "RIFF size")
			assert.Zero(t, got[20]&(0x08|0x04), "VP8X flags")
		} else {
			_, _, err := image.Decode(bytes.NewReader(got))
			require.NoError(t, err, ct)
		}
	}

	e := newSendEnv(t, false)
	_, err := setSignature(t, e.s, e.acc.ID, `<img src="`+dataURL("image/jpeg", jp)+`">`)
	require.NoError(t, err)
	stored := e.rows(t, "SELECT data FROM signature_images")
	require.Len(t, stored, 1)
	assert.NotContains(t, string(stored[0][0].([]byte)), gps)
}

// The HTML mode writes cid: itself: a known image stays and counts, an
// unknown one is dropped and does not; the fifth is refused.
func TestSignatureCodeImages(t *testing.T) {
	e := newSendEnv(t, false)
	acc, err := setSignature(t, e.s, e.acc.ID, `<p>A</p><img src="`+dataURL("image/png", logo(t, 1))+`">`)
	require.NoError(t, err)
	cid := mail.CIDs(strings.TrimPrefix(acc.Signature, SignatureHTMLPrefix))[0]
	code, err := e.s.SignatureCode(bg, acc.SignatureID)
	require.NoError(t, err)
	assert.Equal(t, "<p>A</p>\n<img src=\"cid:"+cid+"\"/>", code)

	unknown := `<img src="cid:sig-00000000000000000000000000000000@godwit">`
	known := `<img src="cid:` + cid + `">`
	acc, err = setSignature(t, e.s, e.acc.ID, `<p>A</p>`+known+unknown+unknown+unknown+unknown)
	require.NoError(t, err)
	assert.Equal(t, []string{cid}, mail.CIDs(strings.TrimPrefix(acc.Signature, SignatureHTMLPrefix)))

	var five strings.Builder
	five.WriteString(known)
	for i := range SignatureImagesMax {
		five.WriteString(`<img src="` + dataURL("image/png", logo(t, uint8(10+i))) + `">`)
	}
	_, err = setSignature(t, e.s, e.acc.ID, five.String())
	assert.ErrorIs(t, err, ErrSignatureImageCount)
	again, err := e.s.SignatureCode(bg, acc.SignatureID)
	require.NoError(t, err)
	assert.Contains(t, again, cid, "a refused save keeps the signature as it was")
}

// The compose page shows a draft's signature as sanitized HTML: a table
// and its styles stay, scripts go, its images come as data:.
func TestSignatureShown(t *testing.T) {
	e := newSendEnv(t, false)
	img := logo(t, 1)
	_, err := setSignature(t, e.s, e.acc.ID, `<table style="color:red"><tr><td>Alice</td><td><img src="`+dataURL("image/png", img)+`"></td></tr></table>`)
	require.NoError(t, err)
	d, err := e.s.NewDraft(bg, e.acc.ID, "new", 0)
	require.NoError(t, err)
	_, _, block := mail.SplitDraft(d.HTML)
	shown, err := e.s.SignatureShown(bg, block+`<script>alert(1)</script><p onclick="x()">y</p>`)
	require.NoError(t, err)
	assert.Contains(t, shown, "<table")
	assert.Contains(t, shown, "color: red")
	assert.Contains(t, shown, dataURL("image/png", img))
	assert.NotContains(t, shown, "script")
	assert.NotContains(t, shown, "onclick")
	assert.NotContains(t, shown, "data-godwit")
	none, err := e.s.SignatureShown(bg, "")
	require.NoError(t, err)
	assert.Empty(t, none)
}
