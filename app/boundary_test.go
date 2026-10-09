package app

import (
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
