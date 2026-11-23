package conn

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMaskTrace(t *testing.T) {
	raw := "T7 UID FETCH 1:* (UID FLAGS BODY.PEEK[1]<0.3000>)\r\n" +
		"* 3 FETCH (UID 42 FLAGS (\\Seen $Personal) BODY[1]<0> {37}\r\n" +
		"Hi Alice,\r\ncall 13800138000 today\r\n" +
		"ok)\r\n" +
		"* (\"Grüße\")\n" +
		"T7 OK done\r\n" +
		"* 4 FET"
	want := "T7 UID FETCH 1:* (UID FLAGS BODY.PEEK[1]<0.3000>)\n" +
		"* 3 FETCH (UID 42 FLAGS (\\Seen .xxxxxxxx) BODY[1]<0> {37}\n" +
		"… 35 bytes in 2 lines of text\n" +
		"xx)\n" +
		"* (\"xx????x\") <LF>\n" +
		"T7 OK xxxx\n" +
		"* 0 xxx <no EOL>" // a cut-off FET does not show 4 is a sequence number
	assert.Equal(t, want, maskTrace([]byte(raw)))
}

// A number is kept where the IMAP grammar puts a protocol field, and masked
// anywhere else, however short: a literal's last line, a body line with
// parentheses and a quoted string are mail (#859).
func TestMaskTraceKeepsOnlyProtocolNumbers(t *testing.T) {
	for raw, want := range map[string]string{
		"T12 UID STORE 5,9:12 +FLAGS.SILENT (\\Seen)":           "T12 UID STORE 5,9:12 +FLAGS.SILENT (\\Seen)",
		"T13 UID FETCH 1:* (FLAGS) (CHANGEDSINCE 715194045007)": "T13 UID FETCH 1:* (FLAGS) (CHANGEDSINCE 715194045007)",
		"* 5 EXISTS": "* 5 EXISTS",
		"* OK [UIDVALIDITY 1700000000] UIDs valid":      "* OK [UIDVALIDITY 1700000000] xxxx xxxxx",
		"* SEARCH 2 3 81":                               "* SEARCH 2 3 81",
		"* VANISHED (EARLIER) 1:5,9":                    "* VANISHED (EARLIER) 1:5,9",
		"T4 OK [COPYUID 38505 304,319:320 3956:3958] x": "T4 OK [COPYUID 38505 304,319:320 3956:3958] x",
		"* STATUS \"Box 7\" (MESSAGES 3 UNSEEN 1)":      "* STATUS \"xxx 0\" (MESSAGES 3 UNSEEN 1)",
		"* 2 FETCH (X-GM-MSGID 1278455344230334865 MODSEQ (12) BINARY.SIZE[1.2] 640 BODY[2.1]<0.300> {12+}": "* 2 FETCH (X-GM-MSGID 1278455344230334865 MODSEQ (12) BINARY.SIZE[1.2] 640 BODY[2.1]<0.300> {12+}",
		"* 1 FETCH (UID 4 ENVELOPE (\"x\" \"Order SEARCH 123 \\\"UID 7\\\"\" NIL))":                         "* 1 FETCH (UID 4 ENVELOPE (\"x\" \"xxxxx SEARCH 000 .\"UID 0.\"\" NIL))",
		"* 3 cups of flour (sifted, 250 g)":                                                                 "* 0 xxxx xx xxxxx (xxxxxx. 000 x)",
		"your code (482913) expires in {5} minutes":                                                         "xxxx xxxx (000000) xxxxxxx xx {0} xxxxxxx",
		"T1000 (model 5)":               "x0000 (xxxxx 0)",
		"call T9 at 13800138000 today)": "xxxx x0 xx 00000000000 xxxxx)",
		"T9 CREATE \"Box 7\"":           "T9 xxxxxx \"xxx 0\"",
		"* SORT 2 3 6":                  "* SORT 2 3 6",
		"T2 FROM 55 (x)":                "T2 FROM 00 (x)",
		"TOTAL COUNT 3 (incl. VAT)":     "xxxxx COUNT 0 (xxxx. xxx)",
		"xx) UID 42 BODY[1] {12}":       "xx) UID 00 BODY[0] {12}",
		// a status response's text, after its [code], is for people (#880)
		"* OK [UIDNEXT 5] 13800138000":                     "* OK [UIDNEXT 5] 00000000000",
		"* NO [ALERT] SEARCH 13800138000":                  "* NO [ALERT] SEARCH 00000000000",
		"T5 NO SEARCH 42 (x)":                              "T5 NO SEARCH 00 (x)",
		"* BYE (UID 7)":                                    "* BYE (UID 0)",
		"+ Ready for 13800138000":                          ". xxxxx xxx 00000000000",
		"* 1 FETCH (UID 42 FLAGS (UID 13800138000) UID 5)": "* 1 FETCH (UID 42 FLAGS (UID 00000000000) UID 5)", // flags are user words
		"T6 UID STORE 4 +FLAGS (COUNT 77)":                 "T6 UID STORE 4 +FLAGS (COUNT 00)",
		"* ok [UIDNEXT 5] 13800138000":                     "* xx [UIDNEXT 5] 00000000000", // keywords are case-insensitive
		"* 1 FETCH (UID 42 flags (UID 13800138000))":       "* 1 FETCH (UID 42 xxxxx (UID 00000000000))",
		"+ SEARCH 13800138000":                             ". SEARCH 00000000000",                 // a continuation's text is for people
		"* LIST () \"/\" BODY[13800138000]<5>":             "* LIST () \".\" BODY[00000000000]<0>", // a section only in a FETCH
		"T8 UID FETCH 7 (BODY.PEEK[2.1]<0.40>)":            "T8 UID FETCH 7 (BODY.PEEK[2.1]<0.40>)",
		"* 1 FETCH (BODY[1] 77 UID 3)":                     "* 1 FETCH (BODY[1] 00 UID 3)",
	} {
		assert.Equal(t, want, maskTrace([]byte(raw+"\r\n")), raw)
	}
}

// A literal's content is mail however much it looks like IMAP: the trace
// counts its bytes from the {N} before it, and keeps no number in it. The
// response going on after it, and the next, keep theirs. A client literal
// waits for the server's "+ " before its bytes start (#880).
func TestMaskTraceKeepsNoNumberInALiteral(t *testing.T) {
	body := "* SEARCH 13800138000\r\nT3 OK [UIDNEXT 77] (x)\r\nsee {20}\r\nend"
	raw := fmt.Sprintf("T2 UID FETCH 9 (BODY.PEEK[TEXT])\r\n* 3 FETCH (UID 9 BODY[TEXT] {%d}\r\n%s UID 9)\r\n", len(body), body) +
		"* 4 FETCH (UID 10)\r\nT2 OK done\r\n" +
		"T3 APPEND INBOX {13}\r\n+ Ready\r\n* SEARCH 4242\r\nT3 OK [APPENDUID 1 11] done\r\n"
	want := "T2 UID FETCH 9 (BODY.PEEK[TEXT])\n" +
		"* 3 FETCH (UID 9 BODY[TEXT] {59}\n" +
		"… 56 bytes in 3 lines of text\n" +
		"xxx UID 0)\n" +
		"* 4 FETCH (UID 10)\n" +
		"T2 OK xxxx\n" +
		"T3 APPEND xxxxx {13}\n" +
		". xxxxx\n" +
		"..xxxxxx.0000\n" +
		"T3 OK [APPENDUID 1 11] xxxx"
	assert.Equal(t, want, maskTrace([]byte(raw)))
}

// A status response's text ending like a literal ({100}) is no literal: the
// responses after it still show.
func TestMaskTraceTakesNoLiteralFromStatusText(t *testing.T) {
	raw := "* OK ready {100}\r\n* 1 FETCH (UID 42)\r\nT1 NO bad {9}\r\n* 2 EXISTS\r\n"
	want := "* OK xxxxx {000}\n* 1 FETCH (UID 42)\nT1 NO xxx {0}\n* 2 EXISTS"
	assert.Equal(t, want, maskTrace([]byte(raw)))
}

func TestMaskTraceCutsLongLines(t *testing.T) {
	got := maskTrace([]byte("* 1 FETCH (" + strings.Repeat("A ", 1000) + ")\r\n"))
	assert.Contains(t, got, "bytes cut) …")
	assert.Less(t, len(got), traceLineMax+40)
}

// A long FETCH line is cut in the middle: its end carries the size of the
// literal that follows, which is what a trace is read for (#611).
func TestMaskTraceKeepsALongLinesLiteralSize(t *testing.T) {
	line := "* 14766 FETCH (UID 14777 RFC822.SIZE 7296 BODYSTRUCTURE (" + strings.Repeat("\"text\" ", 250) +
		") BODY[HEADER.FIELDS (MESSAGE-ID DATE SUBJECT)] {1234}\r\n"
	got := maskTrace([]byte(line))
	assert.True(t, strings.HasPrefix(got, "* 14766 FETCH (UID 14777 RFC822.SIZE 7296 BODYSTRUCTURE ("), got)
	assert.True(t, strings.HasSuffix(got, "BODY[HEADER.FIELDS (MESSAGE-ID DATE SUBJECT)] {1234}"), got)
	assert.Contains(t, got, fmt.Sprintf("… (%d bytes cut) …", len(line)-2-traceLineMax))
}

// A usual multipart FETCH line, BODYSTRUCTURE and all, is kept whole: the
// byte a body structure fails to parse on lies in its middle, and a cut there
// hid it (#801). A bare CR in it stays in the trace.
func TestMaskTraceKeepsAUsualFetchLineWhole(t *testing.T) {
	part := `("TEXT" "PLAIN" ("charset" "UTF-8") NIL NIL "QUOTED-PRINTABLE" 2399 57 NIL NIL NIL)`
	line := "* 2536 FETCH (UID 2576 FLAGS (\\Seen) BODYSTRUCTURE (" + strings.Repeat(part, 6) +
		` "ALTERNATIVE" ("boundary" "----=_NextPart_5FAB") NIL NIL)` + " RFC822.SIZE 9985 \r" +
		" BODY[HEADER.FIELDS (DATE FROM TO CC SUBJECT MESSAGE-ID REFERENCES IN-REPLY-TO)] {1854}\r\n"
	require.Greater(t, len(line), 600)
	got := maskTrace([]byte(line))
	assert.NotContains(t, got, "bytes cut")
	assert.Contains(t, got, "9985 \r BODY[HEADER.FIELDS")
	assert.True(t, strings.HasSuffix(got, "{1854}"), got)
}

func TestTraceKeepsTheEnd(t *testing.T) {
	tr := &trace{}
	tr.Write([]byte("T1 LOGIN alice hunter2\r\n"))
	tr.arm()
	for range 3 * traceMax / 10 {
		tr.Write([]byte("0123456789"))
	}
	tr.Write([]byte("end"))
	b := tr.bytes()
	assert.Len(t, b, traceMax)
	assert.True(t, strings.HasSuffix(string(b), "end"))
	assert.NotContains(t, string(b), "hunter2")
}
