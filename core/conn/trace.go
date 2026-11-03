package conn

import (
	"errors"
	"fmt"
	"log"
	"runtime"
	"strings"
	"sync"
	"weak"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// traceMax is how much of an IMAP connection's recent traffic its trace keeps.
// go-imap reads up to 4 KiB ahead of what it has parsed, so the response it
// failed on lies within the last few KiB, with room for what led up to it.
const traceMax = 16 << 10

// traceLineMax caps one line of a masked trace; a longer one is cut.
const traceLineMax = 300

// trace records an IMAP connection's traffic, both ways, once armed after
// login, so the commands and responses before a parse error can be reported.
// It is in memory only, and LogTrace masks it before it reaches the log.
type trace struct {
	mu    sync.Mutex
	armed bool
	buf   []byte
}

// Write is go-imap's DebugWriter: it never fails the connection.
func (t *trace) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.armed {
		return len(p), nil
	}
	t.buf = append(t.buf, p...)
	if len(t.buf) > 2*traceMax {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-traceMax:]...)
	}
	return len(p), nil
}

// arm starts recording: before it, the traffic carries the login.
func (t *trace) arm() {
	t.mu.Lock()
	t.armed = true
	t.mu.Unlock()
}

func (t *trace) bytes() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]byte(nil), t.buf[max(0, len(t.buf)-traceMax):]...)
}

// traces maps a client to its trace without keeping the client alive.
var traces sync.Map // weak.Pointer[imapclient.Client] -> *trace

func addTrace(c *imapclient.Client, t *trace) {
	k := weak.Make(c)
	traces.Store(k, t)
	runtime.AddCleanup(c, func(k weak.Pointer[imapclient.Client]) { traces.Delete(k) }, k)
}

// LogTrace logs the end of c's traffic, masked, when err is a protocol error:
// a server response go-imap could not parse. The log is what an error report
// carries, so a report of such an error shows what the server sent.
func LogTrace(c *imapclient.Client, err error) {
	if c == nil || !protocolErr(err) {
		return
	}
	v, ok := traces.Load(weak.Make(c))
	if !ok {
		return
	}
	if b := v.(*trace).bytes(); len(b) > 0 {
		log.Printf("imap: traffic before %q, masked:\n%s", firstLine(err.Error()), maskTrace(b))
	}
}

// protocolErr reports whether err is go-imap failing to parse what the server
// sent, rather than a network failure or a NO/BAD the server answered with.
func protocolErr(err error) bool {
	var imapErr *imap.Error // a NO or BAD status
	if err == nil || errors.As(err, &imapErr) {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "imapwire:") || strings.Contains(s, "in response") || strings.Contains(s, "unsupported response type")
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return s
}

// traceWords are the IMAP words a masked trace keeps as they are. Everything
// else that is not syntax or a number is masked, since it may be mail.
var traceWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`
		OK NO BAD BYE PREAUTH CAPABILITY ENABLE ENABLED ID NIL LOGOUT NOOP IDLE DONE
		SELECT EXAMINE CLOSE UNSELECT LIST LSUB STATUS NAMESPACE SEARCH ESEARCH SORT THREAD
		FETCH STORE COPY MOVE APPEND EXPUNGE EXPUNGED EXISTS RECENT UID MODSEQ CHANGEDSINCE VANISHED
		MESSAGES UIDNEXT UIDVALIDITY UNSEEN HIGHESTMODSEQ NOMODSEQ PERMANENTFLAGS READ-ONLY READ-WRITE
		COPYUID APPENDUID ALERT TRYCREATE CLOSED RETURN MIN MAX COUNT ALL SINCE BEFORE ON NOT OR
		FLAGS +FLAGS -FLAGS +FLAGS.SILENT -FLAGS.SILENT INTERNALDATE ENVELOPE BODYSTRUCTURE
		BODY BODY.PEEK BINARY BINARY.PEEK BINARY.SIZE RFC822 RFC822.SIZE RFC822.HEADER RFC822.TEXT
		HEADER HEADER.FIELDS HEADER.FIELDS.NOT TEXT MIME
		REFERENCES IN-REPLY-TO MESSAGE-ID SUBJECT FROM TO CC BCC DATE SENDER REPLY-TO
		X-GM-LABELS X-GM-MSGID X-GM-THRID X-GM-RAW
		PLAIN HTML MULTIPART MIXED ALTERNATIVE RELATED REPORT IMAGE APPLICATION AUDIO VIDEO MESSAGE
		CHARSET NAME FILENAME BOUNDARY FORMAT ATTACHMENT INLINE
		7BIT 8BIT BINARY BASE64 QUOTED-PRINTABLE US-ASCII UTF-8`) {
		traceWords[w] = true
	}
}

// maskTrace keeps a trace's IMAP syntax and masks its content, which may be
// mail: a word not in traceWords becomes x's, a non-ASCII byte '?', each
// keeping its length, so literal sizes can still be checked against what
// follows. Runs of lines that are only text (literal content) shrink to their
// size, and a long line is cut.
func maskTrace(b []byte) string {
	var out strings.Builder
	textBytes, textLines := 0, 0
	flushText := func() {
		if textLines > 0 {
			fmt.Fprintf(&out, "… %d bytes in %d lines of text\n", textBytes, textLines)
			textBytes, textLines = 0, 0
		}
	}
	for line := range strings.Lines(string(b)) {
		if !protocolLine(line) {
			textBytes += len(line)
			textLines++
			continue
		}
		flushText()
		m := maskLine(strings.TrimRight(line, "\r\n"))
		if len(m) > traceLineMax {
			m = fmt.Sprintf("%s… (+%d bytes)", m[:traceLineMax], len(m)-traceLineMax)
		}
		out.WriteString(m)
		out.WriteString(eol(line))
		out.WriteString("\n")
	}
	flushText()
	return strings.TrimRight(out.String(), "\n")
}

// eol shows how a line ended when it was not CRLF: a bare LF, or none (the
// trace ends mid-line).
func eol(line string) string {
	switch {
	case strings.HasSuffix(line, "\r\n"):
		return ""
	case strings.HasSuffix(line, "\n"):
		return " <LF>"
	}
	return " <no EOL>"
}

// protocolLine reports whether line is a command or response, or a literal's
// last line that the response goes on from, rather than literal text.
func protocolLine(line string) bool {
	if strings.HasPrefix(line, "* ") || strings.HasPrefix(line, "+") || strings.HasPrefix(line, "(") {
		return true
	}
	if tag, _, ok := strings.Cut(line, " "); ok && isTag(tag) {
		return true
	}
	return strings.ContainsAny(line, "(){}")
}

// isTag reports whether w is one of go-imap's tags: T1, T2, ...
func isTag(w string) bool {
	return len(w) > 1 && w[0] == 'T' && strings.Trim(w[1:], "0123456789") == ""
}

func isSyntax(c byte) bool {
	return strings.IndexByte(" ()[]{}<>\"\r\n", c) >= 0
}

func maskLine(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); {
		if isSyntax(s[i]) {
			out.WriteByte(s[i])
			i++
			continue
		}
		j := i
		for j < len(s) && !isSyntax(s[j]) {
			j++
		}
		out.WriteString(maskWord(s[i:j]))
		i = j
	}
	return out.String()
}

// maskWord keeps an IMAP word, a tag, a number up to 10 digits (a size, UID or
// section part like 1.2), a sequence set or a system flag (\Seen), and masks
// anything else: a longer number may be a phone number in mail text.
func maskWord(w string) string {
	if traceWords[w] || w == "*" || isTag(w) || len(w) <= 10 && strings.Trim(w, "0123456789.") == "" ||
		strings.ContainsAny(w, ":,") && strings.Trim(w, "0123456789:,*") == "" {
		return w
	}
	if len(w) > 1 && w[0] == '\\' && strings.Trim(strings.ToLower(w[1:]), "abcdefghijklmnopqrstuvwxyz") == "" {
		return w
	}
	b := []byte(w)
	for i, c := range b {
		switch {
		case c >= 0x80:
			b[i] = '?'
		case c >= '0' && c <= '9':
			b[i] = '0'
		case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
			b[i] = 'x'
		default:
			b[i] = '.'
		}
	}
	return string(b)
}
