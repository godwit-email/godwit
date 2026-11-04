package conn

import (
	"slices"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// FindBodySection returns the bytes m holds for sec, as m.FindBodySection,
// but also takes a HEADER.FIELDS section whose echoed list is not the one asked
// for. Alibaba Mail answers HEADER.FIELDS (A B C) with BODY[HEADER.FIELDS (C A)]:
// it drops some of the names, present in the message or not, and reorders the
// rest (#458). go-imap matches the echo exactly, finds nothing, and the headers
// the server did send are lost. Here any non-empty subset of the asked names,
// in any order, matches, once no section matches exactly.
func FindBodySection(m *imapclient.FetchMessageBuffer, sec *imap.FetchItemBodySection) []byte {
	if b := m.FindBodySection(sec); b != nil || len(sec.HeaderFields) == 0 {
		return b
	}
	for _, s := range m.BodySection {
		if r := s.Section; r != nil && r.Specifier == sec.Specifier && slices.Equal(r.Part, sec.Part) &&
			len(r.HeaderFieldsNot) == 0 && (r.Partial == nil) == (sec.Partial == nil) &&
			(r.Partial == nil || r.Partial.Offset == sec.Partial.Offset) && subsetFold(r.HeaderFields, sec.HeaderFields) {
			return s.Bytes
		}
	}
	return nil
}

// subsetFold reports whether sub is non-empty and each of its names is in set,
// ignoring case.
func subsetFold(sub, set []string) bool {
	if len(sub) == 0 {
		return false
	}
	for _, n := range sub {
		if !slices.ContainsFunc(set, func(s string) bool { return strings.EqualFold(s, n) }) {
			return false
		}
	}
	return true
}
