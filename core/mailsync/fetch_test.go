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
