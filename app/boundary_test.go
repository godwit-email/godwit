package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const corePath = "github.com/godwit-email/godwit/core"

// allowedCore are the core packages the UI may import: the Service and pure types.
var allowedCore = map[string]bool{
	corePath: true, corePath + "/mail": true, corePath + "/ai": true,
	corePath + "/events": true, corePath + "/auth": true, corePath + "/plugins": true,
}

func forbiddenImport(path string) bool {
	return strings.HasPrefix(path, corePath+"/") && !allowedCore[path]
}

// TestImportBoundary: app/, internal/ and cmd/godwit/ reach core only through
// *core.Service and its pure types (test files excepted).
func TestImportBoundary(t *testing.T) {
	for path, bad := range map[string]bool{
		corePath: false, corePath + "/events": false, corePath + "/store": true,
		corePath + "/coretest": true, corePath + "/internal/testmail": true, corePath + "x": false,
	} {
		if forbiddenImport(path) != bad {
			t.Errorf("forbiddenImport(%q) = %v", path, !bad)
		}
	}
	checked := 0
	for _, dir := range []string{".", "../internal", "../cmd/godwit"} {
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return err
			}
			f, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			checked++
			for _, im := range f.Imports {
				if ip, _ := strconv.Unquote(im.Path.Value); forbiddenImport(ip) {
					t.Errorf("%s imports %s", p, ip)
				}
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	if checked == 0 {
		t.Fatal("no source files checked")
	}
}

// TestServiceInterface verifies that App accepts any Service implementation,
// non-test UI files do not reference concrete *core.Service outside compile-time
// satisfaction checks, and each Service interface lists only called methods
// (never FollowConversation).
func TestServiceInterface(t *testing.T) {
	h := newHarness(t)
	type wrapped struct{ Service }
	a := New(Deps{Svc: wrapped{h.env.Svc}, Platform: h.plat, Post: h.post, Clock: h.clock})
	if a == nil {
		t.Fatal("New returned nil")
	}

	fset := token.NewFileSet()
	for _, dir := range []string{".", "../webui"} {
		declared := map[string]bool{}
		sf, err := parser.ParseFile(fset, filepath.Join(dir, "service.go"), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(sf, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok || ts.Name.Name != "Service" {
				return true
			}
			iface, ok := ts.Type.(*ast.InterfaceType)
			if !ok {
				return false
			}
			for _, m := range iface.Methods.List {
				for _, name := range m.Names {
					declared[name.Name] = true
				}
			}
			return false
		})
		if declared["FollowConversation"] {
			t.Errorf("%s/service.go must not list FollowConversation", dir)
		}

		called := map[string]bool{}
		err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") || filepath.Base(p) == "service.go" {
				return err
			}
			src, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if strings.Contains(string(src), "*core.Service") {
				t.Errorf("%s still references *core.Service", p)
			}
			f, err := parser.ParseFile(fset, p, src, 0)
			if err != nil {
				return err
			}
			ast.Inspect(f, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok {
					called[sel.Sel.Name] = true
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		for m := range declared {
			if !called[m] {
				t.Errorf("%s/service.go lists unused method %s", dir, m)
			}
		}
	}
}
