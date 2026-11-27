package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"image/gif"
	"net/http"
	"regexp"
	"strings"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/store"
)

// Signature images: png, jpeg, gif or webp, sniffed (never named), at most
// SignatureImageMax bytes each and SignatureImagesMax per signature. They are
// stored per signature, content-addressed, and referenced in the signature as
// cid:sig-<hash>@godwit; a sent mail carries them as inline parts. Any draft
// may show any of them: accounts share signatures.
// Their metadata (EXIF with its GPS, XMP, text) is stripped before storing.
const (
	SignatureImageMax  = 512 << 10
	SignatureImagesMax = 4
)

var (
	// ErrSignatureImage is an image that is not png, jpeg, gif or webp, or is over SignatureImageMax.
	ErrSignatureImage = errors.New("a signature image must be a png, jpeg, gif or webp of at most 512 KB")
	// ErrInlineImage is an image for a draft's body that is not png, jpeg, gif or webp.
	ErrInlineImage = errors.New("an image in the body must be a png, jpeg, gif or webp")
	// ErrSignatureImageCount is a signature with more than SignatureImagesMax images.
	ErrSignatureImageCount = errors.New("a signature has at most 4 images")
)

var signatureImageTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// signatureImageType is data's sniffed type, or ErrSignatureImage.
func signatureImageType(data []byte) (string, error) {
	ct := http.DetectContentType(data)
	if len(data) == 0 || len(data) > SignatureImageMax || !signatureImageTypes[ct] {
		return "", ErrSignatureImage
	}
	return ct, nil
}

// inlineImage is data as an image of a draft's body (#812), with the
// signature images' hygiene: sniffed png, jpeg, gif or webp, metadata
// stripped, content-addressed as img-<hash>@godwit. Its size counts toward
// the message's limit only (attachLimit). Anything else is ErrInlineImage.
func inlineImage(data []byte) (ct string, clean []byte, cid string, err error) {
	ct = http.DetectContentType(data)
	if len(data) == 0 || !signatureImageTypes[ct] {
		return "", nil, "", ErrInlineImage
	}
	if clean, err = stripImageMetadata(ct, data); err != nil {
		return "", nil, "", ErrInlineImage
	}
	sum := sha256.Sum256(clean)
	return ct, clean, "img-" + hex.EncodeToString(sum[:16]) + "@godwit", nil
}

// SignatureImageURL is data as a data: URL for the signature editor, or ErrSignatureImage.
func SignatureImageURL(data []byte) (string, error) {
	ct, err := signatureImageType(data)
	if err != nil {
		return "", err
	}
	return "data:" + ct + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// signatureAssets turns the data: images of a signature's editor HTML into
// stored assets of signature sigID: their sources become cid:, and the statements
// insert the new ones. A cid: source stays only if known has it; any other
// source is dropped. Every image kept counts toward SignatureImagesMax. The
// result is sanitized (SanitizeDraft), keeping those images.
func signatureAssets(sigID int64, editor string, known map[string]bool) (string, []sqlite.Statement, error) {
	var stmts []sqlite.Statement
	var bad error
	seen := map[string]bool{}
	out := mail.ReplaceImages(editor, func(src string) string {
		if bad != nil {
			return ""
		}
		id := ""
		if rest, ok := strings.CutPrefix(src, "data:"); ok {
			meta, b64, _ := strings.Cut(rest, ",")
			data, err := base64.StdEncoding.DecodeString(b64)
			if !strings.HasSuffix(meta, ";base64") || err != nil {
				bad = ErrSignatureImage
				return ""
			}
			ct, err := signatureImageType(data)
			if err == nil {
				data, err = stripImageMetadata(ct, data)
			}
			if err != nil {
				bad = err
				return ""
			}
			sum := sha256.Sum256(data)
			id = "sig-" + hex.EncodeToString(sum[:16]) + "@godwit"
			stmts = append(stmts, sqlite.Statement{SQL: "INSERT OR IGNORE INTO signature_images(signature_id, cid, content_type, data) VALUES (?, ?, ?, ?)",
				Args: []any{sigID, id, ct, data}})
		} else if c, ok := strings.CutPrefix(src, "cid:"); ok && known[c] {
			id = c
			// Another signature's image (HTML mode) becomes this one's too: its
			// cleanup then never takes the bytes from under this one.
			stmts = append(stmts, sqlite.Statement{SQL: `INSERT OR IGNORE INTO signature_images(signature_id, cid, content_type, data)
				SELECT ?, cid, content_type, data FROM signature_images WHERE cid = ? LIMIT 1`, Args: []any{sigID, id}})
		}
		if id == "" {
			return ""
		}
		if seen[id] = true; len(seen) > SignatureImagesMax {
			bad = ErrSignatureImageCount
		}
		return "cid:" + id
	})
	if bad != nil {
		return "", nil, bad
	}
	// Stored clean: what the HTML mode typed is never kept as typed.
	return mail.SanitizeDraft(mail.StripMarkers(out), seen), stmts, nil
}

// unusedSignatureImages deletes the images that neither their signature nor
// any draft shows. It runs after a signature changes or goes.
var unusedSignatureImages = sqlite.Statement{SQL: `DELETE FROM signature_images
	WHERE instr(COALESCE((SELECT html FROM signatures WHERE id = signature_images.signature_id), ''), cid) = 0
	AND NOT EXISTS (SELECT 1 FROM drafts d WHERE instr(d.body_html, signature_images.cid) > 0)`}

// signatureImages looks up only the referenced signature images. With data false
// SQLite reports blob lengths without loading the bytes.
func (s *Service) signatureImages(ctx context.Context, cids []string, data bool) (map[string]mail.Attachment, error) {
	out := map[string]mail.Attachment{}
	if len(cids) == 0 {
		return out, nil
	}
	blob := "NULL"
	if data {
		blob = "data"
	}
	rows, err := s.db.Query(ctx, "SELECT cid, content_type, length(data), "+blob+" FROM signature_images WHERE cid IN (SELECT value FROM json_each(?)) GROUP BY cid", encodeList(cids))
	if err != nil {
		return nil, err
	}
	for _, r := range rows.Values {
		id := store.Str(r[0])
		b, _ := r[3].([]byte)
		out[id] = mail.Attachment{ContentID: id, ContentType: store.Str(r[1]), Data: b, Size: int(store.Int(r[2])), Inline: true}
	}
	return out, nil
}

// signatureCIDs is the set of the stored images' Content-IDs.
func (s *Service) signatureCIDs(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.Query(ctx, "SELECT DISTINCT cid FROM signature_images")
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(rows.Values))
	for _, r := range rows.Values {
		out[store.Str(r[0])] = true
	}
	return out, nil
}

// draftCIDs are the images a draft may show: the stored signature images
// and, for a draftID not 0, the draft's own (its inline attachments).
func (s *Service) draftCIDs(ctx context.Context, draftID int64) (map[string]bool, error) {
	cids, err := s.signatureCIDs(ctx)
	if err != nil || draftID == 0 {
		return cids, err
	}
	rows, err := s.db.Query(ctx, "SELECT cid FROM draft_attachments WHERE draft_id = ? AND cid IS NOT NULL", draftID)
	if err != nil {
		return nil, err
	}
	for _, r := range rows.Values {
		cids[store.Str(r[0])] = true
	}
	return cids, nil
}

// sanitizeDraft is mail.SanitizeDraft keeping the images draftCIDs has for
// draftID (0: the signature images only).
func (s *Service) sanitizeDraft(ctx context.Context, draftID int64, html string) (string, error) {
	cids, err := s.draftCIDs(ctx, draftID)
	if err != nil {
		return "", err
	}
	return mail.SanitizeDraft(html, cids), nil
}

// DraftImageFiles returns the stored files for the requested inline images
// belonging to this draft. The native interface reads them locally (ADR 0038).
func (s *Service) DraftImageFiles(ctx context.Context, draftID int64, cids []string) ([]InlineImage, error) {
	rows, err := s.db.Query(ctx, `SELECT cid, path, content_type FROM draft_attachments
		WHERE draft_id = ? AND cid IN (SELECT value FROM json_each(?))`, draftID, encodeList(cids))
	if err != nil {
		return nil, err
	}
	out := make([]InlineImage, 0, len(rows.Values))
	for _, r := range rows.Values {
		out = append(out, InlineImage{CID: store.Str(r[0]), Path: store.Str(r[1]), ContentType: store.Str(r[2])})
	}
	return out, nil
}

// InlineSignatureImages is html (sanitized compose HTML: a draft, a
// signature) with its signature images as data: URLs, for showing only:
// never save it as a draft, as saving drops data: sources.
func (s *Service) InlineSignatureImages(ctx context.Context, html string) (string, error) {
	cids := mail.CIDs(html)
	if len(cids) == 0 {
		return html, nil
	}
	imgs, err := s.signatureImages(ctx, cids, true)
	if err != nil {
		return "", err
	}
	return mail.ReplaceImages(html, func(src string) string {
		if a, ok := imgs[strings.TrimPrefix(src, "cid:")]; ok && strings.HasPrefix(src, "cid:") {
			return "data:" + a.ContentType + ";base64," + base64.StdEncoding.EncodeToString(a.Data)
		}
		return src
	}), nil
}

// SignatureShown is a draft's signature block (mail.SplitDraft's) as the
// compose page shows it, read-only: sanitized first (SanitizeDraft, the
// stored images kept, markers dropped), then its images as data: URLs. ""
// for none.
func (s *Service) SignatureShown(ctx context.Context, block string) (string, error) {
	if strings.TrimSpace(block) == "" {
		return "", nil
	}
	clean, err := s.sanitizeDraft(ctx, 0, mail.StripMarkers(block))
	if err != nil {
		return "", err
	}
	return s.InlineSignatureImages(ctx, clean)
}

// SignatureEditor is signature id as the settings editor's HTML:
// sanitized, a plain one escaped into a paragraph, its images as data: URLs.
func (s *Service) SignatureEditor(ctx context.Context, id int64) (string, error) {
	code, err := s.signatureCode(ctx, id)
	if err != nil {
		return "", err
	}
	return s.InlineSignatureImages(ctx, code)
}

// SignatureCode is signature id as stored, for the editor's HTML mode:
// sanitized, its images as cid:, one block per line.
func (s *Service) SignatureCode(ctx context.Context, id int64) (string, error) {
	code, err := s.signatureCode(ctx, id)
	return blockLines.ReplaceAllString(code, "$1\n<$2"), err
}

func (s *Service) signatureCode(ctx context.Context, id int64) (string, error) {
	sig, err := s.signature(ctx, id)
	if err != nil {
		return "", err
	}
	cids, err := s.signatureCIDs(ctx)
	if err != nil {
		return "", err
	}
	return SignatureEditorHTML(sig.HTML, cids), nil
}

// blockLines are the ends of blocks followed by an opening tag: SignatureCode
// breaks the line there (whitespace between blocks shows nothing).
var blockLines = regexp.MustCompile(`(</(?:p|div|table|tr|ul|ol|li|blockquote|h[1-6])>|<br/?>)<([^/])`)

// stripImageMetadata drops what a picture says about where and how it was
// taken: EXIF (GPS included), XMP, IPTC and comments. jpeg, png and webp lose
// those segments or chunks losslessly, so stripping twice gives the same bytes
// (and the same cid); a gif is re-encoded. Malformed data is ErrSignatureImage.
func stripImageMetadata(ct string, data []byte) ([]byte, error) {
	var out []byte
	ok := false
	switch ct {
	case "image/jpeg":
		out, ok = stripJPEG(data)
	case "image/png":
		out, ok = stripPNG(data)
	case "image/webp":
		out, ok = stripWebP(data)
	case "image/gif":
		g, err := gif.DecodeAll(bytes.NewReader(data))
		var b bytes.Buffer
		if err == nil && gif.EncodeAll(&b, g) == nil {
			out, ok = b.Bytes(), true
		}
	}
	if !ok {
		return nil, ErrSignatureImage
	}
	return out, nil
}

// stripJPEG drops the APP1 (EXIF, XMP), APP13 (IPTC) and COM segments before
// the scan; APP0, the ICC profile (APP2) and Adobe's APP14 stay.
func stripJPEG(data []byte) ([]byte, bool) {
	if len(data) < 2 || data[0] != 0xFF || data[1] != 0xD8 {
		return nil, false
	}
	out := []byte{0xFF, 0xD8}
	for i := 2; ; {
		if i+4 > len(data) || data[i] != 0xFF {
			return nil, false
		}
		m := data[i+1]
		if m == 0xDA { // start of scan: the rest is image data
			return append(out, data[i:]...), true
		}
		n := int(binary.BigEndian.Uint16(data[i+2:]))
		if n < 2 || i+2+n > len(data) {
			return nil, false
		}
		if m != 0xE1 && m != 0xED && m != 0xFE {
			out = append(out, data[i:i+2+n]...)
		}
		i += 2 + n
	}
}

// stripPNG drops the text, time and EXIF chunks.
func stripPNG(data []byte) ([]byte, bool) {
	const sig = "\x89PNG\r\n\x1a\n"
	if !bytes.HasPrefix(data, []byte(sig)) {
		return nil, false
	}
	out := []byte(sig)
	for i := len(sig); i < len(data); {
		if i+12 > len(data) {
			return nil, false
		}
		n := int(binary.BigEndian.Uint32(data[i:]))
		end := i + 12 + n
		if n < 0 || end > len(data) || end < i {
			return nil, false
		}
		switch string(data[i+4 : i+8]) {
		case "tEXt", "zTXt", "iTXt", "eXIf", "tIME":
		default:
			out = append(out, data[i:end]...)
		}
		i = end
	}
	return out, true
}

// stripWebP drops the EXIF and XMP chunks of the RIFF container and clears
// their flags in VP8X.
func stripWebP(data []byte) ([]byte, bool) {
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return nil, false
	}
	out := append([]byte(nil), data[:12]...)
	for i := 12; i < len(data); {
		if i+8 > len(data) {
			return nil, false
		}
		n := int(binary.LittleEndian.Uint32(data[i+4:]))
		end := i + 8 + n + n&1 // chunks pad to even
		if n < 0 || end > len(data) || end < i {
			return nil, false
		}
		switch string(data[i : i+4]) {
		case "EXIF", "XMP ":
		case "VP8X":
			at := len(out)
			out = append(out, data[i:end]...)
			if n > 0 {
				out[at+8] &^= 0x08 | 0x04 // the EXIF and XMP flags
			}
		default:
			out = append(out, data[i:end]...)
		}
		i = end
	}
	binary.LittleEndian.PutUint32(out[4:], uint32(len(out)-8))
	return out, true
}
