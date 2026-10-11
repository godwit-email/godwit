package mail

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

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

	safe := SanitizeCompose(o.HTML)
	var buf bytes.Buffer
	body := func(iw *gomail.InlineWriter) error {
		for _, p := range []struct{ ct, s string }{{"text/plain", HTMLToText(safe)}, {"text/html", safe}} {
			var ph gomail.InlineHeader
			ph.Set("Content-Type", p.ct+"; charset=utf-8")
			w, err := iw.CreatePart(ph)
			if err != nil {
				return err
			}
			if _, err := w.Write([]byte(p.s)); err != nil {
				return err
			}
			if err := w.Close(); err != nil {
				return err
			}
		}
		return iw.Close()
	}
	if len(o.Attachments) == 0 {
		iw, err := gomail.CreateInlineWriter(&buf, h)
		if err != nil {
			return nil, "", err
		}
		if err := body(iw); err != nil {
			return nil, "", err
		}
		return buf.Bytes(), messageID, nil
	}
	mw, err := gomail.CreateWriter(&buf, h)
	if err != nil {
		return nil, "", err
	}
	iw, err := mw.CreateInline()
	if err != nil {
		return nil, "", err
	}
	if err := body(iw); err != nil {
		return nil, "", err
	}
	for _, a := range o.Attachments {
		var ah gomail.AttachmentHeader
		ct := a.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		ah.Set("Content-Type", ct)
		ah.SetFilename(a.Filename)
		if a.Inline {
			ah.Set("Content-Disposition", "inline")
			ah.SetFilename(a.Filename)
		}
		if a.ContentID != "" {
			ah.Set("Content-ID", "<"+cleanID(a.ContentID)+">")
		}
		w, err := mw.CreateAttachment(ah)
		if err != nil {
			return nil, "", err
		}
		if _, err := w.Write(a.Data); err != nil {
			return nil, "", err
		}
		if err := w.Close(); err != nil {
			return nil, "", err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), messageID, nil
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
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
