package core

import (
	"context"
	"fmt"

	"github.com/godwit-email/godwit/core/gateway"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/store"
)

// MessageText is a message as stored: its text as a model reads it (modelBody)
// and metadata, with the attachments listed but never fetched.
type MessageText struct {
	MessageSummary
	AccountID   int64
	ReplyTo     string
	Text        string
	Truncated   bool
	Attachments []AttachmentInfo
}

// GetMessageText reads the message's text and metadata from the database: the
// text a model reads, without what the reader never shows. Unlike GetMessage it
// sanitizes no HTML and reads or downloads no attachment or inline image.
func (s *Service) GetMessageText(ctx context.Context, id int64) (MessageText, error) {
	rows, err := s.db.Query(ctx, "SELECT "+msgCols+", m.account_id, m.reply_to, m.body_text, m.body_truncated, m.body_html FROM messages m WHERE m.id = ?", id)
	if err != nil {
		return MessageText{}, err
	}
	if len(rows.Values) == 0 {
		return MessageText{}, fmt.Errorf("message %d not found", id)
	}
	r := rows.Values[0]
	m := MessageText{MessageSummary: scanSummary(r), AccountID: store.Int(r[11]), ReplyTo: store.Str(r[12]),
		Truncated: store.Int(r[14]) != 0}
	body := Message{Text: store.Str(r[13])}
	if raw := store.Str(r[15]); raw != "" {
		body.HTML, _ = mail.Sanitize(raw, nil, false) // as mailForModel: the HTML the reader shows
	}
	m.Text = modelBody(body)
	ar, err := s.db.Query(ctx, "SELECT id, filename, content_type, size, inline FROM attachments WHERE message_id = ? ORDER BY id", id)
	if err != nil {
		return MessageText{}, err
	}
	for _, a := range ar.Values {
		m.Attachments = append(m.Attachments, AttachmentInfo{ID: store.Int(a[0]), Filename: store.Str(a[1]),
			ContentType: store.Str(a[2]), Size: int(store.Int(a[3])), Inline: store.Int(a[4]) != 0})
	}
	return m, nil
}

// External audit sources: the Local API's full role, and its mcp role (the
// godwit mcp bridge; "local_mcp", unlike the "mcp" of MCP-server tools).
const (
	AuditLocalAPI = "local_api"
	AuditMCP      = "local_mcp"
)

// AuditExternal writes an audit_log row for a call made from outside the
// app: source (AuditLocalAPI or AuditMCP), the method as action, and the
// target's ID (0 = none). No content is stored.
func (s *Service) AuditExternal(ctx context.Context, source, action string, target int64) error {
	if source != AuditLocalAPI && source != AuditMCP {
		return fmt.Errorf("audit: unknown external source %q", source)
	}
	return s.gateway().Record(ctx, gateway.Entry{Source: source, Tool: action, TargetID: target, Decision: "allow"})
}
