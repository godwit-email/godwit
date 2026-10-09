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
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/richtext"
	"github.com/godwit-email/godwit/internal/theme"
)

// composer is the compose page of one draft: its fields, the editor of the
// body's editable part, the read-only quote and signature, attachments and
// sending. Its draftSaver saves it. The App keeps it (a.drafts) from the
// draft's opening until its saves are done after leaving, so reopening the
// draft meanwhile shows it again.
type composer struct {
	a      *App
	id     int64
	loaded bool
	loads  uint64 // bumped per load of the editor; older conversions are dropped

	acc, accSel           int64 // the sending account; the Select's choice
	to, cc, bcc, replyTo  []string
	pending               [4]string // what is typed in the address fields, not yet an address
	subject               string
	showCc                bool
	ed                    *editor.Editor
	edVer                 uint64 // the editor's Version last seen
	quote, sig            string // the body's read-only parts, as HTML
	quoteOpen             bool
	atts                  []core.DraftAttachment
	attachErr             string
	link                  string
	linkOpen, discardOpen bool

	src      int64 // the message the draft replies to or forwards
	wit      *composeWit
	s        *draftSaver
	sending  bool
	frozen   bool                                                     // the draft is being sent: no edit is taken
	warn     *core.SendCheck                                          // beforeSend's warnings, until sent anyway or edited
	blocked  string                                                   // a plugin's refusal
	deleteFn func(ctx context.Context, id int64) error                // tests swap it
	sendFn   func(ctx context.Context, id int64, digest string) error // tests swap it
}

// prepared is a draft with its body taken apart and converted for the
// editor, off the UI thread.
type prepared struct {
	d          core.Draft
	doc        richtext.Doc
	quote, sig string
}

func prepare(d core.Draft) prepared {
	editable, quote, sig := mail.SplitDraft(d.HTML)
	return prepared{d, richtext.FromComposeHTML(editable), quote, sig}
}

// composerFor is draft id's composer: the one kept, else a new one that
// reads the draft.
func (a *App) composerFor(id int64) *composer {
	if p := a.drafts[id]; p != nil {
		return p
	}
	p := &composer{a: a, id: id, deleteFn: a.svc.DeleteDraft, sendFn: a.sendDraft}
	a.drafts[id] = p
	var pd prepared
	get := a.getDraft
	a.query(func(ctx context.Context) error {
		d, err := get(ctx, id)
		pd = prepare(d)
		return err
	}, func(err error) {
		if !p.here() {
			return
		}
		if err != nil {
			delete(a.drafts, id)
			a.toastErr(err)
			return
		}
		p.fill(pd)
		p.s = newDraftSaver(a, pd.d)
		p.s.snapshot, p.s.commit, p.s.composing = p.snapshot, p.commitAll, p.ed.Composing
		p.wit = newComposeWit(p)
		p.wit.openConversation()
		p.loaded = true
	})
	return p
}

// fill shows pd, replacing what the page shows.
func (p *composer) fill(pd prepared) {
	d := pd.d
	p.loads++
	p.acc, p.accSel = d.AccountID, d.AccountID
	p.to, p.cc, p.bcc = slices.Clone(d.To), slices.Clone(d.Cc), slices.Clone(d.Bcc)
	p.replyTo = nil
	if d.ReplyTo != "" {
		p.replyTo = []string{d.ReplyTo}
	}
	p.pending = [4]string{}
	p.subject, p.atts, p.src = d.Subject, d.Attachments, d.SourceMessageID
	p.showCc = p.showCc || len(d.Cc)+len(d.Bcc) > 0 || d.ReplyTo != ""
	p.quote, p.sig = pd.quote, pd.sig
	if p.ed == nil {
		p.ed = editor.New(pd.doc, p.a.clock.Now)
	} else {
		p.ed.Replace(pd.doc)
	}
	p.edVer = p.ed.Version() // what was loaded is no edit
}

// snapshot is the draft as the page shows it, for the saver.
func (p *composer) snapshot() core.Draft {
	d := core.Draft{AccountID: p.acc, To: slices.Clone(p.to), Cc: slices.Clone(p.cc), Bcc: slices.Clone(p.bcc),
		Subject: p.subject, HTML: mail.JoinDraft(p.ed.HTML(), p.quote, p.sig)}
	if len(p.replyTo) > 0 {
		d.ReplyTo = p.replyTo[0]
	}
	return d
}

func (p *composer) edited() {
	p.warn = nil // the checks were of another draft
	p.s.Edited()
}

func (p *composer) lists() [4]*[]string { return [4]*[]string{&p.to, &p.cc, &p.bcc, &p.replyTo} }

// commit makes what is typed in address field i addresses: a comma
// separates them; Reply-To keeps one, the last.
func (p *composer) commit(i int) {
	list, changed := p.lists()[i], false
	for _, s := range strings.Split(p.pending[i], ",") {
		if s = strings.TrimSpace(s); s == "" || slices.Contains(*list, s) {
			continue
		}
		if i == 3 {
			*list = nil
		}
		*list, changed = append(*list, s), true
	}
	p.pending[i] = ""
	if changed {
		p.edited()
	}
}

// commitAll commits every address field: before a flush, nothing typed is lost.
func (p *composer) commitAll() {
	for i := range p.pending {
		if p.pending[i] != "" {
			p.commit(i)
		}
	}
}

// flush saves what is left; done gets the error that kept it from saving.
// A composer whose draft is saved and not shown is let go.
func (p *composer) flush(done func(err error)) {
	if p.s == nil { // its load is dropped once the route leaves: read it again on reopening
		if !p.shown() {
			delete(p.a.drafts, p.id)
		}
		done(nil)
		return
	}
	p.s.Flush(func(err error) {
		if err == nil && !p.shown() && p.a.drafts[p.id] == p {
			delete(p.a.drafts, p.id)
		}
		done(err)
	})
}

// leave saves what is left as the page goes away, telling a failure.
func (p *composer) leave() { p.flush(p.a.toastErr) }

// flushDrafts flushes every kept draft; done gets their errors once all are done.
func (a *App) flushDrafts(done func(err error)) {
	ps := make([]*composer, 0, len(a.drafts))
	for _, p := range a.drafts {
		ps = append(ps, p)
	}
	left := len(ps)
	if left == 0 {
		done(nil)
		return
	}
	var errs []error
	for _, p := range ps {
		p.flush(func(err error) {
			errs = append(errs, err)
			if left--; left == 0 {
				done(errors.Join(errs...))
			}
		})
	}
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

// shown reports whether the page shows this composer; false from the
// moment the route starts leaving it.
func (p *composer) shown() bool { return p.a.mail.draft == p }

// here reports whether the route shows this draft.
func (p *composer) here() bool {
	return p.a.route.Page == PageCompose && p.a.route.DraftID == p.id
}

// switchAccount moves the draft to account acc, after the saves before it;
// the signature becomes acc's.
func (p *composer) switchAccount(acc int64) {
	svc, id := p.a.svc, p.id
	var d core.Draft
	var sig string
	p.s.Run(func(ctx context.Context, rev int64) (core.Draft, error) {
		var err error
		d, err = svc.SetDraftAccount(ctx, id, acc, rev)
		_, _, sig = mail.SplitDraft(d.HTML)
		return d, err
	}, func(err error) {
		if err != nil {
			p.accSel = p.acc
			if !errors.Is(err, core.ErrStaleDraft) { // the conflict notice tells
				p.a.toastErr(err)
			}
			return
		}
		p.acc, p.accSel, p.sig = d.AccountID, d.AccountID, sig
	})
}

// attachRun runs change, an attachment change that bumps the revision once
// per attachment changed (n), then reads the draft back: a revision beyond
// that is a change from elsewhere, a conflict.
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
			return core.Draft{}, core.ErrStaleDraft
		}
		return d, cerr // the revision moved for what did change
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
// warnings of that check. From the last save until Send returns the draft
// is frozen: an edit then would be neither sent nor saved.
func (p *composer) send(digest string) {
	if p.sending {
		return
	}
	p.sending, p.warn, p.blocked = true, nil, ""
	send, id := p.sendFn, p.id
	p.s.RunFrom(func() { p.frozen = true }, func(ctx context.Context, _ int64) (core.Draft, error) {
		return core.Draft{}, send(ctx, id, digest)
	}, func(err error) {
		p.sending, p.frozen = false, false
		var warned *core.SendWarningsError
		var blocked *core.SendBlockedError
		switch {
		case err == nil:
			p.gone()
			p.a.toast(i18n.T("compose.sent"))
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

// sendDraft sends draft id; with digest, despite that check's warnings.
func (a *App) sendDraft(ctx context.Context, id int64, digest string) error {
	if digest != "" {
		return a.svc.SendAcknowledged(ctx, id, digest)
	}
	return a.svc.Send(ctx, id)
}

// gone ends the composer of a draft sent or deleted: it never saves again.
func (p *composer) gone() {
	p.s.Stop()
	if p.a.drafts[p.id] == p {
		delete(p.a.drafts, p.id)
	}
	if p.here() {
		p.a.back()
	}
}

// discard deletes the draft; saving pauses meanwhile and resumes if that fails.
func (p *composer) discard() {
	p.discardOpen = false
	p.s.Hold()
	del, id := p.deleteFn, p.id
	p.s.Run(func(ctx context.Context, _ int64) (core.Draft, error) {
		return core.Draft{}, del(ctx, id)
	}, func(err error) {
		if err != nil {
			p.s.Release()
			p.a.toastErr(err)
			return
		}
		p.gone()
	})
}

func (p *composer) keepMine() {
	p.atts = p.s.latest.Attachments
	p.s.KeepMine()
}

// useLatest loads the latest draft into the page, converted off the UI
// thread, dropping the local snapshot.
func (p *composer) useLatest() {
	d := *p.s.latest
	p.loads++
	loads := p.loads
	var pd prepared
	p.a.do(func(context.Context) error { pd = prepare(d); return nil }, func(error) {
		if loads != p.loads || !p.s.conflict || p.s.latest == nil || p.s.latest.Revision != d.Revision {
			return // something newer happened meanwhile
		}
		p.fill(pd)
		p.s.UseLatest(d)
	})
}

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

// addresses is address field i: its addresses as chips (red when invalid),
// each with a button taking it out, before an input where Enter, a comma
// or leaving it makes what was typed addresses.
func (p *composer) addresses(c *ui.Context, i int, label string) {
	t, th := theme.Of(c), c.Theme()
	list := p.lists()[i]
	ui.Row(c).Wrap().Grow(1).Shrink(1).Gap(th.Space(1.5)).Children(func() {
		for _, addr := range *list {
			bg, fg := t.Hover, t.Foreground
			if len(invalid([]string{addr})) > 0 {
				bg, fg = t.DangerBg, t.Danger
			}
			ui.Row(c).Key(addr).Gap(th.Space(1)).Padding(th.Space(0.5), th.Space(1), th.Space(0.5), th.Space(2)).Radius(t.Radius.Full).Background(bg).TextColor(fg).Children(func() {
				ui.Text(c, addr).SingleLine()
				ui.ButtonBase(c).Size(th.Space(4), th.Space(4)).Radius(t.Radius.Full).Center().Label(i18n.Tf("compose.remove", addr)).Children(func() {
					ui.Icon(c, icons.Get("x")).Size(theme.IconXs, theme.IconXs)
				}).OnClick(func() {
					*list = slices.DeleteFunc(*list, func(s string) bool { return s == addr })
					p.edited()
				})
			})
		}
		in := ui.TextInputBase(c, &p.pending[i]).Grow(1).MinWidth(th.Space(30)).Label(label)
		if p.pending[i] != "" && (in.Submitted() || strings.Contains(p.pending[i], ",") || !in.Focused()) {
			p.commit(i)
		}
	})
}

func (p *composer) view(c *ui.Context) {
	if !p.loaded {
		return
	}
	t, th := theme.Of(c), c.Theme()
	p.wit.sync()
	ui.Column(c).Grow(1).Shrink(1).Children(func() {
		ui.Column(c).Disabled(p.frozen).Children(func() {
			p.fields(c)
			p.toolbar(c)
		})
		p.wit.banner(c)
		if p.s.conflict {
			ui.Box(c).Padding(th.Space(3), th.Space(5), 0, th.Space(5)).Children(func() { p.conflictNotice(c) })
		}
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Padding(th.Space(4), th.Space(5)).Gap(th.Space(3)).Children(func() { p.body(c) })
		})
		p.notices(c)
		ui.Row(c).Gap(th.Space(2)).Padding(th.Space(3), th.Space(5)).BorderWidth(theme.Hairline, 0, 0, 0).BorderColor(t.Border).Children(func() {
			kit.Button(c, i18n.T("compose.send"), kit.ButtonOpts{Disabled: p.sending}).OnClick(func() { p.send("") })
			kit.Button(c, i18n.T("compose.discard"), kit.ButtonOpts{Variant: kit.Ghost, Disabled: p.sending}).OnClick(func() { p.discardOpen = true })
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
		p.wit.applied = false // Undo would no longer find Wit's edit
		p.edited()
	}
}

// conflictNotice offers the choice a stale write left: keep the local
// version or load the latest; reading the latest may need retrying first.
func (p *composer) conflictNotice(c *ui.Context) {
	small := kit.ButtonOpts{Variant: kit.Outline, Small: true}
	text := i18n.T("compose.conflict")
	if p.s.latestErr != nil {
		text += " · " + humanize(p.s.latestErr)
	}
	kit.Alert(c, kit.Warning, text, func() {
		switch {
		case p.s.latest != nil:
			kit.Button(c, i18n.T("compose.conflict.keep"), small).OnClick(p.keepMine)
			kit.Button(c, i18n.T("compose.conflict.latest"), small).OnClick(p.useLatest)
		case p.s.latestErr != nil:
			kit.Button(c, i18n.T("common.retry"), small).OnClick(p.s.Refetch)
		}
	})
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
	p.row(c, i18n.T("compose.to"), func() {
		p.addresses(c, 0, i18n.T("compose.to.input"))
		if !p.showCc {
			ui.ButtonBase(c).FontSize(t.Text.Sm).TextColor(t.Muted).Children(func() {
				ui.Text(c, i18n.T("compose.cc_bcc"))
			}).OnClick(func() { p.showCc = true })
		}
	})
	if p.showCc {
		p.row(c, i18n.T("compose.cc"), func() { p.addresses(c, 1, i18n.T("compose.cc.input")) })
		p.row(c, i18n.T("compose.bcc"), func() { p.addresses(c, 2, i18n.T("compose.bcc.input")) })
		p.row(c, i18n.T("compose.reply_to"), func() { p.addresses(c, 3, i18n.T("compose.reply_to.input")) })
	}
	if bad := invalid(p.to, p.cc, p.bcc, p.replyTo); len(bad) > 0 {
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
				kit.IconButton(c, "x", i18n.Tf("compose.remove", att.Filename), ghost).OnClick(func() { p.removeAttachment(att.ID) })
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
	if !p.wit.body(c) {
		p.wit.editor(c)
	}
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
