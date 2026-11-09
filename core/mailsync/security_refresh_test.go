package mailsync

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// headerServer is a receiver that only reads header fields, folder by folder.
type headerServer struct {
	backend.Receiver
	calls   map[backend.FolderRef]int
	refused backend.FolderRef
}

func (h *headerServer) Headers(_ context.Context, folder backend.FolderRef, refs []backend.MessageRef, _ []string) (map[backend.MessageRef][]byte, error) {
	h.calls[folder]++
	if folder == h.refused {
		return nil, &backend.FolderError{Folder: folder, Err: errors.New("NO [NOPERM] no access")}
	}
	out := map[backend.MessageRef][]byte{}
	for _, r := range refs {
		out[r] = []byte("Authentication-Results: mx.receiver.test; dkim=pass header.d=company.example\r\nFrom: Alice <alice@company.example>\r\n\r\n")
	}
	return out, nil
}

// The refresh reads each folder's batch of cached headers in one request, and
// a folder the server refuses is skipped without failing the round.
func TestRefreshAuthenticationResultsBatchesAndSkipsRefusedFolders(t *testing.T) {
	ctx := t.Context()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "mail.db"))
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Execute(ctx, "INSERT INTO accounts(id,email,recv_host) VALUES (1,'me@customer.test','imap.receiver.test')")
	require.NoError(t, err)
	_, err = db.Execute(ctx, `INSERT INTO folders(id,account_id,name,server_ref,role,cursor) VALUES
		(1,1,'INBOX','INBOX','inbox','0000000001:0000000003'), (2,1,'Locked','Locked','','0000000001:0000000001')`)
	require.NoError(t, err)
	a, err := loadAccount(ctx, db, 1)
	require.NoError(t, err)
	put := func(f *folder, ref, id string) {
		m := backend.MessageAppeared{Location: backend.Location{Folder: f.ref, Ref: backend.MessageRef(ref)},
			Content: &backend.Content{Date: time.Now(), Header: []byte("From: Alice <alice@company.example>\r\nMessage-ID: <" + id + ">\r\nSubject: hi\r\n\r\n")}}
		require.NoError(t, importMessage(ctx, db, a, f, m, toParsed(m.Content), 1, false, &result{}))
	}
	inbox, locked := &folder{id: 1, ref: "INBOX", name: "INBOX", role: "inbox"}, &folder{id: 2, ref: "Locked", name: "Locked"}
	put(locked, "0000000001:0000000001", "l@x")
	put(inbox, "0000000001:0000000001", "a@x")
	put(inbox, "0000000001:0000000002", "b@x")
	put(inbox, "0000000001:0000000003", "c@x")
	_, err = db.Execute(ctx, "UPDATE messages SET auth_results = NULL")
	require.NoError(t, err)

	h := &headerServer{calls: map[backend.FolderRef]int{}, refused: "Locked"}
	_, err = refreshAuthenticationResults(ctx, db, h, 1, events.New())
	require.NoError(t, err, "a refused folder does not fail the round")
	assert.Equal(t, map[backend.FolderRef]int{"INBOX": 1, "Locked": 1}, h.calls, "one request per folder's batch")
	rows, err := db.Query(ctx, "SELECT message_id FROM messages WHERE auth_results IS NOT NULL ORDER BY message_id")
	require.NoError(t, err)
	assert.Equal(t, [][]any{{"a@x"}, {"b@x"}, {"c@x"}}, rows.Values)
}
