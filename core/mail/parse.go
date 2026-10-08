// Package mail parses MIME messages and sanitizes HTML bodies. It is pure:
// no storage, network or UI dependencies.
package mail

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime/quotedprintable"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-message"
	"github.com/emersion/go-message/charset"
	gomail "github.com/emersion/go-message/mail"
)

const (
	MaxBodyBytes = 2 << 20
	MaxMIMEDepth = 20

	maxAttachmentBytes = 25 << 20 // matches core.MaxMessageBytes; core cannot be imported from here
)

type Parsed struct {
	MessageID, Subject, From, ReplyTo, InReplyTo string
	To, Cc, References                           []string
	Date                                         time.Time
	Text, HTML                                   string
	Truncated                                    bool
	Attachments                                  []Attachment
}

// Attachment Part is the IMAP-style 1-based dot path ("1", "2.1").
// Data is empty when produced by ParseStructure.
type Attachment struct {
	Part, Encoding, Charset, Filename, ContentType, ContentID string
	Inline                                                    bool
	Size                                                      int
	Data                                                      []byte
}

func fmtAddr(a *gomail.Address) string {
	if a.Name == "" {
		return a.Address
	}
	return a.Name + " <" + a.Address + ">"
}

func addrs(h *gomail.Header, key string) []string {
	l, _ := h.AddressList(key) // partial lists are still useful
	var out []string
	for _, a := range l {
		out = append(out, fmtAddr(a))
	}
	return out
}

func cleanID(s string) string { return strings.TrimSpace(strings.Trim(strings.TrimSpace(s), "<>")) }

// classify decides whether a leaf is a displayable body and whether it is inline.
func classify(ct, disp, filename, cid string) (body, inline bool) {
	body = (ct == "text/plain" || ct == "text/html") && disp != "attachment" && filename == ""
	inline = disp == "inline" || (disp == "" && cid != "")
	return
}

// Parse parses a full RFC 5322 message.
func Parse(raw []byte) (*Parsed, error) {
	e, err := message.Read(bytes.NewReader(raw))
	if err != nil && !message.IsUnknownCharset(err) {
		return nil, err
	}
	h := gomail.Header{Header: e.Header}
	p := &Parsed{}
	p.Subject, _ = h.Subject()
	p.MessageID, _ = h.MessageID()
	p.Date, _ = h.Date()
	p.To, p.Cc = addrs(&h, "To"), addrs(&h, "Cc")
	if l := addrs(&h, "From"); len(l) > 0 {
		p.From = l[0]
	}
	if l := addrs(&h, "Reply-To"); len(l) > 0 {
		p.ReplyTo = l[0]
	}
	if l, _ := h.MsgIDList("In-Reply-To"); len(l) > 0 {
		p.InReplyTo = l[0]
	}
	p.References, _ = h.MsgIDList("References")
	p.walk(e, "", 0)
	return p, nil
}

func (p *Parsed) walk(e *message.Entity, path string, depth int) {
	ct, params, _ := e.Header.ContentType()
	if ct == "" {
		ct = "text/plain"
	}
	if mr := e.MultipartReader(); mr != nil {
		if depth >= MaxMIMEDepth {
			return
		}
		for i := 1; ; i++ {
			c, err := mr.NextPart()
			if err != nil && (c == nil || !message.IsUnknownCharset(err)) {
				return
			}
			cp := strconv.Itoa(i)
			if path != "" {
				cp = path + "." + cp
			}
			p.walk(c, cp, depth+1)
		}
	}
	if path == "" {
		path = "1"
	}
	disp, dparams, _ := e.Header.ContentDisposition()
	filename := dparams["filename"]
	if filename == "" {
		filename = params["name"]
	}
	cid := cleanID(e.Header.Get("Content-ID"))
	if body, inline := classify(ct, disp, filename, cid); body {
		p.addBody(ct == "text/html", e.Body)
	} else {
		data, _ := io.ReadAll(io.LimitReader(e.Body, maxAttachmentBytes))
		p.Attachments = append(p.Attachments, Attachment{
			Part: path, Encoding: strings.ToLower(strings.TrimSpace(e.Header.Get("Content-Transfer-Encoding"))),
			Charset: params["charset"], Filename: filename, ContentType: ct, ContentID: cid,
			Inline: inline, Size: len(data), Data: data,
		})
	}
}

func (p *Parsed) addBody(isHTML bool, r io.Reader) {
	dst := &p.Text
	if isHTML {
		dst = &p.HTML
	}
	room := MaxBodyBytes - len(*dst)
	if room <= 0 {
		p.Truncated = true
		return
	}
	b, _ := io.ReadAll(io.LimitReader(r, int64(room)+1))
	if len(b) > room {
		b = b[:room]
		for len(b) > 0 && !utf8.RuneStart(b[len(b)-1]) {
			b = b[:len(b)-1]
		}
		p.Truncated = true
	}
	*dst += string(b)
}

// DecodePart decodes a separately fetched MIME part body using its recorded
// transfer encoding and (for text) charset.
func DecodePart(data []byte, encoding, cs string) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "base64":
		clean := bytes.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return -1
			}
			return r
		}, data)
		var err error
		if data, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(string(clean), "=")); err != nil {
			return nil, err
		}
	case "quoted-printable":
		var err error
		if data, err = io.ReadAll(quotedprintable.NewReader(bytes.NewReader(data))); err != nil {
			return nil, err
		}
	}
	if cs = strings.ToLower(cs); cs == "" || cs == "utf-8" || cs == "us-ascii" {
		return data, nil
	}
	r, err := charset.Reader(cs, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

// ParseStructure walks an IMAP BODYSTRUCTURE and reports the displayable body
// parts to fetch and the attachment metadata, with IMAP part paths.
// ponytail: message/rfc822 children are treated as one attachment, not descended into.
func ParseStructure(bs imap.BodyStructure) (textParts []Attachment, atts []Attachment) {
	var walk func(bs imap.BodyStructure, path string, depth int)
	walk = func(bs imap.BodyStructure, path string, depth int) {
		switch b := bs.(type) {
		case *imap.BodyStructureMultiPart:
			if depth >= MaxMIMEDepth {
				return
			}
			for i, c := range b.Children {
				cp := strconv.Itoa(i + 1)
				if path != "" {
					cp = path + "." + cp
				}
				walk(c, cp, depth+1)
			}
		case *imap.BodyStructureSinglePart:
			if path == "" {
				path = "1"
			}
			var disp string
			var dp map[string]string
			if d := b.Disposition(); d != nil {
				disp, dp = strings.ToLower(d.Value), d.Params
			}
			ct := b.MediaType()
			filename := dp["filename"]
			if filename == "" {
				filename = b.Params["name"]
			}
			cid := cleanID(b.ID)
			body, inline := classify(ct, disp, filename, cid)
			a := Attachment{
				Part: path, Encoding: strings.ToLower(b.Encoding), Charset: b.Params["charset"],
				Filename: filename, ContentType: ct, ContentID: cid, Inline: inline, Size: int(b.Size),
			}
			if body {
				textParts = append(textParts, a)
			} else {
				atts = append(atts, a)
			}
		}
	}
	walk(bs, "", 0)
	return
}

// Snippet returns the first 200 characters with whitespace collapsed.
func Snippet(text string) string {
	s := strings.Join(strings.Fields(text), " ")
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200])
	}
	return s
}
