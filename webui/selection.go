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
// offs[i] is the byte offset in editable of UTF-16 offset i of the text,
// for i up to its length: between two characters, after the inline tags
// that close and before those that open; -1 inside a surrogate pair.
func plainText(editable string) (text string, offs []int) {
	var sb strings.Builder
	z := xhtml.NewTokenizer(strings.NewReader(editable))
	pos := 0              // byte offset of the token read
	gap := 0              // where an offset between characters goes
	opened := false       // an inline tag opened since the last character
	inBlock := false      // a block is open
	content := false      // it has a character or a soft break
	blocks := 0           // blocks begun
	br, brGap := false, 0 // a <br> not yet counted, and the gap before it
	put := func(r rune, at int) {
		offs = append(offs, at)
		if r >= 0x10000 {
			offs = append(offs, -1)
		}
		sb.WriteRune(r)
	}
	begin := func(at int) { // a new line, its text from at
		if blocks > 0 {
			put('\n', gap)
		}
		blocks++
		inBlock, content, br, gap, opened = true, false, false, at, false
	}
	end := func() {
		if inBlock && br {
			gap = brGap // the <br> holding the line open
		}
		inBlock, br = false, false
	}
	char := func() { // before a character or a soft break of the open block
		if br {
			put('\n', brGap)
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
			return sb.String(), append(offs, gap)
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
				if r == '&' {
					if j := strings.IndexByte(s[i:], ';'); j > 0 && j < 40 {
						if dec := html.UnescapeString(s[i : i+j+1]); dec != s[i:i+j+1] {
							char()
							for _, d := range dec {
								put(d, gap) // inside the reference: snapped to its start
							}
							i += j + 1
							gap, opened = at+i, false
							continue
						}
					}
				}
				char()
				put(r, gap)
				i += n
				gap, opened = at+i, false
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
				br, brGap = true, gap
				gap, opened = pos, false
			case blockTags[a] && tt == xhtml.StartTagToken && !containers[a] && inBlock && !content:
				gap = pos // <li><p>: one line
			case blockTags[a]:
				end()
				if tt == xhtml.StartTagToken && !containers[a] {
					begin(pos)
				}
			case tt == xhtml.EndTagToken:
				if !opened {
					gap = pos
				}
			default:
				opened = true
			}
		}
	}
}
