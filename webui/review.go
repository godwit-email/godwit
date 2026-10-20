package webui

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/richtext"
	"github.com/godwit-email/godwit/internal/worddiff"
)

// rewriteRatio is the share of changed words past which a proposal is shown
// as a rewrite, side by side, not word by word.
const rewriteRatio = 0.5

// Review is a pending draft edit as the compose page reviews it.
type Review struct {
	Approval int64  `json:"approval"`
	Tool     string `json:"tool"`
	Kind     string `json:"kind"`
	// Inline means the changes are shown and decided word by word (Finish);
	// else it is a rewrite, decided as a whole (Decide).
	Inline bool `json:"inline"`
	// Ratio is the share of words changed; Exact means the diff completed
	// within its budget, so Ratio is known (else wit.compose.rewrite_plain).
	Ratio float64 `json:"ratio"`
	Exact bool    `json:"exact"`
	// Blocks are the new text with the changes marked, when the block shapes are kept (Inline); Changes
	// is how many changes they hold, the length Finish wants.
	Blocks  []ReviewBlock `json:"blocks"`
	Changes int           `json:"changes"`
	// Old and New are the editable HTML before and after, for the compare.
	Old        string     `json:"old"`
	New        string     `json:"new"`
	OldSubject string     `json:"old_subject,omitempty"`
	Subject    string     `json:"subject,omitempty"`
	SubjectOps []ReviewOp `json:"subject_ops,omitempty"`
}

// ReviewBlock is a block of the review.
type ReviewBlock struct {
	// Kind is paragraph, heading, quote, listItem or pre; Level and Ordered
	// as in the editor (heading level, quote or list depth; an <ol> item).
	Kind    string `json:"kind"`
	Level   int    `json:"level"`
	Ordered bool   `json:"ordered"`
	// Added and Removed mark a whole block added or removed: change Whole
	// (else -1), even when it is empty.
	Added   bool       `json:"added"`
	Removed bool       `json:"removed"`
	Whole   int        `json:"whole"`
	Ops     []ReviewOp `json:"ops"`
}

// ReviewOp is a run of words kept, deleted or inserted.
type ReviewOp struct {
	// Kind is same, del or ins; Change the change it belongs to, -1 for same.
	Kind   string        `json:"kind"`
	Change int           `json:"change"`
	Tokens []ReviewToken `json:"tokens"`
	// Link, on the last op of a change of only a link's address, is the new
	// address (wit.compose.link).
	Link string `json:"link"`
}

// ReviewToken is text in one style.
type ReviewToken struct {
	Text   string `json:"text"`
	Bold   bool   `json:"bold"`
	Italic bool   `json:"italic"`
	Href   string `json:"href"`
}

// proposal is a draft edit's old and new editable part, their word diff and
// the changes in it.
type proposal struct {
	ap         core.Approval
	oldHTML    string
	newHTML    string
	oldSubject string
	subject    string
	old, new   richtext.Doc
	blocks     []worddiff.BlockDiff
	ok         bool // diffed, block shapes kept: changes can be decided one by one
	exact      bool // diffed: ratio is known
	ratio      float64
	changes    []change
	owner      [][]int // per block, per op: its change, -1 for none
	whole      []int   // per block: the change adding or removing it, -1 for none
}

// change is ops [from, to) of blocks[block]; a block added or removed is
// one change of all its ops.
type change struct{ block, from, to int }

func newProposal(ap core.Approval) *proposal {
	var args struct {
		Old, HTML  string
		OldSubject string  `json:"old_subject"`
		Subject    *string `json:"subject"`
	}
	_ = json.Unmarshal(ap.Args, &args)
	r := &proposal{ap: ap, oldHTML: args.Old, newHTML: args.HTML,
		oldSubject: args.OldSubject,
		old:        richtext.FromComposeHTML(args.Old), new: richtext.FromComposeHTML(args.HTML)}
	if args.Subject != nil {
		r.subject = *args.Subject
	}
	r.blocks, r.exact = worddiff.Diff(worddiff.Tokenize(r.old), worddiff.Tokenize(r.new), worddiff.DefaultBudget)
	r.ok = r.exact && r.shapesKept()
	if r.exact {
		r.ratio = worddiff.Ratio(r.blocks)
	}
	if r.ok {
		r.split()
	}
	return r
}

func (r *proposal) inline() bool { return r.ok && r.ratio <= rewriteRatio }

func sameShape(a, b richtext.Block) bool {
	return a.Kind == b.Kind && a.Level == b.Level && a.Ordered == b.Ordered
}

func shape(b richtext.Block) richtext.Block {
	return richtext.Block{Kind: b.Kind, Level: b.Level, Ordered: b.Ordered}
}

// shapesKept reports whether every block is text and each pair aligned has
// one shape: else the change is a rewrite.
func (r *proposal) shapesKept() bool {
	text := func(b richtext.Block) bool { return b.Kind <= richtext.Pre }
	if !all(r.old.Blocks, text) || !all(r.new.Blocks, text) {
		return false
	}
	for _, b := range r.blocks {
		if b.Old >= 0 && b.New >= 0 && !sameShape(r.old.Blocks[b.Old], r.new.Blocks[b.New]) {
			return false
		}
	}
	return true
}

func all[T any](s []T, f func(T) bool) bool {
	return !slices.ContainsFunc(s, func(x T) bool { return !f(x) })
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

// result is the editable part with each change decided as decided (1
// accepted, -1 rejected), each token in its own style.
func (r *proposal) result(decided []int) richtext.Doc {
	var d richtext.Doc
	add := func(text string, st richtext.Inline) {
		blk := &d.Blocks[len(d.Blocks)-1]
		st.Text = ""
		if n := len(blk.Inlines); n > 0 {
			last := blk.Inlines[n-1]
			last.Text = ""
			if last == st {
				blk.Inlines[n-1].Text += text
				return
			}
		}
		st.Text = text
		blk.Inlines = append(blk.Inlines, st)
	}
	for i, bd := range r.blocks {
		src, at := r.new, bd.New
		if at < 0 {
			src, at = r.old, bd.Old
		}
		if k := r.whole[i]; k >= 0 && (bd.Old < 0) == (decided[k] != 1) {
			continue // added and rejected, or removed and accepted
		}
		d.Blocks = append(d.Blocks, shape(src.Blocks[at]))
		for j, op := range bd.Ops {
			if k := r.owner[i][j]; op.Kind != worddiff.Same && k >= 0 && (decided[k] == 1) != (op.Kind == worddiff.Ins) {
				continue // a deletion accepted or an insertion rejected
			}
			for _, t := range op.Tokens {
				add(t.Text, t.Style)
			}
		}
	}
	return d
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

var blockKinds = map[richtext.BlockKind]string{richtext.Paragraph: "paragraph", richtext.Heading: "heading",
	richtext.Quote: "quote", richtext.ListItem: "listItem", richtext.Pre: "pre"}

var opKinds = map[worddiff.OpKind]string{worddiff.Same: "same", worddiff.Del: "del", worddiff.Ins: "ins"}

// review is r for the page.
func (r *proposal) review() Review {
	out := Review{Approval: r.ap.ID, Tool: r.ap.Tool, Kind: r.ap.Summary, Inline: r.inline(), Ratio: r.ratio, Exact: r.exact,
		Blocks: []ReviewBlock{}, Changes: len(r.changes), Old: r.oldHTML, New: r.newHTML,
		OldSubject: r.oldSubject, Subject: r.subject}
	if r.subject != "" && r.subject != r.oldSubject {
		oldDoc := richtext.Doc{Blocks: []richtext.Block{{Kind: richtext.Paragraph, Inlines: []richtext.Inline{{Text: r.oldSubject}}}}}
		newDoc := richtext.Doc{Blocks: []richtext.Block{{Kind: richtext.Paragraph, Inlines: []richtext.Inline{{Text: r.subject}}}}}
		bDiffs, _ := worddiff.Diff(worddiff.Tokenize(oldDoc), worddiff.Tokenize(newDoc), worddiff.DefaultBudget)
		if len(bDiffs) > 0 {
			for _, op := range bDiffs[0].Ops {
				o := ReviewOp{Kind: opKinds[op.Kind], Change: -1, Tokens: []ReviewToken{}}
				for _, t := range op.Tokens {
					tk := ReviewToken{Text: t.Text}
					if n := len(o.Tokens); n > 0 && o.Tokens[n-1].Bold == tk.Bold && o.Tokens[n-1].Italic == tk.Italic && o.Tokens[n-1].Href == tk.Href {
						o.Tokens[n-1].Text += tk.Text
						continue
					}
					o.Tokens = append(o.Tokens, tk)
				}
				out.SubjectOps = append(out.SubjectOps, o)
			}
		}
	}
	if !r.ok {
		return out
	}
	for i, bd := range r.blocks {
		src, at := r.new, bd.New
		if at < 0 {
			src, at = r.old, bd.Old
		}
		sb := src.Blocks[at]
		b := ReviewBlock{Kind: blockKinds[sb.Kind], Level: sb.Level, Ordered: sb.Ordered,
			Added: bd.Old < 0, Removed: bd.New < 0, Whole: r.whole[i], Ops: []ReviewOp{}}
		for j, op := range bd.Ops {
			o := ReviewOp{Kind: opKinds[op.Kind], Change: r.owner[i][j], Tokens: []ReviewToken{}}
			if op.Kind == worddiff.Same {
				o.Change = -1
			}
			if k := r.owner[i][j]; k >= 0 && j == r.changes[k].to-1 {
				o.Link = r.linkNote(k)
			}
			for _, t := range op.Tokens {
				tk := ReviewToken{Text: t.Text, Bold: t.Style.Bold, Italic: t.Style.Italic, Href: t.Style.Link}
				if n := len(o.Tokens); n > 0 {
					if last := o.Tokens[n-1]; last.Bold == tk.Bold && last.Italic == tk.Italic && last.Href == tk.Href {
						o.Tokens[n-1].Text += tk.Text
						continue
					}
				}
				o.Tokens = append(o.Tokens, tk)
			}
			b.Ops = append(b.Ops, o)
		}
		out.Blocks = append(out.Blocks, b)
	}
	return out
}
