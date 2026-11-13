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

	"github.com/emersion/go-message"
	"github.com/emersion/go-message/charset"
	gomail "github.com/emersion/go-message/mail"
	"github.com/godwit-email/godwit/backend"
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
	// The List-Unsubscribe, List-Unsubscribe-Post and List-Id headers as
	// sent, unfolded and trimmed; "" when absent or over MaxListHeaderBytes.
	ListUnsubscribe, ListUnsubscribePost, ListID string
	// The Auto-Submitted and Precedence headers as sent: whether mail is automated.
	AutoSubmitted, Precedence string
	// AuthenticationResults contains only the selected receiving service's results.
	AuthenticationResults string
	AuthenticationTrace   []AuthenticationHeader
	DKIMSigned            bool
	RuleSent              bool // it has RuleHeader: a Godwit rule forwarded it
}

// MaxListHeaderBytes is the longest List-* header kept; a longer one counts as absent.
const MaxListHeaderBytes = 2 << 10

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

func fmtAddr(a *gomail.Address) string {
	return FormatAddress(strings.ToValidUTF8(a.Name, "\uFFFD"), a.Address) // as Subject in Parse
}

func addrs(h *gomail.Header, key string) []string {
	l, _ := h.AddressList(key) // partial lists are still useful
	var out []string
	for _, a := range l {
		out = append(out, fmtAddr(a))
	}
	return out
}

func listHeader(h *gomail.Header, k string) string {
	if v := strings.TrimSpace(h.Get(k)); len(v) <= MaxListHeaderBytes {
		return v
	}
	return ""
}

// validUTF8 replaces bad bytes in a header or BODYSTRUCTURE string with U+FFFD:
// servers pass them through raw (#467), and SQLite rejects non-UTF-8 text,
// which would fail the whole folder's sync.
func validUTF8(s string) string { return strings.ToValidUTF8(s, "\uFFFD") }

func cleanID(s string) string { return strings.TrimSpace(strings.Trim(strings.TrimSpace(s), "<>")) }

// classify decides whether a leaf is a displayable body and whether it is inline.
func classify(ct, disp, filename, cid string) (body, inline bool) {
	body = (ct == "text/plain" || ct == "text/html") && disp != "attachment" && filename == ""
	inline = disp == "inline" || (disp == "" && cid != "")
	return
}

// Parse parses a full RFC 5322 message.
func Parse(raw []byte) (*Parsed, error) {
	e, err := message.Read(bytes.NewReader(validHeader(raw)))
	if err != nil && !recoverable(err) {
		return nil, err
	}
	h := gomail.Header{Header: e.Header}
	p := &Parsed{}
	p.Subject, _ = h.Subject()
	p.Subject = strings.ToValidUTF8(p.Subject, "\uFFFD") // a utf-8 encoded word is not validated when decoded
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
	p.ListUnsubscribe, p.ListUnsubscribePost, p.ListID = listHeader(&h, "List-Unsubscribe"), listHeader(&h, "List-Unsubscribe-Post"), listHeader(&h, "List-Id")
	p.AutoSubmitted, p.Precedence = listHeader(&h, "Auto-Submitted"), listHeader(&h, "Precedence")
	p.AuthenticationTrace = authenticationTrace(&h)
	p.AuthenticationResults = ReceiverAuthenticationResults(p.AuthenticationTrace)
	p.DKIMSigned = strings.TrimSpace(h.Get("DKIM-Signature")) != ""
	p.RuleSent = h.Get(RuleHeader) != ""
	fromFields := h.FieldsByKey("From")
	fromCount := 0
	for fromFields.Next() {
		fromCount++
	}
	if fromCount != 1 || len(addrs(&h, "From")) != 1 {
		p.AuthenticationResults = ""
	}
	if err != nil {
		p.warn("header: %v", err)
	}
	p.walk(e, "", 0)
	return p, nil
}

// validHeader replaces bytes that are not UTF-8 in raw's header block with
// U+FFFD, as ValidText does for bodies: a raw 8-bit header (#468) must neither
// fail the folder's sync in SQLite nor lose the whole address list. The body is
// left to its declared charset.
func validHeader(raw []byte) []byte {
	end := len(raw)
	for _, sep := range []string{"\n\n", "\n\r\n"} {
		if i := bytes.Index(raw, []byte(sep)); i >= 0 && i < end {
			end = i
		}
	}
	if utf8.Valid(raw[:end]) {
		return raw
	}
	return append(bytes.ToValidUTF8(raw[:end], []byte("\uFFFD")), raw[end:]...)
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
			Part: path, Encoding: validUTF8(strings.ToLower(strings.TrimSpace(e.Header.Get("Content-Transfer-Encoding")))),
			Charset: validUTF8(params["charset"]), Filename: validUTF8(filename), ContentType: validUTF8(ct), ContentID: validUTF8(cid),
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

// CutUTF8 keeps at most n bytes of s, at a rune boundary, and reports whether it cut.
func CutUTF8(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	n = max(n, 0)
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n], true
}

// DecodePart decodes a separately fetched MIME part body using its recorded
// transfer encoding and (for text) charset.
func DecodePart(data []byte, encoding, cs string) ([]byte, error) {
	qp := false
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
		// A raw NUL is not legal quoted-printable, but some servers append one
		// after the final CRLF; Go's reader fails the whole part on it.
		qp = true
		var err error
		if data, err = io.ReadAll(quotedprintable.NewReader(bytes.NewReader(bytes.ReplaceAll(data, []byte{0}, nil)))); err != nil {
			return nil, err
		}
	}
	if cs = strings.ToLower(cs); cs != "" && cs != "utf-8" && cs != "us-ascii" {
		r, err := charset.Reader(cs, bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		if data, err = io.ReadAll(r); err != nil {
			return nil, err
		}
	}
	if qp {
		// An "=00" escape decodes to NUL, which no text field can hold. Dropped
		// only after the charset step: in UTF-16 a zero byte is half a character.
		data = bytes.ReplaceAll(data, []byte{0}, nil)
	}
	return data, nil
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

// ParseStructure walks a protocol backend's MIME structure and reports the
// displayable body parts to fetch and the attachment metadata; each one's Part
// is the backend's part reference.
// ponytail: message/rfc822 children are treated as one attachment, not descended into.
func ParseStructure(p *backend.Part) (textParts []Attachment, atts []Attachment) {
	var walk func(p *backend.Part, depth int)
	walk = func(p *backend.Part, depth int) {
		if strings.HasPrefix(p.ContentType, "multipart/") {
			if depth >= MaxMIMEDepth {
				return
			}
			for i := range p.Children {
				walk(&p.Children[i], depth+1)
			}
			return
		}
		filename := p.DispositionParams["filename"]
		if filename == "" {
			filename = p.Params["name"]
		}
		cid := cleanID(p.ContentID)
		body, inline := classify(p.ContentType, p.Disposition, filename, cid)
		a := Attachment{
			Part: string(p.Ref), Encoding: validUTF8(p.Encoding), Charset: validUTF8(p.Params["charset"]),
			Filename: validUTF8(filename), ContentType: validUTF8(p.ContentType), ContentID: validUTF8(cid), Inline: inline, Size: int(p.Size),
		}
		if body {
			textParts = append(textParts, a)
		} else {
			atts = append(atts, a)
		}
	}
	if p != nil {
		walk(p, 0)
	}
	return
}

// Snippet returns the first 200 characters with whitespace collapsed and the
// invisible characters gone: mail pads its preview text with zero-width and
// joiner characters between spaces (a preheader), which would fill the line
// with nothing to see. They are dropped, not made spaces, so none splits a
// word (a soft hyphen); the joiners only at a word's ends, as inside one they
// shape it (Persian's non-joiner, an emoji's joiner).
func Snippet(text string) string {
	if LooksLikeHTML(text) { // markup sent as the plain text: its text, not its tags
		text = HTMLToText(text)
	}
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

// LooksLikeHTML reports whether text, sent as plain text, is HTML markup: it
// starts with a tag and closes one. Some senders put their HTML part in the
// text/plain part as well.
func LooksLikeHTML(text string) bool {
	text = strings.TrimSpace(text)
	return strings.HasPrefix(text, "<") && strings.Contains(text, "</")
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
