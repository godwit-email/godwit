// Package worddiff compares compose documents word by word, formatting
// included: the unit is a word (a CJK character, a run of whitespace, a
// punctuation mark) with its style and link.
package worddiff

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/godwit-email/godwit/internal/richtext"
)

// DefaultBudget is the comparisons a Diff may make before it gives up.
const DefaultBudget = 2_000_000

// Token is a word in its style. Style's Text is empty: it holds the style
// and the link only.
type Token struct {
	Text  string
	Style richtext.Inline
}

// OpKind is what an Op does.
type OpKind int

const (
	Same OpKind = iota
	Del
	Ins
)

// Op is a run of tokens kept, deleted or inserted.
type Op struct {
	Kind   OpKind
	Tokens []Token
}

// BlockDiff is a block's changes: the old block Old and the new block New
// (indexes into Diff's arguments), -1 for a block added or removed.
type BlockDiff struct {
	Old, New int
	Ops      []Op
}

// Tokenize is d's words, a slice per block: Latin text splits at whitespace
// and punctuation (whitespace runs are tokens too), CJK text at each character.
func Tokenize(d richtext.Doc) [][]Token {
	out := make([][]Token, len(d.Blocks))
	for i, b := range d.Blocks {
		var ts []Token
		for _, in := range b.Inlines {
			st := in
			st.Text = ""
			for _, w := range words(in.Text) {
				ts = append(ts, Token{w, st})
			}
		}
		out[i] = ts
	}
	return out
}

func cjk(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}

// class is what a rune joins: 0 letters and digits, 1 whitespace, 2 alone.
func class(r rune) int {
	switch {
	case unicode.IsSpace(r):
		return 1
	case cjk(r) || !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsMark(r):
		return 2
	}
	return 0
}

func words(s string) []string {
	var out []string
	for s != "" {
		r, n := utf8.DecodeRuneInString(s)
		c := class(r)
		for c != 2 && n < len(s) {
			r2, m := utf8.DecodeRuneInString(s[n:])
			if class(r2) != c {
				break
			}
			n += m
		}
		out = append(out, s[:n])
		s = s[n:]
	}
	return out
}

// Diff aligns the blocks of old and new, then compares the words of each
// pair; blocks with no partner are added or removed whole. Tokens are equal
// when their text and their style, link included, are. ok is false once it
// would make more than budget comparisons.
func Diff(old, new [][]Token, budget int) (blocks []BlockDiff, ok bool) {
	edits, ok := myers(old, new, slices.Equal[[]Token], &budget)
	if !ok {
		return nil, false
	}
	var dels, ins []int // the unmatched blocks since the last kept one
	flush := func() bool {
		n := min(len(dels), len(ins))
		for k := range n {
			ops, ok := diffTokens(old[dels[k]], new[ins[k]], &budget)
			if !ok {
				return false
			}
			blocks = append(blocks, BlockDiff{dels[k], ins[k], ops})
		}
		for _, i := range dels[n:] {
			blocks = append(blocks, BlockDiff{i, -1, whole(Del, old[i])})
		}
		for _, j := range ins[n:] {
			blocks = append(blocks, BlockDiff{-1, j, whole(Ins, new[j])})
		}
		dels, ins = dels[:0], ins[:0]
		return true
	}
	for _, e := range edits {
		switch e.kind {
		case Del:
			dels = append(dels, e.a)
		case Ins:
			ins = append(ins, e.b)
		default:
			if !flush() {
				return nil, false
			}
			blocks = append(blocks, BlockDiff{e.a, e.b, whole(Same, old[e.a])})
		}
	}
	if !flush() {
		return nil, false
	}
	return blocks, true
}

func whole(k OpKind, ts []Token) []Op {
	if len(ts) == 0 {
		return nil
	}
	return []Op{{k, ts}}
}

// diffTokens is a block's ops: within a run of changes, deletions first.
func diffTokens(a, b []Token, budget *int) ([]Op, bool) {
	edits, ok := myers(a, b, func(x, y Token) bool { return x == y }, budget)
	if !ok {
		return nil, false
	}
	var ops []Op
	add := func(k OpKind, t Token) {
		if n := len(ops); n > 0 && ops[n-1].Kind == k {
			ops[n-1].Tokens = append(ops[n-1].Tokens, t)
			return
		}
		ops = append(ops, Op{k, []Token{t}})
	}
	var del, ins []Token
	for _, e := range edits {
		switch e.kind {
		case Del:
			del = append(del, a[e.a])
		case Ins:
			ins = append(ins, b[e.b])
		default:
			for _, t := range del {
				add(Del, t)
			}
			for _, t := range ins {
				add(Ins, t)
			}
			del, ins = del[:0], ins[:0]
			add(Same, a[e.a])
		}
	}
	for _, t := range del {
		add(Del, t)
	}
	for _, t := range ins {
		add(Ins, t)
	}
	return ops, true
}

type edit struct {
	kind OpKind
	a, b int // the index in a (Same, Del) and in b (Same, Ins)
}

// myers is the shortest edit script from a to b (Myers' O(ND) algorithm).
// Each diagonal tried and each element compared costs one of budget.
func myers[T any](a, b []T, eq func(x, y T) bool, budget *int) ([]edit, bool) {
	n, m := len(a), len(b)
	// trace[d] holds, for diagonals k = -d, -d+2 … d, the furthest x reached.
	var trace [][]int32
	at := func(d, k int) int {
		if d < 0 {
			return 0
		}
		return int(trace[d][(k+d)/2])
	}
	end := -1
	for d := 0; d <= n+m && end < 0; d++ {
		v := make([]int32, d+1)
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || k != d && at(d-1, k-1) < at(d-1, k+1) {
				x = at(d-1, k+1)
			} else {
				x = at(d-1, k-1) + 1
			}
			y := x - k
			for x < n && y < m {
				*budget--
				if !eq(a[x], b[y]) {
					break
				}
				x, y = x+1, y+1
			}
			if *budget--; *budget < 0 {
				return nil, false
			}
			v[(k+d)/2] = int32(x)
			if x >= n && y >= m {
				end = d
			}
		}
		trace = append(trace, v)
	}
	var out []edit
	x, y := n, m
	for d := end; d > 0; d-- {
		k := x - y
		pk := k - 1
		if k == -d || k != d && at(d-1, k-1) < at(d-1, k+1) {
			pk = k + 1
		}
		px := at(d-1, pk)
		py := px - pk
		for x > px && y > py {
			x, y = x-1, y-1
			out = append(out, edit{Same, x, y})
		}
		if x == px {
			y--
			out = append(out, edit{Ins, x, y})
		} else {
			x--
			out = append(out, edit{Del, x, y})
		}
	}
	for x > 0 && y > 0 {
		x, y = x-1, y-1
		out = append(out, edit{Same, x, y})
	}
	slices.Reverse(out)
	return out, true
}

// Ratio is how much changed: (words deleted + words inserted) / (old words
// + new words), from 0 to 1; whitespace is no word. 0 when both are empty.
func Ratio(blocks []BlockDiff) float64 {
	var changed, total int
	for _, b := range blocks {
		for _, op := range b.Ops {
			n := Words(op.Tokens)
			switch op.Kind {
			case Same:
				total += 2 * n
			default:
				total += n
				changed += n
			}
		}
	}
	if total == 0 {
		return 0
	}
	return float64(changed) / float64(total)
}

// Words counts the tokens that are not whitespace.
func Words(ts []Token) int {
	n := 0
	for _, t := range ts {
		if strings.TrimSpace(t.Text) != "" {
			n++
		}
	}
	return n
}
