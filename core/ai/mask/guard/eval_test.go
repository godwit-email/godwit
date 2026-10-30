package guard

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/ai/mask"
)

// kept are the model's labels masking would take, after magpie's: a date
// is nearly always a mail's own, and a link the rules' (LINK).
var kept = map[string]bool{"private_person": true, "private_address": true, "phone": true, "email": true,
	"account": true, "payment_card": true, "common_secret": true, "ip_address": true}

func modelSpans(m *Model, text string, min float64) []Span {
	var out []Span
	for _, s := range m.Find(text) {
		if kept[s.Label] && s.Score >= min {
			out = append(out, s)
		}
	}
	return out
}

// covered says spans take most of some occurrence of v in text, or, with
// any, a byte of one.
func covered(text, v string, spans []Span, any bool) bool {
	for i := 0; ; {
		j := strings.Index(text[i:], v)
		if j < 0 {
			return false
		}
		a, b := i+j, i+j+len(v)
		n := 0
		for _, s := range spans {
			n += max(0, min(b, s.End)-max(a, s.Start))
		}
		if any && n > 0 || 2*n >= len(v) {
			return true
		}
		i = a + 1
	}
}

// evalCase is a mail of testdata/eval.json: the values in it to Mask, of
// their kinds, and those to Keep as written.
type evalCase struct {
	ID, Text string
	Mask     []struct{ V, Kind string }
	Keep     []string
}

func evalCases(t *testing.T) []evalCase {
	b, err := os.ReadFile("testdata/eval.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []evalCase
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	return cases
}

// TestEval scores the rules, the model and both on testdata/eval.json:
// how many of the values to mask each masks, and how many of those to
// keep each masks wrongly.
func TestEval(t *testing.T) {
	m, err := Load(dir(t))
	if err != nil {
		t.Fatal(err)
	}
	cases := evalCases(t)
	rules := mask.New([]byte("eval"))
	type score struct{ hit, miss, wrong int }
	for _, min := range []float64{Floor, mask.MinScore} {
		var r, md, both score
		byKind := map[string]*[3]int{} // rules, model, both
		var misses, wrongs []string
		for _, c := range cases {
			out, err := rules.Text(c.Text)
			if err != nil {
				t.Fatal(err)
			}
			spans := modelSpans(m, c.Text, min)
			for _, v := range c.Mask {
				if !strings.Contains(c.Text, v.V) {
					t.Fatalf("%s: %q is not in the text", c.ID, v.V)
				}
				byR, byM := !strings.Contains(out, v.V), covered(c.Text, v.V, spans, false)
				k := byKind[v.Kind]
				if k == nil {
					k = &[3]int{}
					byKind[v.Kind] = k
				}
				for i, ok := range []bool{byR, byM, byR || byM} {
					if ok {
						k[i]++
					}
				}
				count(&r.hit, &r.miss, byR)
				count(&md.hit, &md.miss, byM)
				count(&both.hit, &both.miss, byR || byM)
				if !byR && !byM {
					misses = append(misses, fmt.Sprintf("%s %s %q", c.ID, v.Kind, v.V))
				}
			}
			for _, v := range c.Keep {
				byR, byM := !strings.Contains(out, v), covered(c.Text, v, spans, true)
				if byR {
					r.wrong++
				}
				if byM {
					md.wrong++
				}
				if byR || byM {
					both.wrong++
					wrongs = append(wrongs, fmt.Sprintf("%s %q (rules %v, model %v)", c.ID, v, byR, byM))
				}
			}
		}
		total, keep := r.hit+r.miss, 0
		for _, c := range cases {
			keep += len(c.Keep)
		}
		t.Logf("threshold %.1f: masked of %d / wrongly of %d: rules %d / %d, model %d / %d, both %d / %d",
			min, total, keep, r.hit, r.wrong, md.hit, md.wrong, both.hit, both.wrong)
		kinds := make([]string, 0, len(byKind))
		for k := range byKind {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		for _, k := range kinds {
			t.Logf("  %-8s rules %d, model %d, both %d", k, byKind[k][0], byKind[k][1], byKind[k][2])
		}
		t.Logf("  missed by both: %s", strings.Join(misses, "; "))
		t.Logf("  wrongly masked: %s", strings.Join(wrongs, "; "))
	}
}

func count(hit, miss *int, ok bool) {
	if ok {
		*hit++
	} else {
		*miss++
	}
}

// TestMail reads the mail in GODWIT_GUARD_MAIL (JSON lines of id and
// text) and logs how long the model takes and what it finds, by label;
// each value goes to GODWIT_GUARD_REPORT only, never to the log. With
// CoreML it reads the mail on the CPU too, and logs how many spans differ.
func TestMail(t *testing.T) {
	path := os.Getenv("GODWIT_GUARD_MAIL")
	if path == "" {
		t.Skip("GODWIT_GUARD_MAIL is not set")
	}
	t0 := time.Now()
	m, err := Load(dir(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("load %v, on %s (CoreML: %v)", time.Since(t0), m.Device(), m.mlErr)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var msgs []struct{ ID, Text string }
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	sc.Buffer(nil, 64<<20)
	for sc.Scan() {
		var msg struct{ ID, Text string }
		if err := json.Unmarshal(sc.Bytes(), &msg); err != nil {
			t.Fatal(err)
		}
		msgs = append(msgs, msg)
	}
	if len(msgs) == 0 {
		t.Fatal("no mail")
	}
	pct := func(xs []int, p float64) int {
		s := slices.Clone(xs)
		slices.Sort(s)
		return s[min(len(s)-1, int(p*float64(len(s))))]
	}
	read := func(m *Model) [][]Span {
		var ms, toks []int
		var total time.Duration
		var out [][]Span
		for _, msg := range msgs {
			t1 := time.Now()
			out = append(out, modelSpans(m, msg.Text, mask.MinScore))
			d := time.Since(t1)
			total += d
			ms = append(ms, int(d.Milliseconds()))
			toks = append(toks, len(m.tok.encode(msg.Text)))
		}
		t.Logf("%s: %d messages, %v in all: ms p50 %d p90 %d p99 %d max %d; tokens p50 %d p90 %d max %d",
			m.Device(), len(ms), total.Round(time.Millisecond), pct(ms, .5), pct(ms, .9), pct(ms, .99), pct(ms, 1),
			pct(toks, .5), pct(toks, .9), pct(toks, 1))
		return out
	}
	found := read(m)
	if m.ml != nil {
		cpu := read(&Model{tok: m.tok, net: m.net})
		diff, all := 0, 0
		for i := range found {
			key := func(s Span) string { return fmt.Sprint(s.Start, s.End, s.Label) }
			a, c := map[string]bool{}, map[string]bool{}
			for _, s := range found[i] {
				a[key(s)] = true
			}
			for _, s := range cpu[i] {
				c[key(s)] = true
			}
			all += len(a)
			for k := range a {
				if !c[k] {
					diff++
				}
			}
			for k := range c {
				if !a[k] {
					diff++
				}
			}
		}
		t.Logf("spans at MinScore: %d by CoreML, %d found by one of the two only", all, diff)
	}
	var rep *os.File
	if p := os.Getenv("GODWIT_GUARD_REPORT"); p != "" {
		if rep, err = os.Create(p); err != nil {
			t.Fatal(err)
		}
		defer rep.Close()
	}
	rules := mask.New([]byte("eval"))
	labels, fresh := map[string]int{}, map[string]int{}
	for i, msg := range msgs {
		out, err := rules.Text(msg.Text)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range found[i] {
			v := msg.Text[s.Start:s.End]
			labels[s.Label]++
			// what the rules leave as written: what the model adds
			isNew := strings.Contains(out, v)
			if isNew {
				fresh[s.Label]++
			}
			if rep != nil {
				fmt.Fprintf(rep, "%s\t%s\t%.2f\tnew=%v\t%q\n", msg.ID, s.Label, s.Score, isNew, v)
			}
		}
	}
	t.Logf("spans by label (of them left by the rules): %v %v", labels, fresh)
}
