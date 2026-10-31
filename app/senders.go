package app

import (
	"cmp"
	"context"
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// sendersView lists who sends the mail kept outside Sent, Drafts, Trash and
// Junk, most first, each with what it adds up to and three ways to clean
// up: 退订 by its newest list message, 全部归档 (undone from its toast) and
// 屏蔽.
func (p *mailPage) sendersView(c *ui.Context) {
	t := theme.Of(c)
	th, small := c.Theme(), t.Text.Sm
	now := p.a.clock.Now()
	kit.Text(c, i18n.T("mail.senders"), t.Text.Base).FontWeight(500).TextColor(t.Muted).Padding(th.Space(3), th.Space(4), th.Space(2), th.Space(4))
	senders := p.senders
	p.senderList.Key = func(i int) any { return senders[i].Address }
	kit.List(c, p.senderList, len(senders), func(i int) {
		s := senders[i]
		ui.Column(c).Gap(th.Space(1)).Padding(th.Space(2.5), th.Space(2.5), th.Space(2.5), th.Space(3)).Children(func() {
			ui.Text(c, cmp.Or(s.Name, s.Address)).FontWeight(500).SingleLine()
			if s.Name != "" {
				kit.Text(c, s.Address, small).TextColor(t.Muted).SingleLine()
			}
			kit.Text(c, i18n.Tf("mail.senders.counts", s.Messages, s.Unread)+" · "+listTime(now, s.Last), small).TextColor(t.Muted).SingleLine()
			ui.Row(c).Wrap().Gap(th.Space(1.5)).Margin(th.Space(1), 0, 0, 0).Children(func() {
				if s.ListMessage != 0 {
					kit.Button(c, i18n.T("reader.unsubscribe"), kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeSm}).OnClick(func() { p.unsubscribeSender(s) })
				}
				kit.Button(c, i18n.T("mail.senders.archive_all"), kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeSm, Disabled: s.Inbox == 0}).
					OnClick(func() { p.archiveSender(s) })
				if blocked := slices.Contains(p.a.blocked, s.Address); blocked {
					kit.Button(c, i18n.T("reader.unblock_sender"), kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeSm}).
						OnClick(func() { p.a.setBlocked(s.Address, false, func() { p.a.toast(i18n.T("reader.sender_unblocked")) }) })
				} else {
					kit.Button(c, i18n.T("reader.block_sender"), kit.ButtonOpts{Variant: kit.Destructive, Size: kit.SizeSm}).
						OnClick(func() { p.a.setBlocked(s.Address, true, func() { p.a.toast(i18n.T("reader.sender_blocked")) }) })
				}
			})
		})
	}).Grow(1).Padding(0, th.Space(2)).Children(func() {
		if len(senders) == 0 && !p.loading {
			ui.Text(c, i18n.T("mail.empty")).TextColor(t.Muted).Padding(th.Space(3))
		}
	})
}

// unsubscribeSender opens s's newest list message beside the list and
// unsubscribes from it the reader's way, once the reader knows how. Shown
// already, what failed to load (the thread, the body, the way) is tried again.
func (p *mailPage) unsubscribeSender(s core.Sender) {
	r, id := p.reader, s.ListMessage
	same := r.key == [2]int64{s.ListThread, id}
	p.a.route.ThreadID, p.a.route.MessageID = s.ListThread, id
	r.sync(false)
	r.unsubNext = id
	switch b := r.bodies[id]; {
	case b != nil && b.unsub.Method != "":
		r.unsubNext = 0
		r.unsubscribe(id)
	case !same: // just opened: its load leads to loadUnsubscribe
	case !r.loaded:
		r.load()
	case b == nil || !b.ready:
		r.loadBody(id)
	default:
		r.loadUnsubscribe(id)
	}
}

// archiveSender archives s's inbox mail; its toast says how much moved and
// stayed, and offers to put back what moved, even when the move stopped
// part way.
func (p *mailPage) archiveSender(s core.Sender) {
	a := p.a
	var res core.SenderArchive
	a.do(func(ctx context.Context) (err error) {
		res, err = a.svc.ArchiveSender(ctx, s.Address)
		return err
	}, func(err error) {
		if err != nil {
			a.toastErr(err)
		}
		if err != nil && len(res.Moved) == 0 {
			return
		}
		toast := ui.Toast{ID: "archive-sender:" + s.Address, Title: i18n.Tf("mail.senders.archived", len(res.Moved))}
		if res.Skipped > 0 {
			toast.Description = i18n.Tf("mail.senders.skipped", res.Skipped)
		}
		if len(res.Moved) > 0 {
			toast.Action, toast.OnAction = i18n.T("compose.undo"), func() {
				a.do(func(ctx context.Context) error { return a.svc.UndoArchive(ctx, res) }, func(err error) {
					if err != nil {
						a.toastErr(err)
					}
				})
			}
		}
		a.toasts = append(a.toasts, toast)
	})
}
