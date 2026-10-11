package app

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/internal/i18n"
)

// notifyWait is how long new mail gathers into one notification per account.
const notifyWait = time.Second

// arrivals are an account's new messages and their threads, gathering for
// one notification.
type arrivals struct{ msgs, threads []int64 }

// mailArrived gathers ev's new messages for their account's notification,
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

// notify shows one notification for what arrived in the account's inboxes,
// unless the settings turn it off or the window is in front. With preview on
// one message shows its sender and subject, more their account and number;
// with preview off it says only "Godwit" and how many.
func (a *App) notify(acc int64, got *arrivals) {
	st := a.settings
	if !st.NotifyNewMail || a.foreground {
		return
	}
	email := ""
	if i := slices.IndexFunc(a.accounts, func(x core.Account) bool { return x.ID == acc }); i >= 0 {
		email = a.accounts[i].Email
	}
	one, several := i18n.T("notify.new_mail"), i18n.T("notify.new_mails")
	plat, svc, logf := a.plat, a.svc, a.logf
	a.do(func(ctx context.Context) error {
		if !plat.NotificationsSupported() {
			return nil
		}
		msgs, err := inboxArrivals(ctx, svc, acc, got)
		if err != nil {
			logf("notify: account %d: reading new mail: %v", acc, err)
			return nil
		}
		var n Notification
		switch {
		case len(msgs) == 0:
			return nil
		case len(msgs) == 1:
			m := msgs[0]
			n = Notification{ID: "message:" + strconv.FormatInt(m.ID, 10), MessageID: m.ID, Title: "Godwit", Body: one}
			if st.NotifyPreview {
				n.Title, n.Body = displayName(m.From), m.Subject
			}
		default:
			n = Notification{ID: "account:" + strconv.FormatInt(acc, 10), Title: "Godwit", Body: fmt.Sprintf(several, len(msgs))}
			if st.NotifyPreview {
				n.Title = email
			}
		}
		if err := plat.Notify(n); err != nil {
			logf("notify: account %d: %v", acc, err)
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

// showTrayUnread puts the inboxes' unread total on the tray when the
// settings ask for it.
func (a *App) showTrayUnread(inbox int) {
	if !a.settings.UnreadInMenuBar {
		inbox = 0
	}
	if inbox != a.trayUnread {
		a.trayUnread = inbox
		a.plat.SetTrayUnread(inbox)
	}
}

// Compose starts a new draft, as the sidebar's button does.
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
