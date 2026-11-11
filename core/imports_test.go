package core

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestNoIMAPClient: core speaks IMAP only through the protocol backend
// (ADR 0037), so no file of it imports go-imap's client.
func TestNoIMAPClient(t *testing.T) {
	const client = "github.com/emersion/go-imap/v2/imapclient"
	err := filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			if path, _ := strconv.Unquote(imp.Path.Value); path == client {
				t.Errorf("core/%s imports imapclient: go through the protocol backend", filepath.ToSlash(p))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestIMAPBackendStaysBehindTheSeam: UIDVALIDITY and the IMAP backend's
// encodings stay in the backend (ADR 0037). Only the place that picks the
// account's backend imports it.
func TestIMAPBackendStaysBehindTheSeam(t *testing.T) {
	const pkg = "github.com/godwit-email/godwit/backend/imapbackend"
	allowed := map[string]bool{"check.go": true}
	err := filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			if path, _ := strconv.Unquote(imp.Path.Value); path == pkg && !allowed[filepath.ToSlash(p)] {
				t.Errorf("core/%s imports imapbackend: go through backend.Receiver", filepath.ToSlash(p))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
