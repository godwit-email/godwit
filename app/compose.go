package app

import (
	"context"
	"errors"
	netmail "net/mail"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/internal/editor"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/richtext"
	"github.com/godwit-email/godwit/internal/theme"
)

// composer is the compose page of one draft: its fields, the editor of the
// body's editable part, the read-only quote and signature, attachments and
// sending. Its draftSaver saves it.
type composer struct {
	a      *App
	id     int64
	loaded bool

	acc, accSel           int64 // the sending account; the Select's choice
	to, cc, bcc           []string
	replyTo, subject      string
	showCc                bool
	ed                    *editor.Editor
	edVer                 uint64 // the editor's Version last seen
	quote, sig            string // the body's read-only parts, as HTML
	quoteOpen             bool
	atts                  []core.DraftAttachment
	attachErr             string
	link                  string
	linkOpen, discardOpen bool

	s       *draftSaver
	sending bool
	warn    *core.SendCheck // beforeSend's warnings, until sent anyway or edited
	blocked string          // a plugin's refusal
}

// openComposer reads draft id and shows it once read.
func openComposer(a *App, id int64) *composer {
	p := &composer{a: a, id: id}
	var d core.Draft
	a.query(func(ctx context.Context) (err error) {
		d, err = a.svc.GetDraft(ctx, id)
		return err
	}, func(err error) {
		if err != nil {
			a.toastErr(err)
			return
		}
		p.fill(d)
		p.s = newDraftSaver(a, d, p.snapshot, p.ed.Composing)
		p.loaded = true
	})
	return p
}

// fill shows d, replacing what the page shows.
func (p *composer) fill(d core.Draft) {
	p.acc, p.accSel = d.AccountID, d.AccountID
	p.to, p.cc, p.bcc = slices.Clone(d.To), slices.Clone(d.Cc), slices.Clone(d.Bcc)
	p.replyTo, p.subject, p.atts = d.ReplyTo, d.Subject, d.Attachments
	p.showCc = p.showCc || len(d.Cc)+len(d.Bcc) > 0 || d.ReplyTo != ""
	editable, quote, sig := mail.SplitDraft(d.HTML)
	p.quote, p.sig = quote, sig
	doc := richtext.FromComposeHTML(editable)
	if p.ed == nil {
		p.ed = editor.New(doc, p.a.clock.Now)
	} else {
		p.ed.Replace(doc)
	}
	p.edVer = p.ed.Version() // what was loaded is no edit
}

// snapshot is the draft as the page shows it, for the saver.
func (p *composer) snapshot() core.Draft {
	return core.Draft{AccountID: p.acc, To: slices.Clone(p.to), Cc: slices.Clone(p.cc), Bcc: slices.Clone(p.bcc),
		ReplyTo: p.replyTo, Subject: p.subject, HTML: mail.JoinDraft(p.ed.HTML(), p.quote, p.sig)}
}

func (p *composer) edited() {
	p.warn = nil // the checks were of another draft
	p.s.Edited()
}

// leave saves what is left when the page goes away.
func (p *composer) leave(done func()) {
	if p.s == nil {
		done()
		return
	}
	p.s.Flush(func(err error) {
		p.a.toastErr(err)
		done()
	})
}

func (p *composer) title() (string, string) {
	if !p.loaded {
		return i18n.T("compose.new"), ""
	}
	title := p.subject
	if strings.TrimSpace(title) == "" {
		title = i18n.T("compose.new")
	}
	return title, p.s.status()
}

// here reports whether the route still shows this draft.
func (p *composer) here() bool {
	return p.a.route.Page == PageCompose && p.a.route.DraftID == p.id
}

// switchAccount moves the draft to account acc, after the saves before it;
// the signature becomes acc's.
func (p *composer) switchAccount(acc int64) {
	svc, id := p.a.svc, p.id
	var d core.Draft
	p.s.Run(func(ctx context.Context, rev int64) (core.Draft, error) {
		var err error
		d, err = svc.SetDraftAccount(ctx, id, acc, rev)
		return d, staleLatest(ctx, svc, id, &d, err)
	}, func(err error) {
		if err != nil {
			p.accSel = p.acc
			if !errors.Is(err, core.ErrStaleDraft) { // the conflict notice tells
				p.a.toastErr(err)
			}
			return
		}
		p.acc, p.accSel = d.AccountID, d.AccountID
		_, _, p.sig = mail.SplitDraft(d.HTML)
	})
}

// attachRun runs change, an attachment change that bumps the revision by
// n, then reads the draft back: a revision beyond that is a change from
// elsewhere, a conflict.
func (p *composer) attachRun(n func() int64, change func(ctx context.Context) error, done func(err error)) {
	svc, id := p.a.svc, p.id
	var d core.Draft
	p.s.Run(func(ctx context.Context, rev int64) (core.Draft, error) {
		cerr := change(ctx)
		var err error
		if d, err = svc.GetDraft(ctx, id); err != nil {
			return core.Draft{}, errors.Join(cerr, err)
		}
		if d.Revision != rev+n() {
			return d, core.ErrStaleDraft
		}
		return d, cerr
	}, func(err error) {
		if d.ID != 0 {
			p.atts = d.Attachments
		}
		done(err)
	})
}

// attach asks for files and adds them, after the saves before them.
func (p *composer) attach() {
	a, svc, id := p.a, p.a.svc, p.id
	var paths []string
	a.do(func(context.Context) (err error) {
		paths, err = a.plat.OpenFiles(true)
		return err
	}, func(err error) {
		if err != nil || len(paths) == 0 {
			a.toastErr(err)
			return
		}
		var added int64
		p.attachErr = ""
		p.attachRun(func() int64 { return added }, func(ctx context.Context) error {
			var errs []error
			for _, path := range paths {
				if _, err := svc.AddAttachment(ctx, id, path); err != nil {
					errs = append(errs, err)
					continue
				}
				added++
			}
			return errors.Join(errs...)
		}, func(err error) {
			switch {
			case errors.Is(err, core.ErrTooLarge):
				p.attachErr = i18n.Tf("compose.attach.too_large", byteSize(core.MaxMessageBytes))
			case !errors.Is(err, core.ErrStaleDraft):
				a.toastErr(err)
			}
		})
	})
}

func (p *composer) removeAttachment(attID int64) {
	svc, id := p.a.svc, p.id
	p.attachRun(func() int64 { return 1 }, func(ctx context.Context) error {
		return svc.RemoveAttachment(ctx, id, attID)
	}, func(err error) {
		if !errors.Is(err, core.ErrStaleDraft) {
			p.a.toastErr(err)
		}
	})
}

// send saves the newest draft and then sends it; with digest, despite the
// warnings of that check.
func (p *composer) send(digest string) {
	if p.sending {
		return
	}
	p.sending, p.warn, p.blocked = true, nil, ""
	svc, id := p.a.svc, p.id
	p.s.Run(func(ctx context.Context, _ int64) (core.Draft, error) {
		if digest != "" {
			return core.Draft{}, svc.SendAcknowledged(ctx, id, digest)
		}
		return core.Draft{}, svc.Send(ctx, id)
	}, func(err error) {
		p.sending = false
		var warned *core.SendWarningsError
		var blocked *core.SendBlockedError
		switch {
		case err == nil:
			p.s.Stop() // the draft is in the outbox now
			p.a.toast(i18n.T("compose.sent"))
			if p.here() {
				p.a.back()
			}
		case errors.As(err, &warned):
			p.warn = &warned.Check
		case errors.As(err, &blocked):
			p.blocked = humanize(err)
		case errors.Is(err, core.ErrStaleDraft): // the conflict notice tells
		default:
			p.a.toastErr(err)
		}
	})
}

func (p *composer) discard() {
	p.discardOpen = false
	p.s.Stop()
	svc, id := p.a.svc, p.id
	p.s.Run(func(ctx context.Context, _ int64) (core.Draft, error) {
		return core.Draft{}, svc.DeleteDraft(ctx, id)
	}, func(err error) {
		if err != nil {
			p.a.toastErr(err)
			return
		}
		if p.here() {
			p.a.back()
		}
	})
}

func (p *composer) keepMine() {
	p.atts = p.s.conflict.Attachments
	p.s.KeepMine()
}

func (p *composer) useLatest() { p.fill(p.s.UseLatest()) }

// invalid is the addresses in list that do not parse.
func invalid(list ...[]string) []string {
	var out []string
	for _, l := range list {
		for _, s := range l {
			if _, err := netmail.ParseAddress(s); err != nil {
				out = append(out, s)
			}
		}
	}
	return out
}

// view is the compose column. The Wit panel (Task 16) goes beside it.
func (p *composer) view(c *ui.Context) {
	if !p.loaded {
		return
	}
	t, th := theme.Of(c), c.Theme()
	ui.Column(c).Grow(1).Children(func() {
		p.fields(c)
		p.toolbar(c)
		if p.s.conflict != nil {
			ui.Box(c).Padding(th.Space(3), th.Space(5), 0, th.Space(5)).Children(func() {
				kit.Alert(c, kit.Warning, i18n.T("compose.conflict"), func() {
					kit.Button(c, i18n.T("compose.conflict.keep"), kit.ButtonOpts{Variant: kit.Outline, Small: true}).OnClick(p.keepMine)
					kit.Button(c, i18n.T("compose.conflict.latest"), kit.ButtonOpts{Variant: kit.Outline, Small: true}).OnClick(p.useLatest)
				})
			})
		}
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Padding(th.Space(4), th.Space(5)).Gap(th.Space(3)).Children(func() { p.body(c) })
		})
		p.notices(c)
		ui.Row(c).Gap(th.Space(2)).Padding(th.Space(3), th.Space(5)).BorderWidth(theme.Hairline, 0, 0, 0).BorderColor(t.Border).Children(func() {
			kit.Button(c, i18n.T("compose.send"), kit.ButtonOpts{Disabled: p.sending}).OnClick(func() { p.send("") })
			kit.Button(c, i18n.T("compose.discard"), kit.ButtonOpts{Variant: kit.Ghost}).OnClick(func() { p.discardOpen = true })
			ui.Spacer(c)
			ui.Text(c, i18n.T("compose.note")).FontSize(t.Text.Sm).TextColor(t.Muted).Shrink(1)
		})
	})
	kit.Dialog(c, &p.discardOpen, i18n.T("compose.discard.title"), func() {
		ui.Text(c, i18n.T("compose.discard.body")).TextColor(t.Muted)
	}, func() {
		kit.Button(c, i18n.T("common.cancel"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(func() { p.discardOpen = false })
		kit.Button(c, i18n.T("compose.discard.confirm"), kit.ButtonOpts{Variant: kit.Destructive}).OnClick(p.discard)
	})
	if p.ed.Version() != p.edVer {
		p.edVer = p.ed.Version()
		p.edited()
	}
}

// row is a field's row: its name, then the control.
func (p *composer) row(c *ui.Context, name string, control func()) {
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).Gap(th.Space(3)).Padding(th.Space(2), th.Space(5)).BorderWidth(0, 0, theme.Hairline, 0).BorderColor(t.Border).Children(func() {
		ui.Text(c, name).TextColor(t.Muted).Width(th.Space(16)).Shrink(0)
		control()
	})
}

func (p *composer) fields(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	p.row(c, i18n.T("compose.from"), func() {
		opts := make([]kit.Option[int64], 0, len(p.a.accounts))
		for _, acc := range p.a.accounts {
			opts = append(opts, kit.Option[int64]{Value: acc.ID, Label: acc.Email})
		}
		kit.Select(c, &p.accSel, opts).Label(i18n.T("compose.from.select"))
		if p.accSel != p.acc && !p.s.busy {
			p.switchAccount(p.accSel)
		}
	})
	tokens := func(list *[]string, label string) {
		if ui.TokenField(c, list, nil).Grow(1).Label(label).Changed() {
			p.edited()
		}
	}
	p.row(c, i18n.T("compose.to"), func() {
		tokens(&p.to, i18n.T("compose.to.input"))
		if !p.showCc {
			ui.ButtonBase(c).FontSize(t.Text.Sm).TextColor(t.Muted).Children(func() {
				ui.Text(c, i18n.T("compose.cc_bcc"))
			}).OnClick(func() { p.showCc = true })
		}
	})
	if p.showCc {
		p.row(c, i18n.T("compose.cc"), func() { tokens(&p.cc, i18n.T("compose.cc.input")) })
		p.row(c, i18n.T("compose.bcc"), func() { tokens(&p.bcc, i18n.T("compose.bcc.input")) })
		p.row(c, i18n.T("compose.reply_to"), func() {
			if ui.TextInputBase(c, &p.replyTo).Grow(1).Label(i18n.T("compose.reply_to.input")).Changed() {
				p.edited()
			}
		})
	}
	reply := []string{}
	if strings.TrimSpace(p.replyTo) != "" {
		reply = []string{p.replyTo}
	}
	if bad := invalid(p.to, p.cc, p.bcc, reply); len(bad) > 0 {
		ui.Text(c, i18n.Tf("compose.invalid", strings.Join(bad, ", "))).FontSize(t.Text.Sm).TextColor(t.Danger).Padding(th.Space(1.5), th.Space(5))
	}
	p.row(c, i18n.T("compose.subject"), func() {
		if ui.TextInputBase(c, &p.subject).Grow(1).Label(i18n.T("compose.subject.input")).Changed() {
			p.edited()
		}
	})
}

func (p *composer) toolbar(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	ghost := kit.ButtonOpts{Variant: kit.Ghost, Small: true}
	ui.Row(c).Wrap().Gap(th.Space(0.5)).Padding(th.Space(1.5), th.Space(3.5)).BorderWidth(0, 0, theme.Hairline, 0).BorderColor(t.Border).Children(func() {
		kit.IconButton(c, "bold", i18n.T("compose.bold"), ghost).OnClick(p.ed.ToggleBold)
		kit.IconButton(c, "italic", i18n.T("compose.italic"), ghost).OnClick(p.ed.ToggleItalic)
		link := kit.IconButton(c, "link", i18n.T("compose.link"), ghost).OnClick(func() { p.linkOpen = true })
		kit.Popover(c, link, &p.linkOpen, func() {
			kit.Input(c, &p.link, kit.InputOpts{Placeholder: "https://"}).Label(i18n.T("compose.link.url")).Width(theme.ControlWidth)
			kit.Button(c, i18n.T("compose.link.apply"), kit.ButtonOpts{Small: true}).OnClick(func() {
				p.ed.SetLink(p.link)
				p.link, p.linkOpen = "", false
			})
		})
		kit.IconButton(c, "list", i18n.T("compose.list"), ghost).OnClick(func() { p.ed.ToggleList(false) })
		kit.IconButton(c, "list-ordered", i18n.T("compose.list_ordered"), ghost).OnClick(func() { p.ed.ToggleList(true) })
		kit.IconButton(c, "quote", i18n.T("compose.quote"), ghost).OnClick(p.ed.ToggleQuote)
		kit.IconButton(c, "paperclip", i18n.T("compose.attach"), ghost).OnClick(p.attach)
	})
	if len(p.atts) == 0 && p.attachErr == "" {
		return
	}
	ui.Row(c).Wrap().Gap(th.Space(2)).Padding(th.Space(2), th.Space(5)).BorderWidth(0, 0, theme.Hairline, 0).BorderColor(t.Border).Children(func() {
		for _, att := range p.atts {
			ui.Row(c).Key(att.ID).Gap(th.Space(1.5)).Padding(th.Space(1), th.Space(1), th.Space(1), th.Space(2.5)).Radius(t.Radius.Md).Background(t.Hover).Children(func() {
				ui.Text(c, att.Filename).SingleLine()
				ui.Text(c, byteSize(att.Size)).FontSize(t.Text.Sm).TextColor(t.Muted)
				kit.IconButton(c, "x", i18n.Tf("compose.attach.remove", att.Filename), ghost).OnClick(func() { p.removeAttachment(att.ID) })
			})
		}
		if p.attachErr != "" {
			ui.Text(c, p.attachErr).FontSize(t.Text.Sm).TextColor(t.Danger)
		}
	})
}

// body is the editor, then the read-only signature and quote.
func (p *composer) body(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	p.ed.View(c, editor.Options{Label: i18n.T("compose.body"), PasteHTML: p.a.plat.ClipboardHTML}).FillWidth()
	if p.sig != "" {
		ui.Text(c, mail.HTMLToText(p.sig)).FontSize(t.BodyFontSize).TextColor(t.Muted)
	}
	if p.quote == "" {
		return
	}
	ui.ButtonBase(c).AlignSelf(ui.Start).FontSize(t.Text.Sm).TextColor(t.Muted).Children(func() {
		ui.Text(c, i18n.T("compose.quote.show"))
	}).OnClick(func() { p.quoteOpen = !p.quoteOpen })
	if p.quoteOpen {
		ui.Text(c, mail.HTMLToText(p.quote)).FontSize(t.Text.Sm).TextColor(t.Muted).Selectable().
			Padding(0, 0, 0, th.Space(3)).BorderWidth(0, 0, 0, theme.QuoteBar).BorderColor(t.Border)
	}
}

// notices are beforeSend's warnings, with Send anyway, and a block.
func (p *composer) notices(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	if w := p.warn; w != nil {
		ui.Column(c).Margin(0, th.Space(5), th.Space(4), th.Space(5)).Padding(th.Space(3), th.Space(3.5)).Gap(th.Space(2.5)).Radius(t.Radius.Md).
			Background(t.WarningBg).TextColor(t.WarningText).Children(func() {
			for _, x := range w.Warnings {
				ui.Column(c).Gap(th.Space(1)).Children(func() {
					ui.Text(c, i18n.Tf("compose.warn", x.Message)).FontWeight(600)
					ui.Text(c, i18n.Tf("compose.warn.from", x.Plugin)).FontSize(t.Text.Sm)
				})
			}
			ui.Row(c).Children(func() {
				kit.Button(c, i18n.T("compose.send_anyway"), kit.ButtonOpts{Variant: kit.Outline, Disabled: p.sending}).OnClick(func() { p.send(w.Digest) })
			})
		})
	}
	if p.blocked != "" {
		ui.Box(c).Padding(0, th.Space(5), th.Space(4), th.Space(5)).Children(func() {
			kit.Alert(c, kit.Danger, p.blocked, nil)
		})
	}
}
