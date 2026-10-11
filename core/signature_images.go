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
	"strings"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/store"
)

// Signature images: png, jpeg, gif or webp, sniffed (never named), at most
// SignatureImageMax bytes each and SignatureImagesMax per signature. They are
// stored per account, content-addressed, and referenced in the signature as
// cid:sig-<hash>@godwit; a sent mail carries them as inline parts.
// Their metadata (EXIF with its GPS, XMP, text) is stripped before storing.
const (
	SignatureImageMax  = 512 << 10
	SignatureImagesMax = 4
)

var (
	// ErrSignatureImage is an image that is not png, jpeg, gif or webp, or is over SignatureImageMax.
	ErrSignatureImage = errors.New("a signature image must be a png, jpeg, gif or webp of at most 512 KB")
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

// SignatureImageURL is data as a data: URL for the signature editor, or ErrSignatureImage.
func SignatureImageURL(data []byte) (string, error) {
	ct, err := signatureImageType(data)
	if err != nil {
		return "", err
	}
	return "data:" + ct + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// signatureAssets turns the data: images of a signature's editor HTML into
// stored assets of the account: their sources become cid:, and the statements
// insert the new ones. Every image counts toward SignatureImagesMax.
func signatureAssets(accountID int64, editor string) (string, []sqlite.Statement, error) {
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
			stmts = append(stmts, sqlite.Statement{SQL: "INSERT OR IGNORE INTO signature_images(account_id, cid, content_type, data) VALUES (?, ?, ?, ?)",
				Args: []any{accountID, id, ct, data}})
		} else if c, ok := strings.CutPrefix(src, "cid:"); ok {
			id = c // kept only if the account has it (SanitizeDraft)
		}
		if id == "" {
			return ""
		}
		if seen[id] = true; len(seen) > SignatureImagesMax {
			bad = ErrSignatureImageCount
		}
		return "cid:" + id
	})
	return out, stmts, bad
}

// unusedSignatureImages deletes the account's images that neither its
// signature nor any of its drafts shows. It runs after the signature changes.
func unusedSignatureImages(accountID int64) sqlite.Statement {
	return sqlite.Statement{SQL: `DELETE FROM signature_images WHERE account_id = ?1
		AND instr((SELECT signature FROM accounts WHERE id = ?1), cid) = 0
		AND NOT EXISTS (SELECT 1 FROM drafts d WHERE d.account_id = ?1 AND instr(d.body_html, signature_images.cid) > 0)`,
		Args: []any{accountID}}
}

// signatureImages are the account's images by Content-ID; with data false,
// without their bytes.
func (s *Service) signatureImages(ctx context.Context, accountID int64, data bool) (map[string]mail.Attachment, error) {
	q := "SELECT cid, content_type, NULL FROM signature_images WHERE account_id = ?"
	if data {
		q = "SELECT cid, content_type, data FROM signature_images WHERE account_id = ?"
	}
	rows, err := s.db.Query(ctx, q, accountID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]mail.Attachment, len(rows.Values))
	for _, r := range rows.Values {
		b, _ := r[2].([]byte)
		id := store.Str(r[0])
		out[id] = mail.Attachment{ContentID: id, ContentType: store.Str(r[1]), Data: b, Size: len(b), Inline: true}
	}
	return out, nil
}

// signatureCIDs is the set of the account's image Content-IDs.
func (s *Service) signatureCIDs(ctx context.Context, accountID int64) (map[string]bool, error) {
	imgs, err := s.signatureImages(ctx, accountID, false)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(imgs))
	for id := range imgs {
		out[id] = true
	}
	return out, nil
}

// sanitizeDraft is mail.SanitizeDraft keeping the account's signature images.
func (s *Service) sanitizeDraft(ctx context.Context, accountID int64, html string) (string, error) {
	cids, err := s.signatureCIDs(ctx, accountID)
	if err != nil {
		return "", err
	}
	return mail.SanitizeDraft(html, cids), nil
}

// InlineSignatureImages is html (sanitized compose HTML of a draft of the
// account) with its signature images as data: URLs, for showing only: never
// save it, as saving drops data: sources.
func (s *Service) InlineSignatureImages(ctx context.Context, accountID int64, html string) (string, error) {
	if len(mail.CIDs(html)) == 0 {
		return html, nil
	}
	imgs, err := s.signatureImages(ctx, accountID, true)
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

// SignatureImageURLs are the data: URLs of the images html shows, in order:
// the compose page's read-only signature preview.
func (s *Service) SignatureImageURLs(ctx context.Context, accountID int64, html string) ([]string, error) {
	if len(mail.CIDs(html)) == 0 {
		return []string{}, nil
	}
	shown, err := s.InlineSignatureImages(ctx, accountID, html)
	if err != nil {
		return nil, err
	}
	out := []string{}
	mail.ReplaceImages(shown, func(src string) string {
		if strings.HasPrefix(src, "data:") {
			out = append(out, src)
		}
		return src
	})
	return out, nil
}

// SignatureEditor is the account's signature as the settings editor's HTML:
// sanitized, a plain one escaped into a paragraph, its images as data: URLs.
func (s *Service) SignatureEditor(ctx context.Context, accountID int64) (string, error) {
	acc, err := s.account(ctx, accountID)
	if err != nil {
		return "", err
	}
	cids, err := s.signatureCIDs(ctx, accountID)
	if err != nil {
		return "", err
	}
	return s.InlineSignatureImages(ctx, accountID, SignatureEditorHTML(acc.Signature, cids))
}

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
