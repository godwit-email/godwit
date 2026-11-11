package graph

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strconv"
	"strings"

	"github.com/godwit-email/godwit/backend"
)

// maxDepth bounds how deep a MIME structure is parsed.
const maxDepth = 64

type mimePart struct {
	contentType string
	params      map[string]string
	disposition string
	dispParams  map[string]string
	contentID   string
	encoding    string
	body        []byte // raw (transfer-encoded) leaf body
	children    []mimePart
}

func parseMIMEStructure(raw []byte) *mimePart {
	return readEntity(raw, 0)
}

func readEntity(raw []byte, depth int) *mimePart {
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return &mimePart{contentType: "text/plain", body: raw}
	}
	ct := m.Header.Get("Content-Type")
	mediaType, params, err := mime.ParseMediaType(ct)
	if err != nil || mediaType == "" {
		mediaType, params = "text/plain", nil
	}
	mediaType = strings.ToLower(mediaType)
	normParams := lowerMap(params)

	disp, dispParams, _ := mime.ParseMediaType(m.Header.Get("Content-Disposition"))
	disp = strings.ToLower(disp)
	normDispParams := lowerMap(dispParams)

	p := &mimePart{
		contentType: mediaType,
		params:      normParams,
		disposition: disp,
		dispParams:  normDispParams,
		contentID:   strings.TrimSpace(m.Header.Get("Content-Id")),
		encoding:    strings.ToLower(strings.TrimSpace(m.Header.Get("Content-Transfer-Encoding"))),
	}
	body, _ := io.ReadAll(m.Body)
	if strings.HasPrefix(mediaType, "multipart/") && normParams["boundary"] != "" && depth < maxDepth {
		mr := multipart.NewReader(bytes.NewReader(body), normParams["boundary"])
		for {
			part, err := mr.NextRawPart()
			if err != nil {
				break
			}
			var sub bytes.Buffer
			for k, vs := range part.Header {
				for _, v := range vs {
					sub.WriteString(k + ": " + v + "\r\n")
				}
			}
			sub.WriteString("\r\n")
			_, _ = io.Copy(&sub, part)
			p.children = append(p.children, *readEntity(sub.Bytes(), depth+1))
		}
		if len(p.children) > 0 {
			return p
		}
	}
	if strings.HasPrefix(p.contentType, "multipart/") {
		p.contentType = "text/plain"
	}
	p.body = body
	return p
}

var wordDec mime.WordDecoder

func lowerMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if d, err := wordDec.DecodeHeader(v); err == nil {
			v = d
		}
		out[strings.ToLower(k)] = v
	}
	return out
}

func (m *mimePart) toBackend(path string) backend.Part {
	if len(m.children) > 0 {
		p := backend.Part{ContentType: m.contentType, Params: m.params}
		for i := range m.children {
			cp := strconv.Itoa(i + 1)
			if path != "" {
				cp = path + "." + cp
			}
			p.Children = append(p.Children, m.children[i].toBackend(cp))
		}
		return p
	}
	if path == "" {
		path = "1"
	}
	return backend.Part{
		Ref:               backend.PartRef(path),
		ContentType:       m.contentType,
		Params:            m.params,
		Disposition:       m.disposition,
		DispositionParams: m.dispParams,
		ContentID:         m.contentID,
		Encoding:          m.encoding,
		Size:              int64(len(m.body)),
	}
}

func (m *mimePart) findLeaf(ref backend.PartRef) ([]byte, bool) {
	if len(m.children) == 0 {
		if ref == "1" {
			return m.body, true
		}
		return nil, false
	}
	cur := m
	for s := range strings.SplitSeq(string(ref), ".") {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > len(cur.children) {
			return nil, false
		}
		cur = &cur.children[n-1]
	}
	if len(cur.children) > 0 {
		return nil, false
	}
	return cur.body, true
}
