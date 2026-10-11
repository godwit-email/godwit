package core

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/gateway"
	gomail "github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/store"
)

// mailItem is one message as handed to a model: every mail-derived field sits
// inside Block's untrusted_email block. Subject and From are for display.
type mailItem struct {
	MessageID     int64
	Subject, From string
	Date          time.Time
	Suspect       bool
	Quote         string // where the suspect text starts (ai.InjectionQuote)
	Block         string
}

// mailBudget caps body runes per message and across all messages.
type mailBudget struct{ PerMessage, Total int }

var defaultMailBudget = mailBudget{PerMessage: 24000, Total: 48000}

const truncatedNote = "\n[truncated]"

// mailForModel reads a message's text and attachment metadata from the
// database only: no IMAP, no attachment bytes. HTML is the sanitized body
// without inline images; callers use it only through HTMLToText.
func (s *Service) mailForModel(ctx context.Context, id int64) (Message, error) {
	rows, err := s.db.Query(ctx, "SELECT "+msgCols+", m.body_text, m.body_html, m.body_truncated FROM messages m WHERE m.id = ?", id)
	if err != nil {
		return Message{}, err
	}
	if len(rows.Values) == 0 {
		return Message{}, fmt.Errorf("message %d not found", id)
	}
	r := rows.Values[0]
	m := Message{MessageSummary: scanSummary(r), Text: store.Str(r[11]), Truncated: store.Int(r[13]) != 0}
	if raw := store.Str(r[12]); raw != "" {
		m.HTML, _ = gomail.Sanitize(raw, nil, false)
	}
	ar, err := s.db.Query(ctx, "SELECT filename, content_type, size FROM attachments WHERE message_id = ? ORDER BY id", id)
	if err != nil {
		return Message{}, err
	}
	for _, a := range ar.Values {
		m.Attachments = append(m.Attachments, AttachmentInfo{Filename: store.Str(a[0]), ContentType: store.Str(a[1]), Size: int(store.Int(a[2]))})
	}
	return m, nil
}

// mailContext loads ids for a model, in the order given. Bodies are plain
// text (never HTML) cut to budget: the newest message is kept whole (up to
// PerMessage), older ones are cut or omitted, oldest first. Calling it marks
// scope as having read mail.
func (s *Service) mailContext(ctx context.Context, scope *gateway.Scope, ids []int64, budget mailBudget) ([]mailItem, error) {
	scope.MarkReadMail()
	msgs := make([]Message, len(ids))
	for i, id := range ids {
		m, err := s.mailForModel(ctx, id)
		if err != nil {
			return nil, err
		}
		msgs[i] = m
	}
	order := make([]int, len(ids))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { // newest first
		if c := msgs[b].Date.Compare(msgs[a].Date); c != 0 {
			return c
		}
		return int(msgs[b].ID - msgs[a].ID)
	})
	out := make([]mailItem, len(ids))
	left := budget.Total
	for _, i := range order {
		m := msgs[i]
		body := m.Text
		if strings.TrimSpace(body) == "" && m.HTML != "" {
			body = gomail.HTMLToText(m.HTML)
		}
		var att []string
		for _, a := range m.Attachments {
			att = append(att, fmt.Sprintf("%s (%s, %d bytes)", a.Filename, a.ContentType, a.Size))
		}
		head := mailHeader(m.MessageSummary)
		if len(att) > 0 {
			head += "Attachments: " + strings.Join(att, "; ") + "\n"
		}
		quote := ai.InjectionQuote(head + body)
		suspect := quote != ""
		cut := m.Truncated
		if n := min(budget.PerMessage, left); utf8.RuneCountInString(body) > n {
			body, cut = string([]rune(body)[:max(n, 0)]), true
		}
		left -= utf8.RuneCountInString(body)
		block := ai.Untrusted(fmt.Sprintf("message:%d", m.ID), head+"\n"+body, suspect)
		if cut {
			block += truncatedNote
		}
		out[i] = mailItem{MessageID: m.ID, Subject: m.Subject, From: m.From, Date: m.Date, Suspect: suspect, Quote: quote, Block: block}
	}
	return out, nil
}

// selectionContext wraps text selected in message id like a message body:
// same block, PerMessage budget and suspect check. It marks scope as having read mail.
func (s *Service) selectionContext(scope *gateway.Scope, id int64, text string, budget mailBudget) mailItem {
	scope.MarkReadMail()
	quote := ai.InjectionQuote(text)
	suspect := quote != ""
	cut := utf8.RuneCountInString(text) > budget.PerMessage
	if cut {
		text = string([]rune(text)[:budget.PerMessage])
	}
	block := ai.Untrusted(fmt.Sprintf("selection:%d", id), text, suspect)
	if cut {
		block += truncatedNote
	}
	return mailItem{MessageID: id, Suspect: suspect, Quote: quote, Block: block}
}

// summaryContext wraps search results (subject, from, date, snippet) the same
// way and marks scope as having read mail.
func (s *Service) summaryContext(scope *gateway.Scope, list []MessageSummary) []mailItem {
	scope.MarkReadMail()
	out := make([]mailItem, 0, len(list))
	for _, m := range list {
		text := mailHeader(m) + "\n" + m.Snippet
		quote := ai.InjectionQuote(text)
		suspect := quote != ""
		out = append(out, mailItem{MessageID: m.ID, Subject: m.Subject, From: m.From, Date: m.Date,
			Suspect: suspect, Quote: quote, Block: ai.Untrusted(fmt.Sprintf("message:%d", m.ID), text, suspect)})
	}
	return out
}

// subjectContext wraps subject lines only (language clues) the same way and
// marks scope as having read mail.
func (s *Service) subjectContext(scope *gateway.Scope, list []MessageSummary) []mailItem {
	scope.MarkReadMail()
	out := make([]mailItem, 0, len(list))
	for _, m := range list {
		quote := ai.InjectionQuote(m.Subject)
		suspect := quote != ""
		out = append(out, mailItem{MessageID: m.ID, Subject: m.Subject, From: m.From, Date: m.Date,
			Suspect: suspect, Quote: quote, Block: ai.Untrusted(fmt.Sprintf("message:%d", m.ID), "Subject: "+m.Subject, suspect)})
	}
	return out
}

func mailHeader(m MessageSummary) string {
	return "Subject: " + m.Subject + "\nFrom: " + m.From + "\nDate: " + m.Date.UTC().Format(time.RFC3339) + "\n"
}
