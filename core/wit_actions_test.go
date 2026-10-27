package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/gateway"
	"github.com/godwit-email/godwit/core/mailsync"
	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWitBatchActions(t *testing.T) {
	e := newSkillEnv(t)
	ctx := t.Context()

	// Ensure trash, archive and junk folders exist
	_, err := e.s.db.Execute(ctx, "INSERT INTO folders(account_id, name, role) VALUES (1, 'Archive', 'archive'), (1, 'Trash', 'trash'), (1, 'Junk', 'junk')")
	require.NoError(t, err)

	msg1 := addMessage(t, e.s, "Newsletter 1", "news@example.com", "News content", "", 1_700_000_100)
	msg2 := addMessage(t, e.s, "Newsletter 2", "news@example.com", "More news", "", 1_700_000_200)

	// Test SetFlags batch (mark_as_read)
	items := []batchItem{
		{ID: 1, MsgID: msg1, AccountID: 1, Subject: "Newsletter 1", From: "news@example.com", Date: 1_700_000_100},
		{ID: 2, MsgID: msg2, AccountID: 1, Subject: "Newsletter 2", From: "news@example.com", Date: 1_700_000_200},
	}
	ba := batchArgs{
		Action:      "mark_as_read",
		IDs:         []int{1, 2},
		Items:       items,
		SelectedIDs: []int{1, 2},
	}
	b, err := json.Marshal(ba)
	require.NoError(t, err)

	_, res, _, err := e.s.applyBatchAction(ctx, Approval{ID: 1, Tool: "mark_as_read", Args: b})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"applied": 2, "action": "mark_as_read"}, res)

	// Check flags
	rows, err := e.s.db.Query(ctx, "SELECT flags FROM messages WHERE id IN (?, ?)", msg1, msg2)
	require.NoError(t, err)
	require.Len(t, rows.Values, 2)
	assert.Contains(t, store.Str(rows.Values[0][0]), `\Seen`)
	assert.Contains(t, store.Str(rows.Values[1][0]), `\Seen`)

	// Test deselecting an item (checkbox deselection)
	ba.Action = "star_mail"
	ba.SelectedIDs = []int{1} // only star msg1
	b, err = json.Marshal(ba)
	require.NoError(t, err)

	_, res, _, err = e.s.applyBatchAction(ctx, Approval{ID: 2, Tool: "star_mail", Args: b})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"applied": 1, "action": "star_mail"}, res)

	rows, err = e.s.db.Query(ctx, "SELECT id, flags FROM messages WHERE id IN (?, ?)", msg1, msg2)
	require.NoError(t, err)
	for _, r := range rows.Values {
		id := store.Int(r[0])
		flags := store.Str(r[1])
		if id == msg1 {
			assert.Contains(t, flags, `\Flagged`)
		} else {
			assert.NotContains(t, flags, `\Flagged`)
		}
	}

	// Test action switching: user changed action to "archive_mail"
	ba.Action = "archive_mail"
	ba.SelectedIDs = []int{1, 2}
	b, err = json.Marshal(ba)
	require.NoError(t, err)

	_, res, _, err = e.s.applyBatchAction(ctx, Approval{ID: 3, Tool: "move_to_trash", Args: b})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"applied": 2, "action": "archive_mail"}, res)

	// Verify messages moved to archive
	archiveID, _, err := mailsync.RoleFolder(ctx, e.s.db, 1, "archive")
	require.NoError(t, err)
	rows, err = e.s.db.Query(ctx, "SELECT folder_id FROM messages WHERE id IN (?, ?)", msg1, msg2)
	require.NoError(t, err)
	assert.Equal(t, archiveID, store.Int(rows.Values[0][0]))
	assert.Equal(t, archiveID, store.Int(rows.Values[1][0]))
}

func TestWitComposeAndReplies(t *testing.T) {
	e := newSkillEnv(t)
	ctx := t.Context()

	msgID := addMessage(t, e.s, "Project Update", "alice@example.com", "Here is the update.", "", 1_700_000_100)

	// Test reply draft application
	replyArgsJSON, err := json.Marshal(replyForwardArgs{
		AccountID:       1,
		Mode:            "reply",
		SourceMessageID: msgID,
		To:              []string{"alice@example.com"},
		Subject:         "Re: Project Update",
		HTML:            "<p>Thanks Alice!</p>",
	})
	require.NoError(t, err)

	_, res, _, err := e.s.applyReplyOrForwardDraft(ctx, Approval{ID: 10, Tool: "reply_to_mail", Args: replyArgsJSON})
	require.NoError(t, err)
	m := res.(map[string]any)
	assert.True(t, m["created"].(bool))
	draftID := m["draft_id"].(int64)

	// Verify stored draft
	d, err := e.s.GetDraft(ctx, draftID)
	require.NoError(t, err)
	assert.Equal(t, "reply", d.Mode)
	assert.Equal(t, "Re: Project Update", d.Subject)
	assert.Contains(t, d.HTML, "Thanks Alice!")

	// Test forward draft application
	fwdArgsJSON, err := json.Marshal(replyForwardArgs{
		AccountID:       1,
		Mode:            "forward",
		SourceMessageID: msgID,
		To:              []string{"charlie@example.com"},
		Subject:         "Fwd: Project Update",
		HTML:            "<p>FYI</p>",
	})
	require.NoError(t, err)

	_, res, _, err = e.s.applyReplyOrForwardDraft(ctx, Approval{ID: 11, Tool: "forward_mail", Args: fwdArgsJSON})
	require.NoError(t, err)
	m = res.(map[string]any)
	assert.True(t, m["created"].(bool))
	fwdDraftID := m["draft_id"].(int64)

	d, err = e.s.GetDraft(ctx, fwdDraftID)
	require.NoError(t, err)
	assert.Equal(t, "forward", d.Mode)
	assert.Equal(t, "Fwd: Project Update", d.Subject)

	// Test delete draft
	delArgsJSON, err := json.Marshal(deleteDraftArgs{DraftID: draftID})
	require.NoError(t, err)
	_, res, _, err = e.s.applyDeleteDraft(ctx, Approval{ID: 12, Tool: "delete_draft", Args: delArgsJSON})
	require.NoError(t, err)
	m = res.(map[string]any)
	assert.True(t, m["deleted"].(bool))

	// Ensure deleted draft is gone
	_, err = e.s.GetDraft(ctx, draftID)
	assert.Error(t, err)
}

func TestWitLinksAndOpenBrowser(t *testing.T) {
	e := newSkillEnv(t)
	ctx := t.Context()

	// Setup OpenURL callback
	var openedURL atomic.Pointer[string]
	e.s.opts.OpenURL = func(url string) error {
		openedURL.Store(&url)
		return nil
	}

	// Test openLinkInBrowser
	openArgs, _ := json.Marshal(map[string]string{"url": "https://example.com/test"})
	res, err := e.s.openLinkInBrowser(ctx, openArgs)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"opened": true, "url": "https://example.com/test"}, res)
	require.NotNil(t, openedURL.Load())
	assert.Equal(t, "https://example.com/test", *openedURL.Load())

	// Test scheme validation
	badArgs, _ := json.Marshal(map[string]string{"url": "file:///etc/passwd"})
	_, err = e.s.openLinkInBrowser(ctx, badArgs)
	assert.Error(t, err)
}

func TestWitGetAllLinks(t *testing.T) {
	e := newSkillEnv(t)
	ctx := t.Context()

	// Insert message with HTML links
	htmlBody := `<p>Check out <a href="https://godwit.email">Godwit Mail</a> and <a href="https://example.com/privacy">Privacy Policy</a></p>`
	msgID := addMessage(t, e.s, "With Links", "sender@example.com", "Plain text", htmlBody, 1_700_000_100)

	// Directly query links
	rows, err := e.s.db.Query(ctx, "SELECT body_html, body_text FROM messages WHERE id = ?", msgID)
	require.NoError(t, err)
	require.Len(t, rows.Values, 1)

	runCtx := context.WithValue(ctx, runKey{}, &runJob{refs: map[int64]int{msgID: 1}})
	res, err := e.s.getAllLinks(runCtx, []byte(`{"id":1}`))
	require.NoError(t, err)
	m := res.(map[string]any)
	links := m["links"].([]mailLink)
	require.Len(t, links, 2)
	assert.Equal(t, "https://godwit.email", links[0].URL)
	assert.Equal(t, "Godwit Mail", links[0].Text)
	assert.Equal(t, "https://example.com/privacy", links[1].URL)
	assert.Equal(t, "Privacy Policy", links[1].Text)
}

func TestWitUnsubscribe(t *testing.T) {
	e := newSkillEnv(t)
	ctx := t.Context()

	// Mock server for one-click unsubscribe
	var receivedPost atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			receivedPost.Add(1)
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()
	e.s.opts.UnsubscribeClient = srv.Client()

	// Insert message with List-Unsubscribe headers
	res, err := e.s.db.Execute(ctx, `INSERT INTO messages(account_id, folder_id, subject, from_addr, date, body_text,
		list_unsubscribe, list_unsubscribe_post, list_id) VALUES (1, 1, 'Newsletter', 'news@example.com', 1700000000, 'Content', ?, 'List-Unsubscribe=One-Click', 'list.example.com')`,
		"<"+srv.URL+">")
	require.NoError(t, err)
	msgID := res.LastInsertID

	// Create conversation and job
	c, err := e.s.NewConversation(ctx, "ask", 0)
	require.NoError(t, err)
	runCtx := context.WithValue(ctx, runKey{}, &runJob{refs: map[int64]int{msgID: 1}})

	// Prepare unsubscribe
	prep, err := e.s.prepareUnsubscribe(runCtx, Conversation{ID: c.ID}, []byte(`{"id":1}`))
	require.NoError(t, err)
	assert.Contains(t, prep.Summary, "Unsubscribe from list.example.com")

	// Apply unsubscribe
	appr := Approval{ID: 20, Tool: "unsubscribe", Args: prep.Args}
	_, applyRes, _, err := e.s.applyUnsubscribe(ctx, appr)
	require.NoError(t, err)
	m := applyRes.(map[string]any)
	assert.True(t, m["unsubscribed"].(bool))
	assert.Equal(t, "one_click", m["method"])
	assert.Equal(t, 1, int(receivedPost.Load()))
}

func TestWitSearchMailWithLabelAndDates(t *testing.T) {
	e := newSkillEnv(t)
	ctx := t.Context()

	// Insert label "Newsletters"
	res, err := e.s.db.Execute(ctx, "INSERT INTO labels(slug, name, description, color, icon, position) VALUES ('newsletters', 'Newsletters', 'Newsletters', '#ff0000', 'tag', 1)")
	require.NoError(t, err)
	labelID := res.LastInsertID

	now := time.Now().Unix()
	// Insert messages: one recent with label, one older with label, one without label
	res1, err := e.s.db.Execute(ctx, `INSERT INTO messages(account_id, folder_id, subject, from_addr, date, body_text) VALUES (1, 1, 'Recent Newsletter', 'n1@x.com', ?, 'Hello 1')`, now-86400)
	require.NoError(t, err)
	res2, err := e.s.db.Execute(ctx, `INSERT INTO messages(account_id, folder_id, subject, from_addr, date, body_text) VALUES (1, 1, 'Old Newsletter', 'n2@x.com', ?, 'Hello 2')`, now-86400*20)
	require.NoError(t, err)
	_, err = e.s.db.Execute(ctx, `INSERT INTO messages(account_id, folder_id, subject, from_addr, date, body_text) VALUES (1, 1, 'Other Mail', 'o@x.com', ?, 'Hello 3')`, now-86400)
	require.NoError(t, err)

	// Attach labels
	_, err = e.s.db.Execute(ctx, `INSERT INTO message_labels(message_id, label_id, source, active) VALUES (?, ?, 'auto', 1)`, res1.LastInsertID, labelID)
	require.NoError(t, err)
	_, err = e.s.db.Execute(ctx, `INSERT INTO message_labels(message_id, label_id, source, active) VALUES (?, ?, 'auto', 1)`, res2.LastInsertID, labelID)
	require.NoError(t, err)

	// Rebuild search index
	require.NoError(t, e.s.RebuildSearchIndex(ctx))

	_, err = e.s.NewConversation(ctx, "ask", 0)
	require.NoError(t, err)
	runCtx := context.WithValue(ctx, runKey{}, &runJob{scope: &gateway.Scope{}, refs: map[int64]int{}})

	// 1. Search with inline label query: "label:Newsletters"
	resSearch1, err := e.s.searchMail(runCtx, []byte(`{"query":"label:Newsletters"}`))
	require.NoError(t, err)
	m1 := resSearch1.(map[string]any)
	results1 := m1["results"].([]map[string]any)
	assert.Len(t, results1, 2)

	// 2. Search with inline label and relative date: "label: Newsletters since:7d"
	resSearch2, err := e.s.searchMail(runCtx, []byte(`{"query":"label: Newsletters since:7d"}`))
	require.NoError(t, err)
	m2 := resSearch2.(map[string]any)
	results2 := m2["results"].([]map[string]any)
	assert.Len(t, results2, 1)

	// 3. Search with structured parameters: {"label": "Newsletters", "since": "7d"}
	resSearch3, err := e.s.searchMail(runCtx, []byte(`{"label":"Newsletters","since":"7d"}`))
	require.NoError(t, err)
	m3 := resSearch3.(map[string]any)
	results3 := m3["results"].([]map[string]any)
	assert.Len(t, results3, 1)
}
