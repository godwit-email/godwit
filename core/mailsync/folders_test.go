package mailsync

import (
	"slices"
	"testing"

	"github.com/emersion/go-imap/v2"
)

// A role marked SPECIAL-USE beats the same role guessed from a name.
func TestRoles(t *testing.T) {
	list := []*imap.ListData{
		{Mailbox: "INBOX", Delim: '/'},
		{Mailbox: "Sent Messages", Delim: '/'},
		{Mailbox: "[Gmail]/已发邮件", Delim: '/', Attrs: []imap.MailboxAttr{imap.MailboxAttrSent}},
		{Mailbox: "[Gmail]/已加星标", Delim: '/', Attrs: []imap.MailboxAttr{imap.MailboxAttrFlagged}},
		{Mailbox: "[Gmail]/重要", Delim: '/', Attrs: []imap.MailboxAttr{imap.MailboxAttrImportant}},
		{Mailbox: "Trash", Delim: '/'},
	}
	want := []string{"inbox", "", "sent", "flagged", "", "trash"}
	if got := roles(list); !slices.Equal(got, want) {
		t.Fatalf("roles %q, want %q", got, want)
	}
	list[2].Attrs = append(list[2].Attrs, imap.MailboxAttrNoSelect) // the marked one unusable: the guess stands
	want = []string{"inbox", "sent", "sent", "flagged", "", "trash"}
	if got := roles(list); !slices.Equal(got, want) {
		t.Fatalf("roles %q, want %q", got, want)
	}
}
