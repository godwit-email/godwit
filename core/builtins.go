package core

import (
	"context"
	"slices"

	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/godwit-email/godwit/core/store"
)

// A built-in plugin ships inside Godwit as Go code: listed and switched with
// the installed plugins (ListPlugins, SetPluginEnabled), on unless the user
// turned it off, but with no files, sandbox, permissions or uninstall. It
// contributes through the same capability contracts as a script plugin
// (cards here), and owns any surface of its own through Service methods
// that consult builtinOn.
type builtinPlugin struct {
	name         string
	title, about plugins.Text
	// card is the plugin's card for a message, as a script plugin's
	// ctx.cards.register; nil for none.
	card func(m plugins.MailMessage) (Card, bool)
}

// SignInCodesPlugin recognizes sign-in codes and links: the banner, the
// tray's recent list and the reader's card.
const SignInCodesPlugin = "sign-in-codes"

var builtinPlugins = []builtinPlugin{{
	name:  SignInCodesPlugin,
	title: plugins.Text{Default: "Sign-in codes & links", ByLang: map[string]string{"zh-Hans": "验证码与登录链接"}},
	about: plugins.Text{Default: "Finds sign-in codes and links in new mail, on this Mac, and offers them to copy or open",
		ByLang: map[string]string{"zh-Hans": "在本机识别新邮件里的验证码和登录链接，一键复制或打开"}},
	card: signInCodeCard,
}}

func builtin(name string) *builtinPlugin {
	for i := range builtinPlugins {
		if builtinPlugins[i].name == name {
			return &builtinPlugins[i]
		}
	}
	return nil
}

// builtinOn reports whether the built-in plugin is on: a missing setting is on.
func (s *Service) builtinOn(ctx context.Context, name string) (bool, error) {
	st, err := s.loadSettings(ctx)
	return err == nil && !slices.Contains(st.DisabledBuiltins, name), err
}

func (s *Service) builtinInfos(ctx context.Context) ([]PluginInfo, error) {
	st, err := s.loadSettings(ctx)
	if err != nil {
		return nil, err
	}
	var out []PluginInfo
	for _, b := range builtinPlugins {
		out = append(out, PluginInfo{Name: b.name, Title: b.title, Description: b.about, Builtin: true, Enabled: !slices.Contains(st.DisabledBuiltins, b.name)})
	}
	return out, nil
}

// setBuiltinEnabled records the switch in the settings, which announces
// settings.changed; the caller announces plugins.changed.
func (s *Service) setBuiltinEnabled(ctx context.Context, name string, on bool) error {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	st, err := s.loadSettings(ctx)
	if err != nil {
		return err
	}
	st.DisabledBuiltins = slices.DeleteFunc(st.DisabledBuiltins, func(n string) bool { return n == name })
	if !on {
		st.DisabledBuiltins = append(st.DisabledBuiltins, name)
	}
	_, err = s.storeSettings(ctx, st)
	return err
}

// builtinCards are the cards of the built-in plugins that are on. They read
// the stored body, links intact: the plugins' copy is sanitized.
func (s *Service) builtinCards(ctx context.Context, id int64) ([]Card, error) {
	rows, err := s.db.Query(ctx, "SELECT subject, body_text, body_html FROM messages WHERE id = ?", id)
	if err != nil || len(rows.Values) == 0 {
		return nil, err
	}
	m := plugins.MailMessage{ID: id, Subject: store.Str(rows.Values[0][0]), Text: store.Str(rows.Values[0][1]), HTML: store.Str(rows.Values[0][2])}
	var out []Card
	for _, b := range builtinPlugins {
		if b.card == nil {
			continue
		}
		on, err := s.builtinOn(ctx, b.name)
		if err != nil {
			return nil, err
		}
		if c, ok := b.card(m); on && ok {
			c.Plugin, c.Builtin = b.name, true
			out = append(out, c)
		}
	}
	return out, nil
}

// signInCodeCard is the reader's card: a code (Copy), and its mail's sign-in
// or confirmation link beside it (Open link); else the link alone (Open),
// its host shown. Title and a link field's Label are keys ("code", or a
// mail.Link* kind) the app words.
func signInCodeCard(m plugins.MailMessage) (Card, bool) {
	code := mail.SignInCode(m.Subject, m.Text, m.HTML)
	link, kind := mail.ActionLink(m.Subject, m.Text, m.HTML)
	switch {
	case code != "":
		c := Card{Title: "code", Fields: []CardField{{Value: mail.GroupCode(code)}}, Buttons: []CardButton{{Kind: "copy", Value: code}}}
		if link != "" {
			c.Fields = append(c.Fields, CardField{Label: kind, Value: mail.LinkHost(link)})
			c.Buttons = append(c.Buttons, CardButton{Kind: "open-url", Value: link})
		}
		return c, true
	case link != "":
		return Card{Title: kind, Fields: []CardField{{Value: mail.LinkHost(link)}}, Buttons: []CardButton{{Kind: "open-url", Value: link}}}, true
	}
	return Card{}, false
}
