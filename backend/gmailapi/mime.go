package gmailapi

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"net/textproto"
	"strconv"
	"strings"

	"github.com/godwit-email/godwit/backend"
)

const maxMIMEDepth = 64

func decodeBase64URL(s string) ([]byte, error) {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\r', '\n', ' ', '\t', '=':
			return -1
		case '+':
			return '-'
		case '/':
			return '_'
		}
		return r
	}, s)
	return base64.RawURLEncoding.DecodeString(s)
}

// parseMIME walks raw RFC 5322 MIME bytes and returns the backend.Part tree and
// the raw (still transfer-encoded) body bytes of each leaf keyed by its opaque PartRef.
func parseMIME(raw []byte) (*backend.Part, map[backend.PartRef][]byte) {
	parts := map[backend.PartRef][]byte{}
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		body := afterHeader(raw)
		ref := backend.PartRef("1")
		parts[ref] = body
		return &backend.Part{Ref: ref, ContentType: "text/plain", Size: int64(len(body))}, parts
	}
	body, _ := io.ReadAll(m.Body)
	root := walkPart(textproto.MIMEHeader(m.Header), body, "", 0, parts)
	return root, parts
}

func walkPart(hdr textproto.MIMEHeader, body []byte, path string, depth int, parts map[backend.PartRef][]byte) *backend.Part {
	ct, params := parseMedia(hdr.Get("Content-Type"), "text/plain")
	disp, dispParams := parseMedia(hdr.Get("Content-Disposition"), "")
	if strings.HasPrefix(ct, "multipart/") && params["boundary"] != "" && depth < maxMIMEDepth {
		p := &backend.Part{ContentType: ct, Params: params}
		mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		for i := 1; ; i++ {
			part, err := mr.NextRawPart()
			if err != nil {
				break
			}
			childBody, _ := io.ReadAll(part)
			cp := strconv.Itoa(i)
			if path != "" {
				cp = path + "." + cp
			}
			if child := walkPart(part.Header, childBody, cp, depth+1, parts); child != nil {
				p.Children = append(p.Children, *child)
			}
			_ = part.Close()
		}
		if len(p.Children) > 0 {
			return p
		}
	}
	if path == "" {
		path = "1"
	}
	ref := backend.PartRef(path)
	parts[ref] = body
	return &backend.Part{
		Ref:               ref,
		ContentType:       ct,
		Params:            params,
		Disposition:       disp,
		DispositionParams: dispParams,
		ContentID:         strings.TrimSpace(hdr.Get("Content-Id")),
		Encoding:          strings.ToLower(strings.TrimSpace(hdr.Get("Content-Transfer-Encoding"))),
		Size:              int64(len(body)),
	}
}

func parseMedia(v, def string) (string, map[string]string) {
	v = strings.TrimSpace(v)
	if v == "" {
		return def, nil
	}
	mt, params, err := mime.ParseMediaType(v)
	if err != nil {
		base, _, _ := strings.Cut(v, ";")
		base = strings.ToLower(strings.TrimSpace(base))
		if base == "" {
			return def, nil
		}
		return base, nil
	}
	mt = strings.ToLower(strings.TrimSpace(mt))
	if len(params) == 0 {
		params = nil
	}
	return mt, params
}

func afterHeader(raw []byte) []byte {
	if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
		return raw[i+4:]
	}
	if i := bytes.Index(raw, []byte("\n\n")); i >= 0 {
		return raw[i+2:]
	}
	return raw
}
