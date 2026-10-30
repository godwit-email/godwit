package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// outboxEntry is an outbox item with its account's name.
type outboxEntry struct {
	core.OutboxItem
	Account string
}

// outboxAction is a button of an outbox item.
type outboxAction struct {
	key string // under outbox.
	v   kit.Variant
	do  func()
}

// outboxView lists mail not yet sent and filed, with its state; mail that
// may or may not have gone out, or failed, waits for the user's decision.
func (p *mailPage) outboxView(c *ui.Context) {
	t := theme.Of(c)
	th, small := c.Theme(), t.Text.Sm
	now := p.a.clock.Now()
	ui.Text(c, i18n.T("mail.outbox")).FontWeight(500).TextColor(t.Muted).Padding(th.Space(3), th.Space(4), th.Space(2), th.Space(4))
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(0, th.Space(4)).Gap(th.Space(3)).Children(func() {
			if len(p.outbox) == 0 {
				ui.Text(c, i18n.T("mail.empty")).TextColor(t.Muted)
			}
			for _, it := range p.outbox {
				ui.Column(c.Key(it.ID)).Gap(th.Space(1)).Padding(th.Space(2.5)).Radius(t.Radius.Lg).Border(theme.Hairline, t.Border).Children(func() {
					ui.Text(c, subjectOf(it.OutboxItem)).FontWeight(500).SingleLine()
					kit.Text(c, it.Account, small).TextColor(t.Muted).SingleLine()
					kit.Text(c, outboxStatus(it.OutboxItem, now), small).TextColor(t.Muted)
					if it.State == "failed" {
						why, server := failureText(it.OutboxItem)
						kit.Text(c, why, small).TextColor(t.Danger)
						if server != "" {
							kit.Text(c, server, small).TextColor(t.Muted)
						}
					} else if it.LastError != "" {
						kit.Text(c, it.LastError, small).TextColor(t.Danger)
					}
					if acts := p.outboxActions(it.OutboxItem, now); len(acts) > 0 {
						ui.Row(c).Wrap().Gap(th.Space(1.5)).Margin(th.Space(1), 0, 0, 0).Children(func() {
							for _, act := range acts {
								kit.Button(c, i18n.T("outbox."+act.key), kit.ButtonOpts{Variant: act.v, Size: kit.SizeSm}).OnClick(act.do)
							}
						})
					}
				})
			}
		})
	})
}

// outboxActions are what can be done with it: a queued item with its draft
// can be taken back and a retrying one retried now; a failed one edited (with
// its draft), sent again or discarded; one that may have gone out confirmed,
// sent again or discarded.
func (p *mailPage) outboxActions(it core.OutboxItem, now time.Time) []outboxAction {
	var acts []outboxAction
	add := func(key string, v kit.Variant, do func()) { acts = append(acts, outboxAction{key, v, do}) }
	resolve := func(action string, then func()) func() { return func() { p.resolve(it.ID, action, then) } }
	switch it.State {
	case "queued":
		if it.NextAttempt.After(now) {
			add("retry_now", kit.Outline, resolve("resend", nil))
		}
		if it.DraftID != 0 {
			add("cancel", kit.Outline, func() { p.cancel(it.ID) })
		}
	case "failed":
		if it.DraftID != 0 {
			add("edit", kit.Outline, resolve("edit", func() { p.a.plat.OpenCompose(it.DraftID) }))
		}
		add("resend", kit.Outline, resolve("resend", nil))
		add("discard", kit.Destructive, resolve("discard", nil))
	case "send_uncertain", "archive_uncertain":
		add("confirm_sent", kit.Outline, resolve("confirm_sent", nil))
		if it.State == "send_uncertain" { // archive_uncertain was accepted: sending again delivers it twice
			add("resend", kit.Outline, resolve("resend", nil))
		}
		add("discard", kit.Destructive, resolve("discard", nil))
	}
	return acts
}

// outboxStatus names its state as of now: a queued item is in its undo
// window or retrying until its time comes.
func outboxStatus(it core.OutboxItem, now time.Time) string {
	switch {
	case it.State == "queued" && it.NextAttempt.After(now):
		return i18n.Tf("outbox.state.retrying", it.NextAttempt.In(now.Location()).Format("15:04"))
	case inUndoWindow(it, now):
		return i18n.T("outbox.state.undo")
	}
	return i18n.T("outbox.state." + it.State)
}

// inUndoWindow: it is queued, untried, with its draft, and not due yet.
func inUndoWindow(it core.OutboxItem, now time.Time) bool {
	return it.State == "queued" && it.DraftID != 0 && it.Attempts == 0 && it.SendAfter.After(now)
}

func subjectOf(it core.OutboxItem) string {
	return cmp.Or(it.Subject, i18n.T("mail.no_subject"))
}

// failureText says why a failed item failed, in the user's language, and
// the server's own words ("" for none). Without a Failure it is LastError.
func failureText(it core.OutboxItem) (why, server string) {
	f := it.Failure
	switch {
	case f == nil:
		return it.LastError, ""
	case f.Reason == "expired":
		return i18n.T("outbox.failure.expired"), ""
	case f.Enhanced == "5.3.4" || f.Enhanced == "5.2.3" || f.Code == 552 && f.Stage == "DATA":
		why = i18n.T("outbox.failure.too_large")
	case f.Stage == "RCPT" && f.Recipient != "":
		why = i18n.Tf("outbox.failure.recipient", f.Recipient)
	case f.Stage == "MAIL":
		why = i18n.T("outbox.failure.sender")
	default:
		why = i18n.T("outbox.failure.rejected")
	}
	if f.Code != 0 {
		code := strconv.Itoa(f.Code)
		if f.Enhanced != "" {
			code += " " + f.Enhanced
		}
		why = i18n.Tf("outbox.failure.code", why, code)
	}
	return why, f.Message
}

// resolve decides item id; then, if set, runs after it succeeded.
func (p *mailPage) resolve(id int64, action string, then func()) {
	a := p.a
	a.writes.Chain(fmt.Sprintf("outbox:%d", id), func(ctx context.Context) error { return a.svc.ResolveOutbox(ctx, id, action) }, func(err error) {
		if err != nil {
			a.toastErr(err)
		} else if then != nil {
			then()
		}
		p.reload()
	})
}

// cancel takes queued item id back to the drafts.
func (p *mailPage) cancel(id int64) {
	a := p.a
	a.writes.Chain(fmt.Sprintf("outbox:%d", id), func(ctx context.Context) error { return a.svc.CancelOutbox(ctx, id) }, func(err error) {
		a.toastUndoErr(err)
		p.reload()
	})
}

func undoToastID(id int64) string { return fmt.Sprint("undo-send:", id) }

// offerUndo lists every account's outbox after it changed: a message in its
// undo window gets a toast to take it back, once per item, until its time
// comes; the toast of an item that left the window (sending began, or it
// was taken back) closes, and an item no longer listed is forgotten. Only
// the newest listing counts.
func (a *App) offerUndo() {
	a.undoGen++
	gen := a.undoGen
	var queued []outboxEntry
	listed := map[int64]bool{}
	a.do(func(ctx context.Context) error {
		accs, err := a.svc.ListAccounts(ctx)
		if err != nil {
			return err
		}
		for _, acc := range accs {
			items, err := a.svc.ListOutbox(ctx, acc.ID)
			if err != nil {
				return err
			}
			for _, it := range items {
				listed[it.ID] = true
				if it.State == "queued" {
					queued = append(queued, outboxEntry{it, cmp.Or(acc.Alias, acc.Email)})
				}
			}
		}
		return nil
	}, func(err error) {
		if gen != a.undoGen {
			return
		}
		if err != nil {
			a.logf("listing the outbox for undo send: %v", err)
			return
		}
		now, open := a.clock.Now(), map[int64]bool{}
		for _, e := range queued {
			if !inUndoWindow(e.OutboxItem, now) {
				continue
			}
			open[e.ID] = true
			if _, seen := a.undoToasts[e.ID]; seen {
				continue
			}
			a.undoToasts[e.ID] = true
			a.toasts = append(a.toasts, ui.Toast{ID: undoToastID(e.ID), Title: i18n.T("outbox.undo.sending"),
				Description: subjectOf(e.OutboxItem) + " · " + e.Account, Action: i18n.T("outbox.undo"),
				OnAction: func() { a.undoSend(e) },
				Timeout:  min(e.SendAfter.Sub(now), core.UndoSendWindow)}) // the window, should the clocks disagree
		}
		for id, showing := range a.undoToasts {
			if showing && !open[id] {
				a.undoToasts[id] = false
				a.closeToasts = append(a.closeToasts, undoToastID(id))
			}
			if !listed[id] { // out of the outbox for good: item ids are never reused
				delete(a.undoToasts, id)
			}
		}
	})
}

// undoSend takes e back and opens its draft again.
func (a *App) undoSend(e outboxEntry) {
	a.writes.Chain(fmt.Sprintf("outbox:%d", e.ID), func(ctx context.Context) error { return a.svc.CancelOutbox(ctx, e.ID) }, func(err error) {
		if err == nil {
			a.plat.OpenCompose(e.DraftID)
		}
		a.toastUndoErr(err)
	})
}

// OpenDraft opens draft id in a compose window, unless it is in the outbox,
// read-only: then it says so. A draft it cannot read opens, to say why there.
func (a *App) OpenDraft(id int64) {
	inOutbox := false
	a.do(func(ctx context.Context) error {
		d, err := a.svc.GetDraft(ctx, id)
		if err != nil {
			return err
		}
		items, err := a.svc.ListOutbox(ctx, d.AccountID)
		inOutbox = slices.ContainsFunc(items, func(it core.OutboxItem) bool { return it.DraftID == id })
		return err
	}, func(err error) {
		if inOutbox && err == nil {
			a.toast(i18n.T("mail.draft.in_outbox"))
			return
		}
		a.plat.OpenCompose(id)
	})
}

// toastUndoErr says why a take-back failed: sending began, mostly.
func (a *App) toastUndoErr(err error) {
	switch {
	case errors.Is(err, core.ErrInvalidState):
		a.toastAs(kit.ToastWarning, i18n.T("outbox.undo.too_late"))
	case err != nil:
		a.toastErr(err)
	}
}
