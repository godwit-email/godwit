package mask

import (
	"context"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"
)

// What is done with a category's values (Config.Actions): 遮住 sends
// placeholders; 仅警告 sends them as they are, and they are recorded; 拦截
// sends nothing of the request (docs/adr/0025-mask-at-cloud-egress.md).
const (
	Hide  = "mask"
	Warn  = "warn"
	Block = "block"
)

// action is what is done with a value of kind: the user's own kinds are
// always masked.
func (c *compiled) action(kind string) string {
	if a := c.actions[categories[kind]]; categories[kind] != "" && (a == Warn || a == Block) {
		return a
	}
	return Hide
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

// Hit is a value masking found in a request, as it is recorded (脱敏记录):
// its kind and category, its first character and its length in characters,
// never the value itself; and Action, what was done with it.
type Hit struct {
	Kind, Category, First string
	Len                   int
	Action                string
	hash                  string // which value: Found counts each once
}

func (m *Masker) hit(kind, v, action string) Hit {
	r, _ := utf8.DecodeRuneInString(v)
	return Hit{Kind: kind, Category: categories[kind], First: string(r), Len: utf8.RuneCountInString(v), Action: action, hash: m.hash(kind, v)}
}

// Found gathers hits, each value once for each action, in the order they
// were found. Safe for concurrent use; a nil Found gathers nothing.
type Found struct {
	mu   sync.Mutex
	hits []Hit
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
	f := new(Found)
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
