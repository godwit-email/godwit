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
	// shortcut: these still speak IMAP over the backend session's connection
	// (imapbackend.Receiver.Client) until their tickets move them behind
	// Apply and Find; each ticket deletes its entries.
	pending := map[string]string{
		"outbox.go":           "#604",
		"mailsync/folders.go": "#604", // SentFolder
	}
	const client = "github.com/emersion/go-imap/v2/imapclient"
	seen := map[string]bool{}
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
				p = filepath.ToSlash(p)
				seen[p] = true
				if pending[p] == "" {
					t.Errorf("core/%s imports imapclient: go through the protocol backend", p)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for p, ticket := range pending {
		if !seen[p] {
			t.Errorf("core/%s no longer imports imapclient: drop it from pending (%s)", p, ticket)
		}
	}
}
