// Package guard runs AstrLink Guard (QuantumNous/astrlink-guard,
// Apache-2.0), a token classifier for secrets and personal data in Chinese
// and English text: its int8 ONNX model (37 MB) read by onnx.go and run
// by bert.go on the CPU in pure Go, or on a Mac by CoreML through a Swift
// bridge loaded with purego (coreml.go), its tokenizer by tokenizer.go.
//
// Ported from magpie (github.com/yetone/magpie, MIT, internal/pii/guard),
// its CoreML bridge rewritten from cgo to Swift (ADR 0001).
package guard

import (
	"fmt"
	"math"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
)

// labels are the model's BIO tags, by output index (its labels.json).
var labels = []string{"O",
	"B-email", "I-email", "B-phone", "I-phone", "B-account", "I-account",
	"B-payment_card", "I-payment_card", "B-ip_address", "I-ip_address",
	"B-url", "I-url", "B-common_secret", "I-common_secret",
	"B-private_address", "I-private_address", "B-private_date", "I-private_date",
	"B-private_person", "I-private_person"}

// Files are the model's files Load reads, in its folder.
var Files = []string{"model_int8.onnx", "model_int8.onnx_data", "tokenizer.json"}

// Revision is the model's commit on Hugging Face that Files are of.
const Revision = "7216b6a3b897ef903d4b1ba367ae6fb95b0fa5e4"

// Model is the model, loaded.
type Model struct {
	tok *tokenizer
	net *net

	mu       sync.Mutex
	ml       coreML // nil where CoreML isn't, couldn't be made, or failed
	mlDevice string // CoreML's plan's device, while ml is there
	mlErr    error  // why CoreML isn't used
}

// coreML is the network as CoreML reads it: an *mlModel, or a test's.
type coreML interface {
	logits(nt *net, ids []int) ([]float32, error)
}

// CoreML is the device CoreML reads the model on ("Neural Engine", "GPU",
// "CPU", or why its plan can't say), "" while the CPU reads it in Go;
// then why is why CoreML doesn't: it isn't on this computer, couldn't be
// made, or failed.
func (m *Model) CoreML() (device string, why error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ml == nil {
		return "", m.mlErr
	}
	return m.mlDevice, nil
}

// Device is what reads the model: "CoreML " and its device, or "Go" on
// the CPU.
func (m *Model) Device() string {
	if dev, _ := m.CoreML(); dev != "" {
		return "CoreML " + dev
	}
	return "Go"
}

// Span is what the model found: text[Start:End], a Label of its (email,
// phone, account, payment_card, ip_address, url, common_secret,
// private_address, private_date, private_person), and the least of its
// tokens' probabilities.
type Span struct {
	Start, End int
	Label      string
	Score      float64
}

// MinScore is the publisher's default threshold, under which masking
// drops a span unless the user set another (internal/masking).
const MinScore = 0.8

// Floor is the probability under which Find drops a span: the lowest
// threshold a user can set.
const Floor = 0.5

// window is how many tokens the model reads at once, besides <s> and
// </s>; a longer text is read in windows overlapping by overlap, each
// token labelled by the window it is furthest inside.
const (
	window  = maxPos - 2
	overlap = 128
)

// Load reads the model in dir.
func Load(dir string) (*Model, error) {
	tok, err := loadTokenizer(filepath.Join(dir, "tokenizer.json"))
	if err != nil {
		return nil, fmt.Errorf("tokenizer: %w", err)
	}
	nt, err := loadNet(filepath.Join(dir, "model_int8.onnx"))
	if err != nil {
		return nil, fmt.Errorf("model: %w", err)
	}
	if nt.labels != len(labels) {
		return nil, fmt.Errorf("model: %d labels, not %d", nt.labels, len(labels))
	}
	m := &Model{tok: tok, net: nt}
	if ml, err := openCoreML(dir, nt); err != nil {
		m.mlErr = err
	} else {
		m.ml, m.mlDevice = ml, ml.Device
	}
	return m, nil
}

// logits are the network's scores for ids, from CoreML when it is there
// and the window is long enough to gain by it, else over procs cores.
func (m *Model) logits(ids []int, procs int) []float32 {
	if lg, ok := m.coreML(ids); ok {
		return lg
	}
	return m.net.logits(ids, procs)
}

// coreML is CoreML's logits for ids, false where it doesn't read them. A
// read that fails leaves CoreML for good, saying why (CoreML): it fails
// again as a rule, as on a revision the program wasn't made for.
func (m *Model) coreML(ids []int) ([]float32, bool) {
	m.mu.Lock()
	ml := m.ml
	m.mu.Unlock()
	if ml == nil || len(ids) < coremlMin {
		return nil, false
	}
	lg, err := ml.logits(m.net, ids)
	if err == nil {
		return lg, true
	}
	m.mu.Lock()
	if m.ml != nil {
		m.ml, m.mlErr = nil, fmt.Errorf("CoreML failed: %w", err)
	}
	m.mu.Unlock()
	return nil, false
}

// Cores is how many cores the model reads on at most: half of them, and
// never more than 4. It reads beside the agents and everything else on
// the computer, and on all of them a long history unread kept every core
// busy for as long as requests came (xinvxueyuan and shensmart on Discord,
// 10-28: a 32-core Linux box spent 45 CPU seconds on each request).
func Cores() int { return max(1, min(4, runtime.GOMAXPROCS(0)/2)) }

// cores are the Cores() the model reads on, shared by every Find at once:
// a window read on the CPU holds one or more, so requests read side by
// side never take more than Cores() between them.
var cores = make(chan struct{}, Cores())

// take holds a core of cores, waiting for one, and up to want-1 more of
// those free now: how many it holds, to give back.
func take(want int) int {
	cores <- struct{}{}
	n := 1
	for n < want {
		select {
		case cores <- struct{}{}:
			n++
		default:
			return n
		}
	}
	return n
}

func give(n int) {
	for range n {
		<-cores
	}
}

// Find is what the model finds in text, at Floor, each span with its score.
// A text longer than a window is read a window at a time, overlapping.
// On the CPU those windows are read at once, one a core (of Cores()): a
// window's own matmuls split over the cores leave most of each idle in
// the steps between them (a 4-core server read a long prompt for minutes).
// Finds called at once share those cores (cores): a window takes one,
// waiting while others have them all, and splits its matmuls over as many
// more as are free, up to its share of them, so a lone short text is read
// on every core and several texts at once a core each.
func (m *Model) Find(text string) []Span { return m.find(text, m.read) }

// find is Find with the windows' logits from read, given each window's ids.
func (m *Model) find(text string, read func(ids [][]int) [][]float32) []Span {
	toks := m.tok.encode(text)
	n := len(toks)
	if n == 0 {
		return nil
	}
	var starts []int
	for start := 0; ; start = start + window - overlap {
		starts = append(starts, start)
		if start+window >= n {
			break
		}
	}
	ids := make([][]int, len(starts))
	for w, start := range starts {
		end := min(start+window, n)
		ids[w] = make([]int, 0, end-start+2)
		ids[w] = append(ids[w], 0)
		for _, t := range toks[start:end] {
			ids[w] = append(ids[w], t.id)
		}
		ids[w] = append(ids[w], 2)
	}
	lgs := read(ids)
	lab := make([]int, n)
	prob := make([]float64, n)
	margin := make([]int, n)
	for i := range margin {
		margin[i] = -1
	}
	k := len(labels)
	for w, start := range starts {
		end := min(start+window, n)
		lg := lgs[w]
		for i := 0; i < end-start; i++ {
			j := start + i
			mg := min(i, end-start-1-i)
			if mg <= margin[j] {
				continue
			}
			row := lg[(i+1)*k : (i+2)*k]
			best, mx := 0, row[0]
			for c, v := range row {
				if v > mx {
					best, mx = c, v
				}
			}
			var sum float64
			for _, v := range row {
				sum += math.Exp(float64(v - mx))
			}
			margin[j], lab[j], prob[j] = mg, best, 1/sum
		}
	}
	return decode(toks, lab, prob)
}

// read is the logits of each window of ids: on CoreML one at a time, else
// on the CPU at once, as Find says.
func (m *Model) read(ids [][]int) [][]float32 {
	lgs := make([][]float32, len(ids))
	m.mu.Lock()
	onML := m.ml != nil
	m.mu.Unlock()
	if onML {
		for w := range ids {
			procs := take(Cores())
			lgs[w] = m.logits(ids[w], procs)
			give(procs)
		}
		return lgs
	}
	at := min(Cores(), len(ids))
	var next atomic.Int32
	var wg sync.WaitGroup
	for range at {
		wg.Go(func() {
			for w := int(next.Add(1)) - 1; w < len(ids); w = int(next.Add(1)) - 1 {
				procs := take(max(1, Cores()/at))
				lgs[w] = m.net.logits(ids[w], procs)
				give(procs)
			}
		})
	}
	wg.Wait()
	return lgs
}

// decode reads the tokens' BIO tags (lab, by index into labels) into
// spans: a B tag, or an I tag of another kind than the span before it,
// starts one; an I tag of the same kind extends it; O ends it. A span's
// score is its least token's, and those under Floor are dropped.
// Tokens of no text (a lone "▁") neither start nor end a span.
func decode(toks []token, lab []int, prob []float64) []Span {
	var spans []Span
	cur := -1
	for j, t := range toks {
		if t.start == t.end {
			continue
		}
		tag := labels[lab[j]]
		if tag == "O" {
			cur = -1
			continue
		}
		kind := tag[2:]
		if tag[0] == 'B' || cur < 0 || spans[cur].Label != kind {
			spans = append(spans, Span{t.start, t.end, kind, prob[j]})
			cur = len(spans) - 1
			continue
		}
		spans[cur].End = t.end
		spans[cur].Score = min(spans[cur].Score, prob[j])
	}
	out := spans[:0]
	for _, s := range spans {
		if s.Score >= Floor {
			out = append(out, s)
		}
	}
	return out
}
