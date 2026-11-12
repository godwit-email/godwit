package app

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
)

// NotificationClicked shows the window with what a notification was about:
// its message, or the account's inbox for a gathered one. Only the newest
// click navigates, and only if the route has not changed meanwhile; a click
// before the first load waits for it. A click taken already is dropped: the
// backend and the interface may both be given it.
func (a *App) NotificationClicked(id string) {
	if a.noteSeen[id] {
		return
	}
	a.plat.ShowWindow()
	if !a.loaded {
		a.noteFirst = id
		return
	}
	if a.noteSeen == nil {
		a.noteSeen = map[string]bool{}
	}
	a.noteSeen[id] = true
	a.noteGen++
	gen, look := a.noteGen, a.noteRoute
	var r Route
	var ok bool
	a.query(func(ctx context.Context) (err error) {
		r, ok, err = look(ctx, id)
		return err
	}, func(err error) {
		switch {
		case gen != a.noteGen:
		case err != nil:
			a.toastErr(err)
		case ok:
			if a.settings.ThreadView {
				r.MessageID = 0 // the whole thread, as the list opens it
			}
			a.navigate(r)
		}
	})
}

// notificationRoute is where the notification id leads (MessageID set for a
// message); false for an id of no known kind or an account without an inbox.
func (a *App) notificationRoute(ctx context.Context, id string) (Route, bool, error) {
	kind, num, _ := strings.Cut(id, ":")
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil {
		return Route{}, false, nil
	}
	switch kind {
	case "message":
		m, err := a.svc.GetMessageText(ctx, n) // no inline image is downloaded for a route
		if err != nil {
			return Route{}, false, err
		}
		return Route{Page: PageMail, View: core.View{FolderID: m.FolderID}, ThreadID: m.ThreadID, MessageID: m.ID}, true, nil
	case "account":
		fs, err := a.svc.ListFolders(ctx, n)
		if err != nil {
			return Route{}, false, err
		}
		if i := slices.IndexFunc(fs, func(f core.Folder) bool { return f.Role == "inbox" }); i >= 0 {
			return Route{Page: PageMail, View: core.View{FolderID: fs[i].ID}}, true, nil
		}
	}
	return Route{}, false, nil
}

// showUnread updates the app icon and tray according to their settings.
func (a *App) showUnread(inbox int) {
	badge := inbox
	if !a.settings.UnreadOnAppIcon {
		badge = 0
	}
	if badge != a.appUnread {
		a.appUnread = badge
		a.plat.SetAppUnread(badge)
	}
	if !a.settings.UnreadInMenuBar || !a.settings.ShowTrayIcon {
		inbox = 0
	}
	if inbox != a.trayUnread {
		a.trayUnread = inbox
		a.plat.SetTrayUnread(inbox)
	}
}

// What Open opens besides a notification's subject: the main window, or a
// new compose window.
const OpenWindow, OpenCompose = "window", "compose"

// Open shows what a launch (godwit --open) or the backend's tray asks for:
// OpenWindow, OpenCompose, or else a notification id (NotificationClicked).
func (a *App) Open(what string) {
	switch what {
	case OpenWindow:
		a.plat.ShowWindow()
	case OpenCompose:
		a.Compose()
	default:
		a.NotificationClicked(what)
	}
}

// Compose starts a new draft in a compose window, as the sidebar's button does.
func (a *App) Compose() { a.mail.compose() }

// HideTrayIcon hides the menu bar icon by updating the setting. Its menu is
// mostly used with the window hidden, so the window comes up to show the toast.
func (a *App) HideTrayIcon() {
	a.onUI(func() {
		st := a.settings
		if !st.ShowTrayIcon {
			return
		}
		st.ShowTrayIcon = false
		a.changeSettings(st)
		a.plat.ShowWindow()
		a.toast(i18n.T("tray.hidden_toast"))
	})
}

// SyncAll syncs every account now. The status shows syncing until the last one
// returns; a click while that batch is in flight starts nothing more.
func (a *App) SyncAll() {
	if a.syncBatch != nil || len(a.accounts) == 0 {
		return
	}
	b := &syncBatch{pending: make(map[int64]bool, len(a.accounts)), failed: map[int64]bool{}}
	for _, acc := range a.accounts {
		b.pending[acc.ID] = true
	}
	a.syncBatch = b
	a.tick()
	for _, acc := range a.accounts {
		id := acc.ID
		a.do(func(ctx context.Context) error { return a.svc.SyncNow(ctx, id) }, func(err error) { a.onBatchDone(id, err) })
	}
}

// onBatchDone records one account's SyncNow returning. One no longer pending
// was removed meanwhile, so its error is not shown; nor is a cancelled one's, as
// removing the account cancels it.
func (a *App) onBatchDone(id int64, err error) {
	if !a.batchPending(id) {
		return
	}
	delete(a.syncBatch.pending, id)
	if err != nil && !errors.Is(err, context.Canceled) {
		a.syncBatch.failed[id] = true
		a.toastErr(err)
	}
	a.finishBatch()
}

// finishBatch ends the Sync All once no account is pending, setting the last
// sync time when none failed.
func (a *App) finishBatch() {
	if a.syncBatch == nil || len(a.syncBatch.pending) > 0 {
		return
	}
	if len(a.syncBatch.failed) == 0 {
		a.syncedAt = a.clock.Now()
	}
	a.syncBatch = nil
	a.tick()
}

// batchPending reports whether the Sync All in flight still waits on the account.
func (a *App) batchPending(id int64) bool { return a.syncBatch != nil && a.syncBatch.pending[id] }

// TrayIconShown reports whether the tray shows Godwit's icon (ShowTrayIcon).
func (a *App) TrayIconShown() bool { return a.settings.ShowTrayIcon }

// KeepInBackground reports whether the backend keeps receiving once the
// window closes: 后台收信 is past 关窗后停止, or an enabled rule runs in the
// background.
func (a *App) KeepInBackground() bool {
	if a.settings.BackgroundReceive != core.BackgroundReceiveStopOnClose {
		return true
	}
	on, _ := a.svc.RulesInBackground(context.Background()) // an error quits, as with no such rule
	return on
}
