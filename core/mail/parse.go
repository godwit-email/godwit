// Package mail parses MIME messages and sanitizes HTML bodies. It is pure:
// no storage, network or UI dependencies.
package mail

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
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
	"golang.org/x/net/html"
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
	Damaged                                      bool     // best-effort result: some part failed to read or decode
	Warnings                                     []string // what went wrong, for logs
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

// FormatAddress renders a display name and address as one header address that
// net/mail parses back to the same parts. The name stays readable UTF-8 and is
// quoted (with \ and " escaped) unless it is a plain run of words; so is a local
// part that is not a dot-atom ("a b"@example.com).
func FormatAddress(name, addr string) string {
	if at := strings.LastIndexByte(addr, '@'); at > 0 && !dotAtom(addr[:at]) {
		addr = quote(addr[:at]) + addr[at:]
	}
	if name = strings.TrimSpace(name); name == "" {
		return addr
	}
	plain := !strings.Contains(name, "  ") && !strings.ContainsFunc(name, func(r rune) bool {
		return r != ' ' && !atext(r)
	})
	if !plain {
		name = quote(name)
	}
	return name + " <" + addr + ">"
}

// atext reports whether r may appear in an RFC 5322 atom (UTF-8 letters allowed).
func atext(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("!#$%&'*+-/=?^_`{|}~", r)
}

// dotAtom reports whether s is atoms joined by single dots.
func dotAtom(s string) bool {
	for a := range strings.SplitSeq(s, ".") {
		if a == "" || strings.ContainsFunc(a, func(r rune) bool { return !atext(r) }) {
			return false
		}
	}
	return true
}

func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func fmtAddr(a *gomail.Address) string { return FormatAddress(a.Name, a.Address) }

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
	if err != nil && !recoverable(err) {
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
	if err != nil {
		p.warn("header: %v", err)
	}
	p.walk(e, "", 0)
	return p, nil
}

// recoverable: go-message still returns a readable entity (raw body) for these.
func recoverable(err error) bool {
	return message.IsUnknownCharset(err) || message.IsUnknownEncoding(err)
}

func (p *Parsed) warn(format string, a ...any) {
	p.Damaged = true
	p.Warnings = append(p.Warnings, fmt.Sprintf(format, a...))
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
			if err != nil {
				if recoverable(err) {
					p.warn("part %s: %v", path, err)
				} else {
					if err != io.EOF {
						p.warn("multipart %q: %v", path, err)
					}
					return
				}
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
		p.addBody(ct == "text/html", e.Body, path)
	} else {
		data, err := io.ReadAll(io.LimitReader(e.Body, maxAttachmentBytes))
		if err != nil {
			p.warn("part %s: %v", path, err)
		}
		p.Attachments = append(p.Attachments, Attachment{
			Part: path, Encoding: strings.ToLower(strings.TrimSpace(e.Header.Get("Content-Transfer-Encoding"))),
			Charset: params["charset"], Filename: filename, ContentType: ct, ContentID: cid,
			Inline: inline, Size: len(data), Data: data,
		})
	}
}

func (p *Parsed) addBody(isHTML bool, r io.Reader, path string) {
	dst := &p.Text
	if isHTML {
		dst = &p.HTML
	}
	room := MaxBodyBytes - len(*dst)
	if room <= 0 {
		p.Truncated = true
		return
	}
	b, err := io.ReadAll(io.LimitReader(r, int64(room)+1))
	if err != nil {
		p.warn("part %s: %v", path, err)
	}
	if len(b) > room {
		cut := room // last complete rune boundary <= room
		for cut > 0 && !utf8.RuneStart(b[cut]) {
			cut--
		}
		b = b[:cut]
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

// metaCharset returns the charset an HTML part declares in a <meta> tag within
// its first 1024 bytes, found by tokenizing so commented-out tags do not count.
func metaCharset(b []byte) string {
	z := html.NewTokenizer(bytes.NewReader(b[:min(len(b), 1024)]))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return ""
		case html.StartTagToken, html.SelfClosingTagToken:
			name, more := z.TagName()
			if string(name) != "meta" {
				continue
			}
			for more {
				var k, v []byte
				k, v, more = z.TagAttr()
				switch string(k) {
				case "charset":
					return string(v)
				case "content":
					if _, params, err := mime.ParseMediaType(string(v)); err == nil && params["charset"] != "" {
						return params["charset"]
					}
				}
			}
		}
	}
}

// ValidText makes a decoded text part valid UTF-8 when its declared charset cs
// did not: an HTML part's <meta charset> when MIME names none, else U+FFFD for
// the bad bytes. No guessing beyond that: byte ranges cannot tell GBK from
// EUC-KR or Shift_JIS, and mojibake stored as text is worse than U+FFFD.
func ValidText(b []byte, cs string, isHTML bool) []byte {
	if utf8.Valid(b) {
		return b
	}
	if isHTML && cs == "" {
		if m := metaCharset(b); m != "" {
			if d, err := DecodePart(b, "", m); err == nil && utf8.Valid(d) {
				return d
			}
		}
	}
	return bytes.ToValidUTF8(b, []byte("\uFFFD"))
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

// Snippet returns the first 200 characters with whitespace collapsed and the
// invisible characters gone: mail pads its preview text with zero-width and
// joiner characters between spaces (a preheader), which would fill the line
// with nothing to see. They are dropped, not made spaces, so none splits a
// word (a soft hyphen); the joiners only at a word's ends, as inside one they
// shape it (Persian's non-joiner, an emoji's joiner).
func Snippet(text string) string {
	words := strings.Fields(StripInvisible(text))
	kept := words[:0]
	for _, w := range words {
		if w = strings.Trim(w, "\u200c\u200d"); w != "" {
			kept = append(kept, w)
		}
	}
	s := strings.Join(kept, " ")
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200])
	}
	return s
}

// StripInvisible drops the characters of text that show nothing (invisible),
// so the joiners stay: inside a word they shape it.
func StripInvisible(text string) string {
	return strings.Map(func(r rune) rune {
		if invisible(r) {
			return -1
		}
		return r
	}, text)
}

// invisible reports whether r shows nothing: a format character (zero-width
// space, soft hyphen, byte order mark…) but the joiners, which Snippet keeps
// inside a word, the combining grapheme joiner, and the Hangul fillers.
func invisible(r rune) bool {
	switch r {
	case '\u200c', '\u200d':
		return false
	case '\u034f', '\u115f', '\u1160', '\u3164', '\uffa0':
		return true
	}
	return unicode.Is(unicode.Cf, r)
}
