package imapbackend

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

func headerSection() *imap.FetchItemBodySection {
	return &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader, Peek: true,
		HeaderFields: []string{"MESSAGE-ID", "DATE", "SUBJECT", "FROM", "TO", "CC", "REFERENCES", "IN-REPLY-TO", "CONTENT-TYPE", "RECEIVED", "AUTHENTICATION-RESULTS"}}
}

// Alibaba Mail echoes the header list it was asked for cut down and reordered
// (#458); the headers it sent are still the message's header, found under
// the section asked for (the go-imap fork's patch: a bump that loses it fails here).
func TestHeaderReducedEcho(t *testing.T) {
	raw := "From: Ann <ann@x>\r\nSubject: hi\r\nDate: Mon, 02 Jan 2006 15:04:05 +0000\r\nMessage-ID: <a@x>\r\n\r\n"
	echo := &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader,
		HeaderFields: []string{"FROM", "TO", "CC", "SUBJECT", "DATE", "MESSAGE-ID", "REFERENCES", "IN-REPLY-TO", "CONTENT-TYPE", "RECEIVED"}}
	m := &imapclient.FetchMessageBuffer{RFC822Size: 9, BodySection: []imapclient.FetchBodySectionBuffer{{Section: echo, Bytes: []byte(raw)}}}
	if got := string(m.FindBodySection(headerSection())); got != raw {
		t.Fatalf("header %q", got)
	}
}

// A message that is a single part has it as part "1"; a multipart's parts
// are numbered by their place.
func TestPartOf(t *testing.T) {
	if p := partOf(&imap.BodyStructureSinglePart{Type: "text", Subtype: "plain"}); p.Ref != "1" || p.ContentType != "text/plain" {
		t.Fatalf("single part %+v", p)
	}
	p := partOf(&imap.BodyStructureMultiPart{Subtype: "mixed", Children: []imap.BodyStructure{
		&imap.BodyStructureMultiPart{Subtype: "alternative", Children: []imap.BodyStructure{
			&imap.BodyStructureSinglePart{Type: "text", Subtype: "plain", Encoding: "QUOTED-PRINTABLE"},
		}},
		&imap.BodyStructureSinglePart{Type: "application", Subtype: "pdf", Size: 10,
			Extended: &imap.BodyStructureSinglePartExt{Disposition: &imap.BodyStructureDisposition{Value: "ATTACHMENT", Params: map[string]string{"filename": "a.pdf"}}}},
	}})
	if p.ContentType != "multipart/mixed" || len(p.Children) != 2 || p.Children[0].Children[0].Ref != "1.1" ||
		p.Children[0].Children[0].Encoding != "quoted-printable" || p.Children[1].Ref != "2" ||
		p.Children[1].Disposition != "attachment" || p.Children[1].DispositionParams["filename"] != "a.pdf" {
		t.Fatalf("multipart %+v", p)
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
	p := partOf(msgs[0].BodyStructure)
	if len(p.Children) != 2 || p.Children[0].Ref != "1" || p.Children[0].ContentType != "text/plain" ||
		p.Children[1].Ref != "2" || p.Children[1].DispositionParams["filename"] != "a.pdf" {
		t.Fatalf("structure %+v", p)
	}
}

// A Gmail server (X-GM-EXT-1) is asked for X-GM-MSGID with each imported
// message, and the go-imap fork parses it (a bump that loses the patch fails
// here); any other server is never sent the item, which it would refuse.
func TestImportItemsGmailMsgID(t *testing.T) {
	for _, gmail := range []bool{true, false} {
		cc, sc := net.Pipe()
		asked := make(chan bool, 1)
		go func() {
			r := bufio.NewReader(sc)
			caps := "IMAP4rev1"
			if gmail {
				caps += " X-GM-EXT-1"
			}
			fmt.Fprintf(sc, "* OK [CAPABILITY %s] hi\r\n", caps)
			for {
				line, err := r.ReadString('\n')
				if err != nil {
					return
				}
				asked <- strings.Contains(line, "X-GM-MSGID")
				fmt.Fprint(sc, "* 1 FETCH (UID 7 X-GM-MSGID 18000000000000000001)\r\n")
				fmt.Fprintf(sc, "%s OK done\r\n", strings.Fields(line)[0])
			}
		}()
		c := imapclient.New(cc, nil)
		s := &syncer{gmail: c.Caps().Has(GmailFeature), section: headerSection()}
		msgs, err := c.Fetch(imap.UIDSetNum(7), s.importItems()).Collect()
		c.Close()
		if err != nil || len(msgs) != 1 {
			t.Fatalf("gmail %v: fetch: %v, %d messages", gmail, err, len(msgs))
		}
		if got := <-asked; got != gmail {
			t.Fatalf("gmail %v: X-GM-MSGID asked: %v", gmail, got)
		}
		if gmail && gmailID(msgs[0]) != "18000000000000000001" {
			t.Fatalf("X-GM-MSGID %d", msgs[0].GmailMsgID)
		}
	}
}

// \Recent is the session's; any other flag passes as spelled.
func TestFlagsOf(t *testing.T) {
	got := flagsOf([]imap.Flag{imap.FlagSeen, "\\Recent", imap.FlagDeleted, "$Label1"})
	if !got.Seen || got.Flagged || len(got.Keywords) != 2 || got.Keywords[0] != `\Deleted` || got.Keywords[1] != "$Label1" {
		t.Fatalf("flags %+v", got)
	}
}
