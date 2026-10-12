package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/godwit-email/godwit/core/gateway"
	"github.com/stretchr/testify/require"
)

func TestReadAttachment(t *testing.T) {
	s := newMailContextService(t)
	id := addMessage(t, s, "Report", "Bob <bob@x>", "see attached", "", 1_700_000_000)
	add := func(name, ctype, content string) int64 {
		p := filepath.Join(t.TempDir(), name)
		require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
		res, err := s.db.Execute(t.Context(), "INSERT INTO attachments(message_id, filename, content_type, size, inline, cache_path) VALUES (?, ?, ?, ?, 0, ?)", id, name, ctype, len(content), p)
		require.NoError(t, err)
		return res.LastInsertID
	}
	scope := &gateway.Scope{}
	ctx := context.WithValue(t.Context(), runKey{}, &runJob{scope: scope})
	args := func(id int64) json.RawMessage { b, _ := json.Marshal(idArgs{ID: id}); return b }
	csv := add("q.csv", "text/csv", "name,qty\nwidget,2\n")
	for _, f := range []struct{ file, ctype, want string }{
		{"attachment.pdf", "application/pdf", "REPAIR"},
		{"attachment.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", ""},
	} {
		b, err := os.ReadFile("testdata/" + f.file)
		require.NoError(t, err)
		out, err := s.readAttachment(ctx, args(add(f.file, f.ctype, string(b))))
		require.NoError(t, err, f.file)
		txt := out.(map[string]any)["text"].(string)
		require.Contains(t, txt, f.want, f.file)
	}
	img := add("a.png", "image/png", "\x89PNG")

	out, err := s.readAttachment(ctx, args(csv))
	require.NoError(t, err)
	require.Contains(t, out.(map[string]any)["text"], "widget")
	require.True(t, scope.ReadMail())

	_, err = s.readAttachment(ctx, args(img))
	require.ErrorContains(t, err, "not a type")

	items, err := s.mailContext(ctx, scope, []int64{id}, defaultMailBudget)
	require.NoError(t, err)
	require.Contains(t, items[0].Block, "attachment 1,")
}
