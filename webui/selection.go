package webui

import (
	"html"
	"strings"
	"unicode/utf8"

	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// blockTags are the tags that end a line of the plain text.
var blockTags = map[atom.Atom]bool{atom.P: true, atom.Div: true, atom.H1: true, atom.H2: true, atom.H3: true,
	atom.H4: true, atom.H5: true, atom.H6: true, atom.Li: true, atom.Pre: true, atom.Blockquote: true,
	atom.Ul: true, atom.Ol: true}

// containers hold blocks but are no line of their own.
var containers = map[atom.Atom]bool{atom.Blockquote: true, atom.Ul: true, atom.Ol: true}

// plainText is editable HTML's text as the page's editor has it, the ⌘K
// contract both sides keep: blocks joined by "\n", a soft break "\n" (the
// <br> that holds a block's last line open counts not), entities decoded.
// For UTF-16 offset i of the text, up to its length, lo[i] is the byte
// offset in editable right after character i-1 (before the tags that
// close it) and hi[i] right before character i (after the tags that open
// it); both are -1 inside a surrogate pair. Inside a character reference
// both snap to its start.
func plainText(editable string) (text string, lo, hi []int) {
	var sb strings.Builder
	z := xhtml.NewTokenizer(strings.NewReader(editable))
	pos := 0             // byte offset of the token read
	curLo, curHi := 0, 0 // the place after the last character, as lo and hi
	inBlock := false     // a block is open
	content := false     // it has a character or a soft break
	blocks := 0          // blocks begun
	br := false          // a <br> not yet counted
	brLo, brHi := 0, 0   // the place before it
	put := func(r rune, l, h int) {
		lo, hi = append(lo, l), append(hi, h)
		if r >= 0x10000 {
			lo, hi = append(lo, -1), append(hi, -1)
		}
		sb.WriteRune(r)
	}
	begin := func(at int) { // a new line, its text from at
		if blocks > 0 {
			put('\n', curLo, curHi)
		}
		blocks++
		inBlock, content, br, curLo, curHi = true, false, false, at, at
	}
	end := func() {
		if inBlock && br {
			curLo, curHi = brLo, brHi // the <br> holding the line open
		}
		inBlock, br = false, false
	}
	char := func() { // before a character or a soft break of the open block
		if br {
			put('\n', brLo, brHi)
			br = false
		}
		content = true
	}
	for {
		tt := z.Next()
		raw := z.Raw()
		at := pos
		pos += len(raw)
		switch tt {
		case xhtml.ErrorToken:
			end()
			return sb.String(), append(lo, curLo), append(hi, curHi)
		case xhtml.TextToken:
			s := string(raw)
			if !inBlock {
				if strings.Trim(s, " \t\n\r\f") == "" { // between blocks
					continue
				}
				begin(at)
			}
			for i := 0; i < len(s); {
				r, n := utf8.DecodeRuneInString(s[i:])
				dec := string(r)
				if r == '&' {
					if j := strings.IndexByte(s[i:], ';'); j > 0 && j < 40 {
						if d := html.UnescapeString(s[i : i+j+1]); d != s[i:i+j+1] {
							dec, n = d, j+1
						}
					}
				}
				char()
				for k, d := range []rune(dec) {
					if k == 0 {
						put(d, curLo, at+i)
					} else {
						put(d, at+i, at+i) // inside the reference: snapped to its start
					}
				}
				i += n
				curLo, curHi = at+i, at+i
			}
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken, xhtml.EndTagToken:
			name, _ := z.TagName()
			a := atom.Lookup(name)
			switch {
			case a == atom.Br:
				if !inBlock {
					begin(at)
				}
				char()
				br, brLo, brHi = true, curLo, at
				curLo, curHi = pos, pos
			case blockTags[a] && tt == xhtml.StartTagToken && !containers[a] && inBlock && !content:
				curLo, curHi = pos, pos // <li><p>: one line
			case blockTags[a]:
				end()
				if tt == xhtml.StartTagToken && !containers[a] {
					begin(pos)
				}
			default:
				curHi = pos
			}
		}
	}
}

// selectionRange is the byte range of editable for [start, end) of its
// plainText: tight (from right before the first character to right after
// the last), else the narrowest balanced range whose ends sit on any tag
// boundary of the gaps around it, so a formatted run is replaced with its
// tags; ties go to the earlier start, then the earlier end. With none
// balanced (across blocks), it is the tight range.
func selectionRange(editable string, lo, hi []int, start, end int) (int, int) {
	from, to := hi[start], lo[end]
	best := -1
	for _, s := range boundaries(editable, lo[start], hi[start]) {
		for _, e := range boundaries(editable, lo[end], hi[end]) {
			widen := hi[start] - s + e - lo[end] // bytes past the tight range: 0 for it
			if s < e && (best < 0 || widen < best) && balanced(editable[s:e]) {
				from, to, best = s, e, widen
			}
		}
	}
	return from, to
}

// boundaries are the offsets from a to b at which a tag starts or ends:
// editable[a:b] holds only tags.
func boundaries(editable string, a, b int) []int {
	out := []int{a}
	z := xhtml.NewTokenizer(strings.NewReader(editable[a:b]))
	for p := a; z.Next() != xhtml.ErrorToken; {
		p += len(z.Raw())
		out = append(out, p)
	}
	return out
}

// balanced reports whether every tag in h that opens closes in it, and
// every one that closes opened in it.
func balanced(h string) bool {
	z := xhtml.NewTokenizer(strings.NewReader(h))
	var open []string
	for {
		switch z.Next() {
		case xhtml.ErrorToken:
			return len(open) == 0
		case xhtml.StartTagToken:
			if name, _ := z.TagName(); !voids[atom.Lookup(name)] {
				open = append(open, string(name))
			}
		case xhtml.EndTagToken:
			name, _ := z.TagName()
			if len(open) == 0 || open[len(open)-1] != string(name) {
				return false
			}
			open = open[:len(open)-1]
		}
	}
}

var voids = map[atom.Atom]bool{atom.Br: true, atom.Img: true, atom.Hr: true}
