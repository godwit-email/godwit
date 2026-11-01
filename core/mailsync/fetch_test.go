package mailsync

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/godwit-email/godwit/core/mail"
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

// A body structure the server sends malformed (Gmail's empty multipart, #389)
// leaves the connection up, and fillStructures builds it from the whole message.
func TestFillStructuresMalformed(t *testing.T) {
	raw := "Content-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\nContent-Type: text/plain\r\n\r\nhi\r\n" +
		"--b\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=a.pdf\r\n\r\n%PDF\r\n--b--\r\n"
	cc, sc := net.Pipe()
	go func() {
		r := bufio.NewReader(sc)
		fmt.Fprint(sc, "* OK hi\r\n")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			tag := strings.Fields(line)[0]
			switch {
			case strings.Contains(line, "BODYSTRUCTURE"):
				fmt.Fprint(sc, `* 1 FETCH (UID 7 BODYSTRUCTURE (("ALTERNATIVE" ("BOUNDARY" "e7") NIL NIL)("APPLICATION" "PDF" NIL NIL NIL "BASE64" 4 NIL ("ATTACHMENT" ("FILENAME" "a.pdf")) NIL) "MIXED" ("BOUNDARY" "b") NIL NIL))`+"\r\n")
			case strings.Contains(line, "BODY.PEEK[]"):
				fmt.Fprintf(sc, "* 1 FETCH (UID 7 BODY[] {%d}\r\n%s)\r\n", len(raw), raw)
			}
			fmt.Fprintf(sc, "%s OK done\r\n", tag)
		}
	}()
	c := imapclient.New(cc, nil)
	defer c.Close()
	msgs, err := c.Fetch(imap.UIDSetNum(7), &imap.FetchOptions{UID: true, BodyStructure: &imap.FetchItemBodyStructure{Extended: true}}).Collect()
	if err != nil || len(msgs) != 1 || msgs[0].BodyStructure != nil {
		t.Fatalf("fetch: %v, %d messages", err, len(msgs))
	}
	if err := fillStructures(c, msgs); err != nil {
		t.Fatal(err)
	}
	parts, atts := mail.ParseStructure(msgs[0].BodyStructure)
	if len(parts) != 1 || parts[0].Part != "1" || len(atts) != 1 || atts[0].Part != "2" || atts[0].Filename != "a.pdf" {
		t.Fatalf("parts %+v, attachments %+v", parts, atts)
	}
}
