package app

import (
	"context"
	"slices"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/internal/i18n"
)

// The choice has its row under Startup, and it is saved as any other.
func TestSettingsBackgroundSyncChoice(t *testing.T) {
	h, _ := withAccount(t)
	p := h.openSettings("general")
	h.see(i18n.T("settings.background_sync"))

	p.st.BackgroundSync = core.BackgroundSyncAll
	h.drain()
	h.tt.Frame()
	h.until("the choice saved", func() bool {
		st, err := h.env.Svc.GetSettings(context.Background())
		return err == nil && st.BackgroundSync == core.BackgroundSyncAll
	})
}

// 后台收信 has its row under Startup; choosing 关窗后停止 is saved and makes
// closing the window quit at once.
func TestSettingsBackgroundReceiveChoice(t *testing.T) {
	h, _ := withAccount(t)
	p := h.openSettings("general")
	h.see(i18n.T("settings.background_receive"))
	h.see(i18n.T("settings.background_receive.until_quit"))
	if !h.app.KeepInBackground() {
		t.Fatal("the default stops as the window closes")
	}

	p.st.BackgroundReceive = core.BackgroundReceiveStopOnClose
	h.drain()
	h.tt.Frame()
	h.until("the choice saved and in effect", func() bool {
		st, err := h.env.Svc.GetSettings(context.Background())
		return err == nil && st.BackgroundReceive == core.BackgroundReceiveStopOnClose && !h.app.KeepInBackground()
	})
}

func TestBackgroundSyncSettingsReload(t *testing.T) {
	h := newHarness(t)
	h.until("settings loaded", func() bool { return h.app.loaded })
	h.app.SetForeground(false)

	for _, mode := range []string{core.BackgroundSyncAll, core.BackgroundSyncInbox} {
		st := h.app.settings
		st.BackgroundSync = mode
		if _, err := h.env.Svc.SetSettings(context.Background(), st); err != nil {
			t.Fatal(err)
		}
		h.until("settings event", func() bool {
			return slices.ContainsFunc(h.app.dirty, func(ev events.Event) bool { return ev.Type == events.SettingsChanged })
		})
		h.advance(refreshEvery)
		h.until("settings reloaded", func() bool { return h.app.settings.BackgroundSync == mode })
		if got := h.env.Svc.InboxOnly(); got != (mode == core.BackgroundSyncInbox) {
			t.Errorf("reloaded BackgroundSync=%q, inbox only=%v", mode, got)
		}
	}
}
