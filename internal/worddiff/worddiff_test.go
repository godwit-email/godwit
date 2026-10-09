package worddiff

import (
	"strings"
	"testing"

	"github.com/godwit-email/godwit/internal/richtext"
)

func toks(h string) [][]Token { return Tokenize(richtext.FromComposeHTML(h)) }

// render is the diff as text: [-del-] and {+ins+}, blocks on lines.
func render(bs []BlockDiff) string {
	var lines []string
	for _, b := range bs {
		var sb strings.Builder
		for _, op := range b.Ops {
			var t strings.Builder
			for _, tk := range op.Tokens {
				t.WriteString(tk.Text)
			}
			switch op.Kind {
			case Same:
				sb.WriteString(t.String())
			case Del:
				sb.WriteString("[-" + t.String() + "-]")
			case Ins:
				sb.WriteString("{+" + t.String() + "+}")
			}
		}
		lines = append(lines, sb.String())
	}
	return strings.Join(lines, "\n")
}

func TestWordDiff(t *testing.T) {
	cases := []struct {
		name, old, new, want string
		ratio                float64
	}{
		{"english word", "<p>see you on Monday.</p>", "<p>see you on Tuesday.</p>", "see you on [-Monday-]{+Tuesday+}.", 2.0 / 10},
		{"chinese insert", "<p>我们下周见</p>", "<p>我们下周三见</p>", "我们下周{+三+}见", 1.0 / 11},
		{"all different", "<p>one two</p>", "<p>three four</p>", "[-one-]{+three+} [-two-]{+four+}", 1},
		{"empty", "", "", "", 0},
		{"link only", `<p>read <a href="https://a.example/x">the doc</a></p>`, `<p>read <a href="https://b.example/y">the doc</a></p>`,
			"read [-the doc-]{+the doc+}", 4.0 / 6},
		{"bold only", "<p>a big deal</p>", "<p>a <b>big</b> deal</p>", "a [-big-]{+big+} deal", 2.0 / 6},
		{"added block", "<p>hi</p>", "<p>hi</p><p>more</p>", "hi\n{+more+}", 1.0 / 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bs, ok := Diff(toks(c.old), toks(c.new), DefaultBudget)
			if !ok {
				t.Fatal("not ok")
			}
			if got := render(bs); got != c.want {
				t.Fatalf("diff %q, want %q", got, c.want)
			}
			if r := Ratio(bs); r < c.ratio-1e-9 || r > c.ratio+1e-9 || r < 0 || r > 1 {
				t.Fatalf("ratio %v, want %v", r, c.ratio)
			}
		})
	}
}

func TestTokenize(t *testing.T) {
	got := toks("<p>Hi, <b>Lena</b> 你好</p>")
	var texts []string
	for _, tk := range got[0] {
		texts = append(texts, tk.Text)
	}
	if strings.Join(texts, "|") != "Hi|,| |Lena| |你|好" {
		t.Fatalf("tokens %q", texts)
	}
	if !got[0][3].Style.Bold || got[0][3].Style.Text != "" {
		t.Fatalf("style %+v", got[0][3].Style)
	}
}

func TestWordDiffBudget(t *testing.T) {
	var a, b strings.Builder
	for i := range 1000 {
		a.WriteString(" a" + string(rune('a'+i%26)))
		b.WriteString(" b" + string(rune('a'+i%26)))
	}
	old, new := toks("<p>"+a.String()+"</p>"), toks("<p>"+b.String()+"</p>")
	if _, ok := Diff(old, new, 1000); ok {
		t.Fatal("over the budget, still ok")
	}
	if _, ok := Diff(old, new, DefaultBudget*10); !ok {
		t.Fatal("within a large budget, not ok")
	}
}

func TestWordDiffBudgetIdenticalBlocks(t *testing.T) {
	var a strings.Builder
	for range 2000 {
		a.WriteString(" same")
	}
	same := toks("<p>" + a.String() + "</p><p>" + a.String() + "</p>")
	if _, ok := Diff(same, same, 100); ok {
		t.Fatal("identical blocks compared past the budget, still ok")
	}
	if _, ok := Diff(same, same, DefaultBudget); !ok {
		t.Fatal("within the budget, not ok")
	}
}
