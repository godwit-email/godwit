package conn

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
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
	got := maskTrace([]byte("* 1 FETCH (" + strings.Repeat("A ", 400) + ")\r\n"))
	assert.Contains(t, got, "bytes cut) …")
	assert.Less(t, len(got), traceLineMax+40)
}

// A long FETCH line is cut in the middle: its end carries the size of the
// literal that follows, which is what a trace is read for (#611).
func TestMaskTraceKeepsALongLinesLiteralSize(t *testing.T) {
	line := "* 14766 FETCH (UID 14777 RFC822.SIZE 7296 BODYSTRUCTURE (" + strings.Repeat("\"text\" ", 60) +
		") BODY[HEADER.FIELDS (MESSAGE-ID DATE SUBJECT)] {1234}\r\n"
	got := maskTrace([]byte(line))
	assert.True(t, strings.HasPrefix(got, "* 14766 FETCH (UID 14777 RFC822.SIZE 7296 BODYSTRUCTURE ("), got)
	assert.True(t, strings.HasSuffix(got, "BODY[HEADER.FIELDS (MESSAGE-ID DATE SUBJECT)] {1234}"), got)
	assert.Contains(t, got, fmt.Sprintf("… (%d bytes cut) …", len(line)-2-traceLineMax))
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
