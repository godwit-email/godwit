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
var sizeCall = regexp.MustCompile(`^(Padding.*|Gap.*|Margin.*|Height|Width|MinWidth|MaxWidth|MinHeight|MaxHeight|Radius|FontSize|Size|BorderWidth)$`)

// sizeArgs returns the arguments of call that are sizes, nil if none.
func sizeArgs(call *ast.CallExpr) (string, []ast.Expr) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", nil
	}
	name, args := sel.Sel.Name, call.Args
	switch {
	case name == "Border" && len(args) > 0:
		return name, args[:1] // the width, not the color
	case name == "Shadow" && len(args) > 4:
		return name, args[:4] // offset, blur and spread, not the color
	case sizeCall.MatchString(name):
		return name, args
	}
	return "", nil
}

// hasNumber reports whether e holds a number literal other than 0, not
// counting Space(n) and Rem(n), which count in theme units.
func hasNumber(e ast.Node) (lit *ast.BasicLit) {
	ast.Inspect(e, func(m ast.Node) bool {
		if call, ok := m.(*ast.CallExpr); ok {
			if f, ok := call.Fun.(*ast.SelectorExpr); ok && (f.Sel.Name == "Space" || f.Sel.Name == "Rem") {
				return false
			}
		}
		if l, ok := m.(*ast.BasicLit); ok && lit == nil && (l.Kind == token.INT || l.Kind == token.FLOAT) && l.Value != "0" {
			lit = l
		}
		return lit == nil
	})
	return lit
}

// TestNoSizeLiterals: sizes in app/ and internal/kit/ come from the theme
// (c.Theme().Space, theme's Radius and Text tokens and its size constants),
// never from number literals, constants of their own, or the density Scale.
func TestNoSizeLiterals(t *testing.T) {
	for _, dir := range []string{".", "../internal/kit"} {
		paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil || len(paths) == 0 {
			t.Fatalf("no Go files in %s: %v", dir, err)
		}
		fset := token.NewFileSet()
		var files []*ast.File
		numConsts := map[string]bool{} // the package's constants of numbers
		for _, p := range paths {
			if strings.HasSuffix(p, "_test.go") {
				continue
			}
			file, err := parser.ParseFile(fset, p, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			files = append(files, file)
			ast.Inspect(file, func(n ast.Node) bool {
				if g, ok := n.(*ast.GenDecl); ok && g.Tok == token.CONST {
					for _, s := range g.Specs {
						vs := s.(*ast.ValueSpec)
						for i, name := range vs.Names {
							if i < len(vs.Values) && hasNumber(vs.Values[i]) != nil {
								numConsts[name.Name] = true
							}
						}
					}
				}
				return true
			})
		}
		for _, file := range files {
			ast.Inspect(file, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.SelectorExpr:
					if n.Sel.Name == "Scale" {
						t.Errorf("%s: uses Scale", fset.Position(n.Pos()))
					}
				case *ast.CallExpr:
					name, args := sizeArgs(n)
					for _, arg := range args {
						if lit := hasNumber(arg); lit != nil {
							t.Errorf("%s: %s(%s) has a size literal", fset.Position(lit.Pos()), name, lit.Value)
						}
						ast.Inspect(arg, func(m ast.Node) bool {
							if id, ok := m.(*ast.Ident); ok && numConsts[id.Name] {
								t.Errorf("%s: %s(%s) uses a size constant outside internal/theme", fset.Position(id.Pos()), name, id.Name)
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
