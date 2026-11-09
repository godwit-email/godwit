package mailsync

import (
	"testing"
	"unicode/utf8"

	"github.com/godwit-email/godwit/backend"
)

// A body claiming UTF-8 but holding other bytes (GBK here) still parses to
// valid UTF-8, which SQLite requires of text (#151).
func TestToParsedInvalidUTF8(t *testing.T) {
	body := []byte("hi \xc4\xe3\xba\xc3 there")
	for _, sub := range []string{"plain", "html"} {
		p := toParsed(&backend.Content{
			Structure: &backend.Part{Ref: "1", ContentType: "text/" + sub, Params: map[string]string{"charset": "utf-8"}, Size: int64(len(body))},
			Text:      map[backend.PartRef][]byte{"1": body},
		})
		if !utf8.ValidString(p.Text) || !utf8.ValidString(p.HTML) {
			t.Fatalf("%s: invalid UTF-8 kept: text %q html %q", sub, p.Text, p.HTML)
		}
	}
}

// toParsed reads the envelope fields from the fetched headers, not ENVELOPE,
// which a server may send malformed and fail the whole FETCH.
func TestToParsedFromHeaders(t *testing.T) {
	raw := "Message-ID: <a@x>\r\nDate: Mon, 02 Jan 2006 15:04:05 +0000\r\n" +
		"Subject: =?gbk?B?xOO6ww==?=\r\nFrom: Ann <ann@x>\r\nTo: b@x, c@x\r\nCc: d@x\r\n\r\n"
	p := toParsed(&backend.Content{Size: 9, Header: []byte(raw)})
	if p.MessageID != "a@x" || p.Subject != "你好" || p.Date.Year() != 2006 || p.From != "Ann <ann@x>" ||
		len(p.To) != 2 || len(p.Cc) != 1 {
		t.Fatalf("got %+v", p)
	}
}
