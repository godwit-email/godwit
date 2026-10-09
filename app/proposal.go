package app

import (
	"encoding/json"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/richtext"
	"github.com/godwit-email/godwit/internal/worddiff"
)

// rewriteRatio is the share of changed words past which a proposal is shown
// as a rewrite, side by side, not word by word.
const rewriteRatio = 0.5

func isDraftEdit(tool string) bool { return tool == "update_draft" || tool == "update_selection" }

// proposal is a pending draft edit (update_draft, update_selection) as the
// compose page reviews it: the old and the new editable part, their word
// diff, the changes in it and the user's decision on each. view is what
// the review shows: open changes as deletions and insertions (marks),
// decided ones as decided.
type proposal struct {
	ap       core.Approval
	old, new richtext.Doc
	blocks   []worddiff.BlockDiff
	ok       bool // diffed, block shapes kept: changes can be decided one by one
	ratio    float64
	changes  []change
	owner    [][]int // per block, per op: its change, -1 for none
	whole    []int   // per block: the change adding or removing it, -1 for none
	decided  []int   // per change: 0 open, 1 accepted, -1 rejected
	view     richtext.Doc
	marks    []revMark
}

// change is ops [from, to) of blocks[block]; a block added or removed is
// one change of all its ops.
type change struct{ block, from, to int }

// revMark is an open change's deletion or insertion in the view.
type revMark struct {
	from, to richtext.Pos
	kind     worddiff.OpKind
	change   int
}

// newProposal diffs the approval's old and new editable parts (off the UI thread).
func newProposal(ap core.Approval) *proposal {
	var args struct{ Old, HTML string }
	_ = json.Unmarshal(ap.Args, &args)
	r := &proposal{ap: ap, old: richtext.FromComposeHTML(args.Old), new: richtext.FromComposeHTML(args.HTML)}
	r.blocks, r.ok = worddiff.Diff(worddiff.Tokenize(r.old), worddiff.Tokenize(r.new), worddiff.DefaultBudget)
	r.ok = r.ok && r.shapesKept()
	if r.ok {
		r.ratio = worddiff.Ratio(r.blocks)
		r.split()
	}
	r.decided = make([]int, len(r.changes))
	r.render()
	return r
}

// inline reports whether the review shows the changes word by word.
func (r *proposal) inline() bool { return r.ok && r.ratio <= rewriteRatio }

func sameShape(a, b richtext.Block) bool {
	return a.Kind == b.Kind && a.Level == b.Level && a.Ordered == b.Ordered
}

func shape(b richtext.Block) richtext.Block {
	return richtext.Block{Kind: b.Kind, Level: b.Level, Ordered: b.Ordered}
}

// shapesKept reports whether every block is text and each pair aligned has
// one kind: else the change is a rewrite.
func (r *proposal) shapesKept() bool {
	text := func(b richtext.Block) bool { return b.Kind <= richtext.Pre }
	if !slices.ContainsFunc(r.old.Blocks, func(b richtext.Block) bool { return !text(b) }) &&
		!slices.ContainsFunc(r.new.Blocks, func(b richtext.Block) bool { return !text(b) }) {
		for _, b := range r.blocks {
			if b.Old >= 0 && b.New >= 0 && !sameShape(r.old.Blocks[b.Old], r.new.Blocks[b.New]) {
				return false
			}
		}
		return true
	}
	return false
}

func blank(op worddiff.Op) bool { return op.Kind == worddiff.Same && worddiff.Words(op.Tokens) == 0 }

// split finds the changes: in a block, a run of deletions and insertions,
// joined across whitespace kept between two of them.
func (r *proposal) split() {
	r.owner, r.whole = make([][]int, len(r.blocks)), make([]int, len(r.blocks))
	for i, b := range r.blocks {
		r.whole[i] = -1
		own := make([]int, len(b.Ops))
		for j := range own {
			own[j] = -1
		}
		r.owner[i] = own
		if b.Old < 0 || b.New < 0 {
			for j := range own {
				own[j] = len(r.changes)
			}
			r.whole[i] = len(r.changes)
			r.changes = append(r.changes, change{i, 0, len(b.Ops)})
			continue
		}
		for j := 0; j < len(b.Ops); {
			if b.Ops[j].Kind == worddiff.Same {
				j++
				continue
			}
			from := j
			for j < len(b.Ops) && (b.Ops[j].Kind != worddiff.Same || blank(b.Ops[j]) && j+1 < len(b.Ops) && b.Ops[j+1].Kind != worddiff.Same) {
				own[j] = len(r.changes)
				j++
			}
			r.changes = append(r.changes, change{i, from, j})
		}
	}
}

// open is the number of changes not decided.
func (r *proposal) open() int {
	n := 0
	for _, d := range r.decided {
		if d == 0 {
			n++
		}
	}
	return n
}

// keep reports whether op j of block i is in the text with the decisions:
// kept text always, a deletion unless accepted, an insertion once accepted;
// open ones both, when showing.
func (r *proposal) keep(i, j int, kind worddiff.OpKind, showing bool) bool {
	k := r.owner[i][j]
	if kind == worddiff.Same || k < 0 {
		return true
	}
	d := r.decided[k]
	if d == 0 {
		return showing || kind == worddiff.Del
	}
	return d == 1 == (kind == worddiff.Ins)
}

// builder makes blocks from tokens, each token in its own style.
type builder struct {
	d    richtext.Doc
	rune int // in the block being built
}

func (b *builder) block(s richtext.Block) {
	b.d.Blocks = append(b.d.Blocks, s)
	b.rune = 0
}

func (b *builder) add(text string, st richtext.Inline) {
	blk := &b.d.Blocks[len(b.d.Blocks)-1]
	st.Text = ""
	if n := len(blk.Inlines); n > 0 {
		last := blk.Inlines[n-1]
		last.Text = ""
		if last == st {
			blk.Inlines[n-1].Text += text
			b.rune += utf8.RuneCountInString(text)
			return
		}
	}
	st.Text = text
	blk.Inlines = append(blk.Inlines, st)
	b.rune += utf8.RuneCountInString(text)
}

func (b *builder) pos() richtext.Pos { return richtext.Pos{Block: len(b.d.Blocks) - 1, Rune: b.rune} }

// walk builds the text with the decisions, telling each op added.
func (r *proposal) walk(showing bool, added func(b *builder, i, j int)) richtext.Doc {
	var b builder
	for i, bd := range r.blocks {
		src, at := r.new, bd.New
		if at < 0 {
			src, at = r.old, bd.Old
		}
		if k := r.whole[i]; k >= 0 {
			d := r.decided[k]
			if bd.Old < 0 && !(d == 1 || d == 0 && showing) || bd.New < 0 && d == 1 {
				continue // added and not (yet) accepted, or removed and accepted
			}
		}
		b.block(shape(src.Blocks[at]))
		for j, op := range bd.Ops {
			if !r.keep(i, j, op.Kind, showing) {
				continue
			}
			for _, t := range op.Tokens {
				b.add(t.Text, t.Style)
			}
			if added != nil {
				added(&b, i, j)
			}
		}
	}
	return b.d
}

// result is the editable part with every change decided as decided.
func (r *proposal) result() richtext.Doc { return r.walk(false, nil) }

// render makes the view and its marks: an open change's deletions and
// insertions, and after a change of only a link's address, the new address.
func (r *proposal) render() {
	if !r.ok {
		r.view, r.marks = r.new, nil
		return
	}
	r.marks = nil
	r.view = r.walk(true, func(b *builder, i, j int) {
		k := r.owner[i][j]
		if k < 0 || r.decided[k] != 0 {
			return
		}
		op := r.blocks[i].Ops[j]
		n := 0
		for _, t := range op.Tokens {
			n += utf8.RuneCountInString(t.Text)
		}
		to := b.pos()
		from := richtext.Pos{Block: to.Block, Rune: to.Rune - n}
		if op.Kind != worddiff.Same {
			r.marks = append(r.marks, revMark{from, to, op.Kind, k})
		}
		if ch := r.changes[k]; j == ch.to-1 {
			if u := r.linkNote(k); u != "" {
				b.add(" → "+u, richtext.Inline{})
				r.marks = append(r.marks, revMark{to, b.pos(), worddiff.Ins, k})
			}
		}
	})
}

// linkNote is the new address of a change that changes only a link's
// address, "" for any other change.
func (r *proposal) linkNote(k int) string {
	ch := r.changes[k]
	var del, ins strings.Builder
	link := ""
	for _, op := range r.blocks[ch.block].Ops[ch.from:ch.to] {
		for _, t := range op.Tokens {
			switch op.Kind {
			case worddiff.Del:
				del.WriteString(t.Text)
			case worddiff.Ins:
				ins.WriteString(t.Text)
				if t.Style.Link != "" {
					link = t.Style.Link
				}
			}
		}
	}
	if del.String() != ins.String() {
		return ""
	}
	for _, op := range r.blocks[ch.block].Ops[ch.from:ch.to] {
		if op.Kind == worddiff.Del && slices.ContainsFunc(op.Tokens, func(t worddiff.Token) bool { return t.Style.Link == link }) {
			return "" // the address is the same: the style changed
		}
	}
	return link
}

// span is change k's place in the view, from its first mark to its last.
func (r *proposal) span(k int) (from, to richtext.Pos, ok bool) {
	for _, m := range r.marks {
		if m.change != k {
			continue
		}
		if !ok {
			from, ok = m.from, true
		}
		to = m.to
	}
	return from, to, ok
}

// changeAt is the open change marked at p in the view, -1 for none.
func (r *proposal) changeAt(p richtext.Pos) int {
	before := func(a, b richtext.Pos) bool { return a.Block < b.Block || a.Block == b.Block && a.Rune < b.Rune }
	for _, m := range r.marks {
		if !before(p, m.from) && before(p, m.to) {
			return m.change
		}
	}
	return -1
}

// utf16At is p's offset in the editor's terms: UTF-16 code units of the
// text with a newline between blocks.
func utf16At(d richtext.Doc, p richtext.Pos) int {
	off := 0
	for i, b := range d.Blocks {
		text := []rune(plainBlock(b))
		if i == p.Block {
			return off + ui.UTF16Len(string(text[:min(p.Rune, len(text))]))
		}
		off += ui.UTF16Len(string(text)) + 1
	}
	return off
}

// posAt is the Pos of editor offset off.
func posAt(d richtext.Doc, off int) richtext.Pos {
	for i, b := range d.Blocks {
		text := plainBlock(b)
		n := ui.UTF16Len(text)
		if off <= n || i == len(d.Blocks)-1 {
			r, u := 0, 0
			for _, c := range text {
				if u >= off {
					break
				}
				u += ui.UTF16Len(string(c))
				r++
			}
			return richtext.Pos{Block: i, Rune: r}
		}
		off -= n + 1
	}
	return richtext.Pos{}
}

func plainBlock(b richtext.Block) string {
	var sb strings.Builder
	for _, in := range b.Inlines {
		sb.WriteString(in.Text)
	}
	return sb.String()
}
