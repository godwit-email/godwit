package conn

// Workarounds for Tencent Exmail (imap.exmail.qq.com), applied by imapConn.

// charsetFix repairs the charset parameter Tencent Exmail sends in every
// BODYSTRUCTURE / BODY text part, ("charset " " "utf-8"") instead of
// ("charset" "utf-8"), which go-imap cannot parse. It only rewrites that exact
// group; anything else, including a near miss, passes through unchanged. See
// https://github.com/godwit-email/godwit/issues/387.
type charsetFix struct {
	hold  []byte // bytes of a candidate group, withheld until it matches or fails
	phase int    // 0 prefix, 1 value, 2 after the first closing '"', 3 after the second
}

const (
	badCharsetPrefix = `("charset " " "`
	maxCharsetValue  = 64
)

// feed appends to out what b produces, which may be nothing while a candidate
// group is held back.
func (f *charsetFix) feed(out []byte, b byte) []byte {
	if len(f.hold) == 0 {
		if b == '(' {
			f.hold, f.phase = append(f.hold, b), 0
			return out
		}
		return append(out, b)
	}
	ok := false
	switch f.phase {
	case 0:
		ok = lowerASCII(b) == badCharsetPrefix[len(f.hold)]
		if ok && len(f.hold)+1 == len(badCharsetPrefix) {
			f.phase = 1
		}
	case 1:
		n := len(f.hold) - len(badCharsetPrefix)
		switch {
		case b == '"' && n > 0:
			ok, f.phase = true, 2
		case n < maxCharsetValue && charsetValueChar(b):
			ok = true
		}
	case 2:
		ok, f.phase = b == '"', 3
	case 3:
		if b == ')' {
			value := f.hold[len(badCharsetPrefix) : len(f.hold)-2]
			out = append(out, f.hold[:len(`("charset`)]...) // keep the server's case
			out = append(out, `" "`...)
			out = append(out, value...)
			out = append(out, `")`...)
			f.hold = f.hold[:0]
			return out
		}
	}
	if ok {
		f.hold = append(f.hold, b)
		return out
	}
	out = f.flush(out)
	return f.feed(out, b) // b may start a new candidate
}

// flush releases a held candidate unchanged.
func (f *charsetFix) flush(out []byte) []byte {
	out = append(out, f.hold...)
	f.hold = f.hold[:0]
	return out
}

func lowerASCII(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + 'a' - 'A'
	}
	return b
}

// charsetValueChar excludes everything that could end the group or start a
// literal, so a candidate never spans literal data.
func charsetValueChar(b byte) bool {
	return b > ' ' && b < 0x7f && b != '"' && b != '\\' && b != '{' && b != '(' && b != ')'
}

// headerFieldsFix drops the quotes go-imap puts around the field names of a
// HEADER.FIELDS / HEADER.FIELDS.NOT list: Tencent Exmail answers
// HEADER.FIELDS ("FROM" "SUBJECT") with an empty section and only matches
// HEADER.FIELDS (FROM SUBJECT). Both forms are valid IMAP. A name that is not
// an atom keeps its quotes; quoted strings and literals elsewhere in a command
// (mailbox names, search keys, APPEND data) are left alone. See
// https://github.com/godwit-email/godwit/issues/388.
type headerFieldsFix struct {
	recent []byte // the last plain (unquoted, non-literal) command bytes
	quoted bool   // inside a quoted string
	esc    bool   // after a '\' in a quoted string
	lit    int    // literal header: 0 none, 1 in "{N", 2 after "}", 3 after "}\r"
	n      int64  // literal size being parsed
	skip   int64  // literal bytes left to pass through
	list   []byte // a HEADER.FIELDS list being collected, from its '('
}

const maxHeaderList = 4096

var headerListKeys = []string{"HEADER.FIELDS ", "HEADER.FIELDS.NOT "}

// feed appends to out what p produces. A list is withheld until its ')' and
// sent unchanged if a line ends first, so nothing is held past a command.
func (f *headerFieldsFix) feed(out, p []byte) []byte {
	for _, b := range p {
		out = f.byte(out, b)
	}
	return out
}

func (f *headerFieldsFix) byte(out []byte, b byte) []byte {
	if f.skip > 0 {
		f.skip--
		return append(out, b)
	}
	if f.list != nil {
		return f.listByte(out, b)
	}
	if f.quoted {
		switch {
		case f.esc:
			f.esc = false
		case b == '\\':
			f.esc = true
		case b == '"':
			f.quoted = false
		}
		return append(out, b)
	}
	switch {
	case f.lit == 1 && (b >= '0' && b <= '9' && f.n < 1<<40 || b == '+'):
		if b != '+' {
			f.n = f.n*10 + int64(b-'0')
		}
	case f.lit == 1 && b == '}':
		f.lit = 2
	case f.lit == 2 && b == '\r':
		f.lit = 3
	case f.lit == 3 && b == '\n':
		f.lit, f.skip = 0, f.n
	case b == '{':
		f.lit, f.n = 1, 0
	default:
		f.lit = 0
	}
	if b == '(' && f.afterHeaderKey() {
		f.list = append(f.list[:0], b)
		f.recent = f.recent[:0]
		return out
	}
	if b == '"' {
		f.quoted = true
	}
	f.remember(b)
	return append(out, b)
}

func (f *headerFieldsFix) listByte(out []byte, b byte) []byte {
	if b == '\r' || b == '\n' || b == '{' && !f.quoted || len(f.list) >= maxHeaderList {
		// Not a list we understand: send it as it was.
		out = append(out, f.list...)
		f.list = nil
		return f.byte(out, b)
	}
	f.list = append(f.list, b)
	switch {
	case f.quoted && f.esc:
		f.esc = false
	case f.quoted && b == '\\':
		f.esc = true
	case b == '"':
		f.quoted = !f.quoted
	case !f.quoted && b == ')':
		out = unquoteHeaderList(out, f.list)
		f.list = nil
	}
	return out
}

func (f *headerFieldsFix) remember(b byte) {
	const keep = 18 // len("HEADER.FIELDS.NOT ")
	if len(f.recent) == keep {
		copy(f.recent, f.recent[1:])
		f.recent = f.recent[:keep-1]
	}
	f.recent = append(f.recent, b)
}

func (f *headerFieldsFix) afterHeaderKey() bool {
	for _, k := range headerListKeys {
		if len(f.recent) >= len(k) && string(f.recent[len(f.recent)-len(k):]) == k {
			return true
		}
	}
	return false
}

// unquoteHeaderList appends list, "(" item *(SP item) ")", to out with the
// quotes removed from every quoted item that is a plain atom. A list it cannot
// parse is appended unchanged.
func unquoteHeaderList(out, list []byte) []byte {
	start := len(out)
	out = append(out, '(')
	i := 1
	for i < len(list)-1 {
		if i > 1 {
			if list[i] != ' ' {
				return append(out[:start], list...)
			}
			out = append(out, ' ')
			i++
		}
		if list[i] != '"' {
			j := i
			for j < len(list)-1 && list[j] != ' ' {
				j++
			}
			if j == i {
				return append(out[:start], list...)
			}
			out = append(out, list[i:j]...)
			i = j
			continue
		}
		j := i + 1
		atom := true
		for j < len(list)-1 && list[j] != '"' {
			if list[j] == '\\' {
				atom = false
				j++
			} else if !atomChar(list[j]) {
				atom = false
			}
			j++
		}
		if j >= len(list)-1 { // unterminated
			return append(out[:start], list...)
		}
		if atom && j > i+1 {
			out = append(out, list[i+1:j]...)
		} else {
			out = append(out, list[i:j+1]...)
		}
		i = j + 1
	}
	return append(out, ')')
}

// atomChar is RFC 3501 ATOM-CHAR, also excluding ']', which would end the
// section spec.
func atomChar(b byte) bool {
	if b <= ' ' || b >= 0x7f {
		return false
	}
	switch b {
	case '(', ')', '{', '%', '*', '"', '\\', ']':
		return false
	}
	return true
}
