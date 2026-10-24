package app

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// mcpSendState is the confirmation of godwit mcp's send requests (每次确认):
// the oldest waiting one in a dialog over the window, which comes to the
// front with a notification pointing to it. Only its 发送 sends; closing it
// drops the request. A plugin's warnings on 发送 show as the draft list's,
// with 仍然发送; closing them drops the request too.
type mcpSendState struct {
	open     bool
	req      core.MCPSend
	notified map[int64]bool // requests already brought to the front
	warned   bool           // the warnings dialog shows
	warnID   int64          // the request warned about
	warnings core.SendCheck
	changed  bool   // the shown request was replaced: the dialog says so
	hold     bool   // 发送 waits mcpSendHold after a replacement, so what it sends was seen
	unhold   func() // stops the hold's timer
}

// mcpSendHold is how long 发送 stays off after the shown request changes.
const mcpSendHold = 2 * time.Second

// loadMCPSends reads the waiting requests after mcp_sends.changed.
func (a *App) loadMCPSends() {
	var ps []core.MCPSend
	a.do(func(ctx context.Context) (err error) {
		ps, err = a.svc.PendingMCPSends(ctx)
		return err
	}, func(err error) {
		if err != nil {
			a.toastErr(err)
			return
		}
		a.showMCPSends(ps)
	})
}

// showMCPSends shows the oldest of ps, unless one shows already; a new
// request brings the window to the front and is notified once, unless it
// replaces one the dialog shows.
func (a *App) showMCPSends(ps []core.MCPSend) {
	s := &a.mcpSend
	if s.open && slices.ContainsFunc(ps, func(p core.MCPSend) bool { return p.ID == s.req.ID }) {
		return
	}
	shown := s.open
	s.open = len(ps) > 0
	if !s.open {
		return
	}
	s.req = ps[0]
	if s.unhold != nil {
		s.unhold()
	}
	s.changed, s.hold, s.unhold = shown, shown, nil
	if shown {
		s.unhold = a.after(mcpSendHold, func() { s.hold = false })
	}
	if s.notified == nil {
		s.notified = map[int64]bool{}
	}
	fresh := !s.notified[s.req.ID] && !shown
	s.notified[s.req.ID] = true
	if !fresh {
		return
	}
	a.plat.ShowWindow()
	subject := s.req.Draft.Subject
	if subject == "" {
		subject = i18n.T("mail.no_subject")
	}
	n := Notification{ID: "mcp_send:" + strconv.FormatInt(s.req.ID, 10), Title: "Godwit", Body: i18n.Tf("mcp_send.notify", subject)}
	plat, logf := a.plat, a.logf
	a.do(func(context.Context) error {
		if plat.NotificationsSupported() {
			if err := plat.Notify(n); err != nil {
				logf("notify: MCP send: %v", err)
			}
		}
		return nil
	}, nil)
}

// mcpSendDialog is the request's sending account, every recipient (a
// first-time one marked), subject, body and attachments; 发送 sends it, closing drops it.
func (a *App) mcpSendDialog(c *ui.Context) {
	s := &a.mcpSend
	if s.warned {
		w, id, anyway := s.warnings, s.warnID, false
		warningsDialog(c, &s.warned, w, func() { anyway = true })
		if !s.warned {
			a.do(func(ctx context.Context) error {
				return a.svc.ResolveMCPSendAcknowledged(ctx, id, w.Digest, anyway)
			}, func(err error) {
				var warn *core.SendWarningsError
				switch {
				case errors.As(err, &warn): // the checks changed: warned again
					s.warned, s.warnings = true, warn.Check
				case err != nil:
					a.toastErr(err)
				case anyway:
					a.plat.DraftDeleted(w.DraftID)
				}
			})
		}
		return // the next request waits behind it
	}
	if !s.open {
		return
	}
	t, th := theme.Of(c), c.Theme()
	r, d := s.req, s.req.Draft
	sent := false
	kit.Dialog(c, &s.open, i18n.T("mcp_send.title"), kit.DialogOpts{Desc: i18n.T("mcp_send.desc"), Close: i18n.T("common.close")}, func() {
		field := func(label string, body func()) {
			ui.Row(c).Gap(th.Space(3)).AlignItems(ui.Start).Children(func() {
				kit.Text(c, label, t.Text.Sm).TextColor(t.Muted).Width(th.Space(16)).Shrink(0)
				ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(1)).Children(body)
			})
		}
		if s.changed {
			kit.Text(c, i18n.T("mcp_send.changed"), t.Text.Sm).TextColor(t.Muted)
		}
		field(i18n.T("compose.from"), func() { ui.Text(c, r.From).Selectable() })
		for _, l := range []struct {
			label string
			addrs []string
		}{{i18n.T("compose.to"), d.To}, {i18n.T("compose.cc"), d.Cc}, {i18n.T("compose.bcc"), d.Bcc}} {
			if len(l.addrs) == 0 {
				continue
			}
			field(l.label, func() {
				for _, addr := range l.addrs {
					ui.Row(c).Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
						ui.Text(c, addr).FontWeight(600).Selectable()
						if slices.ContainsFunc(r.FirstTime, func(f string) bool { return mail.AddrKey(f) == mail.AddrKey(addr) }) {
							kit.Badge(c, i18n.T("mcp_send.first"))
						}
					})
				}
			})
		}
		field(i18n.T("compose.subject"), func() { ui.Text(c, d.Subject).Selectable() })
		ui.Text(c, mail.HTMLToText(d.HTML)).Selectable().Padding(th.Space(3), 0, 0, 0).BorderWidth(theme.Hairline, 0, 0, 0).BorderColor(t.Border)
		draftAttachments(c, d.Attachments)
	}, func() {
		if kit.Button(c, i18n.T("common.cancel"), kit.ButtonOpts{Variant: kit.Outline}).Clicked() {
			s.open = false
		}
		if kit.Button(c, i18n.T("compose.send"), kit.ButtonOpts{Disabled: s.hold}).Clicked() {
			if a.plat.ComposeOpen(d.ID) { // its window may hold edits not saved yet, as the draft list's Send knows
				a.toast(i18n.T("mail.draft.send_disabled"))
			} else {
				s.open, sent = false, true
			}
		}
	})
	if s.open {
		return
	}
	id := r.ID
	a.do(func(ctx context.Context) error { return a.svc.ResolveMCPSend(ctx, id, sent) }, func(err error) {
		var warn *core.SendWarningsError
		switch {
		case errors.As(err, &warn):
			s.warned, s.warnID, s.warnings = true, id, warn.Check
		case err != nil:
			a.toastErr(err)
		case sent:
			a.plat.DraftDeleted(d.ID)
		}
	})
}
