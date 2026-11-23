package conn

import (
	"errors"
	"fmt"
	"log"
	"runtime"
	"strconv"
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

// traceLineMax caps one line of a masked trace; a longer one is cut in the
// middle, keeping its last traceLineTail bytes: a FETCH line ends with the
// size of the literal that follows ({N}), which the trace is read for. The
// cap leaves a usual FETCH line with its BODYSTRUCTURE whole, since the byte
// a parse fails on is often in the middle of one (#801).
const (
	traceLineMax  = 1500
	traceLineTail = 60
)

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
		masked := TracePrefix + strings.ReplaceAll(maskTrace(b), "\n", "\n"+TracePrefix)
		log.Printf("imap: traffic before %q, masked:\n%s", firstLine(err.Error()), masked)
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

// TracePrefix 标记已脱敏的 trace 行，报告移除前缀后保留其原始字节。
const TracePrefix = "imap> "

// traceWords are the IMAP words a masked trace keeps as they are. Everything
// else that is not syntax, or a number maskLine keeps, is masked: it may be mail.
var traceWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`
		OK NO BAD BYE PREAUTH CAPABILITY ENABLE ENABLED ID NIL LOGOUT NOOP IDLE DONE
		SELECT EXAMINE CLOSE UNSELECT LIST LSUB STATUS NAMESPACE SEARCH ESEARCH SORT THREAD
		FETCH STORE COPY MOVE APPEND EXPUNGE EXPUNGED EXISTS RECENT UID MODSEQ CHANGEDSINCE VANISHED EARLIER
		MESSAGES UIDNEXT UIDVALIDITY UNSEEN HIGHESTMODSEQ NOMODSEQ PERMANENTFLAGS READ-ONLY READ-WRITE
		COPYUID APPENDUID MODIFIED ALERT TRYCREATE CLOSED RETURN MIN MAX COUNT ALL SINCE BEFORE ON NOT OR
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
// follows. Literal content, counted from its {N}, and runs of other lines that
// are only text shrink to their size, and a long line is cut.
func maskTrace(b []byte) string {
	var out strings.Builder
	textBytes, textLines := 0, 0
	flushText := func() {
		if textLines > 0 {
			fmt.Fprintf(&out, "… %d bytes in %d lines of text\n", textBytes, textLines)
			textBytes, textLines = 0, 0
		}
	}
	// lit counts the literal bytes still to come; wait holds a client
	// literal's size until the server's "+ " lets it start.
	lit, wait, server, fetch := 0, 0, false, false
	for line := range strings.Lines(string(b)) {
		if lit >= len(line) {
			lit -= len(line)
			textBytes += len(line)
			textLines++
			continue
		}
		head := ""
		if lit > 0 { // the literal ends in this line: the response goes on after it
			head, line, lit = maskWord(line[:lit]), line[lit:], 0
		} else {
			if wait > 0 && strings.HasPrefix(line, "+") {
				lit, wait = wait, 0
			} else if !strings.HasPrefix(line, "* ") {
				wait = 0
			}
			if !protocolLine(line) {
				textBytes += len(line)
				textLines++
				continue
			}
			server = strings.HasPrefix(line, "* ") || strings.HasPrefix(line, "+")
		}
		flushText()
		s := strings.TrimRight(line, "\r\n")
		if n, sync, ok := literalSize(s); ok && statusText(s) == len(s) { // a status's text holds no literal
			if server || !sync {
				lit = n
			} else {
				wait = n
			}
		}
		if head == "" { // a line after a literal goes on with the same response
			fetch = fetchLine(s)
		}
		m := head + maskLine(s, fetch)
		if len(m) > traceLineMax {
			head := traceLineMax - traceLineTail
			m = fmt.Sprintf("%s… (%d bytes cut) …%s", m[:head], len(m)-traceLineMax, m[len(m)-traceLineTail:])
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

// literalSize reads the literal size ending s: {38}, or {38+} that is sent
// without waiting for the server (sync false).
func literalSize(s string) (n int, sync, ok bool) {
	i := strings.LastIndexByte(s, '{')
	if i < 0 || !strings.HasSuffix(s, "}") {
		return 0, false, false
	}
	w := s[i+1 : len(s)-1]
	n, err := strconv.Atoi(strings.TrimSuffix(w, "+"))
	return n, !strings.HasSuffix(w, "+"), err == nil && n >= 0
}

// isTag reports whether w is one of go-imap's tags: T1, T2, ...
func isTag(w string) bool {
	return len(w) > 1 && w[0] == 'T' && strings.Trim(w[1:], "0123456789") == ""
}

func isSyntax(c byte) bool {
	return strings.IndexByte(" ()[]{}<>\"\r\n", c) >= 0
}

// numKeys are the IMAP words a number follows as a protocol field: a FETCH
// data item (UID 42), a status item or response code ([UIDNEXT 5]), or a
// command's or search result's sequence set (UID FETCH 1:*, SEARCH 2 3).
// Each is in traceWords too, so it shows.
var numKeys = map[string]bool{}

// statusWords open a status response, whose text after its [code] is for
// people. It and wordLists are matched in any case: they mask, and IMAP
// keywords are case-insensitive. Lists that keep a number need capitals.
var statusWords = map[string]bool{"OK": true, "NO": true, "BAD": true, "BYE": true, "PREAUTH": true}

// wordLists are the items whose ( ) lists flags or labels: user words, so a
// UID among them is not the data item.
var wordLists = map[string]bool{"FLAGS": true, "+FLAGS": true, "-FLAGS": true, "+FLAGS.SILENT": true, "-FLAGS.SILENT": true, "PERMANENTFLAGS": true, "X-GM-LABELS": true}

// msgData are the words after "* N" that make N a message sequence number.
var msgData = map[string]bool{"FETCH": true, "EXISTS": true, "EXPUNGE": true, "RECENT": true}

// sectionWords are the FETCH items whose [ ] holds a section, with numbered parts.
var sectionWords = map[string]bool{"BODY": true, "BODY.PEEK": true, "BINARY": true, "BINARY.PEEK": true, "BINARY.SIZE": true}

func init() {
	for _, w := range strings.Fields(`
		UID RFC822.SIZE BINARY.SIZE MODSEQ X-GM-MSGID X-GM-THRID
		MESSAGES UIDNEXT UIDVALIDITY UNSEEN RECENT HIGHESTMODSEQ CHANGEDSINCE MODIFIED
		COPYUID APPENDUID VANISHED EARLIER SEARCH SORT ALL MIN MAX COUNT
		FETCH STORE COPY MOVE EXPUNGE`) {
		numKeys[w] = true
	}
}

// maskLine masks a command or response word by word. A number is kept only
// where the IMAP grammar puts a protocol field, judged by what is around it,
// not by its shape: mail text is full of short numbers (#859). Kept are the
// command tag, "* N" before FETCH/EXISTS/EXPUNGE/RECENT, in a FETCH a section
// part or partial (BODY[1.2]<0.300>), a literal size ({38} ending the line), and on a
// line that starts a command or response, the numbers after a numKeys word.
// Anything in a quoted string is content, and maskTrace never passes it a
// literal's content: it counts each literal's bytes.
// shortcut: a trace that begins inside a literal (it keeps only the last
// traceMax bytes) shows that literal's lines as if protocol until the first
// tag or "* "; drop lines up to then if a report ever shows mail there.
func maskLine(s string, fetch bool) string {
	tag, _, _ := strings.Cut(s, " ")
	tagged := isTag(tag) && isCommandWord(nextWord(s, len(tag)))
	start := tagged || strings.HasPrefix(s, "* ")
	text := statusText(s)
	var out strings.Builder
	prev := "" // the last word outside quotes and sections
	afterKey, section, quoted := false, false, false
	words := 0 // depth inside a list of user words: flags, labels
	for i := 0; i < len(s); {
		if c := s[i]; isSyntax(c) {
			switch {
			case c == '"' && !(quoted && escaped(s, i)):
				quoted = !quoted
			case quoted:
			case c == '(' && (words > 0 || wordLists[strings.ToUpper(prev)] && strings.HasSuffix(s[:i], prev+" ")):
				words++
			case c == ')' && words > 0:
				words--
			case c == '[':
				section = fetch && i > 0 && !isSyntax(s[i-1]) && sectionWords[prev]
			case c == '<':
				section = fetch && i > 0 && s[i-1] == ']'
			case c == ']' || c == '>':
				section = false
			}
			out.WriteByte(c)
			i++
			continue
		}
		j := i
		for j < len(s) && !isSyntax(s[j]) {
			j++
		}
		w, keep := s[i:j], false
		switch {
		case quoted, i >= text, words > 0:
		case i == 0:
			keep = tagged
		case section:
			keep = strings.Trim(w, "0123456789.") == ""
		case s[i-1] == '{' && s[j:] == "}": // a literal size ends the line: {38}, or {38+} not synchronizing
			n := strings.TrimSuffix(w, "+")
			keep = n != "" && strings.Trim(n, "0123456789") == ""
		case isSet(w):
			keep = start && afterKey || i == 2 && s[:2] == "* " && msgData[nextWord(s, j)]
		}
		if !quoted && !section && words == 0 {
			afterKey = numKeys[w] || afterKey && keep
			prev = w
		}
		if keep {
			out.WriteString(w)
		} else {
			out.WriteString(maskWord(w))
		}
		i = j
	}
	return out.String()
}

// statusText is where a status response's text starts (OK, NO, BAD, BYE,
// PREAUTH, after a [code] if any), 0 for a continuation ("+ "), len(s) if s
// is neither: the text is for people, so no number in it is a protocol field.
func statusText(s string) int {
	if strings.HasPrefix(s, "+") { // a continuation: all text
		return 0
	}
	tag, rest, _ := strings.Cut(s, " ")
	w, after, _ := strings.Cut(rest, " ")
	if tag != "*" && !isTag(tag) || !statusWords[strings.ToUpper(w)] {
		return len(s)
	}
	i := len(s) - len(after)
	if strings.HasPrefix(after, "[") {
		if k := strings.IndexByte(after, ']'); k >= 0 {
			return i + k + 1
		}
		return len(s)
	}
	return i
}

// fetchLine reports whether s starts a FETCH command or response, the only
// place a section (BODY[1.2]<0.300>) is one: elsewhere BODY[1] is a name.
func fetchLine(s string) bool {
	f := strings.Fields(s)
	return len(f) > 2 && (f[0] == "*" && f[2] == "FETCH" || isTag(f[0]) && (f[1] == "FETCH" || f[1] == "UID" && f[2] == "FETCH"))
}

// escaped reports whether s[i] follows an odd run of backslashes.
func escaped(s string, i int) bool {
	return (i-len(strings.TrimRight(s[:i], `\`)))%2 == 1
}

// isCommandWord reports whether w could be a command or status after a tag:
// go-imap sends them in capitals (UID, CREATE, OK).
func isCommandWord(w string) bool {
	return w != "" && strings.Trim(w, "ABCDEFGHIJKLMNOPQRSTUVWXYZ-") == ""
}

// isSet reports whether w is a number or a sequence set (1:*,5).
func isSet(w string) bool {
	return strings.Trim(w, "0123456789:,*") == "" && strings.Trim(w, ":,*") != ""
}

// nextWord is the word after s[j:]'s spaces, "" if syntax comes first.
func nextWord(s string, j int) string {
	s = strings.TrimLeft(s[j:], " ")
	if k := strings.IndexFunc(s, func(r rune) bool { return r < 0x80 && isSyntax(byte(r)) }); k >= 0 {
		s = s[:k]
	}
	return s
}

// maskWord keeps an IMAP word, "*" or a system flag (\Seen), and masks
// anything else, digits included: maskLine decides which numbers to keep.
func maskWord(w string) string {
	if traceWords[w] || w == "*" {
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
