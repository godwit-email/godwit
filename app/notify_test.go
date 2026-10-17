package app

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
)

// notifyHarness is a mail harness with one account, no background sync and
// notifications supported; set changes the settings before the App starts.
func notifyHarness(t *testing.T, set func(*core.Settings)) (*harness, core.Account) {
	t.Helper()
	var acc core.Account
	h := newHarnessWith(t, []coretest.Option{coretest.WithoutAutoSync()}, func(e *coretest.Env) {
		acc = e.AddAccount(t, "alice@example.com")
		if err := e.Svc.SyncNow(context.Background(), acc.ID); err != nil { // the first sync is not new mail
			t.Fatal(err)
		}
		if set != nil {
			st, err := e.Svc.GetSettings(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			set(&st)
			if _, err := e.Svc.SetSettings(context.Background(), st); err != nil {
				t.Fatal(err)
			}
		}
	})
	h.plat.mu.Lock()
	h.plat.supported = true
	h.plat.mu.Unlock()
	h.until("the mail page", func() bool { return h.app.loaded && h.app.route.Page == PageMail })
	return h, acc
}

// arrive delivers subjects to the INBOX one sync each and waits until the
// App has seen every arrival.
func (h *harness) arrive(acc core.Account, subjects ...string) {
	h.t.Helper()
	h.arriveIn(acc, "INBOX", subjects...)
}

func (h *harness) arriveIn(acc core.Account, folder string, subjects ...string) {
	h.t.Helper()
	for _, s := range subjects {
		h.env.Deliver(h.t, acc.ID, folder, coretest.Msg("Bob Stone <bob@example.com>", acc.Email, s, "body of "+s))
	}
	h.until("the arrivals", func() bool { got := h.app.newMail[acc.ID]; return got != nil && len(got.msgs) == len(subjects) })
}

// notesAfter lets the arrival window pass and returns the notifications
// shown, waiting for want of them.
func (h *harness) notesAfter(want int) []Notification {
	h.t.Helper()
	h.advance(notifyWait)
	if want > 0 {
		h.until("the notification", func() bool { return len(h.plat.Notes()) >= want })
	}
	h.advance(notifyWait) // nothing more comes
	time.Sleep(20 * time.Millisecond)
	h.drain()
	return h.plat.Notes()
}

func TestNotifyEachMessage(t *testing.T) {
	t.Run("three in a second", func(t *testing.T) {
		h, acc := notifyHarness(t, func(st *core.Settings) { st.NotifyPreview = false })
		h.arrive(acc, "One", "Two", "Three")
		notes := h.notesAfter(3)
		if len(notes) != 3 {
			t.Fatalf("notes %+v, want one per message", notes)
		}
		for _, n := range notes {
			if n.Body != "新邮件" || n.Title != "Godwit" || n.Subtitle != "" || !strings.HasPrefix(n.ID, "message:") || n.MessageID == 0 {
				t.Fatalf("note %+v", n)
			}
		}
	})
	t.Run("preview off", func(t *testing.T) {
		h, acc := notifyHarness(t, func(st *core.Settings) { st.NotifyPreview = false })
		h.arrive(acc, "Secret plans")
		notes := h.notesAfter(1)
		if len(notes) != 1 || notes[0].Title != "Godwit" || notes[0].Body != "新邮件" || notes[0].Sender != "" || notes[0].ThreadID != 0 {
			t.Fatalf("notes %+v, want neither sender nor subject", notes)
		}
	})
	t.Run("preview on, several", func(t *testing.T) {
		h, acc := notifyHarness(t, func(st *core.Settings) { st.NotifyPreview = true })
		for _, subject := range []string{"One", "Two"} {
			h.put(acc, "INBOX", coretest.Msg("Bob Stone <bob@example.com>", acc.Email, subject, "body of "+subject))
		}
		h.syncNow(acc)
		h.until("both arrivals", func() bool { return h.app.newMail[acc.ID] != nil && len(h.app.newMail[acc.ID].msgs) == 2 })
		notes := h.notesAfter(2)
		if len(notes) != 2 {
			t.Fatalf("notes %+v", notes)
		}
		for i, subject := range []string{"One", "Two"} {
			n := notes[i]
			if n.Title != "Bob Stone" || n.Subtitle != subject || !strings.Contains(n.Body, "body of "+subject) || n.MessageID == 0 || n.ThreadID == 0 || n.Sender != "Bob Stone <bob@example.com>" {
				t.Fatalf("note %+v for %q", n, subject)
			}
		}
		if notes[0].ID == notes[1].ID {
			t.Fatal("notifications share an ID")
		}
	})
	t.Run("inbox only", func(t *testing.T) {
		h, acc := notifyHarness(t, func(st *core.Settings) { st.NotifyPreview = false })
		h.arriveIn(acc, "Archive", "Filed away")
		if notes := h.notesAfter(0); len(notes) != 0 {
			t.Fatalf("notes %+v for mail outside the inbox", notes)
		}
		h.env.Deliver(t, acc.ID, "Archive", coretest.Msg("bob@example.com", acc.Email, "Filed", "x"))
		h.env.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", acc.Email, "Hello", "x"))
		h.until("both arrivals", func() bool { return h.app.newMail[acc.ID] != nil && len(h.app.newMail[acc.ID].msgs) == 2 })
		if notes := h.notesAfter(1); len(notes) != 1 || notes[0].Body != "新邮件" || !strings.HasPrefix(notes[0].ID, "message:") {
			t.Fatalf("notes %+v, want the inbox one alone", notes)
		}
	})
	t.Run("failure logged", func(t *testing.T) {
		h, acc := notifyHarness(t, func(st *core.Settings) { st.NotifyPreview = true })
		h.plat.mu.Lock()
		h.plat.notifyErr = errors.New("denied")
		h.plat.mu.Unlock()
		h.arrive(acc, "Private subject", "Another private subject")
		h.notesAfter(2)
		h.until("the log line", func() bool { return len(h.logged()) > 0 })
		if l := strings.Join(h.logged(), "\n"); !strings.Contains(l, "denied") || strings.Contains(l, "Private subject") || strings.Contains(l, "Bob") {
			t.Fatalf("log %q", l)
		}
	})
	t.Run("unsupported is quiet", func(t *testing.T) {
		h, acc := notifyHarness(t, nil)
		h.plat.mu.Lock()
		h.plat.supported = false
		h.plat.mu.Unlock()
		h.arrive(acc, "Nobody sees")
		if notes := h.notesAfter(0); len(notes) != 0 || len(h.logged()) != 0 {
			t.Fatalf("notes %+v, logs %q", notes, h.logged())
		}
	})
	t.Run("preview on", func(t *testing.T) {
		h, acc := notifyHarness(t, func(st *core.Settings) { st.NotifyPreview = true })
		h.arrive(acc, "Lunch")
		notes := h.notesAfter(1)
		if len(notes) != 1 || notes[0].Title != "Bob Stone" || notes[0].Subtitle != "Lunch" || !strings.Contains(notes[0].Body, "body of Lunch") || notes[0].MessageID == 0 || notes[0].Sender != "Bob Stone <bob@example.com>" || notes[0].ThreadID == 0 {
			t.Fatalf("notes %+v", notes)
		}
	})
	t.Run("off", func(t *testing.T) {
		h, acc := notifyHarness(t, func(st *core.Settings) { st.NotifyNewMail = false })
		h.arrive(acc, "Quiet")
		if notes := h.notesAfter(0); len(notes) != 0 {
			t.Fatalf("notes %+v with notifications off", notes)
		}
	})
	t.Run("foreground", func(t *testing.T) {
		h, acc := notifyHarness(t, nil)
		h.app.SetForeground(true)
		h.arrive(acc, "Seen it")
		if notes := h.notesAfter(0); len(notes) != 0 {
			t.Fatalf("notes %+v while in front", notes)
		}
	})
}

func TestNotificationClickOpens(t *testing.T) {
	h, acc := notifyHarness(t, func(st *core.Settings) { st.NotifyPreview = true })
	h.arrive(acc, "Open me")
	notes := h.notesAfter(1)
	h.app.NotificationClicked(notes[0].ID)
	h.until("the message open", func() bool {
		return h.app.route.Page == PageMail && h.app.route.ThreadID != 0 && h.tt.HasText("body of Open me")
	})
	if !h.hasCall("ShowWindow") {
		t.Fatal("the click did not show the window")
	}

	h.arrive(acc, "A", "B")
	notes = h.notesAfter(3)
	for i, subject := range []string{"A", "B"} {
		n := notes[i+1]
		h.app.NotificationClicked(n.ID)
		h.until("the clicked message", func() bool {
			return h.app.route.ThreadID != 0 && h.app.mail.reader.subject == subject
		})
		m, err := h.env.Svc.GetMessage(t.Context(), n.MessageID, false)
		if err != nil || m.ThreadID != h.app.route.ThreadID {
			t.Fatalf("notification %+v opened route %+v: %v", n, h.app.route, err)
		}
	}
	// Notifications left by older versions still open the account's inbox.
	h.app.NotificationClicked("account:" + strconv.FormatInt(acc.ID, 10))
	inbox := h.folderOf(acc, "inbox")
	h.until("the account's inbox", func() bool {
		r := h.app.route
		return r.Page == PageMail && r.View.FolderID == inbox.ID && r.ThreadID == 0
	})
}

// gateClicks makes each notification click's lookup wait for its release.
func gateClicks(a *App) (release func(id string)) {
	gates := map[string]chan struct{}{}
	var mu sync.Mutex
	gate := func(id string) chan struct{} {
		mu.Lock()
		defer mu.Unlock()
		if gates[id] == nil {
			gates[id] = make(chan struct{})
		}
		return gates[id]
	}
	look := a.noteRoute
	a.noteRoute = func(ctx context.Context, id string) (Route, bool, error) {
		<-gate(id)
		return look(ctx, id)
	}
	return func(id string) { close(gate(id)) }
}

func TestNotificationClickLatestWins(t *testing.T) {
	h, acc := notifyHarness(t, nil)
	h.arrive(acc, "First")
	first := h.notesAfter(1)[0].ID
	h.arrive(acc, "Second")
	second := h.notesAfter(2)[1].ID
	release := gateClicks(h.app)

	h.app.NotificationClicked(first)
	h.app.NotificationClicked(second)
	release(second)
	h.until("the second message", func() bool { return h.app.route.ThreadID != 0 && h.app.mail.reader.subject == "Second" })
	want := h.app.route
	release(first) // completes late, out of order
	time.Sleep(20 * time.Millisecond)
	h.drain()
	h.tt.Frame()
	if h.app.route != want {
		t.Fatalf("route %+v, want the later click's %+v", h.app.route, want)
	}

	h.arrive(acc, "Third")
	third := h.notesAfter(3)[2].ID
	h.app.NotificationClicked(third)
	h.app.navigate(Route{Page: PageSettings, Settings: "general"}) // the user moves on meanwhile
	release(third)
	time.Sleep(20 * time.Millisecond)
	h.drain()
	if h.app.route.Page != PageSettings {
		t.Fatalf("route %+v: a click's late result overrode the user's navigation", h.app.route)
	}
}

func TestNotificationClickNowhere(t *testing.T) {
	h, _ := notifyHarness(t, nil)
	h.app.navigate(Route{Page: PageSettings, Settings: "general"})
	h.settle()
	before := h.app.route
	looked := make(chan struct{}, 1)
	look := h.app.noteRoute
	h.app.noteRoute = func(ctx context.Context, id string) (Route, bool, error) {
		defer func() { looked <- struct{}{} }()
		return look(ctx, id)
	}
	for _, id := range []string{"bogus", "message:x", "other:1", "account:999"} { // account 999 has no inbox
		h.app.NotificationClicked(id)
		<-looked
		h.queued("the lookup's result", 1) // nothing else posts: no sync runs
		h.drain()
		if h.app.route != before {
			t.Fatalf("click %q went to %+v", id, h.app.route)
		}
	}
}

func TestNotificationClickBeforeLoad(t *testing.T) {
	var id int64
	h := newHarnessWith(t, []coretest.Option{coretest.WithoutAutoSync()}, func(e *coretest.Env) {
		acc := e.AddAccount(t, "alice@example.com")
		e.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", acc.Email, "Cold start", "body of Cold start"))
		page, err := e.Svc.ListMessages(context.Background(), core.View{Unified: "inbox"}, "", 1)
		if err != nil || len(page.Items) != 1 {
			t.Fatalf("no message: %v", err)
		}
		id = page.Items[0].ID
	})
	h.app.NotificationClicked("message:" + strconv.FormatInt(id, 10)) // the click that launched the app
	// the inbox shows first, its row's snippet the same text as the body
	h.until("the message open", func() bool { return h.app.route.ThreadID != 0 && h.tt.HasText("body of Cold start") })
}

func TestTrayUnread(t *testing.T) {
	h, acc := notifyHarness(t, func(st *core.Settings) { st.UnreadInMenuBar = true })
	h.env.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", acc.Email, "One", "1"))
	h.env.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", acc.Email, "Two", "2"))
	h.waitMail("tray shows 2", func() bool { return slices.Contains(h.plat.Calls(), "SetTrayUnread 2") })

	st := h.app.settings
	st.UnreadInMenuBar = false
	if _, err := h.env.Svc.SetSettings(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	h.waitMail("tray cleared", func() bool { return slices.Contains(h.plat.Calls(), "SetTrayUnread 0") })
}

func TestAppUnreadInitialClear(t *testing.T) {
	for _, tc := range []struct {
		name   string
		on     bool
		unread bool
	}{
		{"empty inbox", true, false},
		{"disabled with unread mail", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarnessWith(t, []coretest.Option{coretest.WithoutAutoSync()}, func(e *coretest.Env) {
				acc := e.AddAccount(t, "alice@example.com")
				if tc.unread {
					e.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", acc.Email, "One", "1"))
				}
				st, err := e.Svc.GetSettings(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				st.UnreadOnAppIcon = tc.on
				if _, err := e.Svc.SetSettings(t.Context(), st); err != nil {
					t.Fatal(err)
				}
			})
			h.until("initial load", func() bool { return h.app.loaded })
			if !slices.Contains(h.plat.Calls(), "SetAppUnread 0") {
				t.Fatal("initial load did not clear the native badge")
			}
		})
	}
}

func TestAppUnread(t *testing.T) {
	h, accs := mailHarness(t, []coretest.Option{coretest.WithoutAutoSync()}, "alice@example.com", "carol@example.com")
	h.put(accs[0], "INBOX", coretest.Msg("bob@example.com", accs[0].Email, "One", "1"))
	h.put(accs[0], "INBOX", coretest.Msg("bob@example.com", accs[0].Email, "Two", "2"))
	h.put(accs[0], "Archive", coretest.Msg("bob@example.com", accs[0].Email, "Archived", "3"))
	h.put(accs[1], "INBOX", coretest.Msg("bob@example.com", accs[1].Email, "Three", "3"))
	for _, acc := range accs {
		h.syncNow(acc)
	}
	h.waitMail("badge shows 3 inbox messages", func() bool { return slices.Contains(h.plat.Calls(), "SetAppUnread 3") })
	if slices.Contains(h.plat.Calls(), "SetTrayUnread 3") {
		t.Fatal("badge enabled the menu bar count")
	}
	for i, acc := range accs {
		page, err := h.env.Svc.ListMessages(t.Context(), core.View{FolderID: h.folderOf(acc, "inbox").ID}, "", 200)
		if err != nil {
			t.Fatal(err)
		}
		var ids []int64
		for _, m := range page.Items {
			ids = append(ids, m.ID)
		}
		start := len(h.plat.Calls())
		if err := h.env.Svc.SetFlags(t.Context(), ids, []string{`\Seen`}, nil); err != nil {
			t.Fatal(err)
		}
		want := "SetAppUnread 1"
		if i == 1 {
			want = "SetAppUnread 0"
		}
		h.waitMail("badge updates after reading", func() bool { return slices.Contains(h.plat.Calls()[start:], want) })
	}
}

func TestAppUnreadSettingIndependent(t *testing.T) {
	h, acc := notifyHarness(t, nil)
	h.env.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", acc.Email, "One", "1"))
	h.waitMail("badge shows 1", func() bool { return slices.Contains(h.plat.Calls(), "SetAppUnread 1") })
	h.openSettings("general")
	start := len(h.plat.Calls())
	h.click("在应用图标显示未读数")
	h.waitMail("badge cleared", func() bool { return slices.Contains(h.plat.Calls()[start:], "SetAppUnread 0") })
	h.until("badge preference saved", func() bool { return h.app.writes.keys["settings"] == nil })
	h.click("在菜单栏显示未读数")
	h.waitMail("tray shows 1", func() bool { return slices.Contains(h.plat.Calls(), "SetTrayUnread 1") })
	start = len(h.plat.Calls())
	h.click("在应用图标显示未读数")
	h.waitMail("badge restored", func() bool { return slices.Contains(h.plat.Calls()[start:], "SetAppUnread 1") })
	start = len(h.plat.Calls())
	h.click("在菜单栏显示未读数")
	h.waitMail("tray cleared", func() bool { return slices.Contains(h.plat.Calls()[start:], "SetTrayUnread 0") })
	if slices.Contains(h.plat.Calls()[start:], "SetAppUnread 0") {
		t.Fatal("disabling the menu bar count cleared the badge")
	}
}

func TestAppUnreadSettingFailureRestoresBadge(t *testing.T) {
	h, acc := notifyHarness(t, nil)
	h.env.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", acc.Email, "One", "1"))
	h.waitMail("badge shows 1", func() bool { return slices.Contains(h.plat.Calls(), "SetAppUnread 1") })
	h.app.saveSettings = func(context.Context, core.Settings) (core.Settings, error) {
		return core.Settings{}, errors.New("disk full")
	}
	h.openSettings("general")
	start := len(h.plat.Calls())
	h.click("在应用图标显示未读数")
	h.see("出错了：disk full")
	var calls []string
	for _, call := range h.plat.Calls()[start:] {
		if strings.HasPrefix(call, "SetAppUnread ") {
			calls = append(calls, call)
		}
	}
	if !slices.Equal(calls, []string{"SetAppUnread 0", "SetAppUnread 1"}) {
		t.Fatalf("badge calls %v, want clear then restore", calls)
	}
}

// slowSettings makes the next settings save wait for release, then fail
// with fail (nil saves), and starts one.
func slowSettings(h *harness, fail error) (started, release chan struct{}) {
	started, release = make(chan struct{}), make(chan struct{})
	save := h.app.saveSettings
	h.app.saveSettings = func(ctx context.Context, st core.Settings) (core.Settings, error) {
		close(started)
		<-release
		if fail != nil {
			return core.Settings{}, fail
		}
		return save(ctx, st)
	}
	st := h.app.settings
	st.Theme = "dark"
	h.app.changeSettings(st)
	return started, release
}

func TestBeforeCloseWaitsAtMostFlushWait(t *testing.T) {
	h, _ := mailHarness(t, nil, "me@example.com")
	started, release := slowSettings(h, nil)
	defer close(release)
	<-started
	closed := false
	h.app.BeforeClose(func() { closed = true })
	h.advance(flushWait)
	h.see("有修改没有保存")
	if closed || !h.hasCall("ShowWindow") {
		t.Fatalf("closed %v, calls %q", closed, h.plat.Calls())
	}
}

func TestBeforeCloseSlowSaveFinishes(t *testing.T) {
	t.Run("while asking", func(t *testing.T) {
		h, _ := mailHarness(t, nil, "me@example.com")
		started, release := slowSettings(h, nil)
		<-started
		closed := false
		h.app.BeforeClose(func() { closed = true })
		h.advance(flushWait)
		h.see("有修改没有保存")
		close(release)
		h.until("closed", func() bool { return closed })
		if h.app.closeAsk {
			t.Fatal("the dialog stays after closing")
		}
	})
	t.Run("after going back", func(t *testing.T) {
		h, _ := mailHarness(t, nil, "me@example.com")
		started, release := slowSettings(h, nil)
		<-started
		closed := false
		h.app.BeforeClose(func() { closed = true })
		h.advance(flushWait)
		h.click("返回")
		close(release)
		h.until("saved", func() bool { return h.app.settings.Theme == "dark" })
		h.advance(time.Second)
		if closed {
			t.Fatal("closed although the user went back")
		}
	})
}

// A write in flight that fails holds the close and asks; going back keeps
// the window, quitting anyway closes.
func TestCloseAsksWhenWriteFails(t *testing.T) {
	h, _ := mailHarness(t, nil, "me@example.com")
	started, release := slowSettings(h, errors.New("disk full"))
	<-started
	closed := false
	h.app.BeforeClose(func() { closed = true })
	close(release)
	h.see("有修改没有保存")
	if closed {
		t.Fatal("closed although a settings write failed")
	}
	h.click("返回")
	h.until("the dialog gone", func() bool { return !h.tt.HasText("有修改没有保存") })
	if closed {
		t.Fatal("going back closed")
	}
	started, release = slowSettings(h, errors.New("disk full"))
	<-started
	h.app.BeforeClose(func() { closed = true })
	close(release)
	h.click("仍然退出")
	if !closed {
		t.Fatal("quitting anyway did not close")
	}
}

// A write that starts while the close waits counts too: its failure asks.
func TestCloseAsksWhenWriteStartedWhileWaitingFails(t *testing.T) {
	h, _ := mailHarness(t, nil, "me@example.com")
	started, release := slowSettings(h, nil)
	<-started
	closed := false
	h.app.BeforeClose(func() { closed = true })
	h.app.saveSettings = func(context.Context, core.Settings) (core.Settings, error) {
		return core.Settings{}, errors.New("disk full")
	}
	st := h.app.settings
	st.Theme = "light"
	h.app.changeSettings(st) // waits behind the running save
	close(release)
	h.see("有修改没有保存")
	if closed {
		t.Fatal("closed although a write started while waiting failed")
	}
}
