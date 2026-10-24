package mail

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/emersion/go-message"
	gomail "github.com/emersion/go-message/mail"
	"golang.org/x/net/html"
)

// Outgoing is a message to be built. Addresses are "Name <addr>" or bare addresses.
type Outgoing struct {
	From, ReplyTo string
	To, Cc, Bcc   []string
	Subject, HTML string
	InReplyTo     string
	References    []string
	Attachments   []Attachment // Data must be filled
	// Related are images the HTML may show as cid:<ContentID> (the account's
	// signature images): sent inline, in multipart/related, when it does.
	Related []Attachment
}

func addrList(in []string) ([]*gomail.Address, error) {
	var out []*gomail.Address
	for _, s := range in {
		a, err := gomail.ParseAddress(s)
		if err != nil {
			return nil, fmt.Errorf("address %q: %w", s, err)
		}
		out = append(out, a)
	}
	return out, nil
}

// Build renders o as RFC 5322 bytes. Bcc is never written to the headers (the
// sender uses it only as envelope recipients). messageID is returned without
// angle brackets, matching Parsed.MessageID.
func Build(o Outgoing, now time.Time) (raw []byte, messageID string, err error) {
	from, err := gomail.ParseAddress(o.From)
	if err != nil {
		return nil, "", fmt.Errorf("from %q: %w", o.From, err)
	}
	at := strings.LastIndex(from.Address, "@")
	if at < 0 {
		return nil, "", fmt.Errorf("from %q has no domain", o.From)
	}
	var rnd [16]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return nil, "", err
	}
	messageID = hex.EncodeToString(rnd[:]) + "@" + from.Address[at+1:]

	var h gomail.Header
	h.SetDate(now)
	h.SetSubject(o.Subject)
	h.SetMessageID(messageID)
	h.SetAddressList("From", []*gomail.Address{from})
	for _, f := range []struct {
		key string
		v   []string
	}{{"To", o.To}, {"Cc", o.Cc}, {"Reply-To", nonEmpty(o.ReplyTo)}} {
		l, err := addrList(f.v)
		if err != nil {
			return nil, "", err
		}
		if len(l) > 0 {
			h.SetAddressList(f.key, l)
		}
	}
	if o.InReplyTo != "" {
		h.SetMsgIDList("In-Reply-To", []string{cleanID(o.InReplyTo)})
	}
	if len(o.References) > 0 {
		ids := make([]string, len(o.References))
		for i, r := range o.References {
			ids[i] = cleanID(r)
		}
		h.SetMsgIDList("References", ids)
	}

	// Only images of Related (the account's signature images) survive, and
	// only the ones the body shows are sent.
	keep := make(map[string]bool, len(o.Related))
	for _, a := range o.Related {
		keep[cleanID(a.ContentID)] = true
	}
	safe := SanitizeDraft(o.HTML, keep)
	shown := map[string]bool{}
	for _, id := range CIDs(safe) {
		shown[id] = true
	}
	var related []Attachment
	for _, a := range o.Related {
		if id := cleanID(a.ContentID); shown[id] {
			shown[id] = false // once
			related = append(related, a)
		}
	}

	// The tree: [mixed: ] [related: ] alternative(text, html) [, images] [, attachments].
	body := func(w *message.Writer) error {
		for _, p := range []struct{ ct, s string }{{"text/plain", HTMLToText(safe)}, {"text/html", safe}} {
			var ph message.Header
			ph.Set("Content-Type", p.ct+"; charset=utf-8")
			ph.Set("Content-Disposition", "inline")
			ph.Set("Content-Transfer-Encoding", "quoted-printable")
			if err := leaf(w, ph, []byte(p.s)); err != nil {
				return err
			}
		}
		return w.Close()
	}
	if len(related) > 0 {
		alt := body
		body = func(w *message.Writer) error {
			if err := sub(w, "multipart/alternative", alt); err != nil {
				return err
			}
			for _, a := range related {
				var ih message.Header
				ih.Set("Content-Type", a.ContentType)
				ih.Set("Content-Disposition", "inline")
				ih.Set("Content-ID", "<"+cleanID(a.ContentID)+">")
				ih.Set("Content-Transfer-Encoding", "base64")
				if err := leaf(w, ih, a.Data); err != nil {
					return err
				}
			}
			return w.Close()
		}
	}
	top := "multipart/alternative"
	if len(related) > 0 {
		top = `multipart/related; type="multipart/alternative"` // RFC 2387: the root's type
	}
	if len(o.Attachments) > 0 {
		inner, innerType := body, top
		top = "multipart/mixed"
		body = func(w *message.Writer) error {
			if err := sub(w, innerType, inner); err != nil {
				return err
			}
			for _, a := range o.Attachments {
				var ah gomail.AttachmentHeader
				ct := a.ContentType
				if ct == "" {
					ct = "application/octet-stream"
				}
				ah.Set("Content-Type", ct)
				ah.Set("Content-Disposition", "attachment")
				ah.SetFilename(a.Filename)
				ah.Set("Content-Transfer-Encoding", "base64")
				if err := leaf(w, ah.Header, a.Data); err != nil {
					return err
				}
			}
			return w.Close()
		}
	}
	h.Set("Content-Type", top)
	var buf bytes.Buffer
	w, err := message.CreateWriter(&buf, h.Header)
	if err != nil {
		return nil, "", err
	}
	if err := body(w); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), messageID, nil
}

// leaf writes a single part of w.
func leaf(w *message.Writer, h message.Header, data []byte) error {
	pw, err := w.CreatePart(h)
	if err != nil {
		return err
	}
	if _, err := pw.Write(data); err != nil {
		return err
	}
	return pw.Close()
}

// sub writes a multipart part of w with media type ct, filled by fill (which closes it).
func sub(w *message.Writer, ct string, fill func(*message.Writer) error) error {
	var h message.Header
	h.Set("Content-Type", ct)
	pw, err := w.CreatePart(h)
	if err != nil {
		return err
	}
	return fill(pw)
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

// VisibleText is HTMLToText of what the reader shows of src, a body as
// Sanitize leaves it: without what styles hide from it and the characters that
// show nothing (StripInvisible). An element is gone when nothing inside it can
// show (concealed); text in a zero font size is gone, but not a descendant's
// that sets its own size.
// shortcut: only an element's own style counts, not a <style> rule hiding its
// class nor the font shorthand; read those too if hidden text turns up that way.
func VisibleText(src string) string {
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		return ""
	}
	var drop func(n *html.Node, noFont bool)
	drop = func(n *html.Node, noFont bool) {
		for c := n.FirstChild; c != nil; {
			next := c.NextSibling
			switch {
			case c.Type == html.TextNode && noFont, c.Type == html.ElementNode && concealed(c):
				n.RemoveChild(c)
			case c.Type == html.ElementNode:
				drop(c, zeroFont(c, noFont))
			}
			c = next
		}
	}
	drop(doc, false)
	var b strings.Builder
	if err := html.Render(&b, doc); err != nil {
		return ""
	}
	return StripInvisible(HTMLToText(b.String()))
}

// concealed: an element nothing inside can show from, as a descendant cannot
// undo display:none or opacity, and max-height:0 hides only when it clips.
func concealed(n *html.Node) bool {
	decls := styleDecls(n)
	clipped := decls["overflow"] == "hidden" || decls["overflow"] == "clip"
	return decls["display"] == "none" || transparent(decls["opacity"]) ||
		(clipped && (decls["max-height"] == "0" || decls["max-height"] == "0px"))
}

// zeroFont: n's text has no size, given whether its parent's has none. A size
// relative to the parent's (em, %) or inherit keeps the parent's.
func zeroFont(n *html.Node, parent bool) bool {
	size := styleDecls(n)["font-size"]
	if size == "" || size == "inherit" {
		return parent
	}
	v, ok := length(size)
	switch {
	case !ok:
		return false // a keyword: small, medium…
	case v == 0:
		return true
	case strings.HasSuffix(size, "%") || (strings.HasSuffix(size, "em") && !strings.HasSuffix(size, "rem")):
		return parent
	}
	return false
}

// HTMLToText converts HTML to plain text: block elements become line breaks,
// list items get a "- " prefix, links render as "text (url)".
func HTMLToText(src string) string {
	var b strings.Builder
	nl := func() {
		if s := b.String(); s != "" && !strings.HasSuffix(s, "\n") {
			b.WriteByte('\n')
		}
	}
	var hrefs []string
	skip := 0
	z := html.NewTokenizer(strings.NewReader(src))
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		t := z.Token()
		switch tt {
		case html.TextToken:
			if skip > 0 {
				continue
			}
			s := strings.Join(strings.Fields("x"+t.Data+"x"), " ")
			s = s[1 : len(s)-1] // keeps one leading/trailing space where the source had whitespace
			if cur := b.String(); cur == "" || strings.HasSuffix(cur, "\n") || strings.HasSuffix(cur, " ") {
				s = strings.TrimLeft(s, " ")
			}
			b.WriteString(s)
		case html.StartTagToken, html.SelfClosingTagToken:
			switch t.Data {
			case "script", "style", "head":
				if tt == html.StartTagToken {
					skip++
				}
			case "br":
				b.WriteByte('\n')
			case "li":
				nl()
				b.WriteString("- ")
			case "td", "th": // a row's cells stay apart: "12 34", not "1234"
				if cur := b.String(); cur != "" && !strings.HasSuffix(cur, "\n") && !strings.HasSuffix(cur, " ") {
					b.WriteByte(' ')
				}
			case "a":
				u := ""
				for _, a := range t.Attr {
					if a.Key == "href" || (a.Key == "data-href" && u == "") {
						u = a.Val
					}
				}
				hrefs = append(hrefs, u)
			case "p", "div", "ul", "ol", "tr", "table", "blockquote", "h1", "h2", "h3", "h4", "h5", "h6", "pre", "hr":
				nl()
			}
		case html.EndTagToken:
			switch t.Data {
			case "script", "style", "head":
				if skip > 0 {
					skip--
				}
			case "a":
				if n := len(hrefs); n > 0 {
					if u := hrefs[n-1]; u != "" {
						b.WriteString(" (" + u + ")")
					}
					hrefs = hrefs[:n-1]
				}
			case "p", "div", "ul", "ol", "li", "tr", "table", "blockquote", "h1", "h2", "h3", "h4", "h5", "h6", "pre":
				nl()
			}
		}
	}
	return strings.TrimSpace(b.String())
}
