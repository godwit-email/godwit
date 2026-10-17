package app

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/internal/i18n"
)

// notifyWait is how long new mail gathers before reading it for notifications.
const notifyWait = time.Second

// arrivals are an account's new messages and their threads awaiting notifications.
type arrivals struct{ msgs, threads []int64 }

// mailArrived gathers ev's new messages for their notifications,
// shown notifyWait after the first of them.
func (a *App) mailArrived(ev events.Event) {
	if len(ev.MessageIDs) == 0 {
		return
	}
	if a.newMail == nil {
		a.newMail = map[int64]*arrivals{}
	}
	acc := ev.AccountID
	got := a.newMail[acc]
	if got == nil {
		got = &arrivals{}
		a.newMail[acc] = got
		a.after(notifyWait, func() {
			delete(a.newMail, acc)
			a.notify(acc, got)
		})
	}
	got.msgs = append(got.msgs, ev.MessageIDs...)
	got.threads = append(got.threads, ev.ThreadIDs...)
}

// notify shows a notification for each message that arrived in the account's
// inboxes, unless disabled or the window is in front. Preview shows the sender,
// subject and snippet; otherwise it says only "Godwit" and "new mail".
func (a *App) notify(acc int64, got *arrivals) {
	st := a.settings
	if !st.NotifyNewMail || a.foreground {
		return
	}
	texts := i18n.In(i18n.Language())
	plat, svc, logf := a.plat, a.svc, a.logf
	codeBanner, now := st.NotifyPreview && plat.SignInCodeBannerSupported(), a.clock.Now()
	a.do(func(ctx context.Context) error {
		if !plat.NotificationsSupported() {
			return nil
		}
		msgs, err := inboxArrivals(ctx, svc, acc, got)
		if err != nil {
			logf("notify: account %d: reading new mail: %v", acc, err)
			return nil
		}
		if codeBanner {
			// Only the newest code takes the banner; older ones in the same batch
			// keep their ordinary notification.
			if codes, err := svc.RecentSignInCodes(ctx, now); err == nil && len(codes) > 0 {
				msgs = slices.DeleteFunc(msgs, func(m core.MessageSummary) bool { return m.ID == codes[0].MessageID })
			}
		}
		for _, m := range msgs {
			n := Notification{ID: "message:" + strconv.FormatInt(m.ID, 10), MessageID: m.ID, Title: "Godwit", Body: texts.NotifyNewMail()}
			if st.NotifyPreview {
				n.Title, n.Subtitle, n.Body = displayName(m.From), m.Subject, m.Snippet
				n.Sender, n.ThreadID = m.From, m.ThreadID
			}
			if err := plat.Notify(n); err != nil {
				logf("notify: account %d: %v", acc, err)
			}
		}
		return nil
	}, nil)
}

// inboxArrivals are the arrived messages that are in an inbox-role folder.
func inboxArrivals(ctx context.Context, svc *core.Service, acc int64, got *arrivals) ([]core.MessageSummary, error) {
	fs, err := svc.ListFolders(ctx, acc)
	if err != nil {
		return nil, err
	}
	inbox := map[int64]bool{}
	for _, f := range fs {
		if f.Role == "inbox" {
			inbox[f.ID] = true
		}
	}
	want := map[int64]bool{}
	for _, id := range got.msgs {
		want[id] = true
	}
	var out []core.MessageSummary
	for _, tid := range slices.Compact(slices.Sorted(slices.Values(got.threads))) {
		th, err := svc.GetThread(ctx, tid)
		if err != nil {
			return nil, err
		}
		for _, m := range th.Messages {
			if want[m.ID] && inbox[m.FolderID] {
				delete(want, m.ID) // a message in two gathered threads counts once
				out = append(out, m)
			}
		}
	}
	return out, nil
}

// NotificationClicked shows the window with what a notification was about:
// its message, or the account's inbox for a gathered one. Only the newest
// click navigates, and only if the route has not changed meanwhile; a click
// before the first load waits for it.
func (a *App) NotificationClicked(id string) {
	a.plat.ShowWindow()
	if !a.loaded {
		a.noteFirst = id
		return
	}
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
		m, err := a.svc.GetMessage(ctx, n, false)
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
	if !a.settings.UnreadInMenuBar {
		inbox = 0
	}
	if inbox != a.trayUnread {
		a.trayUnread = inbox
		a.plat.SetTrayUnread(inbox)
	}
}

// Compose starts a new draft in a compose window, as the sidebar's button does.
func (a *App) Compose() { a.mail.compose() }

// SyncAll syncs every account now.
func (a *App) SyncAll() {
	for _, acc := range a.accounts {
		id := acc.ID
		a.do(func(ctx context.Context) error { return a.svc.SyncNow(ctx, id) }, a.toastErr)
	}
}

// KeepInBackground reports whether closing the window only hides it.
func (a *App) KeepInBackground() bool { return a.settings.KeepInBackground }
