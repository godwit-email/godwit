package conn

import (
	"bytes"
	"io"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chunkConn serves reads from chunks, one per Read, and records writes.
type chunkConn struct {
	net.Conn
	chunks [][]byte
	wrote  bytes.Buffer
}

func (c *chunkConn) Read(p []byte) (int, error) {
	if len(c.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, c.chunks[0])
	if c.chunks[0] = c.chunks[0][n:]; len(c.chunks[0]) == 0 {
		c.chunks = c.chunks[1:]
	}
	return n, nil
}

func (c *chunkConn) Write(p []byte) (int, error) { return c.wrote.Write(p) }

// split cuts s into chunks of size n (1 = byte by byte).
func split(s string, n int) [][]byte {
	var out [][]byte
	for len(s) > 0 {
		k := min(n, len(s))
		out = append(out, []byte(s[:k]))
		s = s[k:]
	}
	return out
}

func readAll(t *testing.T, in string, chunk, bufSize int) string {
	t.Helper()
	c := &imapConn{Conn: &chunkConn{chunks: split(in, chunk)}}
	var got bytes.Buffer
	buf := make([]byte, bufSize)
	for {
		n, err := c.Read(buf)
		got.Write(buf[:n])
		if err == io.EOF {
			return got.String()
		}
		require.NoError(t, err)
	}
}

func TestCharsetFix(t *testing.T) {
	const (
		bad  = `* 2 FETCH (BODYSTRUCTURE (("TEXT" "PLAIN" ("charset " " "utf-8"") NIL NIL "8BIT" 588 44 NIL NIL NIL)("TEXT" "HTML" ("CHARSET " " "GB2312"") NIL NIL "8BIT" 2670 54 NIL NIL NIL) "ALTERNATIVE" ("BOUNDARY" "b1") NIL NIL))` + "\r\n"
		good = `* 2 FETCH (BODYSTRUCTURE (("TEXT" "PLAIN" ("charset" "utf-8") NIL NIL "8BIT" 588 44 NIL NIL NIL)("TEXT" "HTML" ("CHARSET" "GB2312") NIL NIL "8BIT" 2670 54 NIL NIL NIL) "ALTERNATIVE" ("BOUNDARY" "b1") NIL NIL))` + "\r\n"
	)
	for _, chunk := range []int{1, 3, 7, len(bad)} {
		for _, bufSize := range []int{1, 5, 4096} {
			assert.Equal(t, good, readAll(t, bad, chunk, bufSize), "chunk %d buf %d", chunk, bufSize)
		}
	}

	same := []string{
		// well formed
		`* 1 FETCH (BODYSTRUCTURE ("TEXT" "PLAIN" ("charset" "utf-8") NIL NIL "7BIT" 1 1))` + "\r\n",
		// near misses stay as they are
		`("charset " " "utf-8" "x")` + "\r\n",
		`("charset " " """)` + "\r\n",
		`("charset " " "utf-8""` + "\r\n",
		`("charset " " " "x")` + "\r\n",
		`("charset "` + "\r\n",
		`((("charset " " "` + "\r\n",
		// inside a literal
		"* 1 FETCH (BODY[] {25}\r\n" + `("charset " " "utf-8"")` + "\r\n UID 1)\r\n",
	}
	for _, s := range same {
		for _, chunk := range []int{1, len(s)} {
			assert.Equal(t, s, readAll(t, s, chunk, 4096), "%q chunk %d", s, chunk)
		}
	}

	// The empty-literal space still goes in, also right before a fixed group.
	assert.Equal(t,
		"* 1 FETCH (BODY[HEADER.FIELDS (X)] {0}\r\n UID 42 BODY (\"TEXT\" \"PLAIN\" (\"charset\" \"utf-8\")))\r\n",
		readAll(t, "* 1 FETCH (BODY[HEADER.FIELDS (X)] {0}\r\nUID 42 BODY (\"TEXT\" \"PLAIN\" (\"charset \" \" \"utf-8\"\")))\r\n", 1, 4096))
}

func TestHeaderFieldsFix(t *testing.T) {
	cases := []struct{ in, want string }{
		{`a1 UID FETCH 1:* (UID BODY.PEEK[HEADER.FIELDS ("MESSAGE-ID" "DATE" "SUBJECT")])` + "\r\n",
			`a1 UID FETCH 1:* (UID BODY.PEEK[HEADER.FIELDS (MESSAGE-ID DATE SUBJECT)])` + "\r\n"},
		{`a2 FETCH 1 (BODY.PEEK[1.HEADER.FIELDS.NOT ("Received" "X Y" "A\"B")])` + "\r\n",
			`a2 FETCH 1 (BODY.PEEK[1.HEADER.FIELDS.NOT (Received "X Y" "A\"B")])` + "\r\n"},
		{`a3 FETCH 1 (BODY[HEADER.FIELDS (FROM "TO")])` + "\r\n",
			`a3 FETCH 1 (BODY[HEADER.FIELDS (FROM TO)])` + "\r\n"},
		// quoted strings elsewhere are untouched
		{`a4 SELECT "HEADER.FIELDS ("INBOX")` + "\r\n", `a4 SELECT "HEADER.FIELDS ("INBOX")` + "\r\n"},
		{`a5 SEARCH SUBJECT "a \"HEADER.FIELDS (\"X\")" ALL` + "\r\n", `a5 SEARCH SUBJECT "a \"HEADER.FIELDS (\"X\")" ALL` + "\r\n"},
		// a literal's data is untouched
		{"a6 APPEND INBOX {33+}\r\nHEADER.FIELDS (\"FROM\" \"SUBJECT\")\r\n", "a6 APPEND INBOX {33+}\r\nHEADER.FIELDS (\"FROM\" \"SUBJECT\")\r\n"},
		// unterminated list goes out unchanged
		{`a7 FETCH 1 (BODY[HEADER.FIELDS ("FROM" "TO"` + "\r\n", `a7 FETCH 1 (BODY[HEADER.FIELDS ("FROM" "TO"` + "\r\n"},
	}
	for _, tc := range cases {
		for _, chunk := range []int{1, 4, len(tc.in)} {
			cc := &chunkConn{}
			c := &imapConn{Conn: cc}
			for _, p := range split(tc.in, chunk) {
				n, err := c.Write(p)
				require.NoError(t, err)
				require.Equal(t, len(p), n)
			}
			assert.Equal(t, tc.want, cc.wrote.String(), "%q chunk %d", tc.in, chunk)
		}
	}
}
