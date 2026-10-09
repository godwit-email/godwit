package editor

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/richtext"
)

const label = "Body"

type fixture struct {
	t   *testing.T
	tt  *ui.Tester
	e   *Editor
	o   Options
	now time.Time
}

// setup shows an editor of html, focused with the caret at the end.
func setup(t *testing.T, html string, o Options) *fixture {
	t.Helper()
	f := &fixture{t: t, e: New(richtext.FromComposeHTML(html)), now: time.Unix(1e9, 0)}
	f.e.Now = func() time.Time { return f.now }
	o.Label = label
	f.o = o
	f.tt = ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Children(func() { f.e.View(c, f.o).FillWidth() })
	}, 600, 400)
	if err := f.tt.Click(label); err != nil {
		t.Fatal(err)
	}
	f.tt.Key(ui.Cmd, ui.KeyDown)
	return f
}

func (f *fixture) html(want string) {
	f.t.Helper()
	if got := f.e.HTML(); got != want {
		f.t.Fatalf("HTML = %q, want %q", got, want)
	}
}

func (f *fixture) caret(want int) {
	f.t.Helper()
	if s := f.e.Selection(); s.Range != (ui.TextInputRange{Start: want, End: want}) {
		f.t.Fatalf("selection = %+v, want caret %d", s, want)
	}
}

func (f *fixture) keys(mods ui.Modifiers, k ui.Key, n int) {
	for range n {
		f.tt.Key(mods, k)
	}
}

func TestTypeAndBackspace(t *testing.T) {
	f := setup(t, "", Options{})
	v := f.e.Version()
	f.tt.Type("hello")
	f.html("<p>hello</p>")
	f.caret(5)
	f.keys(0, ui.KeyBackspace, 2)
	f.html("<p>hel</p>")
	f.tt.Key(ui.Cmd, ui.KeyUp)
	f.tt.Key(0, ui.KeyDelete)
	f.html("<p>el</p>")
	if f.e.Version() != v+4 {
		t.Fatalf("version %d, want %d", f.e.Version(), v+4)
	}
}

func TestIMECompose(t *testing.T) {
	f := setup(t, "<p>a</p>", Options{})
	v := f.e.Version()
	f.tt.Compose("ni", 2)
	if !f.e.Composing() || strings.Contains(f.e.HTML(), "ni") || f.e.Version() != v {
		t.Fatalf("composing %v, HTML %q, version %d", f.e.Composing(), f.e.HTML(), f.e.Version())
	}
	if r, ok := f.e.MarkedRange(); !ok || r != (ui.TextInputRange{Start: 1, End: 3}) {
		t.Fatalf("marked %v %v", r, ok)
	}
	if s, _ := f.e.TextForRange(ui.TextInputRange{End: 3}); s != "ani" {
		t.Fatalf("client text %q", s)
	}
	if _, ok := f.tt.TextCaret(); !ok {
		t.Fatal("no IME caret")
	}
	f.tt.Type("你")
	if f.e.Composing() {
		t.Fatal("still composing")
	}
	f.html("<p>a你</p>")
	f.caret(2)

	f.tt.Compose("hao", 3)
	f.tt.Key(0, ui.KeyEscape)
	if f.e.Composing() {
		t.Fatal("Esc left the composition")
	}
	f.html("<p>a你</p>")
	f.caret(2)
	f.tt.Compose("x", 1)
	f.tt.Compose("", 0)
	f.html("<p>a你</p>")
	if f.e.Composing() {
		t.Fatal("empty composition left a mark")
	}
	f.tt.Command("undo")
	f.html("<p>a</p>")
}

func TestEnterSplitsAndLists(t *testing.T) {
	f := setup(t, "<p>abcd</p>", Options{})
	f.keys(0, ui.KeyLeft, 2)
	f.tt.Key(0, ui.KeyEnter)
	f.html("<p>ab</p><p>cd</p>")
	f.caret(3)
	f.e.ToggleList(false)
	f.html("<p>ab</p><ul><li>cd</li></ul>")
	f.tt.Key(ui.Cmd, ui.KeyDown)
	f.tt.Key(0, ui.KeyEnter)
	f.tt.Type("x")
	f.html("<p>ab</p><ul><li>cd</li><li>x</li></ul>")
	f.tt.Key(0, ui.KeyEnter)
	f.tt.Key(0, ui.KeyEnter)
	f.html("<p>ab</p><ul><li>cd</li><li>x</li></ul><p><br></p>")
	f.tt.Type("y")
	f.e.ToggleList(true)
	f.html("<p>ab</p><ul><li>cd</li><li>x</li></ul><ol><li>y</li></ol>")
	f.e.ToggleQuote()
	f.html("<p>ab</p><ul><li>cd</li><li>x</li></ul><blockquote><p>y</p></blockquote>")
	f.e.ToggleQuote()
	f.html("<p>ab</p><ul><li>cd</li><li>x</li></ul><p>y</p>")
}

func TestBoldItalicShortcuts(t *testing.T) {
	f := setup(t, "<p>abc</p>", Options{})
	f.tt.Command("selectAll")
	f.tt.Key(ui.Cmd, ui.KeyB)
	f.html("<p><b>abc</b></p>")
	f.tt.Key(ui.Cmd, ui.KeyB)
	f.html("<p>abc</p>")
	f.tt.Key(ui.Cmd, ui.KeyDown)
	f.tt.Key(ui.Cmd, ui.KeyI)
	f.tt.Type("d")
	f.html("<p>abc<i>d</i></p>")
	f.tt.Key(ui.Shift, ui.KeyLeft)
	f.e.SetLink("javascript:alert(1)")
	f.html("<p>abc<i>d</i></p>")
	f.e.SetLink("https://example.com")
	f.html(`<p>abc<a href="https://example.com"><i>d</i></a></p>`)
}

func TestSelectionAcrossBlocks(t *testing.T) {
	f := setup(t, "<p>abc</p><p>def</p>", Options{})
	f.tt.Key(ui.Cmd, ui.KeyUp)
	f.tt.Key(0, ui.KeyRight)
	f.tt.Key(0, ui.KeyDown)
	f.caret(5)
	f.tt.Key(0, ui.KeyUp)
	f.caret(1)
	f.keys(ui.Shift, ui.KeyRight, 4)
	if s := f.e.Selection(); s.Range != (ui.TextInputRange{Start: 1, End: 5}) || s.Reversed {
		t.Fatalf("selection %+v", s)
	}
	if s, _ := f.e.TextForRange(f.e.Selection().Range); s != "bc\nd" {
		t.Fatalf("selected %q", s)
	}
	a, b := f.e.DocumentSelection()
	if a != (richtext.Pos{Block: 0, Rune: 1}) || b != (richtext.Pos{Block: 1, Rune: 1}) {
		t.Fatalf("document selection %v %v", a, b)
	}
	f.tt.Command("copy")
	if f.tt.Clipboard() != "bc\nd" {
		t.Fatalf("clipboard %q", f.tt.Clipboard())
	}
	f.tt.Type("X")
	f.html("<p>aXef</p>")
	f.tt.Key(ui.Shift, ui.KeyHome)
	if s := f.e.Selection(); s.Range != (ui.TextInputRange{Start: 0, End: 2}) || !s.Reversed {
		t.Fatalf("selection %+v", s)
	}
	f.tt.Command("cut")
	f.html("<p>ef</p>")
	if f.tt.Clipboard() != "aX" {
		t.Fatalf("clipboard %q", f.tt.Clipboard())
	}
}

func TestPasteHTMLSanitized(t *testing.T) {
	f := setup(t, "<p>x</p>", Options{PasteHTML: func() string {
		return `<p>hi <script>alert(1)</script><b onclick="x()">bold</b></p>`
	}})
	f.tt.Command("paste")
	f.html("<p>xhi <b>bold</b></p>")
	f.tt.Command("undo")
	f.html("<p>x</p>")

	f.o.PasteHTML = func() string { return "" }
	f.tt.Frame()
	f.tt.SetClipboard("one\ntwo")
	f.tt.Command("paste")
	f.html("<p>xone</p><p>two</p>")
}

func TestUndoRedoGrouping(t *testing.T) {
	f := setup(t, "", Options{})
	f.tt.Type("a")
	f.tt.Type("b")
	f.tt.Type("c")
	f.now = f.now.Add(2 * time.Second)
	f.tt.Type("d")
	f.tt.Command("undo")
	f.html("<p>abc</p>")
	f.tt.Command("undo")
	f.html("<p><br></p>")
	f.tt.Command("redo")
	f.tt.Command("redo")
	f.html("<p>abcd</p>")

	f.tt.Key(0, ui.KeyLeft)
	f.tt.Type("x")
	f.tt.Command("undo")
	f.html("<p>abcd</p>")

	f.tt.Command("selectAll")
	f.e.ToggleBold()
	f.html("<p><b>abcd</b></p>")
	f.tt.Command("undo")
	f.html("<p>abcd</p>")

	v := f.e.Version()
	f.e.Replace(richtext.FromComposeHTML("<p>new</p>"))
	f.html("<p>new</p>")
	if f.e.Version() == v {
		t.Fatal("Replace kept the version")
	}
	f.tt.Command("undo")
	f.html("<p>abcd</p>")
}

func TestEmojiAndCJKBoundaries(t *testing.T) {
	f := setup(t, "", Options{})
	f.tt.Type("你好👍🏽")
	f.caret(6)
	if s, r := f.e.TextForRange(ui.TextInputRange{Start: 0, End: 2}); s != "你好" || r != (ui.TextInputRange{End: 2}) {
		t.Fatalf("TextForRange %q %v", s, r)
	}
	f.tt.Key(0, ui.KeyLeft)
	f.caret(2)
	f.tt.Key(0, ui.KeyRight)
	f.caret(6)
	f.tt.Key(0, ui.KeyBackspace)
	f.html("<p>你好</p>")
	f.caret(2)
	f.tt.Key(0, ui.KeyBackspace)
	f.html("<p>你</p>")
	a, _ := f.e.DocumentSelection()
	if a != (richtext.Pos{Rune: 1}) {
		t.Fatalf("pos %v", a)
	}
}

func TestClickPlacesCaret(t *testing.T) {
	f := setup(t, "<p>hello</p><p>world</p>", Options{})
	box, _ := f.tt.Find(label)
	b, _, ok := f.e.BoundsForRange(ui.TextInputRange{Start: 9, End: 9})
	if !ok {
		t.Fatal("no bounds")
	}
	f.tt.ClickAt(box.X+b.X, box.Y+b.Y+b.H/2)
	f.caret(9)
	f.tt.Type("X")
	f.html("<p>hello</p><p>worXld</p>")

	// Dragging selects; a double click selects a word.
	b0, _, _ := f.e.BoundsForRange(ui.TextInputRange{Start: 1, End: 1})
	b3, _, _ := f.e.BoundsForRange(ui.TextInputRange{Start: 3, End: 3})
	f.tt.Press(box.X+b0.X, box.Y+b0.Y+b0.H/2)
	f.tt.Move(box.X+b3.X, box.Y+b3.Y+b3.H/2)
	f.tt.Release(box.X+b3.X, box.Y+b3.Y+b3.H/2)
	if s := f.e.Selection(); s.Range != (ui.TextInputRange{Start: 1, End: 3}) {
		t.Fatalf("drag selection %+v", s)
	}
	f.e.handle(ui.InputEvent{Kind: ui.InputPointerDown, Clicks: 2, X: b0.X, Y: b0.Y + b0.H/2})
	if s := f.e.Selection(); s.Range != (ui.TextInputRange{Start: 0, End: 5}) {
		t.Fatalf("word selection %+v", s)
	}
}

func TestReadOnlyIgnoresInput(t *testing.T) {
	calls := 0
	f := setup(t, "<p>keep</p>", Options{ReadOnly: true, OnReadOnlyInput: func() { calls++ },
		Highlights: []Highlight{{From: richtext.Pos{Rune: 1}, To: richtext.Pos{Rune: 3}, Strike: true}}})
	v := f.e.Version()
	f.tt.Type("x")
	f.tt.Key(0, ui.KeyBackspace)
	f.tt.Key(0, ui.KeyEnter)
	f.tt.Key(ui.Cmd, ui.KeyB)
	f.tt.Compose("ni", 2)
	f.tt.Command("paste")
	f.tt.Command("cut")
	f.tt.Command("undo")
	f.html("<p>keep</p>")
	if f.e.Version() != v || f.e.Composing() || calls != 8 {
		t.Fatalf("version %d (was %d), composing %v, calls %d", f.e.Version(), v, f.e.Composing(), calls)
	}
	f.tt.Command("selectAll")
	f.tt.Command("copy")
	if f.tt.Clipboard() != "keep" {
		t.Fatalf("clipboard %q", f.tt.Clipboard())
	}
}

// The model operations work without a view.
func TestReplaceOps(t *testing.T) {
	d := richtext.FromComposeHTML("<p>a<b>bc</b></p><ul><li>de</li></ul>")
	got, at := replace(d, richtext.Pos{Block: 0, Rune: 2}, richtext.Pos{Block: 1, Rune: 1}, richtext.Doc{})
	if h := richtext.ToHTML(got); h != "<p>a<b>b</b>e</p>" || at != (richtext.Pos{Rune: 2}) {
		t.Fatalf("%q %v", h, at)
	}
	if h := richtext.ToHTML(d); h != "<p>a<b>bc</b></p><ul><li>de</li></ul>" {
		t.Fatalf("replace changed its input: %q", h)
	}
	if p := posAt(d, 4); p != (richtext.Pos{Block: 1, Rune: 0}) || offset(d, p) != 4 {
		t.Fatalf("posAt %v", p)
	}
}
