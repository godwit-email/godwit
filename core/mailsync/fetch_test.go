package mailsync

import (
	"testing"
	"unicode/utf8"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// A body claiming UTF-8 but holding other bytes (GBK here) still parses to
// valid UTF-8, which SQLite requires of text (#151).
func TestToParsedInvalidUTF8(t *testing.T) {
	body := []byte("hi \xc4\xe3\xba\xc3 there")
	for _, sub := range []string{"plain", "html"} {
		m := &imapclient.FetchMessageBuffer{BodyStructure: &imap.BodyStructureSinglePart{
			Type: "text", Subtype: sub, Params: map[string]string{"charset": "utf-8"}, Size: uint32(len(body)),
		}}
		p := toParsed(m, map[string][]byte{"1": body})
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
	m := &imapclient.FetchMessageBuffer{RFC822Size: 9, BodySection: []imapclient.FetchBodySectionBuffer{{Section: headerSection(), Bytes: []byte(raw)}}}
	p := toParsed(m, nil)
	if p.MessageID != "a@x" || p.Subject != "你好" || p.Date.Year() != 2006 || p.From != "Ann <ann@x>" ||
		len(p.To) != 2 || len(p.Cc) != 1 {
		t.Fatalf("got %+v", p)
	}
	if k := twinKey(m); k != "a@x\x009" {
		t.Fatalf("twinKey %q", k)
	}
}
