package testmail_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/backendtest"
	"github.com/godwit-email/godwit/backend/jmap"
	"github.com/godwit-email/godwit/core/internal/testmail"
)

func jmapReceiver(t *testing.T, s *testmail.JMAP, sessionURL string) *jmap.Receiver {
	r := jmap.New(jmap.Config{SessionURL: sessionURL, Credentials: func(context.Context) (backend.Credentials, error) {
		return backend.Credentials{Username: "alice", Password: s.Password()}, nil
	}})
	t.Cleanup(func() { r.Close() })
	return r
}

type jmapServer struct{ *testmail.JMAP }

func (s jmapServer) Receiver(t *testing.T) backend.Receiver {
	return jmapReceiver(t, s.JMAP, s.SessionURL())
}
func (s jmapServer) Deliver(_ *testing.T, folder string, raw []byte) { s.JMAP.Deliver(folder, raw) }
func (s jmapServer) Reset(*testing.T, string)                        { s.ExpireState() }
func (s jmapServer) RejectAuth(*testing.T)                           { s.JMAP.RejectAuth() }

func TestJMAPBackendContract(t *testing.T) {
	backendtest.Run(t, func(t *testing.T) backendtest.Server { return jmapServer{testmail.StartJMAP(t)} })
}

func tctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)
	return c
}

// jmapSync runs one round from cursor and returns the changes and the new cursor.
func jmapSync(t *testing.T, r backend.Receiver, cursor backend.Cursor, local backend.Local) ([]backend.Change, backend.Cursor, error) {
	t.Helper()
	var cs []backend.Change
	_, err := r.Sync(tctx(t), backend.SyncRequest{Account: cursor, Local: local}, func(p backend.Page) error {
		cs = append(cs, p.Changes...)
		if p.Account != "" {
			cursor = p.Account
		}
		return nil
	})
	return cs, cursor, err
}

// held is a Local that holds every given ServerID in every folder.
type held map[string]bool

func (h held) KnownIDs(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = h[id]
	}
	return out, nil
}

func (h held) HeldRefs(_ context.Context, _ backend.FolderRef, refs []backend.MessageRef) (map[backend.MessageRef]bool, error) {
	out := map[backend.MessageRef]bool{}
	for _, r := range refs {
		out[r] = h[string(r)]
	}
	return out, nil
}

// An expired state makes the next round reset every folder and claim its mail again.
func TestJMAPStateExpired(t *testing.T) {
	s := testmail.StartJMAP(t)
	id := s.Deliver("INBOX", backendtest.Message("a@example.com", "a"))
	r := jmapReceiver(t, s, s.SessionURL())
	_, cur, err := jmapSync(t, r, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	s.ExpireState()
	cs, next, err := jmapSync(t, r, cur, held{string(id): true})
	if err != nil {
		t.Fatal(err)
	}
	var reset, again bool
	for _, c := range cs {
		switch c := c.(type) {
		case backend.FolderReset:
			reset = reset || c.Ref == backend.FolderRef(s.Inbox)
		case backend.MessageAppeared:
			again = again || reset && c.Ref == backend.MessageRef(id) && c.Content == nil
		}
	}
	if !reset || !again || next == cur {
		t.Errorf("after an expired state got %+v, cursor %q -> %q; want a reset, the known message again and a new cursor", cs, cur, next)
	}
}

// Watch reconnects after its EventSource drops and wakes for what came meanwhile.
func TestJMAPEventSourceDropped(t *testing.T) {
	s := testmail.StartJMAP(t)
	r := jmapReceiver(t, s, s.SessionURL())
	c, cancel := context.WithCancel(tctx(t))
	defer cancel()
	wake := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- r.Watch(c, func([]backend.FolderRef) {
			select {
			case wake <- struct{}{}:
			default:
			}
		})
	}()
	<-wake // the state on connect
	s.DropEventSources()
	s.Deliver("INBOX", backendtest.Message("a@example.com", "a"))
	select {
	case <-wake:
	case err := <-done:
		t.Fatalf("Watch ended after a drop: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("Watch did not wake after its EventSource dropped")
	}
	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Errorf("Watch = %v after cancel", err)
	}
}

// Merged emails are gone and the merged one, under its new id, appears.
func TestJMAPMergedIDChanges(t *testing.T) {
	s := testmail.StartJMAP(t)
	a := s.Deliver("INBOX", backendtest.Message("a@example.com", "a"))
	b := s.Deliver("INBOX", backendtest.Message("a@example.com", "a"))
	r := jmapReceiver(t, s, s.SessionURL())
	_, cur, err := jmapSync(t, r, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	merged := s.MergeEmails(a, b)
	cs, _, err := jmapSync(t, r, cur, held{string(a): true, string(b): true})
	if err != nil {
		t.Fatal(err)
	}
	gone := map[backend.MessageRef]bool{}
	var appeared bool
	for _, c := range cs {
		switch c := c.(type) {
		case backend.MessageGone:
			gone[c.Ref] = true
		case backend.MessageAppeared:
			appeared = appeared || c.Ref == backend.MessageRef(merged) && c.ServerID == string(merged) && c.Content != nil
		}
	}
	if !gone[backend.MessageRef(a)] || !gone[backend.MessageRef(b)] || !appeared {
		t.Errorf("after a merge got %+v; want %s and %s gone and %s appeared with content", cs, a, b, merged)
	}
}

// A 401 in the middle of a session is ErrAuthFailed.
func TestJMAPUnauthorized(t *testing.T) {
	s := testmail.StartJMAP(t)
	r := jmapReceiver(t, s, s.SessionURL())
	_, cur, err := jmapSync(t, r, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	s.RejectNextRequest()
	if _, _, err := jmapSync(t, r, cur, nil); !errors.Is(err, backend.ErrAuthFailed) {
		t.Errorf("Sync after a 401 = %v, want ErrAuthFailed", err)
	}
}

// credentialTrap is a host that records whether credentials reached it.
func credentialTrap(t *testing.T) (*httptest.Server, *atomic.Bool) {
	var got atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Store(got.Load() || r.Header.Get("Authorization") != "")
		http.Error(w, "trap", http.StatusTeapot)
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

// A redirect to another host is not followed, so the credentials stay home.
func TestJMAPCrossHostRedirect(t *testing.T) {
	s := testmail.StartJMAP(t)
	trap, got := credentialTrap(t)
	s.Redirect("/jmap/session", trap.URL+"/jmap/session")
	if _, _, err := jmapSync(t, jmapReceiver(t, s, s.SessionURL()), "", nil); err == nil {
		t.Error("Sync followed a redirect to another host")
	}
	if got.Load() {
		t.Error("the credentials reached the other host")
	}
}

// Discovery at /.well-known/jmap may redirect to another host: Check follows
// it and reports the session host it reached.
func TestJMAPWellKnownRedirect(t *testing.T) {
	s := testmail.StartJMAP(t)
	front := testmail.StartJMAP(t)
	front.Redirect("/.well-known/jmap", s.SessionURL())
	c, err := jmapReceiver(t, s, front.URL()+"/.well-known/jmap").Check(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.TrimPrefix(s.URL(), "http://"); c.Host != want {
		t.Errorf("Check host %q, want the session host %q", c.Host, want)
	}
}

// A downloadUrl on another host than the API's is used.
func TestJMAPDownloadElsewhere(t *testing.T) {
	s := testmail.StartJMAP(t)
	id := s.Deliver("INBOX", backendtest.Message("a@example.com", "a"))
	s.SeparateDownloadHost()
	r := jmapReceiver(t, s, s.SessionURL())
	rc, err := r.Fetch(tctx(t), backend.ContentRequest{Location: backend.Location{Folder: backend.FolderRef(s.Inbox), Ref: backend.MessageRef(id)}})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if b, _ := io.ReadAll(rc); !strings.Contains(string(b), "body of a") {
		t.Errorf("downloaded %q", b)
	}
}
