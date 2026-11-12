package app

import (
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
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
	t.Setenv(uiTransport, "socket")
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
