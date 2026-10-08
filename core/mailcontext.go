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
)

// mailItem is one message as handed to a model: every mail-derived field sits
// inside Block's untrusted_email block. Subject and From are for display.
type mailItem struct {
	MessageID     int64
	Subject, From string
	Suspect       bool
	Block         string
}

// mailBudget caps body runes per message and across all messages.
type mailBudget struct{ PerMessage, Total int }

var defaultMailBudget = mailBudget{PerMessage: 24000, Total: 48000}

const truncatedNote = "\n[truncated]"

// mailContext loads ids for a model, in the order given. Bodies are plain
// text (never HTML) cut to budget: the newest message is kept whole (up to
// PerMessage), older ones are cut or omitted, oldest first. Calling it marks
// scope as having read mail.
func (s *Service) mailContext(ctx context.Context, scope *gateway.Scope, ids []int64, budget mailBudget) ([]mailItem, error) {
	scope.MarkReadMail()
	msgs := make([]Message, len(ids))
	for i, id := range ids {
		m, err := s.GetMessage(ctx, id, false)
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
		suspect := ai.SuspectInjection(head + body)
		cut := m.Truncated
		if n := min(budget.PerMessage, left); utf8.RuneCountInString(body) > n {
			body, cut = string([]rune(body)[:max(n, 0)]), true
		}
		left -= utf8.RuneCountInString(body)
		block := ai.Untrusted(fmt.Sprintf("message:%d", m.ID), head+"\n"+body)
		if cut {
			block += truncatedNote
		}
		out[i] = mailItem{MessageID: m.ID, Subject: m.Subject, From: m.From, Suspect: suspect, Block: block}
	}
	return out, nil
}

// summaryContext wraps search results (subject, from, date, snippet) the same
// way and marks scope as having read mail.
func (s *Service) summaryContext(scope *gateway.Scope, list []MessageSummary) []mailItem {
	scope.MarkReadMail()
	out := make([]mailItem, 0, len(list))
	for _, m := range list {
		text := mailHeader(m) + "\n" + m.Snippet
		out = append(out, mailItem{MessageID: m.ID, Subject: m.Subject, From: m.From,
			Suspect: ai.SuspectInjection(text), Block: ai.Untrusted(fmt.Sprintf("message:%d", m.ID), text)})
	}
	return out
}

func mailHeader(m MessageSummary) string {
	return "Subject: " + m.Subject + "\nFrom: " + m.From + "\nDate: " + m.Date.UTC().Format(time.RFC3339) + "\n"
}
