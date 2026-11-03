package kit

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// Tokens come in from the buttons, text is typed after them, and Backspace
// in the empty input takes out the last token; the value keeps the ids.
func TestTemplateField(t *testing.T) {
	value := "{date} "
	opts := TemplateOpts{Label: "Naming", Insert: "Insert", Remove: func(l string) string { return "Remove " + l },
		Tokens: []TemplateToken{{ID: "date", Label: "Date"}, {ID: "subject", Label: "Subject"}, {ID: "from", Label: "Sender"}}}
	tt := ui.NewTester(themed(func(c *ui.Context) {
		o := opts
		o.Preview = "~/Mail/2026-11-03"
		TemplateField(c, &value, o)
	}), 480, 200)
	if !tt.HasText("Date") || tt.HasText("{date}") {
		t.Fatalf("the date token is not a chip: %q", tt.Texts())
	}
	if err := tt.Click("Subject"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if value != "{date} {subject}" {
		t.Fatalf("after inserting: %q", value)
	}
	if err := tt.Click("Naming"); err != nil {
		t.Fatal(err)
	}
	tt.Type("/x")
	tt.Frame()
	if value != "{date} {subject}/x" {
		t.Fatalf("after typing: %q", value)
	}
	tt.Key(0, ui.KeyBackspace)
	tt.Key(0, ui.KeyBackspace)
	tt.Key(0, ui.KeyBackspace)
	tt.Frame()
	if value != "{date} " {
		t.Fatalf("after Backspace: %q", value)
	}
	if err := tt.Click("Remove Date"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if value != " " {
		t.Fatalf("after removing: %q", value)
	}
}

func TestParseTemplate(t *testing.T) {
	toks := []TemplateToken{{ID: "date"}, {ID: "subject"}}
	got := parseTemplate("{date} {nope}/{subject}", toks)
	want := []templateSeg{{id: "date"}, {text: " {nope}/"}, {id: "subject"}}
	if len(got) != len(want) {
		t.Fatalf("parse = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parse = %+v, want %+v", got, want)
		}
	}
	if s := formatTemplate(got); s != "{date} {nope}/{subject}" {
		t.Fatalf("format = %q", s)
	}
}
