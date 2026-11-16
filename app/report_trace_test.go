package app

import (
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"testing"

	"github.com/godwit-email/godwit/core"
)

// The backend logs through slog's text handler, which writes LogTrace's
// record as one line with its line breaks escaped. The report must still keep
// the trace's bytes (RFC822.SIZE, HEADER.FIELDS and dates are not hosts),
// and mask the error text before it, even one that carries an escaped
// "\nimap> " of its own (#801).
func TestReportKeepsAnSlogEscapedTrace(t *testing.T) {
	trace := `* 2536 FETCH (UID 2576 INTERNALDATE "xx-xxx-2020 12:33:59 .0000" RFC822.SIZE 9985 BODY[HEADER.FIELDS (DATE FROM MESSAGE-ID)] {1854}
xxxxxxxx. xxxx 000.00.00.00 (xxxxxxx [000.00.00.00])`
	errText := `sync "INBOX" on imap.qq.com: in response-data: \nimap> mail.example.com`
	masked := core.TracePrefix + strings.ReplaceAll(trace, "\n", "\n"+core.TracePrefix)

	var recent logRing
	slog.New(slog.NewTextHandler(&recent, nil)).Info(fmt.Sprintf("imap: traffic before %q, masked:\n%s", errText, masked))
	if strings.Contains(strings.TrimSpace(recent.String()), "\n") {
		t.Fatalf("slog should write the trace as one line: %q", recent.String())
	}

	u, err := url.Parse(issueURL("unexpected protocol error", recent.String()))
	if err != nil {
		t.Fatal(err)
	}
	got := u.Query().Get("logs")
	for _, want := range []string{
		`RFC822.SIZE 9985 BODY[HEADER.FIELDS (DATE FROM MESSAGE-ID)] {1854}`,
		`\"xx-xxx-2020 12:33:59 .0000\"`,
		`xxxxxxxx. xxxx 000.00.00.00 (xxxxxxx [000.00.00.00])`,
		`on <host>: in response-data`,
		`imap> <host>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report log lacks %q:\n%s", want, got)
		}
	}
	for _, host := range []string{"imap.qq.com", "mail.example.com"} {
		if strings.Contains(got, host) {
			t.Errorf("report log names %s:\n%s", host, got)
		}
	}
}

// slog writes the whole trace as one line, and issueURL drops whole lines
// to fit its link: the report must still keep the trace's newest responses
// when several long FETCH lines do not all fit.
func TestReportKeepsTheNewestOfALongEscapedTrace(t *testing.T) {
	part := `("TEXT" "PLAIN" ("xxxxxxx" "UTF-8") NIL NIL "QUOTED-PRINTABLE" 2399 57 NIL NIL NIL)`
	var lines []string
	for n := range 7 {
		lines = append(lines, fmt.Sprintf("* %d FETCH (UID %d BODYSTRUCTURE (%s \"ALTERNATIVE\") RFC822.SIZE 9985 BODY[HEADER.FIELDS (DATE FROM)] {1854}",
			2530+n, 2570+n, strings.Repeat(part, 7)), "… 1854 bytes in 30 lines of text", ")")
	}
	masked := core.TracePrefix + strings.Join(lines, "\n"+core.TracePrefix)

	var recent logRing
	slog.New(slog.NewTextHandler(&recent, nil)).Info(fmt.Sprintf("imap: traffic before %q, masked:\n%s", "sync \"INBOX\"", masked))
	u, err := url.Parse(issueURL("unexpected protocol error", recent.String()))
	if err != nil {
		t.Fatal(err)
	}
	got := u.Query().Get("logs")
	if !strings.Contains(got, "* 2536 FETCH (UID 2576 BODYSTRUCTURE") {
		t.Fatalf("report dropped the newest FETCH response:\n%s", got)
	}
	if strings.Contains(got, "* 2530 FETCH") {
		t.Fatalf("expected the oldest responses to go first:\n%s", got)
	}
}
