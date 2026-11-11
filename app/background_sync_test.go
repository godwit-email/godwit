package app

import (
	"context"
	"slices"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/internal/i18n"
)

// The window in front is what ends the narrow sync: the setting alone does not.
func TestSetForegroundNarrowsSync(t *testing.T) {
	h := newHarness(t)
	h.until("settings loaded", func() bool { return h.app.loaded })
	h.app.settings.BackgroundSync = core.BackgroundSyncInbox

	h.app.SetForeground(false)
	if !h.env.Svc.InboxOnly() {
		t.Fatal("the background did not narrow the sync")
	}
	h.app.SetForeground(true)
	if h.env.Svc.InboxOnly() {
		t.Fatal("the window in front kept the sync narrow")
	}

	h.app.settings.BackgroundSync = core.BackgroundSyncAll
	h.app.SetForeground(false)
	if h.env.Svc.InboxOnly() {
		t.Fatal("all folders narrowed the sync")
	}
}

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

// Choosing all folders while the window is behind another app widens the
// sync at once, without waiting for a focus change.
func TestBackgroundSyncSettingApplies(t *testing.T) {
	h := newHarness(t)
	h.until("settings loaded", func() bool { return h.app.loaded })
	h.app.foreground = false
	all := h.app.settings
	all.BackgroundSync = core.BackgroundSyncAll
	inbox := all
	inbox.BackgroundSync = core.BackgroundSyncInbox

	h.app.settingsEffects(all, inbox)
	if !h.env.Svc.InboxOnly() {
		t.Fatal("inbox only did not narrow the sync")
	}
	h.app.settingsEffects(inbox, all)
	if h.env.Svc.InboxOnly() {
		t.Fatal("all folders kept the sync narrow")
	}
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
