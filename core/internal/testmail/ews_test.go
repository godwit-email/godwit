package testmail_test

import (
	"context"
	"errors"
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
