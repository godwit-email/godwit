package app

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/godwit-email/godwit/backend/conn"
	"github.com/godwit-email/godwit/core"
)

// The link 报告问题 opens carries an IMAP parse error's masked trace: it keeps
// the protocol numbers the parse error is read by (tag, sequence number, UID,
// literal size), and none of the digits of a fetched message body, even in a
// body line the trace shows because it has parentheses or ends the literal (#859).
func TestReportURLCarriesNoBodyDigits(t *testing.T) {
	const body = "Hi,\r\nyour code (482913) expires\r\nin 37 minutes, ref 6650"
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		c.Write([]byte("* OK [CAPABILITY IMAP4rev1] hi\r\n"))
		r := bufio.NewReader(c)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			f := strings.Fields(line)
			if len(f) < 2 {
				return
			}
			switch strings.ToUpper(f[1]) {
			case "LOGIN":
				c.Write([]byte(f[0] + " OK [CAPABILITY IMAP4rev1] logged in\r\n"))
			case "FETCH":
				fmt.Fprintf(c, "* 3 FETCH (UID 4242 RFC822.SIZE 9985 BODY[TEXT] {%d}\r\n%s)\r\n* (Seen)\r\n", len(body), body)
			default:
				c.Write([]byte(f[0] + " OK done\r\n"))
			}
		}
	}()
	c, err := conn.DialIMAP(context.Background(), conn.Endpoint{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port,
		Security: conn.None, Username: "alice", Password: "hunter2"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.Fetch(imap.SeqSetNum(3), &imap.FetchOptions{UID: true, RFC822Size: true,
		BodySection: []*imap.FetchItemBodySection{{Specifier: imap.PartSpecifierText}}}).Collect()
	if err == nil {
		t.Fatal("the fetch parsed a response it cannot")
	}

	h, _ := withAccount(t)
	log.SetOutput(RecentLog)
	log.SetFlags(0) // a timestamp's digits are not the body's
	t.Cleanup(func() { log.SetOutput(os.Stderr); log.SetFlags(log.LstdFlags) })
	conn.LogTrace(c, err)
	h.app.toastErr(err)
	h.click("报告问题")
	u, err := url.Parse(h.callArg("OpenURL"))
	if err != nil {
		t.Fatal(err)
	}
	logs := u.Query().Get("logs")
	for _, want := range []string{"* 3 FETCH (UID 4242 RFC822.SIZE 9985 BODY[TEXT] {" + fmt.Sprint(len(body)) + "}", "* (xxxx)"} {
		if !strings.Contains(logs, want) {
			t.Errorf("report log lacks %q:\n%s", want, logs)
		}
	}
	if !regexp.MustCompile(`(?m)^T\d+ FETCH 3 \(`).MatchString(logs) {
		t.Errorf("report log lacks the tagged FETCH command:\n%s", logs)
	}
	for _, d := range []string{"482913", "37", "6650"} {
		if strings.Contains(u.String(), d) {
			t.Errorf("report link carries %q from the message body:\n%s", d, logs)
		}
	}
}

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
