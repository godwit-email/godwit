package gmailapi_test

import (
	"context"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/backendtest"
	"github.com/godwit-email/godwit/backend/gmailapi"
	"github.com/godwit-email/godwit/backend/gmailapi/gmailapitest"
)

type contractServer struct{ *gmailapitest.Server }

func (s contractServer) creds() backend.CredentialsFunc {
	return func(context.Context) (backend.Credentials, error) {
		return backend.Credentials{Username: "alice@gmail.com", Token: s.Token()}, nil
	}
}

func (s contractServer) Receiver(t *testing.T) backend.Receiver {
	r := gmailapi.New(gmailapi.Config{BaseURL: s.URL(), Credentials: s.creds()})
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func (s contractServer) Deliver(_ *testing.T, folder string, raw []byte) {
	s.Server.Deliver(folder, raw)
}

func (s contractServer) Reset(t *testing.T, _ string) {
	t.Skip("Gmail API message IDs are immutable; historyId expiry reconciles via FolderSnapshot")
}

func (s contractServer) RejectAuth(*testing.T) { s.Server.RejectAuth() }

func (s contractServer) Sender(*testing.T) backend.Sender {
	return gmailapi.NewSender(gmailapi.Config{BaseURL: s.URL(), Credentials: s.creds()})
}

func (s contractServer) Accepted(*testing.T) []backendtest.Delivery {
	got := s.Server.Accepted()
	out := make([]backendtest.Delivery, len(got))
	for i, d := range got {
		out[i] = backendtest.Delivery{Rcpts: d.Rcpts, Raw: d.Raw}
	}
	return out
}

func (s contractServer) Refuse(_ *testing.T, rcpt string) { s.Server.Refuse(rcpt) }
func (s contractServer) LoseVerdict(*testing.T)           { s.Server.LoseVerdict() }

func TestReceiverContract(t *testing.T) {
	backendtest.Run(t, func(t *testing.T) backendtest.Server {
		return contractServer{gmailapitest.Start(t)}
	})
}

func TestSenderContract(t *testing.T) {
	backendtest.RunSender(t, func(t *testing.T) backendtest.SendServer {
		return contractServer{gmailapitest.Start(t)}
	})
}

type memLocal struct {
	folders map[backend.FolderRef]map[backend.MessageRef]backend.Flags
	ids     map[string]bool
}

func newMemLocal() *memLocal {
	return &memLocal{
		folders: map[backend.FolderRef]map[backend.MessageRef]backend.Flags{},
		ids:     map[string]bool{},
	}
}

func (l *memLocal) KnownIDs(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = l.ids[id]
	}
	return out, nil
}

func (l *memLocal) HeldRefs(_ context.Context, f backend.FolderRef, refs []backend.MessageRef) (map[backend.MessageRef]bool, error) {
	out := map[backend.MessageRef]bool{}
	for _, r := range refs {
		_, out[r] = l.folders[f][r]
	}
	return out, nil
}

func (l *memLocal) Held(_ context.Context, f backend.FolderRef) (map[backend.MessageRef]string, error) {
	out := map[backend.MessageRef]string{}
	for r := range l.folders[f] {
		out[r] = string(r)
	}
	return out, nil
}

func (l *memLocal) Lost(context.Context) ([]string, error) { return nil, nil }

func (l *memLocal) apply(p backend.Page) {
	for _, c := range p.Changes {
		switch c := c.(type) {
		case backend.MessageAppeared:
			if l.folders[c.Folder] == nil {
				l.folders[c.Folder] = map[backend.MessageRef]backend.Flags{}
			}
			l.folders[c.Folder][c.Ref] = c.Flags
			l.ids[c.ServerID] = true
		case backend.MessageGone:
			delete(l.folders[c.Folder], c.Ref)
		case backend.FlagsChanged:
			if _, ok := l.folders[c.Folder][c.Ref]; ok {
				l.folders[c.Folder][c.Ref] = c.Flags
			}
		case backend.FolderSnapshot:
			for ref := range l.folders[c.Folder] {
				if fl, ok := c.Messages[ref]; ok {
					l.folders[c.Folder][ref] = fl
				} else {
					delete(l.folders[c.Folder], ref)
				}
			}
		}
	}
}

// When startHistoryId expires (404), Sync reconciles by emitting FolderSnapshot
// without FolderReset: vanished locations disappear, flag edits apply, and new mail arrives.
func TestHistoryExpiryReconcilesWithSnapshots(t *testing.T) {
	srv := gmailapitest.Start(t)
	srv.SetPageLimit(2)
	idKeep := srv.Deliver("INBOX", backendtest.Message("keep@x", "keep"))
	idGone := srv.Deliver("INBOX", backendtest.Message("gone@x", "gone"))
	r := contractServer{srv}.Receiver(t)

	local := newMemLocal()
	var cur backend.Cursor
	states := map[backend.FolderRef]backend.FolderState{}
	runSync := func() []backend.Change {
		var changes []backend.Change
		_, err := r.Sync(t.Context(), backend.SyncRequest{
			Account:      cur,
			Folders:      maps.Clone(states),
			Local:        local,
			HeaderFields: []string{"Subject", "Message-ID"},
		}, func(p backend.Page) error {
			local.apply(p)
			changes = append(changes, p.Changes...)
			if p.Account != "" {
				cur = p.Account
			}
			for f, st := range p.Folders {
				states[f] = st
			}
			return nil
		})
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
		return changes
	}

	runSync()
	if len(local.folders["INBOX"]) != 2 {
		t.Fatalf("initial INBOX = %d, want 2", len(local.folders["INBOX"]))
	}

	// Mutate while history expires so 404 forces full snapshot reconciliation.
	srv.ModifyLabels(idKeep, []string{"STARRED"}, []string{"UNREAD"})
	srv.ModifyLabels(idGone, nil, []string{"INBOX"})
	idNew := srv.Deliver("INBOX", backendtest.Message("new@x", "new"))
	srv.ExpireHistory()

	cs := runSync()
	hasSnapshot := false
	for _, c := range cs {
		if _, reset := c.(backend.FolderReset); reset {
			t.Fatalf("unexpected FolderReset on history expiry: %+v", c)
		}
		if snap, ok := c.(backend.FolderSnapshot); ok && snap.Folder == "INBOX" {
			hasSnapshot = true
		}
	}
	if !hasSnapshot {
		t.Fatal("expected FolderSnapshot for INBOX on 404 history expiry")
	}
	if _, ok := local.folders["INBOX"][backend.MessageRef(idGone)]; ok {
		t.Error("archived message remained in INBOX after snapshot")
	}
	if _, ok := local.folders[gmailapi.AllMailRef][backend.MessageRef(idGone)]; !ok {
		t.Error("archived message missing from All Mail")
	}
	fl := local.folders["INBOX"][backend.MessageRef(idKeep)]
	if !fl.Seen || !fl.Flagged {
		t.Errorf("kept message flags = %+v, want Seen and Flagged", fl)
	}
	if _, ok := local.folders["INBOX"][backend.MessageRef(idNew)]; !ok {
		t.Error("new message missing from INBOX after snapshot")
	}
}

// HTTP 429 and 403 rateLimitExceeded serialize per account and back off.
func TestRateLimitSerializesAndBacksOff(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		retryAfter string
		wantWaits  []time.Duration
	}{
		{"HTTP429WithRetryAfter", http.StatusTooManyRequests, "3", []time.Duration{3 * time.Second, 3 * time.Second}},
		{"HTTP403RateLimitExponential", http.StatusForbidden, "", []time.Duration{time.Second, 2 * time.Second}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := gmailapitest.Start(t)
			srv.ThrottleNext(2, tc.status, tc.retryAfter)

			var mu sync.Mutex
			var waits []time.Duration
			cfg := gmailapi.Config{
				BaseURL: srv.URL(),
				Credentials: func(context.Context) (backend.Credentials, error) {
					return backend.Credentials{Username: "alice@gmail.com", Token: srv.Token()}, nil
				},
				Wait: func(_ context.Context, d time.Duration) error {
					mu.Lock()
					waits = append(waits, d)
					mu.Unlock()
					return nil
				},
			}
			r1 := gmailapi.New(cfg)
			r2 := gmailapi.New(cfg)
			defer r1.Close()
			defer r2.Close()

			var wg sync.WaitGroup
			errs := make([]error, 2)
			for i, r := range []*gmailapi.Receiver{r1, r2} {
				wg.Add(1)
				go func(i int, r *gmailapi.Receiver) {
					defer wg.Done()
					_, errs[i] = r.Check(t.Context())
				}(i, r)
			}
			wg.Wait()
			for i, err := range errs {
				if err != nil {
					t.Fatalf("Check[%d]: %v", i, err)
				}
			}
			if got := srv.MaxInFlight(); got != 1 {
				t.Errorf("MaxInFlight = %d, want 1 (per-account serialization)", got)
			}
			if !slices.Equal(waits, tc.wantWaits) {
				t.Errorf("waits = %v, want %v", waits, tc.wantWaits)
			}
		})
	}
}

func TestAPIDisabledReturnsReason(t *testing.T) {
	srv := gmailapitest.Start(t)
	srv.SetAPIDisabled(true)
	r := contractServer{srv}.Receiver(t)
	_, err := r.Check(t.Context())
	if !errors.Is(err, gmailapi.ErrAPIDisabled) {
		t.Fatalf("Check = %v, want ErrAPIDisabled", err)
	}
}

func TestMultiLabelStarredImportantTrashAndMultipart(t *testing.T) {
	srv := gmailapitest.Start(t)
	workID := srv.EnsureLabel("Work/Urgent")
	raw := []byte("From: a@example.com\r\nTo: b@example.com\r\nSubject: multi\r\n" +
		"Message-ID: <multi@example.com>\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"bnd\"\r\n\r\n" +
		"--bnd\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nhello multipart\r\n" +
		"--bnd\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=\"a.bin\"\r\nContent-Transfer-Encoding: base64\r\n\r\nYWJjZA==\r\n" +
		"--bnd--\r\n")
	msgID := srv.AddMessage(raw, "INBOX", "STARRED", "IMPORTANT", workID)

	r := contractServer{srv}.Receiver(t)
	var folders map[backend.FolderRef]backend.Folder
	folders = map[backend.FolderRef]backend.Folder{}
	var apps []backend.MessageAppeared
	_, err := r.Sync(t.Context(), backend.SyncRequest{
		HeaderFields: []string{"Subject"},
		TextBytes:    1024,
		TextParts: func(p *backend.Part) []backend.PartRef {
			return []backend.PartRef{p.Children[0].Ref}
		},
	}, func(p backend.Page) error {
		for _, c := range p.Changes {
			switch c := c.(type) {
			case backend.FolderUpdated:
				folders[c.Ref] = c.Folder
			case backend.MessageAppeared:
				apps = append(apps, c)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if folders["STARRED"].Role != backend.RoleFlagged {
		t.Errorf("STARRED role = %q, want %q", folders["STARRED"].Role, backend.RoleFlagged)
	}
	if folders["IMPORTANT"].Role != "" {
		t.Errorf("IMPORTANT role = %q, want empty", folders["IMPORTANT"].Role)
	}
	if folders[gmailapi.AllMailRef].Role != backend.RoleAll {
		t.Errorf("AllMail role = %q, want %q", folders[gmailapi.AllMailRef].Role, backend.RoleAll)
	}
	if folders[backend.FolderRef(workID)].Parent == "" {
		t.Errorf("Work/Urgent parent = empty, want Work's label ID")
	}
	if len(apps) != 5 { // INBOX, IMPORTANT, STARRED, Label_2, All Mail
		t.Fatalf("got %d locations, want 5: %+v", len(apps), apps)
	}
	if !apps[0].Flags.Seen || !apps[0].Flags.Flagged || apps[0].Content == nil {
		t.Fatalf("first appearance = %+v, want Seen+Flagged with Content", apps[0])
	}
	for _, a := range apps[1:] {
		if a.Content != nil {
			t.Errorf("secondary location %q carried duplicate Content", a.Folder)
		}
	}

	// Fetch attachment leaf part via opaque PartRef.
	attRef := apps[0].Content.Structure.Children[1].Ref
	rc, err := r.Fetch(t.Context(), backend.ContentRequest{Location: apps[0].Location, Part: attRef})
	if err != nil {
		t.Fatalf("Fetch attachment: %v", err)
	}
	attBytes, _ := io.ReadAll(rc)
	_ = rc.Close()
	if strings.TrimSpace(string(attBytes)) != "YWJjZA==" {
		t.Errorf("attachment bytes = %q, want YWJjZA==", attBytes)
	}

	// Delete calls /trash, stripping INBOX and adding TRASH.
	res, err := r.Apply(t.Context(), backend.Delete{At: backend.Location{Folder: "INBOX", Ref: backend.MessageRef(msgID), ServerID: msgID}})
	if err != nil || res.Outcome != backend.Done {
		t.Fatalf("Delete = %+v, %v", res, err)
	}
	gotLabels := srv.MessageLabels(msgID)
	if !slices.Contains(gotLabels, "TRASH") || slices.Contains(gotLabels, "INBOX") {
		t.Errorf("labels after Trash = %v, want TRASH without INBOX", gotLabels)
	}
}

func TestCrossHostRedirectRefused(t *testing.T) {
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer evil.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.URL+r.URL.Path, http.StatusFound)
	}))
	defer origin.Close()

	r := gmailapi.New(gmailapi.Config{
		BaseURL: origin.URL,
		Credentials: func(context.Context) (backend.Credentials, error) {
			return backend.Credentials{Token: "tok"}, nil
		},
	})
	defer r.Close()
	if _, err := r.Check(t.Context()); err == nil || !strings.Contains(err.Error(), "redirect to another host") {
		t.Fatalf("Check on cross-host redirect = %v, want redirect refusal", err)
	}
}

func TestHistoryExpiryReconcilesMovedOutOfWindowMessage(t *testing.T) {
	srv := gmailapitest.Start(t)
	lblA := srv.EnsureLabel("LabelA")
	lblB := srv.EnsureLabel("LabelB")
	oldID := srv.AddMessage(
		[]byte("From: a@example.com\r\nTo: b@example.com\r\nSubject: old\r\nDate: Mon, 9 Nov 2026 08:00:00 +0000\r\n\r\nold\r\n"),
		lblA,
	)
	newID := srv.AddMessage(
		[]byte("From: a@example.com\r\nTo: b@example.com\r\nSubject: new\r\nDate: Mon, 9 Nov 2026 10:00:00 +0000\r\n\r\nnew\r\n"),
		lblB,
	)
	r := contractServer{srv}.Receiver(t)
	local := newMemLocal()
	var cur backend.Cursor
	states := map[backend.FolderRef]backend.FolderState{}
	win := backend.Window{Newest: 1}
	runSync := func() {
		_, err := r.Sync(t.Context(), backend.SyncRequest{
			Account: cur,
			Folders: maps.Clone(states),
			Window:  win,
			Local:   local,
		}, func(p backend.Page) error {
			local.apply(p)
			if p.Account != "" {
				cur = p.Account
			}
			for f, st := range p.Folders {
				states[f] = st
			}
			return nil
		})
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
	}

	runSync()
	if _, ok := local.folders[backend.FolderRef(lblA)][backend.MessageRef(oldID)]; !ok {
		t.Fatal("oldID should start in LabelA")
	}

	// Move oldID from A to B (behind newID, outside B's Newest:1 window), then expire history.
	srv.ModifyLabels(oldID, []string{lblB}, []string{lblA})
	srv.ExpireHistory()
	runSync()

	if _, ok := local.folders[backend.FolderRef(lblA)][backend.MessageRef(oldID)]; ok {
		t.Error("oldID remained in LabelA after 404 reconciliation")
	}
	if _, ok := local.folders[backend.FolderRef(lblB)][backend.MessageRef(oldID)]; !ok {
		t.Error("oldID missing from LabelB after 404 reconciliation outside B's window")
	}
	if _, ok := local.folders[backend.FolderRef(lblB)][backend.MessageRef(newID)]; !ok {
		t.Error("newID missing from LabelB")
	}
}

func TestApplyValidatesSourceAndImmutableSystemLabels(t *testing.T) {
	srv := gmailapitest.Start(t)
	lblA := srv.EnsureLabel("A")
	lblB := srv.EnsureLabel("B")
	lblC := srv.EnsureLabel("C")
	msgID := srv.AddMessage(backendtest.Message("m@x", "m"), lblA)
	r := contractServer{srv}.Receiver(t)

	// Remote move A -> C before queued A -> B move: Apply must return ErrNotFound without adding B.
	srv.ModifyLabels(msgID, []string{lblC}, []string{lblA})
	_, err := r.Apply(t.Context(), backend.Move{
		At: backend.Location{Folder: backend.FolderRef(lblA), Ref: backend.MessageRef(msgID), ServerID: msgID},
		To: backend.FolderRef(lblB),
	})
	if !errors.Is(err, backend.ErrNotFound) {
		t.Fatalf("Move from lost source = %v, want ErrNotFound", err)
	}
	if !slices.Equal(srv.MessageLabels(msgID), []string{lblC}) {
		t.Fatalf("labels after rejected stale move = %v, want [%s]", srv.MessageLabels(msgID), lblC)
	}

	// Idempotent retry when already at destination C succeeds.
	res, err := r.Apply(t.Context(), backend.Move{
		At: backend.Location{Folder: backend.FolderRef(lblA), Ref: backend.MessageRef(msgID), ServerID: msgID},
		To: backend.FolderRef(lblC),
	})
	if err != nil || res.Outcome != backend.Done {
		t.Fatalf("idempotent Move retry = %+v, %v", res, err)
	}

	// Moving into or out of SENT / DRAFT to a user folder returns Unsupported.
	sentID := srv.AddMessage(backendtest.Message("s@x", "sent"), "SENT")
	res, err = r.Apply(t.Context(), backend.Move{
		At: backend.Location{Folder: "SENT", Ref: backend.MessageRef(sentID), ServerID: sentID},
		To: backend.FolderRef(lblB),
	})
	if err != nil || res.Outcome != backend.Unsupported || res.Reason != gmailapi.ReasonSystemLabelsFixed {
		t.Fatalf("Move out of SENT = %+v, %v; want Unsupported(%s)", res, err, gmailapi.ReasonSystemLabelsFixed)
	}
}
