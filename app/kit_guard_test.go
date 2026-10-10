package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rawPrimitives are the mygo Base controls the app builds only through internal/kit.
var rawPrimitives = map[string]bool{"ButtonBase": true, "TextInputBase": true, "TextAreaBase": true, "DialogBase": true, "PopoverBase": true, "TooltipBase": true}

// rawCalls returns, for the Go source src, each call to a raw mygo primitive
// or to a Tooltip method not of the kit package, as "position: call".
func rawCalls(name, src string) ([]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		return nil, err
	}
	var found []string
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
		if pkg == "ui" && rawPrimitives[sel.Sel.Name] || sel.Sel.Name == "Tooltip" && pkg != "kit" {
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
	}{
		{`b.Tooltip("x")`, true},
		{`kit.Tooltip(c, b, "x")`, false},
		{"ui.ButtonBase(c).\n\tLabel(\"x\")", true},
	} {
		found, err := rawCalls("snippet.go", "package p\nfunc f() {\n"+tc.code+"\n}\n")
		if err != nil {
			t.Fatal(err)
		}
		if (len(found) > 0) != tc.flagged {
			t.Errorf("%q: flagged %v, want %v", tc.code, found, tc.flagged)
		}
	}
}
