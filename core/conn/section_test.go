package conn

import (
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// Alibaba Mail echoes a HEADER.FIELDS list cut down and reordered (#458); the
// section still holds the headers it sent.
func TestFindBodySectionReducedEcho(t *testing.T) {
	asked := &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader, Peek: true,
		HeaderFields: []string{"MESSAGE-ID", "DATE", "SUBJECT", "FROM", "LIST-ID", "DKIM-SIGNATURE"}}
	m := &imapclient.FetchMessageBuffer{BodySection: []imapclient.FetchBodySectionBuffer{
		{Section: &imap.FetchItemBodySection{Specifier: imap.PartSpecifierText}, Bytes: []byte("body")},
		{Section: &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader,
			HeaderFields: []string{"From", "SUBJECT", "date", "MESSAGE-ID"}}, Bytes: []byte("hdr")},
	}}
	if got := string(FindBodySection(m, asked)); got != "hdr" {
		t.Fatalf("got %q", got)
	}
}

// An exact echo still wins, and a section that is not a subset of the asked
// names, another part's, or a HEADER.FIELDS.NOT one never matches.
func TestFindBodySectionStrict(t *testing.T) {
	asked := &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader, HeaderFields: []string{"FROM", "SUBJECT"}}
	hdr := func(part []int, names ...string) imapclient.FetchBodySectionBuffer {
		return imapclient.FetchBodySectionBuffer{Section: &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader, Part: part, HeaderFields: names}, Bytes: []byte(names[0])}
	}
	m := &imapclient.FetchMessageBuffer{BodySection: []imapclient.FetchBodySectionBuffer{hdr(nil, "SUBJECT"), hdr(nil, "FROM", "SUBJECT")}}
	if got := string(FindBodySection(m, asked)); got != "FROM" {
		t.Fatalf("exact echo not preferred: %q", got)
	}
	for _, s := range []imapclient.FetchBodySectionBuffer{
		hdr(nil, "FROM", "TO"),
		hdr([]int{2}, "FROM"),
		{Section: &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader, HeaderFieldsNot: []string{"TO"}}, Bytes: []byte("x")},
		{Section: &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader}, Bytes: []byte("x")},
	} {
		m := &imapclient.FetchMessageBuffer{BodySection: []imapclient.FetchBodySectionBuffer{s}}
		if got := FindBodySection(m, asked); got != nil {
			t.Fatalf("%+v matched: %q", s.Section, got)
		}
	}
}
