package app

import (
	"cmp"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
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
