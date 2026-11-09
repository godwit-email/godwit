package testmail

import (
	"sync"
	"sync/atomic"
	"testing"

	gojmap "git.sr.ht/~rockorager/go-jmap"
	"git.sr.ht/~rockorager/go-jmap/jmapmemserver"
	"git.sr.ht/~rockorager/go-jmap/mail/mailbox"
)

// JMAP is a running in-memory JMAP server with one account, alice/secret,
// that starts with an inbox. Its hooks (ExpireState, DropEventSources,
// MergeEmails, RejectNextRequest, Redirect, SeparateDownloadHost) inject
// faults.
type JMAP struct {
	*jmapmemserver.Server
	Inbox    gojmap.ID
	rejected atomic.Bool
	boxes    sync.Map // name -> gojmap.ID of mailboxes Mailbox created
}

// StartJMAP starts a JMAP server, closed when t ends.
func StartJMAP(t testing.TB) *JMAP {
	s := &JMAP{Server: jmapmemserver.New("alice", "secret")}
	t.Cleanup(s.Close)
	s.Inbox = s.CreateMailbox("Inbox", mailbox.RoleInbox)
	return s
}

// SessionURL is where the session resource is.
func (s *JMAP) SessionURL() string { return s.URL() + "/jmap/session" }

// Password is the password a client should use: "secret", or a wrong one
// after RejectAuth.
func (s *JMAP) Password() string {
	if s.rejected.Load() {
		return "wrong"
	}
	return "secret"
}

// RejectAuth makes Password wrong from now on, so the server refuses it.
func (s *JMAP) RejectAuth() { s.rejected.Store(true) }

// Mailbox is the id of the mailbox named name, created if need be; "INBOX"
// is the inbox.
func (s *JMAP) Mailbox(name string) gojmap.ID {
	if name == "INBOX" {
		return s.Inbox
	}
	if id, ok := s.boxes.Load(name); ok {
		return id.(gojmap.ID)
	}
	id := s.CreateMailbox(name, "")
	s.boxes.Store(name, id)
	return id
}

// Deliver puts raw into the mailbox named folder, as mail arriving.
func (s *JMAP) Deliver(folder string, raw []byte) gojmap.ID {
	return s.AddEmail([]gojmap.ID{s.Mailbox(folder)}, raw)
}
