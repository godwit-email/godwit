package app

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/richtext"
	"github.com/godwit-email/godwit/internal/theme"
)

// Settings › Signatures: the signatures accounts share ("signatures"), and
// one of them ("signature:<id>") with its editor, the accounts using it by
// default and its removal.

func signatureRoute(id int64) string { return "signature:" + strconv.FormatInt(id, 10) }

// shownSignature is the signature a "signature:<id>" section shows; false
// for another section or one that is gone (the list shows instead).
func (p *settingsPage) shownSignature(sec string) (core.Signature, bool) {
	rest, ok := strings.CutPrefix(sec, "signature:")
	if !ok {
		return core.Signature{}, false
	}
	id, _ := strconv.ParseInt(rest, 10, 64)
	for _, s := range p.a.signatures {
		if s.ID == id {
			return s, true
		}
	}
	return core.Signature{}, false
}

// signatureUsers are the accounts whose default signature is id, as the
// App shows them (a switch flipped shows at once).
func (a *App) signatureUsers(id int64) []core.Account {
	var out []core.Account
	for _, acc := range a.accounts {
		if acc.SignatureID == id {
			out = append(out, acc)
		}
	}
	return out
}

func usedBy(n int) string {
	if n == 0 {
		return i18n.T("settings.signatures.unused")
	}
	return i18n.Tf("settings.signatures.used_by", n)
}

// newSignature makes an untitled signature and opens it.
func (p *settingsPage) newSignature() {
	a := p.a
	var sig core.Signature
	a.do(func(ctx context.Context) (err error) {
		sig, err = a.svc.CreateSignature(ctx, i18n.T("settings.signatures.untitled"))
		return err
	}, func(err error) {
		if err != nil {
			a.toastErr(err)
			return
		}
		a.signatures = append(a.signatures, sig)
		a.navigate(Route{Page: PageSettings, Settings: signatureRoute(sig.ID)})
	})
}

// signaturesView lists the signatures, each with its users and the start
// of its text; a row opens it. None yet is the empty state.
func (p *settingsPage) signaturesView(c *ui.Context) {
	a := p.a
	t, th := theme.Of(c), c.Theme()
	sigs := a.signatures
	var add []func()
	if len(sigs) > 0 {
		add = append(add, func() {
			kit.Button(c, i18n.T("settings.signatures.new"), kit.ButtonOpts{Icon: "plus"}).PaddingX(th.Space(3)).OnClick(p.newSignature)
		})
	}
	pageHeading(c, i18n.T("settings.signatures"), i18n.T("settings.signatures.sub"), add...)
	if len(sigs) == 0 {
		kit.Empty(c, kit.EmptyOpts{Icon: "quill-pen", Title: i18n.T("settings.signatures.empty.title"), Desc: i18n.T("settings.signatures.empty.desc"), Dashed: true, Actions: func() {
			kit.EmptyAction(c, i18n.T("settings.signatures.new"), kit.ButtonOpts{}).OnClick(p.newSignature)
		}})
		return
	}
	var rows []func()
	for _, sig := range sigs {
		rows = append(rows, func() {
			kit.Pressable(c, kit.PressableOpts{OnClick: func() { a.navigate(Route{Page: PageSettings, Settings: signatureRoute(sig.ID)}) }}).
				Label(sig.Name).Radius(0).FillWidth().Gap(th.Space(4)).Padding(th.Space(3), th.Space(4)).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(2)).Children(func() {
					ui.Row(c).Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
						kit.Text(c, sig.Name, t.Text.Body).FontWeight(500).SingleLine().Shrink(1)
						kit.Text(c, usedBy(len(a.signatureUsers(sig.ID))), t.Text.Sm).TextColor(t.Muted).SingleLine()
					})
					lines := core.SignaturePreview(sig.HTML, 2)
					ui.Column(c).FillWidth().Padding(th.Space(2), th.Space(2.5)).Radius(t.Radius.Lg).Background(t.Sidebar).Children(func() {
						if len(lines) == 0 {
							kit.Text(c, i18n.T("settings.signatures.blank"), t.Text.Sm).TextColor(t.Idle)
						}
						for i, l := range lines {
							line := kit.Text(c, l, t.Text.Sm).SingleLine()
							if i == 0 {
								line.FontWeight(600)
							} else {
								line.TextColor(t.Muted)
							}
						}
					})
				})
				ui.Icon(c, icons.Get("chevron-right")).Size(theme.Icon, theme.Icon).TextColor(t.Muted)
			})
		})
	}
	ui.Column(c).Gap(theme.SectionGap).Children(func() {
		settingsCard(c, rows...)
		kit.Text(c, i18n.T("settings.signatures.foot"), t.Text.Sm).TextColor(t.Muted)
	})
	p.syncSettings()
	defer p.syncSettings() // what the switch changed while building
	settingsSection(c, i18n.T("settings.signatures.options"), "", func() {
		kit.SwitchRow(c, &p.st.SignatureDelimiter, i18n.T("settings.signatures.delimiter"), i18n.T("settings.signatures.delimiter.help"))
	})
}

// sigForm is the shown signature's name field: typing saves once it
// pauses; until then the field keeps what was typed.
type sigForm struct {
	id      int64
	name    string
	pending bool
	stop    func()
	gen     uint64
}

// signatureView is one signature: its name, its editor (the signature
// page), the accounts using it by default and its removal.
func (p *settingsPage) signatureView(c *ui.Context, sig core.Signature) {
	a := p.a
	t, th := theme.Of(c), c.Theme()
	f := &p.sigForm
	if f.id != sig.ID {
		p.commitSignatureName()
		*f = sigForm{id: sig.ID}
	}
	if !f.pending {
		f.name = sig.Name
	}
	users := a.signatureUsers(sig.ID)

	ui.Column(c).Gap(theme.HeadingGap).Children(func() {
		kit.Pressable(c, kit.PressableOpts{Bare: true, OnClick: func() { a.navigate(Route{Page: PageSettings, Settings: "signatures"}) }}).
			Label(i18n.T("settings.signatures")).AlignSelf(ui.Start).Gap(th.Space(0.5)).TextColor(t.Muted).Children(func() {
			ui.Icon(c, icons.Get("chevron-left")).Size(theme.Icon, theme.Icon)
			kit.Text(c, i18n.T("settings.signatures"), t.Text.Sm)
		})
		kit.Text(c, sig.Name, t.Text.Display).FontWeight(600)
		kit.Text(c, usedBy(len(users)), t.Text.Body).TextColor(t.Muted)
	})

	bad := !validSignatureName(f.name)
	settingsSection(c, i18n.T("settings.account.basic"), "", func() {
		settingRow(c, i18n.T("settings.signature.name"), i18n.T("settings.signature.name.help"), func() {
			if kit.Input(c, &f.name, kit.InputOpts{Invalid: bad}).Width(theme.SettingsInputWidth).Radius(t.Radius.Lg).Label(i18n.T("settings.signature.name")).Changed() {
				p.editSignatureName()
			}
		})
	})

	if p.sigCode != nil && p.sigCode.id != sig.ID {
		p.closeCode()
	}
	coding := p.sigCode != nil && p.sigCode.id == sig.ID // a code left open for another is saving
	help := i18n.T("settings.account.signature.help")
	if coding {
		help = i18n.T("settings.account.signature.html.help")
	}
	settingsSection(c, i18n.T("settings.signature.content"), help, func() {
		p.signature(sig.ID)
		switch {
		case coding:
			p.codeView(c)
		case p.sigErr != nil:
			settingBlock(c, func() { ui.Text(c, humanize(p.sigErr)).TextColor(t.Muted) })
		case p.sigView != nil:
			ui.WebView(c, p.sigView).FillWidth().Height(theme.SignatureEditorHeight) // the page draws its toolbar and editor
		default:
			ui.Box(c).FillWidth().Height(theme.SignatureEditorHeight)
		}
	})

	names := map[int64]string{}
	for _, s := range a.signatures {
		names[s.ID] = s.Name
	}
	var rows []func()
	for _, acc := range a.accounts {
		rows = append(rows, func() {
			on := acc.SignatureID == sig.ID
			note := i18n.T("settings.signature.using")
			switch {
			case on:
			case names[acc.SignatureID] != "":
				note = i18n.Tf("settings.signature.uses", names[acc.SignatureID])
			default:
				note = i18n.T("settings.signature.uses_none")
			}
			ui.Row(c).FillWidth().Gap(th.Space(3)).Padding(th.Space(2), th.Space(4)).MinHeight(th.Space(12)).AlignItems(ui.Center).Children(func() {
				a.accountMark(c, acc)
				ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(0.5)).Children(func() {
					kit.Text(c, acc.Email, t.Text.Body).FontWeight(500).SingleLine()
					kit.Text(c, note, t.Text.Sm).TextColor(t.Muted)
				})
				was := on
				if kit.Switch(c, &on).Label(i18n.Tf("settings.signature.default.label", acc.Email)).Changed() && on != was {
					id := int64(0)
					if on {
						id = sig.ID
					}
					p.setAccountSignature(acc.ID, id)
				}
			})
		})
	}
	settingsSection(c, i18n.T("settings.signature.default"), i18n.T("settings.signature.default.foot"), rows...)

	settingsSection(c, i18n.T("settings.signature.delete"), "", func() {
		settingRow(c, i18n.T("settings.signature.delete.row"), deleteHint(users), func() {
			settingButton(c, i18n.T("settings.signature.delete"), kit.DestructiveOutline, false).Label(i18n.Tf("settings.signature.delete.aria", sig.Name)).OnClick(func() { p.sigDelete = true })
		})
	})
	p.deleteSignatureDialog(c, sig, users)
	p.linkDialog(c)
}

// deleteHint says which accounts a deletion leaves without a signature.
func deleteHint(users []core.Account) string {
	if len(users) == 0 {
		return i18n.T("settings.signature.delete.unused")
	}
	emails := make([]string, len(users))
	for i, u := range users {
		emails[i] = u.Email
	}
	return i18n.Tf("settings.signature.delete.help", strings.Join(emails, i18n.T("settings.signature.list_sep")))
}

func (p *settingsPage) deleteSignatureDialog(c *ui.Context, sig core.Signature, users []core.Account) {
	if !p.sigDelete {
		return
	}
	a := p.a
	kit.Dialog(c, &p.sigDelete, i18n.Tf("settings.signature.delete.confirm", sig.Name),
		kit.DialogOpts{Size: kit.DialogSm, Desc: deleteHint(users), Close: i18n.T("common.close")}, nil, func() {
			kit.Button(c, i18n.T("common.cancel"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(func() { p.sigDelete = false })
			kit.Button(c, i18n.T("common.delete"), kit.ButtonOpts{Variant: kit.Destructive}).OnClick(func() {
				p.sigDelete = false
				if f := &p.sigForm; f.id == sig.ID && f.stop != nil {
					f.stop()
					*f = sigForm{}
				}
				a.do(func(ctx context.Context) error { return a.svc.DeleteSignature(ctx, sig.ID) }, func(err error) {
					if err != nil {
						a.toastErr(err)
						return
					}
					for i := range a.accounts {
						if a.accounts[i].SignatureID == sig.ID {
							a.accounts[i].SignatureID, a.accounts[i].Signature = 0, ""
						}
					}
					a.navigate(Route{Page: PageSettings, Settings: "signatures"})
					a.load()
				})
			})
		})
}

func validSignatureName(name string) bool {
	n := utf8.RuneCountInString(strings.TrimSpace(name))
	return n > 0 && n <= core.MaxSignatureName
}

// editSignatureName saves the name field once typing pauses; an invalid
// name is not saved.
func (p *settingsPage) editSignatureName() {
	f := &p.sigForm
	f.gen++
	f.pending = true
	if f.stop != nil {
		f.stop()
	}
	f.stop = p.a.after(accountSaveDelay, func() {
		f.stop = nil
		p.saveSignatureName()
	})
}

// commitSignatureName saves at once a name still waiting for typing to pause.
func (p *settingsPage) commitSignatureName() {
	if f := &p.sigForm; f.stop != nil {
		f.stop()
		f.stop = nil
		p.saveSignatureName()
	}
}

func (p *settingsPage) saveSignatureName() {
	a, f := p.a, &p.sigForm
	id, gen, name := f.id, f.gen, strings.TrimSpace(f.name)
	if !validSignatureName(name) {
		return // stays pending: the field shows it invalid
	}
	a.writes.Chain(signatureRoute(id), func(ctx context.Context) error {
		_, err := a.svc.UpdateSignature(ctx, id, core.SignaturePatch{Name: &name})
		return err
	}, func(err error) {
		if f.id == id && f.gen == gen {
			f.pending = false
		}
		if err != nil {
			a.toastErr(err)
		}
		a.load()
	})
}

// setAccountSignature makes signature id (0 none) the account's default,
// shown at once.
func (p *settingsPage) setAccountSignature(accountID, id int64) {
	a := p.a
	for i := range a.accounts {
		if a.accounts[i].ID == accountID {
			a.accounts[i].SignatureID = id
		}
	}
	p.patch(accountID, core.AccountPatch{SignatureID: &id}, nil)
}

// sigDoc is a signature's preview: its editor HTML (images as data:) as a
// richtext document, for the stored HTML it was read for.
type sigDoc struct {
	html string
	doc  richtext.Doc
	ok   bool
}

// signaturePreview is sig's preview, read once per stored HTML; false
// until read.
func (p *settingsPage) signaturePreview(sig core.Signature) (richtext.Doc, bool) {
	a := p.a
	if d, ok := p.sigDocs[sig.ID]; ok && d.html == sig.HTML {
		return d.doc, d.ok
	}
	p.sigDocs[sig.ID] = sigDoc{html: sig.HTML}
	var html string
	a.do(func(ctx context.Context) (err error) {
		html, err = a.svc.SignatureEditor(ctx, sig.ID)
		return err
	}, func(err error) {
		if d := p.sigDocs[sig.ID]; err == nil && d.html == sig.HTML {
			p.sigDocs[sig.ID] = sigDoc{html: sig.HTML, doc: richtext.FromComposeHTML(html), ok: true}
		}
	})
	return richtext.Doc{}, false
}

// accountSignatureView is the account page's 签名: its default signature
// (or none), a read-only preview, and where signatures are edited.
func (p *settingsPage) accountSignatureView(c *ui.Context, acc core.Account) {
	a := p.a
	t, th := theme.Of(c), c.Theme()
	opts := make([]kit.Option[int64], 0, len(a.signatures)+1)
	var cur *core.Signature
	for i, s := range a.signatures {
		opts = append(opts, kit.Option[int64]{Value: s.ID, Label: s.Name})
		if s.ID == acc.SignatureID {
			cur = &a.signatures[i]
		}
	}
	opts = append(opts, kit.Option[int64]{Value: 0, Label: i18n.T("settings.account.signature.none")})
	rows := []func(){func() {
		settingRow(c, i18n.T("settings.account.signature.default"), i18n.T("settings.account.signature.default.help"), func() {
			sel := acc.SignatureID
			if cur == nil {
				sel = 0
			}
			settingSelect(c, i18n.T("settings.account.signature.default"), &sel, opts)
			if sel != acc.SignatureID && (cur != nil || sel != 0) {
				p.setAccountSignature(acc.ID, sel)
			}
		})
	}}
	if cur != nil {
		rows = append(rows, func() {
			settingBlock(c, func() {
				kit.Text(c, i18n.T("settings.account.signature.preview"), t.Text.Sm).TextColor(t.Muted)
				ui.Column(c).FillWidth().Padding(th.Space(2.5), th.Space(3)).Radius(t.Radius.Lg).Background(t.Sidebar).Children(func() {
					if doc, ok := p.signaturePreview(*cur); ok {
						richtext.Render(c, doc, richtext.RenderOptions{OnLink: a.openLink})
					} else {
						kit.Text(c, " ", t.Text.Body)
					}
				})
			})
		})
	}
	ui.Column(c).Gap(theme.SectionGap).Children(func() {
		sectionTitle(c, i18n.T("settings.account.signature"))
		settingsCard(c, rows...)
		// The sentence names where signatures are edited, and that name opens it.
		where := i18n.T("settings.account.signature.where")
		before, after, _ := strings.Cut(i18n.Tf("settings.account.signature.foot", where), where)
		ui.RichText(c).FontSize(t.Text.Sm).TextColor(t.Muted).Children(func() {
			ui.Text(c, before)
			if ui.Link(c, where, "").TextColor(t.Foreground).Underline().Clicked() {
				a.navigate(Route{Page: PageSettings, Settings: "signatures"})
			}
			ui.Text(c, after)
		})
	})
}

// sigCode is a signature in the HTML mode: its stored HTML (images as
// cid:) in MyGo's code editor, saved as typing pauses. What Go refuses
// (too many images, an image it cannot take) is said, never retried.
type sigCode struct {
	id      int64
	st      editor.State
	ver     uint64 // the buffer's version last saved or loaded
	stop    func()
	refused string // the toolbar's alert, a catalog key; "" none
	loading bool
	closing bool // leaving: the last save runs, and the mode ends if it works
}

// openCode puts the shown signature in the HTML mode: its code is read,
// then edited natively where the page was.
func (p *settingsPage) openCode() {
	a, id := p.a, p.sigShown
	if id == 0 || p.sigCode != nil {
		return
	}
	sc := &sigCode{id: id, loading: true}
	sc.st = editor.State{SoftWrap: true, NoFolding: true, Label: i18n.T("settings.account.signature.html.label")}
	p.sigCode = sc
	var code string
	a.do(func(ctx context.Context) (err error) {
		code, err = a.svc.SignatureCode(ctx, id)
		return err
	}, func(err error) {
		if p.sigCode != sc {
			return
		}
		if err != nil {
			p.sigCode = nil
			a.toastErr(err)
			return
		}
		sc.st.Buffer = editor.NewBuffer(code)
		sc.st.Buffer.SetLanguage(editor.LanguageNamed("HTML"))
		sc.ver, sc.loading = sc.st.Buffer.Version(), false
	})
}

// codeView is the HTML mode's card body: a toolbar with what Go refused
// and the pressed 「</> HTML」 toggle, over the code editor.
func (p *settingsPage) codeView(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	sc := p.sigCode
	ui.Column(c).FillWidth().Height(theme.SignatureCodeHeight).Children(func() {
		ui.Row(c).FillWidth().Gap(th.Space(2)).Padding(th.Space(1), th.Space(1.5)).AlignItems(ui.Center).Background(t.Sidebar).Children(func() {
			if sc.refused != "" {
				kit.Text(c, i18n.T(sc.refused), t.Text.Sm).TextColor(t.Danger).SingleLine().Shrink(1).Padding(0, 0, 0, th.Space(2))
			}
			ui.Box(c).Grow(1)
			// The page's toggle, pressed: 28 high, 8 padding, a 16 icon and text-xs (Signature.tsx's Bar).
			kit.Pressable(c, kit.PressableOpts{Bare: true, OnClick: p.closeCode}).Label(i18n.T("settings.account.signature.html.label")).
				Height(th.Space(7)).PaddingX(th.Space(2)).Gap(th.Space(1.5)).AlignItems(ui.Center).Radius(t.Radius.Md).Background(t.HoverTint).Children(func() {
				ui.Icon(c, icons.Get("code")).Size(theme.Icon, theme.Icon)
				kit.Text(c, i18n.T("settings.account.signature.html"), t.Text.Xs).FontWeight(500)
			})
		})
		hairline(c)
		if sc.loading {
			ui.Box(c).FillWidth().Grow(1)
			return
		}
		editor.View(c, &sc.st).FillWidth().Grow(1)
		if v := sc.st.Buffer.Version(); v != sc.ver {
			sc.ver = v
			if sc.stop != nil {
				sc.stop()
			}
			sc.stop = p.a.after(accountSaveDelay, func() {
				sc.stop = nil
				p.saveCode(sc)
			})
		}
	})
}

// saveCode stores the code as the signature: Go sanitizes it. A refusal
// stays said until a save works.
func (p *settingsPage) saveCode(sc *sigCode, then ...func(error)) {
	a, id := p.a, sc.id
	html := sc.st.Buffer.String()
	if strings.TrimSpace(html) != "" {
		html = core.SignatureHTMLPrefix + html
	} else {
		html = ""
	}
	a.writes.Chain(signatureRoute(id), func(ctx context.Context) error {
		_, err := a.svc.UpdateSignature(ctx, id, core.SignaturePatch{HTML: &html})
		return err
	}, func(err error) {
		switch {
		case err == nil:
			sc.refused = ""
		case errors.Is(err, core.ErrSignatureImageCount):
			sc.refused = "settings.account.signature.too_many"
		case errors.Is(err, core.ErrSignatureImage):
			sc.refused = "settings.account.signature.image_invalid"
		default:
			a.toastErr(err)
		}
		for _, f := range then {
			f(err)
		}
	})
}

// commitCode saves at once code still waiting for typing to pause.
func (p *settingsPage) commitCode() {
	if sc := p.sigCode; sc != nil && sc.stop != nil {
		sc.stop()
		sc.stop = nil
		p.saveCode(sc)
	}
}

// closeCode leaves the HTML mode once the code is saved: the page shows
// the signature again as Go cleaned it. A save that fails keeps the code
// open, its edit and what went wrong in view (leaving again tries again).
func (p *settingsPage) closeCode() {
	sc := p.sigCode
	if sc == nil || sc.closing {
		return
	}
	if sc.loading { // nothing typed yet
		p.sigCode = nil
		return
	}
	if sc.stop != nil {
		sc.stop()
		sc.stop = nil
	}
	sc.closing = true
	p.saveCode(sc, func(err error) {
		sc.closing = false
		if err != nil || p.sigCode != sc {
			return
		}
		p.sigCode = nil
		if p.sigShown == sc.id && !p.sigPosted && p.sigShow != nil {
			p.sigShow(sc.id) // the page reads what was stored
		}
	})
}

// sigLink is the link dialog the signature page asked for (SignatureLink):
// the address typed, whether it is refused, and the page's answer.
type sigLink struct {
	open, bad, focused bool
	url                string
	had                bool // the selection is in a link: the dialog can take it off
	answer             func(url string, ok bool)
}

// SignatureLink asks for a link's address in a dialog over the signature
// page: answer gets it (ok), "" with ok to take the link off, or ok false
// when cancelled. current is the link's at the selection, "" none.
func (a *App) SignatureLink(current string, answer func(url string, ok bool)) {
	p := a.pages[PageSettings].(*settingsPage)
	if l := p.sigLink; l != nil {
		l.answer("", false) // an older ask the page gave up on
	}
	if _, ok := p.shownSignature(a.route.Settings); !ok || a.route.Page != PageSettings {
		answer("", false)
		return
	}
	p.sigLink = &sigLink{open: true, url: current, had: current != "", answer: answer}
}

// linkHref is s as a link's address when it is http(s)://host… or
// mailto:target, as the page's safeHref takes it.
func linkHref(s string) (string, bool) {
	s = strings.TrimSpace(s)
	low := strings.ToLower(s)
	if !strings.HasPrefix(low, "http://") && !strings.HasPrefix(low, "https://") && !strings.HasPrefix(low, "mailto:") {
		return "", false
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", false
	}
	if u.Scheme == "mailto" && u.Opaque == "" && u.Path == "" || u.Scheme != "mailto" && u.Host == "" {
		return "", false
	}
	return u.String(), true
}

// linkDialog is the link dialog: the address, 移除链接 for a link already
// there, 取消 and 添加链接. Closing it any other way cancels.
func (p *settingsPage) linkDialog(c *ui.Context) {
	l := p.sigLink
	if l == nil {
		return
	}
	done := func(url string, ok bool) {
		if p.sigLink == l {
			p.sigLink = nil
			l.answer(url, ok)
		}
	}
	submit := func() {
		href, ok := linkHref(l.url)
		if !ok {
			l.bad = true
			return
		}
		done(href, true)
	}
	kit.Dialog(c, &l.open, i18n.T("compose.link"), kit.DialogOpts{Size: kit.DialogSm, Close: i18n.T("common.close")}, func() {
		errText := ""
		if l.bad {
			errText = i18n.T("settings.signature.link.invalid")
		}
		kit.Field(c, i18n.T("compose.link.url"), "", errText, func() {
			in := kit.Input(c, &l.url, kit.InputOpts{Placeholder: "https://", Invalid: l.bad}).Label(i18n.T("compose.link.url"))
			if !l.focused {
				l.focused = true
				in.Focus()
			}
			if in.Changed() {
				l.bad = false
			}
			if in.Submitted() {
				submit()
			}
		})
	}, func() {
		if l.had {
			kit.Button(c, i18n.T("settings.signature.link.remove"), kit.ButtonOpts{Variant: kit.DestructiveOutline}).OnClick(func() { done("", true) })
			ui.Box(c).Grow(1)
		}
		kit.Button(c, i18n.T("common.cancel"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(func() { done("", false) })
		kit.Button(c, i18n.T("compose.link.apply"), kit.ButtonOpts{}).OnClick(submit)
	})
	if !l.open {
		done("", false) // Escape, a click outside, the close button
	}
}
