package app

import (
	"context"
	"slices"
	"strings"
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

// arrive delivers subjects one sync each and waits until the App has seen
// every arrival.
func (h *harness) arrive(acc core.Account, subjects ...string) {
	h.t.Helper()
	for _, s := range subjects {
		h.env.Deliver(h.t, acc.ID, "INBOX", coretest.Msg("Bob Stone <bob@example.com>", acc.Email, s, "body of "+s))
	}
	h.until("the arrivals", func() bool { return len(h.app.newMail[acc.ID]) == len(subjects) })
}

// notesAfter lets the coalescing window pass and returns the notifications
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

func TestNotifyCoalesces(t *testing.T) {
	t.Run("three in a second", func(t *testing.T) {
		h, acc := notifyHarness(t, nil)
		h.arrive(acc, "One", "Two", "Three")
		notes := h.notesAfter(1)
		if len(notes) != 1 {
			t.Fatalf("notes %+v, want one", notes)
		}
		if n := notes[0]; n.Body != "3 封新邮件" || n.Title != acc.Email || !strings.HasPrefix(n.ID, "account:") {
			t.Fatalf("note %+v", n)
		}
	})
	t.Run("preview off", func(t *testing.T) {
		h, acc := notifyHarness(t, nil)
		h.arrive(acc, "Secret plans")
		notes := h.notesAfter(1)
		if len(notes) != 1 || strings.Contains(notes[0].Title+notes[0].Body, "Secret plans") || notes[0].Body != "新邮件" {
			t.Fatalf("notes %+v", notes)
		}
	})
	t.Run("preview on", func(t *testing.T) {
		h, acc := notifyHarness(t, func(st *core.Settings) { st.NotifyPreview = true })
		h.arrive(acc, "Lunch")
		notes := h.notesAfter(1)
		if len(notes) != 1 || notes[0].Title != "Bob Stone" || notes[0].Body != "Lunch" || notes[0].MessageID == 0 {
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
	notes = h.notesAfter(2)
	h.app.NotificationClicked(notes[1].ID)
	inbox := h.folderOf(acc, "inbox")
	h.until("the account's inbox", func() bool {
		r := h.app.route
		return r.Page == PageMail && r.View.FolderID == inbox.ID && r.ThreadID == 0
	})
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

func TestBeforeCloseWaitsAtMostFlushWait(t *testing.T) {
	h, _ := mailHarness(t, nil, "me@example.com")
	p := h.openCompose()
	started, release := blockSaves(p)
	defer close(release)
	h.writeBody("slow disk")
	closed := false
	h.app.BeforeClose(func() { closed = true })
	<-started
	h.advance(flushWait)
	h.see("草稿没有保存")
	if closed || !h.hasCall("ShowWindow") {
		t.Fatalf("closed %v, calls %q", closed, h.plat.Calls())
	}
}
