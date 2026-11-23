package mask

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"
)

// What is done with a category's values (Config.Actions): 遮住 sends
// placeholders; 仅警告 sends them as they are, and they are recorded; 拦截
// sends nothing of the request (docs/adr/0025-mask-at-cloud-egress.md).
const (
	Mask  = "mask"
	Warn  = "warn"
	Block = "block"
)

// action is what is done with a value of kind: the user's own kinds are
// always masked.
func (c *compiled) action(kind string) string {
	if a := c.actions[categories[kind]]; categories[kind] != "" && (a == Warn || a == Block) {
		return a
	}
	return Mask
}

// BlockedError refuses a request that holds values of Categories, each set
// to Block: nothing of it is sent.
type BlockedError struct{ Categories []string }

func (e *BlockedError) Error() string {
	return "masking blocked the request: it holds " + strings.Join(e.Categories, ", ")
}

// with is e with cats too, each once, sorted; e may be nil.
func (e *BlockedError) with(cats ...string) *BlockedError {
	out := &BlockedError{}
	if e != nil {
		out.Categories = slices.Clone(e.Categories)
	}
	for _, c := range cats {
		if !slices.Contains(out.Categories, c) {
			out.Categories = append(out.Categories, c)
		}
	}
	slices.Sort(out.Categories)
	return out
}

// What found a value (Hit.Source): a rule, the user's or a built-in one, or
// a value known before; or 识别模型 (Config.Recognizer).
const (
	SourceRule       = "rule"
	SourceRecognizer = "recognizer"
)

// Hit is a value masking found in a request, as it is recorded (脱敏记录):
// its kind and category, its first character and its length in characters,
// never the value itself; Action, what was done with it; and Source, what
// found it.
type Hit struct {
	Kind, Category, First string
	Len                   int
	Action, Source        string
	hash                  string // which value: Found counts each once
}

func (m *Masker) hit(sp span, v, action string) Hit {
	r, _ := utf8.DecodeRuneInString(v)
	source := SourceRule
	if sp.recognized {
		source = SourceRecognizer
	}
	return Hit{Kind: sp.kind, Category: categories[sp.kind], First: string(r), Len: utf8.RuneCountInString(v), Action: action, Source: source, hash: m.hash(sp.kind, v)}
}

// Found gathers hits, each value once for each action, in the order they
// were found. Safe for concurrent use; a nil Found gathers nothing.
type Found struct {
	mu   sync.Mutex
	hits []Hit
	// learn masks every value whatever its action, for its placeholder: Learn
	learn bool
	// premasked: Godwit masked the text before (Premasked), so its placeholders
	// are the masker's own, not planted: they go as they are
	premasked bool
}

func (f *Found) learning() bool { return f != nil && f.learn }

func (f *Found) trusts() bool { return f != nil && f.premasked }

type premaskedKey struct{}

// Premasked says ctx's requests carry text Godwit masked itself (RequestTexts),
// cut into pieces after: its placeholders are not defused at the egress.
func Premasked(ctx context.Context) context.Context {
	return context.WithValue(ctx, premaskedKey{}, true)
}

func (f *Found) add(hs ...Hit) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, h := range hs {
		if !slices.Contains(f.hits, h) {
			f.hits = append(f.hits, h)
		}
	}
}

// Hits is what f gathered.
func (f *Found) Hits() []Hit {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.hits)
}

// Count is how many of f's values had action.
func (f *Found) Count(action string) int {
	n := 0
	for _, h := range f.Hits() {
		if h.Action == action {
			n++
		}
	}
	return n
}

type tagKey struct{}

type tag struct {
	feature string
	found   *Found
}

// Tag says ctx's requests are for feature (总结, Wit …), as Record is told,
// and gathers what they find in found too, if set.
func Tag(ctx context.Context, feature string, found *Found) context.Context {
	return context.WithValue(ctx, tagKey{}, tag{feature, found})
}

// Request masks body, one request to a service off this device, as Body
// does, and records what it found (Record, and ctx's Found): a request
// that is blocked too.
func (m *Masker) Request(ctx context.Context, body string) (string, error) {
	f := &Found{premasked: ctx.Value(premaskedKey{}) != nil}
	out, err := m.bodyTo(body, f)
	m.sent(ctx, f)
	return out, err
}

// RequestTexts masks texts, one request's, as Text does each, and records
// what they held as Request does.
func (m *Masker) RequestTexts(ctx context.Context, texts []string) ([]string, error) {
	f := new(Found)
	defer m.sent(ctx, f)
	out := make([]string, len(texts))
	var block *BlockedError
	for i, t := range texts {
		var err error
		if out[i], err = m.textTo(t, f); err != nil {
			b, ok := err.(*BlockedError)
			if !ok {
				return nil, err
			}
			block = block.with(b.Categories...)
		}
	}
	if block != nil {
		return nil, block
	}
	return out, nil
}

// RequestWhole masks texts, the distinct texts of nodes that one request
// sends, as RequestTexts does and by what Text finds in whole: a value
// whose label is in another node is found. nodes are where in whole the texts stand (the rest, a
// subject, is context only). A value is cut at their edges, and each piece
// of it is masked wherever it stands in texts, at word edges, with a
// placeholder of its own that is not learned: the value whole is. Records
// what whole held as Request does.
func (m *Masker) RequestWhole(ctx context.Context, whole string, nodes [][2]int, texts []string) (out []string, err error) {
	f := new(Found)
	defer m.sent(ctx, f)
	defer func() {
		if r := recover(); r != nil {
			out, err = nil, fmt.Errorf("mask: %v", r)
		}
	}()
	if err := m.learnNames(whole); err != nil {
		return nil, err
	}
	s, spans, block, err := m.spans(whole, true, true, f)
	if err != nil {
		return nil, err
	}
	if block != nil {
		return nil, block
	}
	var cuts []cut
	for _, sp := range spans {
		p, err := m.placeholder(sp.kind, sp.value, sp.recognized, sp.own)
		if err != nil {
			return nil, err
		}
		for _, n := range nodes {
			lo, hi := max(sp.start, n[0]), min(sp.end, n[1])
			raw := strings.TrimSpace(s[lo:max(lo, hi)])
			if raw == "" {
				continue
			}
			c := cut{raw, p}
			if lo != sp.start || hi != sp.end {
				if c.placeholder, err = m.fragment(sp.kind, raw); err != nil {
					return nil, err
				}
			}
			cuts = append(cuts, c)
		}
	}
	slices.SortStableFunc(cuts, func(a, b cut) int { return len(b.raw) - len(a.raw) })
	out = make([]string, len(texts))
	for i, t := range texts {
		// each text masked on its own too: read whole, two codes are no one code (mail.SignInCode)
		o, err := m.text(t, true, true, f)
		if err != nil {
			b, ok := err.(*BlockedError)
			if !ok {
				return nil, err
			}
			block = block.with(b.Categories...)
		}
		out[i] = replaceCuts(o, cuts)
	}
	if block != nil {
		return nil, block
	}
	return out, nil
}

// cut is a piece of a value, raw, and its placeholder.
type cut struct{ raw, placeholder string }

// replaceCuts is s with each cut in it, the longest first, replaced by its
// placeholder, where it does not run on into a word.
func replaceCuts(s string, cuts []cut) string {
	if len(cuts) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for i := 0; i < len(s); i++ {
		for _, c := range cuts {
			j := i + len(c.raw)
			if !strings.HasPrefix(s[i:], c.raw) ||
				i > 0 && alnumRune(lastRune(s[:i])) && alnumRune(firstRune(c.raw)) ||
				j < len(s) && alnumRune(firstRune(s[j:])) && alnumRune(lastRune(c.raw)) {
				continue
			}
			b.WriteString(s[last:i])
			b.WriteString(c.placeholder)
			last, i = j, j-1
			break
		}
	}
	b.WriteString(s[last:])
	return b.String()
}

// sent records what f found in a request of ctx.
func (m *Masker) sent(ctx context.Context, f *Found) {
	hits := f.Hits()
	if len(hits) == 0 {
		return
	}
	t, _ := ctx.Value(tagKey{}).(tag)
	t.found.add(hits...)
	if m.Record != nil {
		m.Record(t.feature, hits)
	}
}
