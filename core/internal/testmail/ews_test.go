package testmail_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/backendtest"
	"github.com/godwit-email/godwit/backend/ews"
	"github.com/godwit-email/godwit/core/internal/testmail"
)

func ewsReceiver(t *testing.T, s *testmail.EWS) *ews.Receiver {
	r := ews.New(ews.Config{URL: s.URL(), Credentials: func(context.Context) (backend.Credentials, error) {
		return backend.Credentials{Username: "alice", Password: s.Password()}, nil
	}})
	t.Cleanup(func() { r.Close() })
	return r
}

type ewsServer struct{ *testmail.EWS }

func (s ewsServer) Receiver(t *testing.T) backend.Receiver          { return ewsReceiver(t, s.EWS) }
func (s ewsServer) Deliver(_ *testing.T, folder string, raw []byte) { s.EWS.Deliver(folder, raw) }
func (s ewsServer) Reset(t *testing.T, _ string)                    { t.Skip("EWS: no SyncState reset yet") }
func (s ewsServer) RejectAuth(*testing.T)                           { s.EWS.RejectAuth() }

// shortcut: only the parts EWS implements so far; Run the whole contract once
// incremental sync, write-back and push are in.
func TestEWSBackendContract(t *testing.T) {
	backendtest.RunOnly(t, func(t *testing.T) backendtest.Server { return ewsServer{testmail.StartEWS(t)} },
		"Describe", "Check", "FirstSync", "CursorResumes", "AuthFailed")
}

// syncAll runs one round from nothing and returns its changes.
func syncAll(t *testing.T, r backend.Receiver) []backend.Change {
	t.Helper()
	var cs []backend.Change
	_, err := r.Sync(tctx(t), backend.SyncRequest{HeaderFields: []string{"Subject"}}, func(p backend.Page) error {
		cs = append(cs, p.Changes...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

// Folders take their roles from the server's distinguished folders; a
// subfolder is named by its path; calendars and the like are not mail.
func TestEWSFolders(t *testing.T) {
	s := testmail.StartEWS(t)
	s.Deliver("Inbox/Projects", backendtest.Message("a@example.com", "a"))
	got := map[string]backend.Folder{}
	for _, c := range syncAll(t, ewsReceiver(t, s)) {
		if f, ok := c.(backend.FolderUpdated); ok {
			got[f.Name] = f.Folder
		}
	}
	for name, role := range map[string]backend.Role{"Inbox": backend.RoleInbox, "Sent Items": backend.RoleSent, "Drafts": backend.RoleDrafts,
		"Deleted Items": backend.RoleTrash, "Junk Email": backend.RoleJunk, "Archive": backend.RoleArchive, "Inbox/Projects": ""} {
		if f, ok := got[name]; !ok || f.Role != role {
			t.Errorf("folder %q = %+v (found %v), want role %q", name, f, ok, role)
		}
	}
	if p := got["Inbox/Projects"]; p.Parent != got["Inbox"].Ref || p.Delim != "/" {
		t.Errorf("subfolder %+v, want the inbox as its parent", p)
	}
	if _, ok := got["Calendar"]; ok {
		t.Error("the calendar is listed as a mail folder")
	}
}

// A server older than Exchange 2016 fails the add-account check with its reason.
func TestEWSServerTooOld(t *testing.T) {
	s := testmail.StartEWS(t)
	s.SetVersion(15, 0) // Exchange 2013
	_, err := ewsReceiver(t, s).Check(tctx(t))
	if !errors.Is(err, ews.ErrServerTooOld) || err.Error() != ews.ReasonServerTooOld {
		t.Errorf("Check on Exchange 2013 = %v, want ErrServerTooOld", err)
	}
	s.SetVersion(15, 1) // Exchange 2016
	if _, err := ewsReceiver(t, s).Check(tctx(t)); err != nil {
		t.Errorf("Check on Exchange 2016 = %v", err)
	}
}

// Refused credentials are ErrAuthFailed.
func TestEWSAuthFailed(t *testing.T) {
	s := testmail.StartEWS(t)
	s.RejectAuth()
	if _, err := ewsReceiver(t, s).Describe(tctx(t)); !errors.Is(err, backend.ErrAuthFailed) {
		t.Errorf("Describe with a wrong password = %v, want ErrAuthFailed", err)
	}
}

func (s ewsServer) Sender(*testing.T) backend.Sender {
	return ews.NewSender(ews.Config{URL: s.URL(), Credentials: func(context.Context) (backend.Credentials, error) {
		return backend.Credentials{Username: "alice", Password: s.Password()}, nil
	}})
}

func (s ewsServer) Accepted(*testing.T) []backendtest.Delivery {
	var out []backendtest.Delivery
	for _, m := range s.EWS.Sent() {
		out = append(out, backendtest.Delivery{Rcpts: m.Rcpts, Raw: m.Raw})
	}
	return out
}

func (s ewsServer) Refuse(_ *testing.T, rcpt string) { s.EWS.Refuse(rcpt) }
func (s ewsServer) LoseVerdict(*testing.T)           { s.EWS.LoseVerdict() }

func TestEWSSenderContract(t *testing.T) {
	backendtest.RunSender(t, func(t *testing.T) backendtest.SendServer { return ewsServer{testmail.StartEWS(t)} })
}

// A Bcc recipient gets the message but is not named in it, and the sent
// copy is filed in Sent Items once.
func TestEWSSendBccAndSentCopy(t *testing.T) {
	s := testmail.StartEWS(t)
	raw := backendtest.Message("bcc@example.com", "hello")
	snd := ewsServer{s}.Sender(t)
	if !snd.SavesSent() {
		t.Error("SavesSent = false")
	}
	if err := snd.Send(tctx(t), "a@example.com", []string{"b@example.com", "hidden@example.com"}, raw); err != nil {
		t.Fatal(err)
	}
	sent := s.Sent()
	if len(sent) != 1 || !slices.Contains(sent[0].Rcpts, "hidden@example.com") {
		t.Fatalf("sent %+v, want one message to hidden@ too", sent)
	}
	if bytes.Contains(sent[0].Raw, []byte("hidden@")) {
		t.Errorf("the message names its Bcc recipient: %s", sent[0].Raw)
	}
	if n := s.Count("Sent Items"); n != 1 {
		t.Errorf("Sent Items has %d messages, want 1", n)
	}
}
