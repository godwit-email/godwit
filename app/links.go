package app

import (
	"context"
	"net/url"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/kit"
)

// openLink handles a link activated in mail content: mailto: starts a
// draft to its addresses; http(s) asks first, showing the whole address;
// any other scheme is ignored.
func (a *App) openLink(link string) {
	u, err := url.Parse(link)
	if err != nil {
		return
	}
	switch strings.ToLower(u.Scheme) {
	case "mailto":
		a.mailto(u)
	case "http", "https":
		a.link, a.linkOpen = link, true
	}
}

// mailto starts a new draft from the reader's account (else the default
// one) to the link's addresses, and opens it.
func (a *App) mailto(u *url.URL) {
	addrs, _ := url.PathUnescape(u.Opaque)
	var to []string
	for _, s := range strings.Split(addrs, ",") {
		if s = strings.TrimSpace(s); s != "" {
			to = append(to, s)
		}
	}
	acc := a.mail.reader.account()
	if acc == 0 {
		acc = a.defaultAccount()
	}
	var id int64
	a.do(func(ctx context.Context) error {
		d, err := a.svc.NewDraft(ctx, acc, "new", 0)
		if err != nil {
			return err
		}
		d.To = to
		d, err = a.svc.SaveDraft(ctx, d)
		id = d.ID
		return err
	}, func(err error) {
		if err != nil {
			a.toastErr(err)
			return
		}
		a.navigate(Route{Page: PageCompose, DraftID: id})
	})
}

// defaultAccount is the default account's ID, else the first's; 0 for none.
func (a *App) defaultAccount() int64 {
	for _, acc := range a.accounts {
		if acc.IsDefault {
			return acc.ID
		}
	}
	if len(a.accounts) > 0 {
		return a.accounts[0].ID
	}
	return 0
}

// linkDialog asks before a link opens: the real address in full, its host
// in bold; Open, Copy link or Cancel (Escape).
func (a *App) linkDialog(c *ui.Context) {
	if !a.linkOpen {
		return
	}
	link := a.link
	kit.Dialog(c, &a.linkOpen, i18n.T("link.title"), func() {
		ui.RichText(c, linkSpans(link)...)
	}, func() {
		if kit.Button(c, i18n.T("common.cancel"), kit.ButtonOpts{Variant: kit.Outline}).Clicked() {
			a.linkOpen = false
		}
		if kit.Button(c, i18n.T("link.copy"), kit.ButtonOpts{Variant: kit.Outline}).Clicked() {
			c.WriteClipboard(link)
			a.linkOpen = false
			a.toast(i18n.T("link.copied"))
		}
		if kit.Button(c, i18n.T("link.open"), kit.ButtonOpts{}).AutoFocus().Clicked() {
			a.linkOpen = false
			a.do(func(context.Context) error { return a.plat.OpenURL(link) }, a.toastErr)
		}
	})
}

// linkSpans are the address with its host in bold.
func linkSpans(link string) []ui.Span {
	u, err := url.Parse(link)
	if err != nil || u.Host == "" {
		return []ui.Span{{Text: link}}
	}
	i := strings.Index(link, u.Host)
	if i < 0 {
		return []ui.Span{{Text: link}}
	}
	j := i + len(u.Host)
	return []ui.Span{{Text: link[:i]}, {Text: u.Host, Weight: 600}, {Text: link[j:]}}
}
