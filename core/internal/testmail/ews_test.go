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

func ewsReceiver(t *testing.T, s *testmail.EWS) *ews.Receiver { return ewsReceiverAs(t, s, "alice") }

// ewsReceiverAs signs in with login, the login name as the user typed it.
func ewsReceiverAs(t *testing.T, s *testmail.EWS, login string) *ews.Receiver {
	r := ews.New(ews.Config{URL: s.URL(), Credentials: func(context.Context) (backend.Credentials, error) {
		return backend.Credentials{Username: login, Password: s.Password()}, nil
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

// ewsNTLMServer is the fake over NTLM only, as Exchange's EWS directory is
// by default, signed in to with a domain login name.
type ewsNTLMServer struct{ ewsServer }

func (s ewsNTLMServer) Receiver(t *testing.T) backend.Receiver {
	return ewsReceiverAs(t, s.EWS, `EXAMPLE\alice`)
}

func TestEWSBackendContractNTLM(t *testing.T) {
	backendtest.RunOnly(t, func(t *testing.T) backendtest.Server {
		s := testmail.StartEWS(t)
		s.UseNTLM()
		return ewsNTLMServer{ewsServer{s}}
	}, "Check", "FirstSync", "AuthFailed")
}

// NTLM takes the login name as DOMAIN\user or as a UPN.
func TestEWSNTLMLoginNames(t *testing.T) {
	for _, login := range []string{`EXAMPLE\alice`, `example\Alice`, "alice@example.com"} {
		s := testmail.StartEWS(t)
		s.UseNTLM()
		if _, err := ewsReceiverAs(t, s, login).Check(tctx(t)); err != nil {
			t.Errorf("Check as %s = %v", login, err)
		}
	}
	s := testmail.StartEWS(t)
	s.UseNTLM()
	if _, err := ewsReceiverAs(t, s, `OTHER\alice`).Check(tctx(t)); !errors.Is(err, backend.ErrAuthFailed) {
		t.Errorf(`Check as OTHER\alice = %v, want ErrAuthFailed`, err)
	}
}

// A server that refuses NTLM without channel binding, yet takes the same
// password over Basic, requires Extended Protection: the check says so.
func TestEWSExtendedProtection(t *testing.T) {
	s := testmail.StartEWS(t)
	s.UseNTLM()
	s.RequireExtendedProtection()
	_, err := ewsReceiverAs(t, s, `EXAMPLE\alice`).Check(tctx(t))
	if !errors.Is(err, ews.ErrExtendedProtection) || err.Error() != ews.ReasonExtendedProtection {
		t.Errorf("Check under Extended Protection = %v, want ErrExtendedProtection", err)
	}
	s.RejectAuth() // a wrong password is still a wrong password
	if _, err := ewsReceiverAs(t, s, `EXAMPLE\alice`).Check(tctx(t)); !errors.Is(err, backend.ErrAuthFailed) {
		t.Errorf("Check with a wrong password under Extended Protection = %v, want ErrAuthFailed", err)
	}
}
