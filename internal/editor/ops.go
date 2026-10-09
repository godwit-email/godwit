package editor

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/richtext"
)

// The operations here are pure: they never change the Doc they are given
// (its Blocks and Inlines slices are shared with undo snapshots), and return
// a new one.

type (
	Doc    = richtext.Doc
	Block  = richtext.Block
	Inline = richtext.Inline
	Pos    = richtext.Pos
)

// blockText is a block's text: its inlines' together.
func blockText(b Block) string {
	var sb strings.Builder
	for _, in := range b.Inlines {
		sb.WriteString(in.Text)
	}
	return sb.String()
}

func runeLen(b Block) int {
	n := 0
	for _, in := range b.Inlines {
		n += utf8.RuneCountInString(in.Text)
	}
	return n
}

// plain is the text input methods see: the blocks' text, a newline between
// blocks.
func plain(d Doc) string {
	s := make([]string, len(d.Blocks))
	for i, b := range d.Blocks {
		s[i] = blockText(b)
	}
	return strings.Join(s, "\n")
}

// offset is p as a UTF-16 offset in plain(d).
func offset(d Doc, p Pos) int {
	off := 0
	for i := 0; i < p.Block && i < len(d.Blocks); i++ {
		off += ui.UTF16Len(blockText(d.Blocks[i])) + 1
	}
	if p.Block >= len(d.Blocks) {
		return max(0, off-1)
	}
	t := blockText(d.Blocks[p.Block])
	return off + ui.UTF16Len(string([]rune(t)[:min(max(p.Rune, 0), utf8.RuneCountInString(t))]))
}

// posAt is the place of UTF-16 offset off in plain(d), clamped to it.
func posAt(d Doc, off int) Pos {
	off = max(off, 0)
	for i, b := range d.Blocks {
		t := blockText(b)
		if n := ui.UTF16Len(t); off <= n || i == len(d.Blocks)-1 {
			return Pos{Block: i, Rune: utf8.RuneCountInString(t[:ui.UTF16ByteOffset(t, off)])}
		} else {
			off -= n + 1
		}
	}
	return Pos{}
}

func less(a, b Pos) bool { return a.Block < b.Block || a.Block == b.Block && a.Rune < b.Rune }

// style is an inline's style without its text.
func style(in Inline) Inline { in.Text = ""; return in }

// split cuts ins at rune r into new slices.
func split(ins []Inline, r int) (left, right []Inline) {
	for i, in := range ins {
		n := utf8.RuneCountInString(in.Text)
		if r >= n {
			left = append(left, in)
			r -= n
			continue
		}
		if r > 0 {
			k := len(string([]rune(in.Text)[:r]))
			a, b := in, in
			a.Text, b.Text = in.Text[:k], in.Text[k:]
			left = append(left, a)
			right = append(right, b)
			i++
		}
		right = append(right, ins[i:]...)
		return left, right
	}
	return left, nil
}

// normalize drops empty runs and joins neighbours of one style.
func normalize(ins []Inline) []Inline {
	out := ins[:0:0]
	for _, in := range ins {
		if in.Text == "" {
			continue
		}
		if n := len(out); n > 0 && style(out[n-1]) == style(in) {
			out[n-1].Text += in.Text
			continue
		}
		out = append(out, in)
	}
	return out
}

func cat(parts ...[]Inline) []Inline { return normalize(slices.Concat(parts...)) }

// replace puts frag's blocks in place of a..b: the first joins the text
// before a, in a's block, and the last the text after b. It returns where
// frag ends.
func replace(d Doc, a, b Pos, frag Doc) (Doc, Pos) {
	if less(b, a) {
		a, b = b, a
	}
	head, _ := split(d.Blocks[a.Block].Inlines, a.Rune)
	_, tail := split(d.Blocks[b.Block].Inlines, b.Rune)
	fb := frag.Blocks
	if len(fb) == 0 {
		fb = []Block{{}}
	}
	first := d.Blocks[a.Block]
	var mid []Block
	if len(fb) == 1 {
		first.Inlines = cat(head, fb[0].Inlines, tail)
	} else {
		first.Inlines = cat(head, fb[0].Inlines)
		mid = slices.Clone(fb[1:])
		last := &mid[len(mid)-1]
		last.Inlines = cat(last.Inlines, tail)
	}
	blocks := slices.Concat(d.Blocks[:a.Block], []Block{first}, mid, d.Blocks[b.Block+1:])
	end := Pos{Block: a.Block + len(fb) - 1, Rune: runeLen(fb[len(fb)-1])}
	if len(fb) == 1 {
		end.Rune += a.Rune
	}
	return Doc{Blocks: blocks}, end
}

// textFrag is s in style st, its lines as blocks of like's kind.
func textFrag(s string, st Inline, like Block) Doc {
	var d Doc
	for line := range strings.SplitSeq(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		b := Block{Kind: like.Kind, Level: like.Level, Ordered: like.Ordered}
		if line != "" {
			st.Text = line
			b.Inlines = []Inline{st}
		}
		d.Blocks = append(d.Blocks, b)
	}
	return d
}

// slice is the part of d from a to b.
func slice(d Doc, a, b Pos) Doc {
	if less(b, a) {
		a, b = b, a
	}
	var out Doc
	for i := a.Block; i <= b.Block; i++ {
		blk := d.Blocks[i]
		ins := blk.Inlines
		if i == b.Block {
			ins, _ = split(ins, b.Rune)
		}
		if i == a.Block {
			_, ins = split(ins, a.Rune)
		}
		blk.Inlines = normalize(ins)
		out.Blocks = append(out.Blocks, blk)
	}
	return out
}

// eachRun calls f with every run from a to b, after splitting the runs at
// a and b, and returns d with the runs f changed.
func eachRun(d Doc, a, b Pos, f func(*Inline)) Doc {
	if less(b, a) {
		a, b = b, a
	}
	blocks := slices.Clone(d.Blocks)
	for i := a.Block; i <= b.Block; i++ {
		from, to := 0, runeLen(blocks[i])
		if i == a.Block {
			from = a.Rune
		}
		if i == b.Block {
			to = b.Rune
		}
		head, rest := split(blocks[i].Inlines, from)
		mid, tail := split(rest, to-from)
		for k := range mid {
			f(&mid[k])
		}
		blocks[i].Inlines = cat(head, mid, tail)
	}
	return Doc{Blocks: blocks}
}

// every reports whether every run from a to b has pred.
func every(d Doc, a, b Pos, pred func(Inline) bool) bool {
	ok := true
	eachRun(d, a, b, func(in *Inline) { ok = ok && pred(*in) })
	return ok
}

// styleAt is the style text typed at p takes: the rune's before it, or
// after it at a block's start. Links do not grow.
func styleAt(d Doc, p Pos) Inline {
	ins := d.Blocks[p.Block].Inlines
	left, right := split(ins, p.Rune)
	var st Inline
	if len(left) > 0 {
		st = style(left[len(left)-1])
	} else if len(right) > 0 {
		st = style(right[0])
	}
	st.Link = ""
	return st
}

// setBlocks applies f to the blocks from..to.
func setBlocks(d Doc, from, to int, f func(*Block)) Doc {
	blocks := slices.Clone(d.Blocks)
	for i := from; i <= to; i++ {
		f(&blocks[i])
	}
	return Doc{Blocks: blocks}
}

// wordAt is the word around rune r of s, in runes.
func wordAt(s string, r int) (int, int) {
	rs := []rune(s)
	word := func(i int) bool { return unicode.IsLetter(rs[i]) || unicode.IsDigit(rs[i]) || rs[i] == '_' }
	a, b := min(r, len(rs)), min(r, len(rs))
	for a > 0 && word(a-1) {
		a--
	}
	for b < len(rs) && word(b) {
		b++
	}
	if a == b && b < len(rs) {
		b++ // not in a word: the character itself
	}
	return a, b
}
