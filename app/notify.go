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

// notifyWait is how long new mail gathers into one notification per account.
const notifyWait = time.Second

// mailArrived gathers ev's new messages for their account's notification,
// shown notifyWait after the first of them.
func (a *App) mailArrived(ev events.Event) {
	if len(ev.MessageIDs) == 0 {
		return
	}
	if a.newMail == nil {
		a.newMail = map[int64][]int64{}
	}
	acc := ev.AccountID
	if _, waiting := a.newMail[acc]; !waiting {
		a.after(notifyWait, func() {
			ids := a.newMail[acc]
			delete(a.newMail, acc)
			a.notify(acc, ids)
		})
	}
	a.newMail[acc] = append(a.newMail[acc], ev.MessageIDs...)
}

// notify shows one notification for ids, unless the settings turn it off or
// the window is in front: one message by its sender (and subject, with
// preview on), more by their number.
func (a *App) notify(acc int64, ids []int64) {
	st := a.settings
	if !st.NotifyNewMail || a.foreground || len(ids) == 0 {
		return
	}
	email := ""
	if i := slices.IndexFunc(a.accounts, func(x core.Account) bool { return x.ID == acc }); i >= 0 {
		email = a.accounts[i].Email
	}
	n := Notification{ID: "account:" + strconv.FormatInt(acc, 10), Title: email, Body: i18n.Tf("notify.new_mails", len(ids))}
	newMail, plat, svc := i18n.T("notify.new_mail"), a.plat, a.svc
	a.do(func(ctx context.Context) error {
		if !plat.NotificationsSupported() {
			return nil
		}
		if len(ids) == 1 {
			m, err := svc.GetMessage(ctx, ids[0], false)
			if err != nil {
				return err
			}
			n = Notification{ID: "message:" + strconv.FormatInt(m.ID, 10), MessageID: m.ID, Title: displayName(m.From), Body: newMail}
			if st.NotifyPreview {
				n.Body = m.Subject
			}
		}
		return plat.Notify(n) // ponytail: a denied or failed notification is dropped silently
	}, nil)
}

// NotificationClicked shows the window with what a notification was about:
// its message, or the account's inbox for a gathered one.
func (a *App) NotificationClicked(id string) {
	a.plat.ShowWindow()
	kind, num, _ := strings.Cut(id, ":")
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil {
		return
	}
	switch kind {
	case "message":
		var m core.Message
		a.do(func(ctx context.Context) (err error) {
			m, err = a.svc.GetMessage(ctx, n, false)
			return err
		}, func(err error) {
			if err != nil {
				a.toastErr(err)
				return
			}
			r := Route{Page: PageMail, View: core.View{FolderID: m.FolderID}, ThreadID: m.ThreadID}
			if !a.settings.ThreadView {
				r.MessageID = m.ID // as the list opens a message
			}
			a.navigate(r)
		})
	case "account":
		var fs []core.Folder
		a.do(func(ctx context.Context) (err error) {
			fs, err = a.svc.ListFolders(ctx, n)
			return err
		}, func(err error) {
			if err != nil {
				a.toastErr(err)
				return
			}
			if i := slices.IndexFunc(fs, func(f core.Folder) bool { return f.Role == "inbox" }); i >= 0 {
				a.navigate(Route{Page: PageMail, View: core.View{FolderID: fs[i].ID}})
			}
		})
	}
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
