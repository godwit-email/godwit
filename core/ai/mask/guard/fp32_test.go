package guard

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFP32 holds bert.go and CoreML to the publisher's float32 model, run
// by ONNX Runtime outside Go, in GODWIT_GUARD_FP32 (the folder of its
// model.onnx): the first run writes each window's ids to windows.jsonl
// there, for fp32_logits.py to write logits.bin from; the next compares
// what each finds, on testdata/eval.json and the mail in GODWIT_GUARD_MAIL.
func TestFP32(t *testing.T) {
	out := os.Getenv("GODWIT_GUARD_FP32")
	if out == "" {
		t.Skip("GODWIT_GUARD_FP32 is not set")
	}
	m, err := Load(dir(t))
	if err != nil {
		t.Fatal(err)
	}
	cases := evalCases(t)
	texts := make([]string, 0, len(cases))
	for _, c := range cases {
		texts = append(texts, c.Text)
	}
	if p := os.Getenv("GODWIT_GUARD_MAIL"); p != "" {
		f, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(nil, 64<<20)
		for sc.Scan() {
			var msg struct{ Text string }
			if err := json.Unmarshal(sc.Bytes(), &msg); err != nil {
				t.Fatal(err)
			}
			texts = append(texts, msg.Text)
		}
		f.Close()
	}
	lgPath := filepath.Join(out, "logits.bin")
	b, err := os.ReadFile(lgPath)
	if os.IsNotExist(err) {
		var w strings.Builder
		for _, s := range texts {
			m.find(s, func(ids [][]int) [][]float32 {
				lgs := make([][]float32, len(ids))
				for i, x := range ids {
					j, _ := json.Marshal(x)
					w.Write(j)
					w.WriteByte('\n')
					lgs[i] = make([]float32, len(x)*len(labels))
				}
				return lgs
			})
		}
		if err := os.WriteFile(filepath.Join(out, "windows.jsonl"), []byte(w.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Skipf("wrote %d texts' windows; run fp32_logits.py, then this again", len(texts))
	}
	if err != nil {
		t.Fatal(err)
	}
	at := 0
	fp32 := func(ids [][]int) [][]float32 {
		lgs := make([][]float32, len(ids))
		for w, x := range ids {
			n := len(x) * len(labels)
			lgs[w] = make([]float32, n)
			for i := range n {
				lgs[w][i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*(at+i):]))
			}
			at += n
		}
		return lgs
	}
	// every span at Floor, by backend, then text
	all := map[string][][]Span{}
	cpu := &Model{tok: m.tok, net: m.net}
	for _, s := range texts {
		all["fp32"] = append(all["fp32"], cpu.find(s, fp32))
		all["Go"] = append(all["Go"], cpu.Find(s))
		if m.ml != nil {
			all["CoreML"] = append(all["CoreML"], m.Find(s))
		}
	}
	if at*4 != len(b) {
		t.Fatalf("logits.bin has %d floats, the windows %d", len(b)/4, at)
	}
	at08 := func(spans []Span, min float64) map[string]bool {
		set := map[string]bool{}
		for _, s := range spans {
			if kept[s.Label] && s.Score >= min {
				set[fmt.Sprint(s.Start, s.End, s.Label)] = true
			}
		}
		return set
	}
	for _, name := range []string{"Go", "CoreML"} {
		if all[name] == nil {
			continue
		}
		for _, min := range []float64{.6, .7, .75, .8, .85, .9} {
			var both, onlyA, onlyF int
			for i := range texts {
				a, f := at08(all[name][i], min), at08(all["fp32"][i], MinScore)
				for k := range a {
					if f[k] {
						both++
					} else {
						onlyA++
					}
				}
				for k := range f {
					if !a[k] {
						onlyF++
					}
				}
			}
			t.Logf("%-6s at %.2f vs fp32 at %.2f: %d the same, %d only %s's, %d only fp32's",
				name, min, MinScore, both, onlyA, name, onlyF)
		}
	}
	// the eval's values, as each masks them at MinScore
	for _, name := range []string{"fp32", "Go", "CoreML"} {
		if all[name] == nil {
			continue
		}
		hit, total, wrong := 0, 0, 0
		var misses []string
		for i, c := range cases {
			var spans []Span
			for _, s := range all[name][i] {
				if kept[s.Label] && s.Score >= MinScore {
					spans = append(spans, s)
				}
			}
			for _, v := range c.Mask {
				total++
				if covered(c.Text, v.V, spans, false) {
					hit++
				} else {
					misses = append(misses, v.V)
				}
			}
			for _, v := range c.Keep {
				if covered(c.Text, v, spans, true) {
					wrong++
				}
			}
		}
		t.Logf("eval, model alone, %s: %d of %d masked, %d wrongly; missed %q", name, hit, total, wrong, misses)
	}
}
