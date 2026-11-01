package app

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// clientForm asks for the user's own OAuth client: the client_secret_….json
// Google Cloud offers, dropped or chosen, or its ID and secret typed in.
type clientForm struct {
	id, secret string
	savedID    string // the account's, which 换一个 puts back; "" adding one
	imported   bool   // id and secret came from a file
	err        error  // the last file's failure
	manual     bool   // the typed-in fields show
	public     bool   // the provider's public client (auth.PublicClient) instead
	showSecret bool
	gen        uint64 // bumped per import: a read finishing after another started is dropped
}

// client is pub when chosen, else the typed or imported client, trimmed.
func (f *clientForm) client(pub *auth.PublicClient) core.OAuthClient {
	if !f.ownChosen(pub) {
		return core.OAuthClient{ID: pub.ID, Secret: pub.Secret}
	}
	return core.OAuthClient{ID: strings.TrimSpace(f.id), Secret: strings.TrimSpace(f.secret)}
}

// ownChosen: the user's own client is asked for, pub not chosen.
func (f *clientForm) ownChosen(pub *auth.PublicClient) bool { return pub == nil || !f.public }

// publicClient is p's public client, or nil.
func publicClient(p auth.Provider) *auth.PublicClient {
	if p.OAuth == nil {
		return nil
	}
	return p.OAuth.Public
}

// choice picks the user's own client or pub; nothing without pub.
func (f *clientForm) choice(c *ui.Context, pub *auth.PublicClient) {
	if pub == nil {
		return
	}
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).Gap(th.Space(4)).AlignItems(ui.Center).Wrap().Children(func() {
		kit.Text(c, i18n.T("onboarding.own_client.choice"), t.Text.Base).TextColor(t.Muted)
		kit.Radio(c.Key("own"), &f.public, false, i18n.T("onboarding.own_client.choice.own"))
		kit.Radio(c.Key("public"), &f.public, true, i18n.T("onboarding.own_client.choice.public"))
	})
}

// change drops the imported file, or a failed one's message, for another;
// what was typed stays.
func (f *clientForm) change() {
	f.gen++
	if f.imported {
		f.id, f.secret, f.imported = f.savedID, "", false
	}
	f.err = nil
}

// clientFileMax bounds a client file read: Google's are well under 1 KB.
const clientFileMax = 64 << 10

// importFile reads path, or a file the user chooses for "", off the UI thread.
func (f *clientForm) importFile(a *App, path string) {
	f.gen++
	gen, plat := f.gen, a.plat
	var id, secret string
	a.do(func(context.Context) error {
		if path == "" {
			paths, err := plat.OpenFiles(false)
			if err != nil || len(paths) == 0 {
				return err // canceled: nothing changes
			}
			path = paths[0]
		}
		fh, err := os.Open(path)
		if err != nil {
			return err
		}
		defer fh.Close()
		data, err := io.ReadAll(io.LimitReader(fh, clientFileMax))
		if err != nil {
			return err
		}
		id, secret, err = auth.ParseClientFile(data)
		return err
	}, func(err error) {
		if gen != f.gen || err == nil && id == "" {
			return
		}
		f.err = err
		if err == nil {
			f.id, f.secret, f.imported = id, secret, true
		}
	})
}

// view is pub's notice when chosen, else the file's row and, unless a file is
// imported, the typed-in fields under a disclosure.
func (f *clientForm) view(c *ui.Context, a *App, secretHelp string, pub *auth.PublicClient) {
	t, th := theme.Of(c), c.Theme()
	if !f.ownChosen(pub) {
		kit.Alert(c, kit.Warning, i18n.T("onboarding.own_client.public.notice"), nil)
		return
	}
	change := func() {
		kit.Button(c, i18n.T("onboarding.own_client.file.change"), kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeSm}).OnClick(f.change)
	}
	switch {
	case f.err != nil:
		ui.Column(c).Gap(th.Space(1.5)).Children(func() {
			msg, why := i18n.T("onboarding.own_client.file.unreadable"), ""
			switch {
			case errors.Is(f.err, auth.ErrWebClient):
				msg, why = i18n.T("onboarding.own_client.file.web"), i18n.T("onboarding.own_client.file.web.why")
			case errors.Is(f.err, auth.ErrNotClientFile):
				msg = i18n.T("onboarding.own_client.file.not_client")
			}
			f.row(c, a, t.Danger, false, func() {
				ui.Icon(c, icons.Get("triangle-alert")).Size(theme.IconSm, theme.IconSm).TextColor(t.Danger)
				kit.Text(c, msg, t.Text.Sm).TextColor(t.Danger).Grow(1).Shrink(1)
				change()
			})
			if why != "" {
				kit.Text(c, why, t.Text.Xs).TextColor(t.Muted).LineHeight(theme.LineProse)
			}
		})
	case f.imported:
		f.row(c, a, t.Border, false, func() {
			ui.Icon(c, icons.Get("check")).Size(theme.IconSm, theme.IconSm).TextColor(t.Success)
			kit.Text(c, i18n.T("onboarding.own_client.file.imported"), t.Text.Sm)
			kit.Text(c, f.id, t.Text.Xs).Font(theme.Mono).TextColor(t.Muted).MaxLines(1).Grow(1).Shrink(1)
			change()
		})
	default:
		f.row(c, a, t.Border, true, func() {
			ui.Icon(c, icons.Get("file-text")).Size(theme.IconSm, theme.IconSm).TextColor(t.Muted)
			kit.Text(c, i18n.T("onboarding.own_client.file.drop"), t.Text.Sm).TextColor(t.Muted).Grow(1).Shrink(1)
			kit.Button(c, i18n.T("onboarding.own_client.file.choose"), kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeSm}).OnClick(func() { f.importFile(a, "") })
		})
	}
	if !f.imported {
		// Pulled left by its padding, so the chevron lines up with the fields (as manualView).
		kit.Collapsible(c, &f.manual, func() { lineText(c, i18n.T("onboarding.own_client.manual"), t.Text.Base) }, func() {
			ui.Column(c).Gap(th.Space(4)).Children(func() {
				ui.Field(c, i18n.T("onboarding.own_client.id"), func() {
					kit.Input(c, &f.id, kit.InputOpts{Placeholder: "….apps.googleusercontent.com"}).Height(inputHeight(c))
				})
				label := i18n.T("onboarding.own_client.secret")
				ui.Field(c, label, func() { secretInput(c, &f.secret, &f.showSecret, label) }).Description(secretHelp)
			})
		}).AlignSelf(ui.Start).Margin(0, 0, 0, -th.Space(2))
	}
}

// row is one line of the file's state, in a border of color (dashed while
// waiting for a file), taking a file dropped on it in any state.
func (f *clientForm) row(c *ui.Context, a *App, color ui.Color, dashed bool, children func()) {
	t, th := theme.Of(c), c.Theme()
	r := ui.Row(c).Gap(th.Space(2.5)).AlignItems(ui.Center).MinHeight(th.Space(11)).Padding(th.Space(1.5), th.Space(1.5), th.Space(1.5), th.Space(3)).
		Radius(t.Radius.Lg).Border(theme.Stroke, color)
	if dashed {
		r.BorderStyle(ui.BorderDashed)
	}
	if r.FileDragOver() {
		r.Border(theme.Stroke, t.Ring).Background(t.Sidebar)
	}
	if files := r.DroppedFiles(); len(files) > 0 {
		f.importFile(a, files[0])
	}
	r.Children(children)
}
