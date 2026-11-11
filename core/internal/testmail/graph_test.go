package testmail_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/backendtest"
	"github.com/godwit-email/godwit/backend/graph"
	"github.com/godwit-email/godwit/core/internal/testmail"
)

func graphReceiver(t *testing.T, s *testmail.Graph, wait func(context.Context, time.Duration) error) *graph.Receiver {
	r := graph.NewReceiver(graph.Config{
		URL: s.URL(),
		Credentials: func(context.Context) (backend.Credentials, error) {
			return backend.Credentials{Username: "alice@outlook.com", Token: s.Token()}, nil
		},
		Wait: wait,
	})
	t.Cleanup(func() { _ = r.Close() })
	return r
}

type graphServer struct{ *testmail.Graph }

func (s graphServer) Receiver(t *testing.T) backend.Receiver {
	return graphReceiver(t, s.Graph, nil)
}

func (s graphServer) Deliver(_ *testing.T, folder string, raw []byte) {
	s.Graph.Deliver(folder, raw)
}

func (s graphServer) Reset(_ *testing.T, folder string) {
	s.Graph.Reset(folder)
}

func (s graphServer) RejectAuth(*testing.T) {
	s.Graph.RejectAuth()
}

func TestGraphBackendContract(t *testing.T) {
	backendtest.Run(t, func(t *testing.T) backendtest.Server {
		return graphServer{testmail.StartGraph(t)}
	})
}

func syncRound(t *testing.T, r backend.Receiver, account *backend.Cursor, states map[backend.FolderRef]backend.FolderState, local backend.Local) ([]backend.Change, int) {
	t.Helper()
	var changes []backend.Change
	pages := 0
	_, err := r.Sync(tctx(t), backend.SyncRequest{
		Account:      *account,
		Folders:      maps.Clone(states),
		Local:        local,
		HeaderFields: []string{"Message-ID", "Subject"},
		TextBytes:    4096,
	}, func(p backend.Page) error {
		pages++
		changes = append(changes, p.Changes...)
		if p.Account != "" {
			*account = p.Account
		}
		for f, st := range p.Folders {
			states[f] = st
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	return changes, pages
}

// Delta pagination walks @odata.nextLink across both folder listings and
// per-folder messages/delta pages before committing @odata.deltaLink.
func TestGraphDeltaPagination(t *testing.T) {
	s := testmail.StartGraph(t)
	s.SetDeltaPageSize(2)
	s.SetListPageSize(2)

	nestedID := s.Folder("Work/Projects")
	for i := range 5 {
		s.Deliver("INBOX", backendtest.Message("m"+string(rune('a'+i))+"@example.com", "subj-"+string(rune('a'+i))))
	}
	s.Deliver("Work/Projects", backendtest.Message("proj@example.com", "project"))

	r := graphReceiver(t, s, nil)
	var account backend.Cursor
	states := map[backend.FolderRef]backend.FolderState{}
	cs, pages := syncRound(t, r, &account, states, nil)
	if pages < 4 {
		t.Fatalf("got %d pages, want multi-page sync", pages)
	}

	var seenNested bool
	inboxCount := 0
	inboxRef := backend.FolderRef(s.Folder("INBOX"))
	for _, c := range cs {
		switch c := c.(type) {
		case backend.FolderUpdated:
			if c.Ref == backend.FolderRef(nestedID) && c.Name == "Work/Projects" {
				seenNested = true
			}
		case backend.MessageAppeared:
			if c.Folder == inboxRef {
				inboxCount++
			}
		}
	}
	if !seenNested {
		t.Errorf("nested folder Work/Projects not listed correctly in %+v", cs)
	}
	if inboxCount != 5 {
		t.Errorf("inbox messages = %d, want 5", inboxCount)
	}

	// Deliver 3 more messages so incremental delta also spans multiple pages.
	for i := range 3 {
		s.Deliver("INBOX", backendtest.Message("inc"+string(rune('a'+i))+"@example.com", "inc"))
	}
	cs2, _ := syncRound(t, r, &account, states, nil)
	inc := 0
	for _, c := range cs2 {
		if m, ok := c.(backend.MessageAppeared); ok && m.Folder == inboxRef && !m.Backfill {
			inc++
		}
	}
	if inc != 3 {
		t.Errorf("incremental arrivals = %d, want 3", inc)
	}
}

// Both HTTP 410 Gone and HTTP 400 with innerError.code == "syncStateNotFound"
// emit FolderReset and resync the folder cleanly.
func TestGraphDeltaInvalidationVariants(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		inner  bool
	}{
		{"410_gone", http.StatusGone, false},
		{"400_inner_syncStateNotFound", http.StatusBadRequest, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testmail.StartGraph(t)
			id := s.Deliver("INBOX", backendtest.Message("one@example.com", "one"))
			r := graphReceiver(t, s, nil)

			var account backend.Cursor
			states := map[backend.FolderRef]backend.FolderState{}
			syncRound(t, r, &account, states, nil)

			s.ResetWith("INBOX", tc.status, tc.inner)
			inboxRef := backend.FolderRef(s.Folder("INBOX"))
			cs, _ := syncRound(t, r, &account, states, held{id: true})

			resetIdx := slices.IndexFunc(cs, func(c backend.Change) bool {
				return c == backend.FolderReset{Ref: inboxRef}
			})
			appearIdx := slices.IndexFunc(cs, func(c backend.Change) bool {
				m, ok := c.(backend.MessageAppeared)
				return ok && m.Folder == inboxRef && m.ServerID == id && m.Content == nil
			})
			if resetIdx < 0 || appearIdx <= resetIdx {
				t.Fatalf("got changes %+v, want FolderReset before MessageAppeared", cs)
			}
		})
	}
}

// Every request carries Prefer: IdType="ImmutableId", keeping message IDs
// stable across folder moves even though mutable Graph IDs change on move.
func TestGraphImmutableID(t *testing.T) {
	s := testmail.StartGraph(t)
	id := s.Deliver("INBOX", backendtest.Message("immut@example.com", "immutable"))
	r := graphReceiver(t, s, nil)

	if _, err := r.Check(tctx(t)); err != nil {
		t.Fatalf("Check: %v", err)
	}

	var account backend.Cursor
	states := map[backend.FolderRef]backend.FolderState{}
	syncRound(t, r, &account, states, nil)

	inboxRef := backend.FolderRef(s.Folder("INBOX"))
	archiveRef := backend.FolderRef(s.Folder("Archive"))

	res, err := r.Apply(tctx(t), backend.Move{
		At: backend.Location{Folder: inboxRef, Ref: backend.MessageRef(id), ServerID: id},
		To: archiveRef,
	})
	if err != nil {
		t.Fatalf("Move: %v", err)
	}
	if res.Outcome != backend.Done || string(res.Ref) != id {
		t.Fatalf("Move result = %+v, want Done with unchanged ImmutableId %q", res, id)
	}

	// Verify a raw request WITHOUT Prefer: IdType="ImmutableId" gets a different (mutable) ID.
	req, _ := http.NewRequestWithContext(tctx(t), http.MethodGet, s.URL()+"/me/mailFolders/"+string(archiveRef)+"/messages", nil)
	req.Header.Set("Authorization", "Bearer "+s.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if s.NonImmutableRequests() != 1 {
		t.Errorf("NonImmutableRequests = %d, want 1 (only our raw probe)", s.NonImmutableRequests())
	}
}

// 429 and 503 responses with Retry-After back off via Wait, and concurrent
// calls for one account run strictly one at a time.
func TestGraphThrottleAndSingleFlight(t *testing.T) {
	s := testmail.StartGraph(t)
	s.Deliver("INBOX", backendtest.Message("t@example.com", "throttled"))

	var mu sync.Mutex
	var waits []time.Duration
	r := graphReceiver(t, s, func(_ context.Context, d time.Duration) error {
		mu.Lock()
		waits = append(waits, d)
		mu.Unlock()
		return nil
	})

	s.Throttle(2, http.StatusTooManyRequests, "7")
	if _, err := r.Check(tctx(t)); err != nil {
		t.Fatalf("Check after 429 retries: %v", err)
	}
	if !slices.Equal(waits, []time.Duration{7 * time.Second, 7 * time.Second}) {
		t.Errorf("429 waits = %v, want [7s 7s]", waits)
	}

	waits = nil
	s.Throttle(1, http.StatusServiceUnavailable, "3")
	if _, err := r.Describe(tctx(t)); err != nil {
		t.Fatalf("Describe: %v", err)
	}
	var account backend.Cursor
	states := map[backend.FolderRef]backend.FolderState{}
	syncRound(t, r, &account, states, nil)
	if !slices.Equal(waits, []time.Duration{3 * time.Second}) {
		t.Errorf("503 waits = %v, want [3s]", waits)
	}

	// Single-flight serialization across concurrent callers on the same account.
	s.SetLatency(20 * time.Millisecond)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r2 := graphReceiver(t, s, nil)
			if _, err := r2.Check(tctx(t)); err != nil {
				t.Errorf("concurrent Check: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := s.MaxInFlight(); got > 1 {
		t.Errorf("MaxInFlight = %d, want 1", got)
	}
}

// Custom keywords round-trip via Outlook categories, partial delta updates
// preserve existing flags, well-known folder paths resolve standard roles
// without selecting v1.0-unsupported wellKnownName or /me, and Purge uses
// permanentDelete rather than moving Junk items to Deleted Items.
func TestGraphCategoriesFlagsAndDelete(t *testing.T) {
	s := testmail.StartGraph(t)
	s.SetPartialFlagDeltas(true)

	// Fake server models v1.0 constraints: /me is 403 without User.Read, and
	// $select=wellKnownName on mailFolders is 400 BadRequest.
	for _, probe := range []struct {
		path       string
		wantStatus int
	}{
		{"/me?$select=id", http.StatusForbidden},
		{"/me/mailFolders?$select=id,wellKnownName", http.StatusBadRequest},
	} {
		req, _ := http.NewRequestWithContext(tctx(t), http.MethodGet, s.URL()+probe.path, nil)
		req.Header.Set("Authorization", "Bearer "+s.Token())
		req.Header.Set("Prefer", `IdType="ImmutableId"`)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != probe.wantStatus {
			t.Fatalf("GET %s = %d, want %d", probe.path, resp.StatusCode, probe.wantStatus)
		}
	}

	id := s.Deliver("INBOX", backendtest.Message("cat@example.com", "categories"))
	junkID := s.Deliver("Junk Email", backendtest.Message("junk@example.com", "spam"))
	r := graphReceiver(t, s, nil)
	if _, err := r.Check(tctx(t)); err != nil {
		t.Fatalf("Check without User.Read or wellKnownName: %v", err)
	}

	var account backend.Cursor
	states := map[backend.FolderRef]backend.FolderState{}
	cs, _ := syncRound(t, r, &account, states, nil)

	inboxRef := backend.FolderRef(s.Folder("INBOX"))
	junkRef := backend.FolderRef(s.Folder("Junk Email"))
	trashRef := backend.FolderRef(s.Folder("Deleted Items"))
	var foundInboxRole bool
	for _, c := range cs {
		if fu, ok := c.(backend.FolderUpdated); ok && fu.Ref == inboxRef && fu.Role == backend.RoleInbox {
			foundInboxRole = true
		}
	}
	if !foundInboxRole {
		t.Fatal("Inbox role was not resolved via well-known folder paths")
	}

	loc := backend.Location{Folder: inboxRef, Ref: backend.MessageRef(id), ServerID: id}
	res, err := r.Apply(tctx(t), backend.SetFlags{
		At:  loc,
		Add: backend.Flags{Flagged: true, Keywords: []string{"ProjectX", "Urgent"}},
	})
	if err != nil || res.Outcome != backend.Done {
		t.Fatalf("SetFlags add = %+v, %v", res, err)
	}

	// Simulate another client toggling only isRead; partial delta omits flag & categories.
	s.MarkRead(id, true)
	cs2, _ := syncRound(t, r, &account, states, held{id: true})
	var gotFlags *backend.Flags
	for _, c := range cs2 {
		if fc, ok := c.(backend.FlagsChanged); ok && fc.Ref == loc.Ref {
			gotFlags = &fc.Flags
		}
	}
	if gotFlags == nil || !gotFlags.Seen || !gotFlags.Flagged || !slices.Equal(gotFlags.Keywords, []string{"ProjectX", "Urgent"}) {
		t.Fatalf("FlagsChanged after partial delta = %+v, want Seen+Flagged+[ProjectX Urgent]", gotFlags)
	}

	// Purge from Junk Email permanently deletes the message (does NOT move it to Deleted Items).
	junkLoc := backend.Location{Folder: junkRef, Ref: backend.MessageRef(junkID), ServerID: junkID}
	if res, err := r.Apply(tctx(t), backend.Purge{At: junkLoc}); err != nil || res.Outcome != backend.Done {
		t.Fatalf("Purge from Junk = %+v, %v", res, err)
	}
	if _, _, _, _, exists := s.MessageState(junkID); exists {
		t.Fatal("Purge from Junk left message in mailbox (Deleted Items)")
	}

	// Delete in Inbox moves to Deleted Items; Delete in Deleted Items deletes permanently.
	if res, err := r.Apply(tctx(t), backend.Delete{At: loc}); err != nil || res.Outcome != backend.Done {
		t.Fatalf("Delete from Inbox = %+v, %v", res, err)
	}
	trashLoc := backend.Location{Folder: trashRef, Ref: backend.MessageRef(id), ServerID: id}
	if res, err := r.Apply(tctx(t), backend.Delete{At: trashLoc}); err != nil || res.Outcome != backend.Done {
		t.Fatalf("Delete from Trash = %+v, %v", res, err)
	}
	if _, err := r.Fetch(tctx(t), backend.ContentRequest{Location: trashLoc}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("Fetch after permanent delete = %v, want ErrNotFound", err)
	}
}

// A folder created right after GET /me/mailFolders finishes (before folders()
// returns) remains ahead of the saved hierarchy delta cursor and is discovered
// together with its mail on the very next sync round.
func TestGraphFolderCreatedAfterListingObservedNextSync(t *testing.T) {
	s := testmail.StartGraph(t)
	r := graphReceiver(t, s, nil)

	createdOnce := false
	var lateFolderID string
	s.SetAfterFolderList(func() {
		if !createdOnce {
			createdOnce = true
			lateFolderID = s.Folder("LateFolder")
			s.Deliver("LateFolder", backendtest.Message("late@example.com", "in late folder"))
		}
	})

	var account backend.Cursor
	states := map[backend.FolderRef]backend.FolderState{}
	syncRound(t, r, &account, states, nil)
	s.SetAfterFolderList(nil)
	if !createdOnce {
		t.Fatal("afterFolderList hook did not fire")
	}

	// Next round must observe LateFolder via the saved folder delta cursor.
	cs, _ := syncRound(t, r, &account, states, nil)
	lateRef := backend.FolderRef(lateFolderID)
	var seenFolder, seenMsg bool
	for _, c := range cs {
		switch c := c.(type) {
		case backend.FolderUpdated:
			if c.Ref == lateRef && c.Name == "LateFolder" {
				seenFolder = true
			}
		case backend.MessageAppeared:
			if c.Folder == lateRef {
				seenMsg = true
			}
		}
	}
	if !seenFolder || !seenMsg {
		t.Fatalf("next sync changes = %+v, want LateFolder FolderUpdated and MessageAppeared", cs)
	}
}

// Syncing with Newest: 1 and then widening the window to all mail backfills the
// older message excluded during the initial sync without resetting or Manual LoadOlder.
func TestGraphWindowWidens(t *testing.T) {
	s := testmail.StartGraph(t)
	oldID := s.Deliver("INBOX", backendtest.Message("old@example.com", "old"))
	newID := s.Deliver("INBOX", backendtest.Message("new@example.com", "new"))
	r := graphReceiver(t, s, nil)

	var account backend.Cursor
	states := map[backend.FolderRef]backend.FolderState{}
	round := func(w backend.Window, local backend.Local) []backend.MessageRef {
		t.Helper()
		var got []backend.MessageRef
		_, err := r.Sync(tctx(t), backend.SyncRequest{
			Account:      account,
			Folders:      maps.Clone(states),
			Window:       w,
			Local:        local,
			HeaderFields: []string{"Message-ID", "Subject"},
			TextBytes:    4096,
		}, func(p backend.Page) error {
			for _, c := range p.Changes {
				if m, ok := c.(backend.MessageAppeared); ok {
					got = append(got, m.Ref)
				}
			}
			if p.Account != "" {
				account = p.Account
			}
			for f, st := range p.Folders {
				states[f] = st
			}
			return nil
		})
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
		return got
	}

	got := round(backend.Window{Newest: 1}, nil)
	if !slices.Equal(got, []backend.MessageRef{backend.MessageRef(newID)}) {
		t.Fatalf("Newest: 1 got %v, want [%s]", got, newID)
	}
	local := held{newID: true}
	if got = round(backend.Window{Newest: 1}, local); len(got) != 0 {
		t.Fatalf("same window again got %v, want nothing", got)
	}
	if got = round(backend.Window{}, local); !slices.Equal(got, []backend.MessageRef{backend.MessageRef(oldID)}) {
		t.Fatalf("widened to all mail got %v, want [%s]", got, oldID)
	}
	local[oldID] = true
	if got = round(backend.Window{}, local); len(got) != 0 {
		t.Fatalf("all mail again got %v, want nothing", got)
	}
}

// Each delta page commits with the nextLink after it: a round cut off midway
// resumes there, reporting no message twice and the rest still as backfill.
func TestGraphDeltaResumesAfterInterruptedRound(t *testing.T) {
	s := testmail.StartGraph(t)
	s.SetDeltaPageSize(2)
	for i := range 5 {
		s.Deliver("INBOX", backendtest.Message(fmt.Sprintf("m%d@example.com", i), "subj"))
	}
	inbox := backend.FolderRef(s.Folder("INBOX"))
	r := graphReceiver(t, s, nil)
	var account backend.Cursor
	states := map[backend.FolderRef]backend.FolderState{}
	seen := map[backend.MessageRef]int{}
	cut := errors.New("cut off")
	inboxPages := 0
	_, err := r.Sync(tctx(t), backend.SyncRequest{Folders: states}, func(p backend.Page) error {
		if _, ok := p.Folders[inbox]; ok {
			if inboxPages++; inboxPages == 2 {
				return cut
			}
		}
		for _, c := range p.Changes {
			if m, ok := c.(backend.MessageAppeared); ok && m.Folder == inbox {
				seen[m.Ref]++
			}
		}
		if p.Account != "" {
			account = p.Account
		}
		maps.Copy(states, p.Folders)
		return nil
	})
	if !errors.Is(err, cut) {
		t.Fatalf("Sync = %v, want the commit's error", err)
	}
	if st := states[inbox]; st.Cursor == "" || st.Synced {
		t.Fatalf("inbox state after one page = %+v, want the nextLink, unsynced", st)
	}
	cs, _ := syncRound(t, r, &account, states, nil)
	for _, c := range cs {
		if m, ok := c.(backend.MessageAppeared); ok && m.Folder == inbox {
			if !m.Backfill {
				t.Errorf("resumed first sync reported %s as a new arrival", m.Ref)
			}
			seen[m.Ref]++
		}
	}
	if len(seen) != 5 {
		t.Errorf("messages seen = %d, want 5", len(seen))
	}
	for ref, n := range seen {
		if n != 1 {
			t.Errorf("%s reported %d times", ref, n)
		}
	}
	if !states[inbox].Synced {
		t.Errorf("inbox not synced after the resumed round")
	}
}

// Delta requests ask for big pages: Graph's own is 10 messages.
func TestGraphDeltaAsksForLargePages(t *testing.T) {
	s := testmail.StartGraph(t)
	for i := range 30 {
		s.Deliver("INBOX", backendtest.Message(fmt.Sprintf("m%d@example.com", i), "subj"))
	}
	inbox := backend.FolderRef(s.Folder("INBOX"))
	r := graphReceiver(t, s, nil)
	inboxPages := 0
	_, err := r.Sync(tctx(t), backend.SyncRequest{Folders: map[backend.FolderRef]backend.FolderState{}}, func(p backend.Page) error {
		if _, ok := p.Folders[inbox]; ok {
			inboxPages++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if inboxPages != 1 {
		t.Errorf("inbox delta took %d pages, want 1", inboxPages)
	}
}

// A purge throttled by Graph asks Confirm again before retrying: one called
// off during the back-off deletes nothing.
func TestGraphPurgeConfirmsBeforeThrottledRetry(t *testing.T) {
	s := testmail.StartGraph(t)
	id := s.Deliver("INBOX", backendtest.Message("p@example.com", "purge"))
	r := graphReceiver(t, s, func(context.Context, time.Duration) error { return nil })
	asked := 0
	res, err := r.Apply(tctx(t), backend.Purge{
		At: backend.Location{Folder: backend.FolderRef(s.Folder("INBOX")), Ref: backend.MessageRef(id), ServerID: id},
		Confirm: func() bool {
			if asked++; asked == 1 {
				s.Throttle(1, http.StatusTooManyRequests, "1") // the permanentDelete that follows
				return true
			}
			return false // restored during the back-off
		},
	})
	if err != nil || res.Outcome != backend.Failed || res.Reason != backend.Cancelled {
		t.Fatalf("Purge = %+v, %v; want Failed/Cancelled", res, err)
	}
	if asked != 2 {
		t.Errorf("Confirm asked %d times, want 2", asked)
	}
	if _, _, _, _, exists := s.MessageState(id); !exists {
		t.Fatal("the purge went ahead after Confirm called it off")
	}
}
