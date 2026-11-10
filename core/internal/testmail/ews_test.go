package testmail_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

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

// busyReceiver is a receiver that records the back-offs it waits out
// instead of waiting.
func busyReceiver(t *testing.T, s *testmail.EWS) (*ews.Receiver, *[]time.Duration) {
	var waits []time.Duration
	r := ews.New(ews.Config{URL: s.URL(), Credentials: func(context.Context) (backend.Credentials, error) {
		return backend.Credentials{Username: "alice", Password: s.Password()}, nil
	}, Wait: func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}})
	t.Cleanup(func() { r.Close() })
	return r, &waits
}

// ErrorServerBusy, as a SOAP fault or a response message, is waited out for
// BackOffMilliseconds and the request sent again, up to three times.
func TestEWSServerBusyRetries(t *testing.T) {
	for _, fault := range []bool{true, false} {
		s := testmail.StartEWS(t)
		s.Busy(3, 2*time.Second, fault)
		r, waits := busyReceiver(t, s)
		if _, err := r.Describe(tctx(t)); err != nil {
			t.Fatalf("Describe after 3 busy answers (fault %v) = %v", fault, err)
		}
		if want := []time.Duration{2 * time.Second, 2 * time.Second, 2 * time.Second}; !slices.Equal(*waits, want) {
			t.Errorf("waited %v (fault %v), want %v", *waits, fault, want)
		}
	}
}

// A server still busy after three retries fails the request with a plain
// error, not a sign-in failure; a back-off over a minute waits a minute.
func TestEWSServerBusyGivesUp(t *testing.T) {
	s := testmail.StartEWS(t)
	s.Busy(4, 10*time.Minute, false)
	r, waits := busyReceiver(t, s)
	_, err := r.Describe(tctx(t))
	if err == nil || errors.Is(err, backend.ErrAuthFailed) {
		t.Errorf("Describe after 4 busy answers = %v, want a plain error", err)
	}
	if n := s.Requests(); n != 4 {
		t.Errorf("sent %d requests, want 4", n)
	}
	if want := []time.Duration{time.Minute, time.Minute, time.Minute}; !slices.Equal(*waits, want) {
		t.Errorf("waited %v, want %v", *waits, want)
	}
}

// An account's requests go one at a time, even from several backends (as
// the receiver and the sender are).
func TestEWSRequestsOneAtATime(t *testing.T) {
	s := testmail.StartEWS(t)
	s.SetLatency(20 * time.Millisecond)
	errs := make(chan error)
	for range 3 {
		go func() {
			_, err := ewsReceiver(t, s).Check(tctx(t))
			errs <- err
		}()
	}
	for range 3 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if n := s.MaxConcurrent(); n != 1 {
		t.Errorf("%d requests at once, want 1", n)
	}
}

// Waiting out a back-off ends with ctx.
func TestEWSServerBusyWaitCancels(t *testing.T) {
	s := testmail.StartEWS(t)
	s.Busy(1, time.Minute, true)
	ctx, cancel := context.WithTimeout(tctx(t), 50*time.Millisecond)
	defer cancel()
	if _, err := ewsReceiver(t, s).Describe(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Describe cancelled while backing off = %v, want DeadlineExceeded", err)
	}
}
