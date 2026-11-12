package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/rpc"
	"github.com/godwit-email/godwit/rpcbind"
)

type fakeNotifySink struct {
	mu         sync.Mutex
	supported  bool
	codeBanner bool
	notifyErr  error
	notes      []Notification
}

func (s *fakeNotifySink) NotificationsSupported() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.supported
}

func (s *fakeNotifySink) SignInCodeBannerSupported() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.codeBanner
}

func (s *fakeNotifySink) Notify(n Notification) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notes = append(s.notes, n)
	return s.notifyErr
}

func (s *fakeNotifySink) Notes() []Notification {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.notes)
}

type standaloneNotifierRig struct {
	t          *testing.T
	env        *coretest.Env
	acc        core.Account
	sink       *fakeNotifySink
	clock      *fakeClock
	notifier   *MailNotifier
	foreground atomic.Bool
	received   atomic.Int64

	logMu sync.Mutex
	logs  []string
}

func newNotifierRig(t *testing.T, set func(*core.Settings)) *standaloneNotifierRig {
	t.Helper()
	env := coretest.New(t, coretest.WithoutAutoSync())
	acc := env.AddAccount(t, "alice@example.com")
	if err := env.Svc.SyncNow(context.Background(), acc.ID); err != nil {
		t.Fatal(err)
	}
	if set != nil {
		st, err := env.Svc.GetSettings(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		set(&st)
		if _, err := env.Svc.SetSettings(context.Background(), st); err != nil {
			t.Fatal(err)
		}
	}
	r := &standaloneNotifierRig{
		t:     t,
		env:   env,
		acc:   acc,
		sink:  &fakeNotifySink{supported: true, codeBanner: true},
		clock: newFakeClock(),
	}
	r.clock.now = time.Now().Truncate(time.Second)
	r.notifier = NewMailNotifier(MailNotifierDeps{
		Svc:        env.Svc,
		Sink:       r.sink,
		Clock:      r.clock,
		Foreground: r.foreground.Load,
		Language:   func() string { return "en-US" },
		Logf: func(format string, args ...any) {
			r.logMu.Lock()
			defer r.logMu.Unlock()
			r.logs = append(r.logs, fmt.Sprintf(format, args...))
		},
	})
	ch, unsub := env.Svc.Subscribe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range ch {
			r.notifier.HandleEvent(ev)
			if ev.Type == events.MessageReceived && len(ev.MessageIDs) > 0 {
				r.received.Add(1)
			}
		}
	}()
	t.Cleanup(func() {
		unsub()
		<-done
		r.notifier.Close()
	})
	return r
}

func (r *standaloneNotifierRig) logged() []string {
	r.logMu.Lock()
	defer r.logMu.Unlock()
	return slices.Clone(r.logs)
}

func (r *standaloneNotifierRig) deliver(folder string, raw ...[]byte) {
	r.t.Helper()
	before := r.received.Load()
	for _, msg := range raw {
		r.env.Deliver(r.t, r.acc.ID, folder, msg)
	}
	want := before + int64(len(raw))
	deadline := time.Now().Add(5 * time.Second)
	for r.received.Load() < want {
		if time.Now().After(deadline) {
			r.t.Fatalf("timed out waiting for %d MessageReceived events (got %d)", want, r.received.Load())
		}
		time.Sleep(time.Millisecond)
	}
}

func (r *standaloneNotifierRig) waitNotes(want int) []Notification {
	r.t.Helper()
	r.clock.advance(NotifyWait)
	if want > 0 {
		deadline := time.Now().Add(5 * time.Second)
		for len(r.sink.Notes()) < want {
			if time.Now().After(deadline) {
				r.t.Fatalf("timed out waiting for %d notifications; got %+v", want, r.sink.Notes())
			}
			time.Sleep(time.Millisecond)
		}
	}
	time.Sleep(20 * time.Millisecond)
	return r.sink.Notes()
}

func TestMailNotifierBatchesInboxArrivals(t *testing.T) {
	r := newNotifierRig(t, func(st *core.Settings) { st.NotifyPreview = false })

	// Unrelated or empty events do nothing.
	r.notifier.HandleEvent(events.Event{Type: events.MessagesChanged, AccountID: r.acc.ID, MessageIDs: []int64{1}})
	r.notifier.HandleEvent(events.Event{Type: events.MessageReceived, AccountID: r.acc.ID})

	r.deliver("INBOX",
		coretest.Msg("Bob <bob@example.com>", r.acc.Email, "First", "body one"),
		coretest.Msg("Bob <bob@example.com>", r.acc.Email, "Second", "body two"),
	)
	r.deliver("Archive",
		coretest.Msg("Bob <bob@example.com>", r.acc.Email, "Archived", "body three"),
	)

	// Before NotifyWait elapses, arrivals stay batched.
	r.clock.advance(NotifyWait / 2)
	time.Sleep(20 * time.Millisecond)
	if got := r.sink.Notes(); len(got) != 0 {
		t.Fatalf("notifications fired before NotifyWait: %+v", got)
	}

	notes := r.waitNotes(2)
	if len(notes) != 2 {
		t.Fatalf("notes = %+v, want 2 inbox notifications", notes)
	}
	for _, n := range notes {
		if n.Title != "Godwit" || n.Body != "New mail" || n.Subtitle != "" || n.Sender != "" || n.ThreadID != 0 || !strings.HasPrefix(n.ID, "message:") || n.MessageID == 0 {
			t.Fatalf("unexpected preview-off notification: %+v", n)
		}
	}
	if notes[0].ID == notes[1].ID {
		t.Fatalf("duplicate notification ID %q", notes[0].ID)
	}
}

func TestMailNotifierPreviewOn(t *testing.T) {
	r := newNotifierRig(t, func(st *core.Settings) { st.NotifyPreview = true })

	r.deliver("INBOX",
		coretest.Msg("Bob Builder <bob@example.com>", r.acc.Email, "Lunch plans", "Meet at noon"),
		coretest.Msg("carol@example.com", r.acc.Email, "Report", "Attached summary"),
	)

	notes := r.waitNotes(2)
	if len(notes) != 2 {
		t.Fatalf("notes = %+v, want 2", notes)
	}
	if notes[0].Title != "Bob Builder" || notes[0].Subtitle != "Lunch plans" || !strings.Contains(notes[0].Body, "Meet at noon") || notes[0].Sender != "Bob Builder <bob@example.com>" || notes[0].ThreadID == 0 || notes[0].MessageID == 0 {
		t.Fatalf("first preview note = %+v", notes[0])
	}
	if notes[1].Title != "carol@example.com" || notes[1].Subtitle != "Report" || !strings.Contains(notes[1].Body, "Attached summary") || notes[1].Sender != "carol@example.com" || notes[1].ThreadID == 0 {
		t.Fatalf("second preview note = %+v", notes[1])
	}
}

func TestMailNotifierSuppression(t *testing.T) {
	t.Run("window in foreground", func(t *testing.T) {
		r := newNotifierRig(t, nil)
		r.foreground.Store(true)
		r.deliver("INBOX", coretest.Msg("bob@example.com", r.acc.Email, "Seen", "body"))
		if notes := r.waitNotes(0); len(notes) != 0 {
			t.Fatalf("foreground notified: %+v", notes)
		}
		// Leaving foreground lets subsequent batches notify.
		r.foreground.Store(false)
		r.deliver("INBOX", coretest.Msg("bob@example.com", r.acc.Email, "Background", "body"))
		if notes := r.waitNotes(1); len(notes) != 1 || notes[0].Subtitle != "Background" {
			t.Fatalf("background notes = %+v", notes)
		}
	})

	t.Run("setting disabled", func(t *testing.T) {
		r := newNotifierRig(t, func(st *core.Settings) { st.NotifyNewMail = false })
		r.deliver("INBOX", coretest.Msg("bob@example.com", r.acc.Email, "Quiet", "body"))
		if notes := r.waitNotes(0); len(notes) != 0 {
			t.Fatalf("disabled setting notified: %+v", notes)
		}
	})

	t.Run("platform unsupported", func(t *testing.T) {
		r := newNotifierRig(t, nil)
		r.sink.mu.Lock()
		r.sink.supported = false
		r.sink.mu.Unlock()
		r.deliver("INBOX", coretest.Msg("bob@example.com", r.acc.Email, "Unsupported", "body"))
		if notes := r.waitNotes(0); len(notes) != 0 || len(r.logged()) != 0 {
			t.Fatalf("unsupported notes = %+v, logs = %q", notes, r.logged())
		}
	})
}

func (r *standaloneNotifierRig) roleCount(role string) int {
	r.t.Helper()
	fs, err := r.env.Svc.ListFolders(context.Background(), r.acc.ID)
	if err != nil {
		r.t.Fatal(err)
	}
	i := slices.IndexFunc(fs, func(f core.Folder) bool { return f.Role == role })
	if i < 0 {
		r.t.Fatalf("no folder with role %q", role)
	}
	page, err := r.env.Svc.ListMessages(context.Background(), core.View{FolderID: fs[i].ID}, "", 200)
	if err != nil {
		r.t.Fatal(err)
	}
	return len(page.Items)
}

func (r *standaloneNotifierRig) waitUntil(desc string, cond func() bool) {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			r.t.Fatalf("timed out waiting for %s", desc)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestMailNotifierBlockedSenders(t *testing.T) {
	t.Run("moves to junk even while foreground", func(t *testing.T) {
		r := newNotifierRig(t, nil)
		if err := r.env.IMAP(r.acc.ID).User.Create("Junk", nil); err != nil {
			t.Fatal(err)
		}
		if err := r.env.Svc.SyncNow(context.Background(), r.acc.ID); err != nil {
			t.Fatal(err)
		}
		if err := r.env.Svc.BlockSender(context.Background(), "spammer@example.com"); err != nil {
			t.Fatal(err)
		}
		r.foreground.Store(true)

		r.deliver("INBOX", coretest.Msg("Spammer <SPAMMER@example.com>", r.acc.Email, "Win a prize", "click here"))
		r.clock.advance(NotifyWait)
		r.waitUntil("blocked mail moved to junk", func() bool { return r.roleCount("junk") == 1 })
		if got := r.roleCount("inbox"); got != 0 {
			t.Fatalf("inbox count = %d, want 0", got)
		}
		if notes := r.sink.Notes(); len(notes) != 0 {
			t.Fatalf("notes = %+v, want none", notes)
		}
	})

	t.Run("moves blocked sender while notifying legitimate mail in same batch", func(t *testing.T) {
		r := newNotifierRig(t, nil)
		if err := r.env.IMAP(r.acc.ID).User.Create("Junk", nil); err != nil {
			t.Fatal(err)
		}
		if err := r.env.Svc.SyncNow(context.Background(), r.acc.ID); err != nil {
			t.Fatal(err)
		}
		if err := r.env.Svc.BlockSender(context.Background(), "spammer@example.com"); err != nil {
			t.Fatal(err)
		}

		r.deliver("INBOX",
			coretest.Msg("Spammer <spammer@example.com>", r.acc.Email, "Spam", "buy now"),
			coretest.Msg("Friend <friend@example.com>", r.acc.Email, "Hello", "real mail"),
		)
		notes := r.waitNotes(1)
		r.waitUntil("blocked mail moved to junk", func() bool { return r.roleCount("junk") == 1 })
		if len(notes) != 1 || notes[0].Subtitle != "Hello" {
			t.Fatalf("notes = %+v, want only Friend's mail", notes)
		}
	})

	t.Run("without junk folder keeps mail in inbox unnotified and logs", func(t *testing.T) {
		r := newNotifierRig(t, nil)
		if err := r.env.Svc.BlockSender(context.Background(), "spammer@example.com"); err != nil {
			t.Fatal(err)
		}

		r.deliver("INBOX", coretest.Msg("Spammer <spammer@example.com>", r.acc.Email, "Private spam subject", "x"))
		notes := r.waitNotes(0)
		r.waitUntil("junk move failure logged", func() bool { return len(r.logged()) > 0 })
		if len(notes) != 0 {
			t.Fatalf("notes = %+v, want none", notes)
		}
		if got := r.roleCount("inbox"); got != 1 {
			t.Fatalf("inbox count = %d, want 1 preserved", got)
		}
		logs := strings.Join(r.logged(), "\n")
		if !strings.Contains(logs, "junk") || strings.Contains(logs, "Private spam subject") || strings.Contains(logs, "spammer@example.com") {
			t.Fatalf("unexpected log %q", logs)
		}
	})
}

func TestMailNotifierSignInCodes(t *testing.T) {
	t.Run("suppresses newest sign-in code when banner supported and preview on", func(t *testing.T) {
		r := newNotifierRig(t, func(st *core.Settings) { st.NotifyPreview = true })
		r.deliver("INBOX",
			coretest.Msg("first@example.com", r.acc.Email, "Verification code: 111111", ""),
			coretest.Msg("second@example.com", r.acc.Email, "Verification code: 222222", ""),
		)
		notes := r.waitNotes(1)
		if len(notes) != 1 || notes[0].Subtitle != "Verification code: 111111" {
			t.Fatalf("notes = %+v, want only older code notified while newest uses banner", notes)
		}
	})

	t.Run("keeps sign-in code notification when banner unsupported", func(t *testing.T) {
		r := newNotifierRig(t, func(st *core.Settings) { st.NotifyPreview = true })
		r.sink.mu.Lock()
		r.sink.codeBanner = false
		r.sink.mu.Unlock()
		r.deliver("INBOX", coretest.Msg("login@example.com", r.acc.Email, "Verification code: 333333", ""))
		notes := r.waitNotes(1)
		if len(notes) != 1 || notes[0].Subtitle != "Verification code: 333333" {
			t.Fatalf("notes = %+v, want ordinary notification when banner unsupported", notes)
		}
	})

	t.Run("keeps sign-in code notification when preview off", func(t *testing.T) {
		r := newNotifierRig(t, func(st *core.Settings) { st.NotifyPreview = false })
		r.deliver("INBOX", coretest.Msg("login@example.com", r.acc.Email, "Verification code: 444444", ""))
		notes := r.waitNotes(1)
		if len(notes) != 1 || notes[0].Body != "New mail" {
			t.Fatalf("notes = %+v, want generic notification when preview off", notes)
		}
	})
}

func TestMailNotifierErrorLoggingAndClose(t *testing.T) {
	t.Run("logs notify error without leaking sender or subject", func(t *testing.T) {
		r := newNotifierRig(t, func(st *core.Settings) { st.NotifyPreview = true })
		r.sink.mu.Lock()
		r.sink.notifyErr = errors.New("os notification denied")
		r.sink.mu.Unlock()

		r.deliver("INBOX", coretest.Msg("Alice Secret <secret@example.com>", r.acc.Email, "Confidential Subject", "Confidential Body"))
		r.waitNotes(1)
		r.waitUntil("error logged", func() bool { return len(r.logged()) > 0 })
		logs := strings.Join(r.logged(), "\n")
		if !strings.Contains(logs, "os notification denied") || strings.Contains(logs, "Secret") || strings.Contains(logs, "Confidential") {
			t.Fatalf("unsafe or missing log: %q", logs)
		}
	})

	t.Run("close cancels pending batch", func(t *testing.T) {
		r := newNotifierRig(t, nil)
		r.deliver("INBOX", coretest.Msg("bob@example.com", r.acc.Email, "Before close", "body"))
		r.notifier.Close()
		if notes := r.waitNotes(0); len(notes) != 0 {
			t.Fatalf("closed notifier still notified: %+v", notes)
		}
	})
}

type stubNotifierSvc struct {
	folders  []core.Folder
	threads  map[int64]core.Thread
	blocked  []string
	moved    []int64
	settings core.Settings
}

func (s *stubNotifierSvc) ListFolders(context.Context, int64) ([]core.Folder, error) {
	return s.folders, nil
}
func (s *stubNotifierSvc) GetThread(_ context.Context, id int64) (core.Thread, error) {
	return s.threads[id], nil
}
func (s *stubNotifierSvc) BlockedSenders(context.Context) ([]string, error) {
	return s.blocked, nil
}
func (s *stubNotifierSvc) MoveToRole(_ context.Context, ids []int64, _ string) error {
	s.moved = append(s.moved, ids...)
	return nil
}
func (s *stubNotifierSvc) RecentSignInCodes(context.Context, time.Time) ([]core.SignInCode, error) {
	return nil, nil
}
func (s *stubNotifierSvc) GetSettings(context.Context) (core.Settings, error) {
	return s.settings, nil
}

func TestMailNotifierNarrowServiceInterface(t *testing.T) {
	msg := core.MessageSummary{ID: 42, ThreadID: 10, FolderID: 1, From: "Ada <ada@example.com>", Subject: "Hi"}
	svc := &stubNotifierSvc{
		folders:  []core.Folder{{ID: 1, Role: "inbox"}},
		threads:  map[int64]core.Thread{10: {ID: 10, Messages: []core.MessageSummary{msg}}, 11: {ID: 11, Messages: []core.MessageSummary{msg}}},
		settings: core.Settings{NotifyNewMail: true, NotifyPreview: false, UILanguage: "zh-Hans"},
	}
	sink := &fakeNotifySink{supported: true}
	clock := newFakeClock()
	n := NewMailNotifier(MailNotifierDeps{
		Svc:      svc,
		Sink:     sink,
		Clock:    clock,
		Language: func() string { return "en-US" },
	})
	defer n.Close()

	// Message 42 referenced across two gathered threads notifies once, using UILanguage.
	n.HandleEvent(events.Event{Type: events.MessageReceived, AccountID: 7, MessageIDs: []int64{42}, ThreadIDs: []int64{10, 11}})
	clock.advance(NotifyWait)
	deadline := time.Now().Add(5 * time.Second)
	for len(sink.Notes()) < 1 {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for notification")
		}
		time.Sleep(time.Millisecond)
	}
	notes := sink.Notes()
	if len(notes) != 1 || notes[0].ID != "message:42" || notes[0].Body != "新邮件" {
		t.Fatalf("notes = %+v, want single zh-Hans notification", notes)
	}
}

func TestMailNotifierOverUISocket(t *testing.T) {
	ctx := t.Context()
	r := newNotifierRig(t, func(st *core.Settings) { st.NotifyPreview = true })
	// Re-wire Foreground directly to core.Service.UIForeground, as the backend process does.
	r.notifier.foreground = r.env.Svc.UIForeground

	sockDir, err := os.MkdirTemp("", "gw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	srv, err := rpcbind.Listen(sockDir, r.env.Svc, "test")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ctx)
	t.Cleanup(func() { srv.Close() })

	dialUI := func() *rpc.Client {
		t.Helper()
		c, err := rpc.Dial(sockDir, rpc.RoleUI, "ui-test", rpcbind.NewCodec())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}

	// Zero RoleUI connections: treated as not in foreground, so incoming mail notifies.
	r.deliver("INBOX", coretest.Msg("bob@example.com", r.acc.Email, "No UI", "body"))
	if notes := r.waitNotes(1); len(notes) != 1 || notes[0].Subtitle != "No UI" {
		t.Fatalf("notes without UI = %+v", notes)
	}

	// ui1 reports foreground: incoming mail produces NO notification.
	ui1 := dialUI()
	if err := ui1.Call(ctx, "SetUIForeground", nil, true); err != nil {
		t.Fatal(err)
	}
	r.deliver("INBOX", coretest.Msg("bob@example.com", r.acc.Email, "Suppressed", "body"))
	if notes := r.waitNotes(1); len(notes) != 1 {
		t.Fatalf("foreground UI did not suppress notification: %+v", notes)
	}

	// ui2 connects and reports background: still suppressed while ui1 is foreground.
	ui2 := dialUI()
	if err := ui2.Call(ctx, "SetUIForeground", nil, false); err != nil {
		t.Fatal(err)
	}
	r.deliver("INBOX", coretest.Msg("bob@example.com", r.acc.Email, "Still suppressed", "body"))
	if notes := r.waitNotes(1); len(notes) != 1 {
		t.Fatalf("union foreground did not suppress notification: %+v", notes)
	}

	// ui1 disconnects while ui2 stays in background: notifications resume.
	if err := ui1.Close(); err != nil {
		t.Fatal(err)
	}
	r.waitUntil("ui1 foreground cleared", func() bool { return !r.env.Svc.UIForeground() })
	r.deliver("INBOX", coretest.Msg("bob@example.com", r.acc.Email, "After ui1 drop", "body"))
	if notes := r.waitNotes(2); len(notes) != 2 || notes[1].Subtitle != "After ui1 drop" {
		t.Fatalf("notes after ui1 disconnect = %+v", notes)
	}

	// ui2 comes to foreground (suppresses), then disconnects leaving zero UI connections (resumes).
	if err := ui2.Call(ctx, "SetUIForeground", nil, true); err != nil {
		t.Fatal(err)
	}
	r.deliver("INBOX", coretest.Msg("bob@example.com", r.acc.Email, "ui2 foreground", "body"))
	if notes := r.waitNotes(2); len(notes) != 2 {
		t.Fatalf("ui2 foreground did not suppress: %+v", notes)
	}
	if err := ui2.Close(); err != nil {
		t.Fatal(err)
	}
	r.waitUntil("all UI connections closed", func() bool { return !r.env.Svc.UIConnected() })
	r.deliver("INBOX", coretest.Msg("bob@example.com", r.acc.Email, "After all UI gone", "body"))
	if notes := r.waitNotes(3); len(notes) != 3 || notes[2].Subtitle != "After all UI gone" {
		t.Fatalf("notes after all UI gone = %+v", notes)
	}
}
