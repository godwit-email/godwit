package core

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func msgIDs(ms []MessageSummary) []int64 {
	var out []int64
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}

func addConversation(t *testing.T, s *Service, kind, title string, updated int64) int64 {
	t.Helper()
	r, err := s.db.Execute(t.Context(), "INSERT INTO conversations(kind, title, model, created_at, updated_at) VALUES (?, ?, '', ?, ?)", kind, title, updated, updated)
	require.NoError(t, err)
	return r.LastInsertID
}

func convIDs(cs []Conversation) []int64 {
	var out []int64
	for _, c := range cs {
		out = append(out, c.ID)
	}
	return out
}

func TestFindConversationsByTitleRecentlyUpdatedFirst(t *testing.T) {
	s := newMailContextService(t)
	old := addConversation(t, s, "ask", "Budget questions", 100)
	recent := addConversation(t, s, "ask", "Q4 budget", 300)
	addConversation(t, s, "ask", "Lunch plans", 400)
	addConversation(t, s, "assist", "budget reply", 500) // a draft's panel, not the palette's
	addMessage(t, s, "notes", "Bob <bob@x>", "the budget is in the body only", "", 200)

	got, err := s.Find(t.Context(), FindQuery{SearchQuery: SearchQuery{Text: "budget"}})
	require.NoError(t, err)
	require.Equal(t, []int64{recent, old}, convIDs(got.Conversations.Items))
	require.Equal(t, 2, got.Conversations.Total)

	got, err = s.Find(t.Context(), FindQuery{SearchQuery: SearchQuery{Text: "budget"}, Limit: 1})
	require.NoError(t, err)
	require.Equal(t, []int64{recent}, convIDs(got.Conversations.Items))
	require.Equal(t, 2, got.Conversations.Total)
}

func TestFindContactsAreSendersByAddressLatestFirst(t *testing.T) {
	s := newMailContextService(t)
	addMessage(t, s, "a", "Bob <bob@x>", "", "", 100)
	robert := addMessage(t, s, "b", "Robert B <BOB@x>", "", "", 300)
	addMessage(t, s, "c", "bob@x", "", "", 200)
	bobby := addMessage(t, s, "d", "Bobby <bobby@y>", "", "", 400)
	addMessage(t, s, "e", "Alice <alice@x>", "", "", 500)
	addMessage(t, s, "f", "Me <me@x>", "", "", 600) // the account's own, sent

	got, err := s.Find(t.Context(), FindQuery{SearchQuery: SearchQuery{Text: "bob"}})
	require.NoError(t, err)
	require.Equal(t, []Contact{
		{Name: "Bobby", Address: "bobby@y", Messages: 1, Last: time.Unix(400, 0), Received: 1, Unread: 1, Inbox: 1, Latest: bobby},
		{Name: "Robert B", Address: "bob@x", Messages: 3, Last: time.Unix(300, 0), Received: 3, Unread: 3, Inbox: 3, Latest: robert},
	}, got.Contacts.Items)
	require.Equal(t, 2, got.Contacts.Total)

	got, err = s.Find(t.Context(), FindQuery{SearchQuery: SearchQuery{Text: "robert"}})
	require.NoError(t, err)
	require.Equal(t, []Contact{{Name: "Robert B", Address: "bob@x", Messages: 3, Last: time.Unix(300, 0), Received: 3, Unread: 3, Inbox: 3, Latest: robert}}, got.Contacts.Items, "any name it used")

	got, err = s.Find(t.Context(), FindQuery{SearchQuery: SearchQuery{Text: "me@"}})
	require.NoError(t, err)
	require.Empty(t, got.Contacts.Items, "not the user")
}

func addAttachment(t *testing.T, s *Service, messageID int64, filename string, inline bool) int64 {
	t.Helper()
	r, err := s.db.Execute(t.Context(), "INSERT INTO attachments(message_id, filename, content_type, size, inline) VALUES (?, ?, 'application/pdf', 10, ?)", messageID, filename, inline)
	require.NoError(t, err)
	return r.LastInsertID
}

func TestFindAttachmentsByFilenameWithTheirMessage(t *testing.T) {
	s := newMailContextService(t)
	old := addMessage(t, s, "Invoices", "Bob <bob@x>", "", "", 100)
	recent := addMessage(t, s, "More", "Carol <carol@x>", "", "", 200)
	a := addAttachment(t, s, old, "invoice-march.pdf", false)
	b := addAttachment(t, s, recent, "Invoice-April.pdf", false)
	addAttachment(t, s, recent, "invoice-logo.png", true) // inline: part of the body
	addAttachment(t, s, recent, "notes.txt", false)

	got, err := s.Find(t.Context(), FindQuery{SearchQuery: SearchQuery{Text: "invoice"}, Limit: 1})
	require.NoError(t, err)
	require.Equal(t, []FoundAttachment{{
		AttachmentInfo: AttachmentInfo{ID: b, Filename: "Invoice-April.pdf", ContentType: "application/pdf", Size: 10},
		MessageID:      recent, Subject: "More", From: "Carol <carol@x>", Date: time.Unix(200, 0),
	}}, got.Attachments.Items)
	require.Equal(t, 2, got.Attachments.Total)

	got, err = s.Find(t.Context(), FindQuery{SearchQuery: SearchQuery{Text: "march"}})
	require.NoError(t, err)
	require.Len(t, got.Attachments.Items, 1)
	require.Equal(t, a, got.Attachments.Items[0].ID)
}

func TestFindFiltersOnlyMailAndAttachments(t *testing.T) {
	s := newMailContextService(t)
	bob := addMessage(t, s, "report", "Bob <bob@x>", "", "", 100)
	carol := addMessage(t, s, "report", "Carol <carol@x>", "", "", 200)
	fromBob := addAttachment(t, s, bob, "report.pdf", false)
	addAttachment(t, s, carol, "report.pdf", false)
	addConversation(t, s, "ask", "report draft", 300)

	got, err := s.Find(t.Context(), FindQuery{SearchQuery: SearchQuery{Text: "report", From: "bob"}})
	require.NoError(t, err)
	require.Equal(t, []int64{bob}, msgIDs(got.Mail.Items))
	require.Len(t, got.Attachments.Items, 1)
	require.Equal(t, fromBob, got.Attachments.Items[0].ID)
	require.Equal(t, FindGroup[Conversation]{}, got.Conversations)
	require.Equal(t, FindGroup[Contact]{}, got.Contacts)

	got, err = s.Find(t.Context(), FindQuery{SearchQuery: SearchQuery{From: "bob"}})
	require.NoError(t, err)
	require.Equal(t, []int64{bob}, msgIDs(got.Mail.Items), "a filter needs no text")
	require.Equal(t, 1, got.Attachments.Total)
}

func TestFindKindFindsOnlyThatGroup(t *testing.T) {
	s := newMailContextService(t)
	m := addMessage(t, s, "report", "Reporter <rep@x>", "", "", 100)
	addAttachment(t, s, m, "report.pdf", false)
	addConversation(t, s, "ask", "report draft", 300)
	q := SearchQuery{Text: "rep"}

	got, err := s.Find(t.Context(), FindQuery{SearchQuery: q, Kind: "contact"})
	require.NoError(t, err)
	require.Equal(t, FindResult{Contacts: FindGroup[Contact]{Items: []Contact{{Name: "Reporter", Address: "rep@x", Messages: 1, Last: time.Unix(100, 0), Received: 1, Unread: 1, Inbox: 1, Latest: m}}, Total: 1}}, got)

	got, err = s.Find(t.Context(), FindQuery{SearchQuery: q, Kind: "mail"})
	require.NoError(t, err)
	require.Equal(t, 1, got.Mail.Total)
	require.Zero(t, got.Conversations.Total+got.Contacts.Total+got.Attachments.Total)

	got, err = s.Find(t.Context(), FindQuery{SearchQuery: q, Kind: "conversation"})
	require.NoError(t, err)
	require.Equal(t, 1, got.Conversations.Total)
	require.Zero(t, got.Mail.Total+got.Contacts.Total+got.Attachments.Total)

	got, err = s.Find(t.Context(), FindQuery{SearchQuery: q, Kind: "attachment"})
	require.NoError(t, err)
	require.Equal(t, 1, got.Attachments.Total)
	require.Zero(t, got.Mail.Total+got.Contacts.Total+got.Conversations.Total)

	got, err = s.Find(t.Context(), FindQuery{SearchQuery: SearchQuery{Text: "rep", From: "rep"}, Kind: "contact"})
	require.NoError(t, err)
	require.Equal(t, FindResult{}, got, "filters leave contacts out")

	_, err = s.Find(t.Context(), FindQuery{SearchQuery: q, Kind: "thread"})
	require.Error(t, err)
}

func TestFindNothingShowsWhatIsRecent(t *testing.T) {
	s := newMailContextService(t)
	a := addMessage(t, s, "one", "Bob <bob@x>", "", "", 100)
	b := addMessage(t, s, "two", "Bob <bob@x>", "", "", 200)
	c := addMessage(t, s, "three", "Carol <carol@x>", "", "", 300)
	att := addAttachment(t, s, a, "old.pdf", false)
	newer := addAttachment(t, s, c, "new.pdf", false)
	addAttachment(t, s, c, "logo.png", true)
	conv := addConversation(t, s, "ask", "", 50) // untitled yet
	later := addConversation(t, s, "ask", "Plans", 60)

	got, err := s.Find(t.Context(), FindQuery{SearchQuery: SearchQuery{Text: " "}})
	require.NoError(t, err)
	require.Equal(t, []int64{c, b, a}, msgIDs(got.Mail.Items))
	require.Equal(t, []int64{later, conv}, convIDs(got.Conversations.Items))
	require.Equal(t, []Contact{
		{Name: "Bob", Address: "bob@x", Messages: 2, Last: time.Unix(200, 0), Received: 2, Unread: 2, Inbox: 2, Latest: b},
		{Name: "Carol", Address: "carol@x", Messages: 1, Last: time.Unix(300, 0), Received: 1, Unread: 1, Inbox: 1, Latest: c},
	}, got.Contacts.Items, "most mail first")
	require.Len(t, got.Attachments.Items, 2)
	require.Equal(t, []int64{newer, att}, []int64{got.Attachments.Items[0].ID, got.Attachments.Items[1].ID})
}

func TestFindMailCountsEveryHitAndShowsTheNewest(t *testing.T) {
	s := newMailContextService(t)
	a := addMessage(t, s, "budget plan", "Bob <bob@x>", "", "", 100)
	b := addMessage(t, s, "budget review", "Bob <bob@x>", "", "", 300)
	c := addMessage(t, s, "budget final", "Bob <bob@x>", "", "", 200)
	addMessage(t, s, "lunch", "Bob <bob@x>", "", "", 400)

	got, err := s.Find(t.Context(), FindQuery{SearchQuery: SearchQuery{Text: "budget"}, Limit: 2})
	require.NoError(t, err)
	require.Equal(t, []int64{b, c}, msgIDs(got.Mail.Items))
	require.Equal(t, 3, got.Mail.Total)

	got, err = s.Find(t.Context(), FindQuery{SearchQuery: SearchQuery{Text: "budget"}})
	require.NoError(t, err)
	require.Equal(t, []int64{b, c, a}, msgIDs(got.Mail.Items))
}
