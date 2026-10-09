package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// sizeCall names the methods whose arguments are sizes.
var sizeCall = regexp.MustCompile(`^(Padding.*|Gap.*|Margin.*|Height|Width|MinWidth|MaxWidth|MinHeight|MaxHeight|Radius|FontSize)$`)

// TestNoSizeLiterals: sizes in app/ and internal/kit/ come from the theme
// (c.Theme().Space, theme's Radius and Text tokens and layout constants),
// never from number literals or the density Scale.
func TestNoSizeLiterals(t *testing.T) {
	for _, dir := range []string{".", "../internal/kit"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("no Go files in %s: %v", dir, err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, f, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(file, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.SelectorExpr:
					if n.Sel.Name == "Scale" {
						t.Errorf("%s: uses Scale", fset.Position(n.Pos()))
					}
				case *ast.CallExpr:
					sel, ok := n.Fun.(*ast.SelectorExpr)
					if !ok || !sizeCall.MatchString(sel.Sel.Name) {
						return true
					}
					for _, arg := range n.Args {
						ast.Inspect(arg, func(m ast.Node) bool {
							if call, ok := m.(*ast.CallExpr); ok {
								if f, ok := call.Fun.(*ast.SelectorExpr); ok && (f.Sel.Name == "Space" || f.Sel.Name == "Rem") {
									return false // Space(n) and Rem(n) count in theme units
								}
							}
							if lit, ok := m.(*ast.BasicLit); ok && (lit.Kind == token.INT || lit.Kind == token.FLOAT) && lit.Value != "0" {
								t.Errorf("%s: %s(%s) has a size literal", fset.Position(lit.Pos()), sel.Sel.Name, lit.Value)
							}
							return true
						})
					}
				}
				return true
			})
		}
	}
}
