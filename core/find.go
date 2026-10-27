package core

import (
	"cmp"
	"context"
	"fmt"
	"net/mail"
	"slices"
	"strings"
	"time"

	gomail "github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/store"
)

// FindQuery is a search across kinds: Kind "" finds every kind, or one of
// "mail", "conversation", "contact" and "attachment". Limit caps each group
// (0: the default). The filters apply to mail and attachments; with one
// set, the other groups find nothing.
type FindQuery struct {
	SearchQuery
	Kind  string
	Limit int
}

// FindGroup is the first hits of a kind and how many there are in all.
type FindGroup[T any] struct {
	Items []T
	Total int
}

// FindResult is Find's hits by kind.
type FindResult struct {
	Mail          FindGroup[MessageSummary]
	Conversations FindGroup[Conversation] // Wit's (kind "ask"), as ListConversations lists them
	Contacts      FindGroup[Contact]
	Attachments   FindGroup[FoundAttachment]
}

// FoundAttachment is an attachment and the message it came with.
type FoundAttachment struct {
	AttachmentInfo
	MessageID     int64
	Subject, From string
	Date          time.Time
}

// Contact is a sender of synced mail, by address: the latest name it used,
// how many messages it sent and when the latest came.
type Contact struct {
	Name, Address string
	Messages      int
	Last          time.Time
}

// Find searches mail, Wit conversations, contacts and attachments at once,
// each group newest first. With no text and no filter each group is what is
// recent, contacts those who sent the most mail.
func (s *Service) Find(ctx context.Context, q FindQuery) (FindResult, error) {
	var res FindResult
	if !slices.Contains([]string{"", "mail", "conversation", "contact", "attachment"}, q.Kind) {
		return res, fmt.Errorf("unknown kind %q", q.Kind)
	}
	want := func(kind string, filterable bool) bool {
		return (q.Kind == "" || q.Kind == kind) && (filterable || !q.Filtered())
	}
	var err error
	if want("mail", true) {
		if res.Mail, err = s.findMail(ctx, q); err != nil {
			return res, err
		}
	}
	if want("conversation", false) {
		if res.Conversations, err = s.findConversations(ctx, q); err != nil {
			return res, err
		}
	}
	if want("contact", false) {
		if res.Contacts, err = s.findContacts(ctx, q); err != nil {
			return res, err
		}
	}
	if want("attachment", true) {
		if res.Attachments, err = s.findAttachments(ctx, q); err != nil {
			return res, err
		}
	}
	return res, nil
}

// firstOf is a group of all, its first limit shown.
func firstOf[T any](all []T, limit int) FindGroup[T] {
	return FindGroup[T]{Items: all[:min(len(all), clampLimit(limit))], Total: len(all)}
}

func (s *Service) findMail(ctx context.Context, q FindQuery) (FindGroup[MessageSummary], error) {
	cond, args := searchText(q.Text)
	fcond, fargs := searchFilter(q.SearchQuery)
	cond, args = cond+" AND "+fcond, append(args, fargs...)
	rows, err := s.db.Query(ctx, "SELECT count(*) FROM messages m WHERE "+cond, args...)
	if err != nil {
		return FindGroup[MessageSummary]{}, err
	}
	items, err := s.searchMessages(ctx, cond, args, q.Limit)
	return FindGroup[MessageSummary]{Items: items, Total: int(store.Int(rows.Values[0][0]))}, err
}

// findAttachments matches the filenames of attachments that are not part of
// a body; the filters apply to their messages.
func (s *Service) findAttachments(ctx context.Context, q FindQuery) (FindGroup[FoundAttachment], error) {
	fcond, fargs := searchFilter(q.SearchQuery)
	cond := `a.inline = 0 AND a.filename LIKE ? ESCAPE '\' AND ` + fcond
	args := append([]any{likeArg(strings.TrimSpace(q.Text))}, fargs...)
	from := " FROM attachments a JOIN messages m ON m.id = a.message_id WHERE " + cond
	n, err := s.db.Query(ctx, "SELECT count(*)"+from, args...)
	if err != nil {
		return FindGroup[FoundAttachment]{}, err
	}
	rows, err := s.db.Query(ctx, "SELECT a.id, a.filename, a.content_type, a.size, m.id, m.subject, m.from_addr, m.date"+from+
		" ORDER BY m.date DESC, a.id DESC LIMIT ?", append(args, clampLimit(q.Limit))...)
	if err != nil {
		return FindGroup[FoundAttachment]{}, err
	}
	g := FindGroup[FoundAttachment]{Items: make([]FoundAttachment, 0, len(rows.Values)), Total: int(store.Int(n.Values[0][0]))}
	for _, r := range rows.Values {
		g.Items = append(g.Items, FoundAttachment{
			AttachmentInfo: AttachmentInfo{ID: store.Int(r[0]), Filename: store.Str(r[1]), ContentType: store.Str(r[2]), Size: int(store.Int(r[3]))},
			MessageID:      store.Int(r[4]), Subject: store.Str(r[5]), From: store.Str(r[6]), Date: time.Unix(store.Int(r[7]), 0),
		})
	}
	return g, nil
}

// findContacts matches the senders of synced mail by any name they used or
// their address; the user's own accounts are not contacts.
func (s *Service) findContacts(ctx context.Context, q FindQuery) (FindGroup[Contact], error) {
	own, err := s.db.Query(ctx, "SELECT email FROM accounts")
	if err != nil {
		return FindGroup[Contact]{}, err
	}
	skip := map[string]bool{}
	for _, r := range own.Values {
		skip[gomail.AddrKey(store.Str(r[0]))] = true
	}
	// shortcut: groups every sender in Go on each keystroke; keep a senders table if large stores get slow.
	fcond, args := searchFilter(q.SearchQuery)
	rows, err := s.db.Query(ctx, "SELECT m.from_addr, count(*), max(m.date) FROM messages m WHERE "+fcond+" GROUP BY m.from_addr ORDER BY 3, 1", args...)
	if err != nil {
		return FindGroup[Contact]{}, err
	}
	text := strings.ToLower(strings.TrimSpace(q.Text))
	byAddr, hit := map[string]*Contact{}, map[string]bool{}
	for _, r := range rows.Values {
		from := store.Str(r[0])
		addr := gomail.AddrKey(from)
		if addr == "" || skip[addr] {
			continue
		}
		var name string
		if a, err := mail.ParseAddress(from); err == nil {
			name = a.Name
		}
		c := byAddr[addr]
		if c == nil {
			c = &Contact{Address: addr}
			byAddr[addr] = c
		}
		if name != "" {
			c.Name = name // rows come oldest first: the latest name wins
		}
		c.Messages += int(store.Int(r[1]))
		c.Last = time.Unix(store.Int(r[2]), 0)
		hit[addr] = hit[addr] || strings.Contains(addr, text) || strings.Contains(strings.ToLower(name), text)
	}
	all := make([]Contact, 0, len(hit))
	for addr, ok := range hit {
		if ok {
			all = append(all, *byAddr[addr])
		}
	}
	slices.SortFunc(all, func(a, b Contact) int {
		if text == "" { // nothing typed: the people mailed most
			return cmp.Or(cmp.Compare(b.Messages, a.Messages), b.Last.Compare(a.Last), strings.Compare(a.Address, b.Address))
		}
		return cmp.Or(b.Last.Compare(a.Last), strings.Compare(a.Address, b.Address))
	})
	return firstOf(all, q.Limit), nil
}

// findConversations matches titles only, not what was said.
func (s *Service) findConversations(ctx context.Context, q FindQuery) (FindGroup[Conversation], error) {
	rows, err := s.db.Query(ctx, "SELECT "+convCols+` WHERE kind = 'ask' AND title LIKE ? ESCAPE '\' ORDER BY updated_at DESC, id DESC`,
		likeArg(strings.TrimSpace(q.Text)))
	if err != nil {
		return FindGroup[Conversation]{}, err
	}
	all := make([]Conversation, 0, len(rows.Values))
	for _, r := range rows.Values {
		all = append(all, scanConversation(r))
	}
	return firstOf(all, q.Limit), nil
}
