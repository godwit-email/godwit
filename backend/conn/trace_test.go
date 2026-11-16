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
		"* 3 FETCH (UID 42 FLAGS (\\Seen $Personal) BODY[1]<0> {38}\r\n" +
		"Hi Alice,\r\ncall 13800138000 today\r\n" +
		"ok)\r\n" +
		"* (\"Grüße\")\n" +
		"T7 OK done\r\n" +
		"* 4 FET"
	want := "T7 UID FETCH 1:* (UID FLAGS BODY.PEEK[1]<0.3000>)\n" +
		"* 3 FETCH (UID 42 FLAGS (\\Seen .xxxxxxxx) BODY[1]<0> {38}\n" +
		"… 35 bytes in 2 lines of text\n" +
		"xx)\n" +
		"* (\"xx????x\") <LF>\n" +
		"T7 OK xxxx\n" +
		"* 4 xxx <no EOL>"
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
