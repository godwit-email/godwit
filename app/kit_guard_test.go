package app

import (
	"cmp"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// rawPrimitives are the mygo Base controls the app builds only through internal/kit.
var rawPrimitives = map[string]bool{"ButtonBase": true, "TextInputBase": true, "TextAreaBase": true, "DialogBase": true, "PopoverBase": true, "TooltipBase": true, "Split": true, "List": true, "WebView": true}

// rawCalls returns, for the Go source src, each call to a raw mygo primitive
// or to a Tooltip method not of the kit package, as "position: call".
func rawCalls(name, src string) ([]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		return nil, err
	}
	// The names the file gives mygo's ui and internal/kit, aliases included.
	uiName, kitName := "", ""
	for _, im := range file.Imports {
		name := ""
		if im.Name != nil {
			name = im.Name.Name
		}
		switch strings.Trim(im.Path.Value, `"`) {
		case "github.com/egoist/mygo/ui":
			uiName = cmp.Or(name, "ui")
		case "github.com/godwit-email/godwit/internal/kit":
			kitName = cmp.Or(name, "kit")
		}
	}
	var found []string
	if uiName == "." {
		found = append(found, fset.Position(file.Package).String()+": dot import of ui")
	}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, _ := sel.X.(*ast.Ident)
		pkg := ""
		if id != nil {
			pkg = id.Name
		}
		if pkg != "" && pkg == uiName && rawPrimitives[sel.Sel.Name] || sel.Sel.Name == "Tooltip" && (kitName == "" || pkg != kitName) {
			found = append(found, fset.Position(sel.Sel.Pos()).String()+": "+sel.Sel.Name)
		}
		return true
	})
	return found, nil
}

// TestNoRawPrimitives: every control in app/ comes from internal/kit.
func TestNoRawPrimitives(t *testing.T) {
	paths, err := filepath.Glob("*.go")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no Go files: %v", err)
	}
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		src, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		found, err := rawCalls(p, string(src))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range found {
			t.Errorf("%s; use internal/kit", f) // f is "position: call"
		}
	}
}

func TestRawPrimitiveScanner(t *testing.T) {
	for _, tc := range []struct {
		code    string
		flagged bool
		imports string
	}{
		{`b.Tooltip("x")`, true, ""},
		{`kit.Tooltip(c, b, "x")`, false, ""},
		{"ui.ButtonBase(c).\n\tLabel(\"x\")", true, ""},
		{`raw.ButtonBase(c)`, true, `raw "github.com/egoist/mygo/ui"`},
		{`k.Tooltip(c, b, "x")`, false, `k "github.com/godwit-email/godwit/internal/kit"`},
		{`ButtonBase(c)`, true, `. "github.com/egoist/mygo/ui"`},
	} {
		imports := cmp.Or(tc.imports, "\"github.com/egoist/mygo/ui\"\n\"github.com/godwit-email/godwit/internal/kit\"")
		found, err := rawCalls("snippet.go", "package p\nimport (\n"+imports+"\n)\nfunc f() {\n"+tc.code+"\n}\n")
		if err != nil {
			t.Fatal(err)
		}
		if (len(found) > 0) != tc.flagged {
			t.Errorf("%q: flagged %v, want %v", tc.code, found, tc.flagged)
		}
	}
}

// TestNoGeist: the native UI uses the system's fonts; code-like text is theme.Mono.
func TestNoGeist(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), `"Geist`) {
			t.Errorf("%s names the Geist font; use theme.Mono", f)
		}
	}
}

var (
	roundedBox   = regexp.MustCompile(`Radius\(t\.Radius\.\w+\)`)
	filledBox    = regexp.MustCompile(`Background\(|Border\(`)
	kitControl   = regexp.MustCompile(`kit\.(Input|InputGroup|Select|SelectWith|Button|IconButton|ButtonFrame)\(`)
	kitField     = regexp.MustCompile(`kit\.(Input|InputGroup|Select|SelectWith|SelectButton)\(`)
	fieldResized = regexp.MustCompile(`\.(Height|Radius)\(`)
	spaceHeight  = regexp.MustCompile(`\.Height\((?:th|c\.Theme\(\))\.Space\(([0-9.]+)\)\)`)
	controlSteps = []string{"6", "7", "8", "9"} // 24, 28, 32, 36: the scale of the design system's README
)

// appLines calls fn with each line of app/'s Go files that are not tests.
func appLines(t *testing.T, fn func(file string, n int, line string)) {
	t.Helper()
	paths, err := filepath.Glob("*.go")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no Go files: %v", err)
	}
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		src, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			fn(p, i+1, line)
		}
	}
}

// TestNoNewHandStyledBoxes: a page draws no surface of its own, a box it
// rounds and fills or outlines by hand, where a kit component (Card, Panel,
// BadgeWith, Alert, StatusDot) belongs. testdata/hand_styled.txt pins how
// many each file still has: the count only goes down, and the pin with it.
// shortcut: reads line by line, so a chain broken across lines escapes it; walk the AST as rawCalls does once they slip through.
func TestNoNewHandStyledBoxes(t *testing.T) {
	got := map[string]int{}
	appLines(t, func(file string, _ int, line string) {
		if roundedBox.MatchString(line) && filledBox.MatchString(line) {
			got[file]++
		}
	})
	var want []string
	for _, f := range slices.Sorted(maps.Keys(got)) {
		want = append(want, fmt.Sprintf("%s %d", f, got[f]))
	}
	pinned, err := os.ReadFile("testdata/hand_styled.txt")
	if err != nil {
		t.Fatal(err)
	}
	pins := map[string]int{}
	for _, l := range strings.Fields(strings.ReplaceAll(strings.TrimSpace(string(pinned)), " ", ":")) {
		f, n, _ := strings.Cut(l, ":")
		pins[f], _ = strconv.Atoi(n)
	}
	for f, n := range got {
		if n > pins[f] {
			t.Errorf("%s draws %d surfaces by hand, %d pinned: build the new one with internal/kit (or add what kit lacks)", f, n, pins[f])
		}
	}
	for f, n := range pins {
		if got[f] < n {
			t.Errorf("%s is down to %d hand-drawn surfaces from %d: lower its line in testdata/hand_styled.txt", f, got[f], n)
		}
	}
	if t.Failed() {
		t.Logf("testdata/hand_styled.txt as it is now:\n%s", strings.Join(want, "\n"))
	}
}

// TestControlHeightsOnScale: a single-line kit control is 24, 28, 32 or 36 high;
// a page gives it no height between, and an input or a select none at all,
// nor a corner: its Size says both.
func TestControlHeightsOnScale(t *testing.T) {
	appLines(t, func(file string, n int, line string) {
		if kitField.MatchString(line) && fieldResized.MatchString(line) {
			t.Errorf("%s:%d: an input or select given its own height or corner; set InputOpts.Size or SelectOpts.Size (a bare input in a group takes AlignSelf(ui.Stretch))", file, n)
		}
		if !kitControl.MatchString(line) {
			return
		}
		for _, m := range spaceHeight.FindAllStringSubmatch(line, -1) {
			if !slices.Contains(controlSteps, m[1]) {
				t.Errorf("%s:%d: a control Space(%s) high; the scale is Space(6), Space(7), Space(8) and Space(9)", file, n, m[1])
			}
		}
	})
}
