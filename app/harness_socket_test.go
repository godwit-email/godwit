package app

import (
	"slices"
	"strconv"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/events"
)

// backendArrive delivers subject to acc's INBOX and waits until the
// backend's notifier has taken the arrival.
func (h *harness) backendArrive(acc core.Account, subject string) {
	h.t.Helper()
	want := h.received.Load() + 1
	h.env.Deliver(h.t, acc.ID, "INBOX", coretest.Msg("Bob <bob@example.com>", acc.Email, subject, "body of "+subject))
	h.until("the backend's notifier to take "+subject, func() bool { return h.received.Load() >= want })
}

func TestSocketUINotifiesFromBackend(t *testing.T) {
	h, acc := notifyHarness(t, func(st *core.Settings) { st.NotifyPreview = true })

	h.app.SetForeground(true)
	h.until("the backend to see the window in front", h.env.Svc.UIForeground)
	h.backendArrive(acc, "While in front")
	h.waitMail("the list to show it", func() bool { return h.tt.HasText("While in front") })
	if notes := h.notesAfter(0); len(notes) != 0 {
		t.Fatalf("notes %+v while the window is in front", notes)
	}

	h.dropUI()
	h.until("the UI to disconnect", func() bool { return !h.env.Svc.UIConnected() })
	h.backendArrive(acc, "While closed")
	if notes := h.notesAfter(1); len(notes) != 1 || notes[0].Subtitle != "While closed" {
		t.Fatalf("notes %+v, want one for the mail that came while the UI was gone", notes)
	}

	h.connectUI()
	h.see("While closed")
}

// A click on a backend's notification reaches the App over its events and
// opens the message; the same click handed to the App as well (macOS may
// give it to either process) does nothing more.
func TestSocketUIOpensBackendClick(t *testing.T) {
	h, acc := notifyHarness(t, func(st *core.Settings) { st.NotifyPreview = true })
	h.backendArrive(acc, "Clicked in the backend")
	n := h.notesAfter(1)[0]
	h.env.Svc.Publish(events.Event{Type: events.NotificationClicked, Notification: n.ID})
	h.until("the message open", func() bool {
		return h.app.route.ThreadID != 0 && h.tt.HasText("body of Clicked in the backend")
	})
	h.app.NotificationClicked(n.ID)
	h.env.Svc.Publish(events.Event{Type: events.NotificationClicked, Notification: "account:" + strconv.FormatInt(acc.ID, 10)})
	h.until("the account's inbox", func() bool { return h.app.route.ThreadID == 0 })
	shown := 0
	for _, c := range h.plat.Calls() {
		if c == "ShowWindow" {
			shown++
		}
	}
	if shown != 2 {
		t.Fatalf("ShowWindow %d times, want 2: the repeated click was not dropped", shown)
	}
}

// An interface started again after it exited loads the mail list and the
// unread counts whole, mail that came meanwhile included: no event of it
// reached an interface.
func TestSocketUIReopensFresh(t *testing.T) {
	h, acc := notifyHarness(t, nil)
	h.dropUI()
	h.until("the UI to disconnect", func() bool { return !h.env.Svc.UIConnected() })
	h.backendArrive(acc, "First while closed")
	h.backendArrive(acc, "Second while closed")
	n := len(h.plat.Calls())
	h.connectUI()
	h.see("First while closed")
	h.see("Second while closed")
	h.until("the app icon to count both", func() bool { return slices.Contains(h.plat.Calls()[n:], "SetAppUnread 2") })
}

// Open and Compose in the backend's tray reach the App over its events: Open
// shows the window, Compose opens a new compose window.
func TestSocketUIOpensFromBackendTray(t *testing.T) {
	h, _ := notifyHarness(t, nil)
	h.env.Svc.Publish(events.Event{Type: events.OpenRequested})
	h.until("the window shown", func() bool { return slices.Contains(h.plat.Calls(), "ShowWindow") })
	h.env.Svc.Publish(events.Event{Type: events.ComposeRequested})
	h.composeOpened(0)
}

// What a launch asks the App to open (godwit --open, as the backend starts
// it or a second launch hands it over): the window, a compose window, or a
// notification's message.
func TestAppOpen(t *testing.T) {
	h, acc := notifyHarness(t, func(st *core.Settings) { st.NotifyPreview = true })
	h.backendArrive(acc, "Opened at launch")
	h.dropUI()
	h.connectUI() // the interface the click started
	h.app.Open(h.notesAfter(1)[0].ID)
	h.until("the message open", func() bool {
		return h.app.route.ThreadID != 0 && h.tt.HasText("body of Opened at launch")
	})
	h.app.Open(OpenCompose)
	h.composeOpened(0)
	n := len(h.plat.Calls())
	h.app.Open(OpenWindow)
	if !slices.Contains(h.plat.Calls()[n:], "ShowWindow") {
		t.Fatal("Open(OpenWindow) did not show the window")
	}
}

// Quit in the backend's tray quits the App as its own Quit does.
func TestSocketUIQuitsFromBackendTray(t *testing.T) {
	h := newHarness(t)
	h.env.Svc.Publish(events.Event{Type: events.QuitRequested})
	h.until("the App to quit", func() bool { return h.quits.Load() == 1 })
}

// An interface the tray starts with --open compose opens the compose window
// once its accounts have loaded, not drop it for having none yet.
func TestAppOpenComposeBeforeLoad(t *testing.T) {
	h, _ := withAccount(t)
	h.dropUI()
	h.connectUI() // nothing loaded yet: its loads run as the test drives the UI thread
	h.app.Open(OpenCompose)
	h.composeOpened(0)
}
