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
// contributes through the plugin capability contracts (tags here), and owns any surface of its own through Service methods
// that consult builtinOn.
type builtinPlugin struct {
	name         string
	title, about plugins.Text
	// tags are what the plugin puts after a message's subject; nil for none.
	tags func(m plugins.MailMessage) []Tag
}

// SignInCodesPlugin recognizes sign-in codes and links: the banner, the
// tray's recent list and the reader's card.
const SignInCodesPlugin = "sign-in-codes"

var builtinPlugins = []builtinPlugin{{
	name:  SignInCodesPlugin,
	title: plugins.Text{Default: "Sign-in codes & links", ByLang: map[string]string{"zh-Hans": "验证码与登录链接"}},
	about: plugins.Text{Default: "Finds sign-in codes and links in new mail, on this Mac, and offers them to copy or open",
		ByLang: map[string]string{"zh-Hans": "在本机识别新邮件里的验证码和登录链接，一键复制或打开"}},
	tags: signInCodeTags,
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

// builtinTags are the tags of the built-in plugins that are on. They read
// the stored body, links intact: the plugins' copy is sanitized.
func (s *Service) builtinTags(ctx context.Context, id int64) ([]Tag, error) {
	rows, err := s.db.Query(ctx, "SELECT subject, body_text, body_html FROM messages WHERE id = ?", id)
	if err != nil || len(rows.Values) == 0 {
		return nil, err
	}
	m := plugins.MailMessage{ID: id, Subject: store.Str(rows.Values[0][0]), Text: store.Str(rows.Values[0][1]), HTML: store.Str(rows.Values[0][2])}
	var out []Tag
	for _, b := range builtinPlugins {
		if b.tags == nil {
			continue
		}
		on, err := s.builtinOn(ctx, b.name)
		if err != nil {
			return nil, err
		}
		if !on {
			continue
		}
		for _, t := range b.tags(m) {
			t.Plugin, t.Builtin = b.name, true
			out = append(out, t)
		}
	}
	return out, nil
}

// signInCodeTags are a code to copy and the mail's sign-in, confirmation
// or password-reset link to open, its host shown.
func signInCodeTags(m plugins.MailMessage) []Tag {
	var out []Tag
	if code := mail.SignInCode(m.Subject, m.Text, m.HTML); code != "" {
		out = append(out, Tag{Kind: "copy", Text: code, Value: code})
	}
	if link, kind := mail.ActionLink(m.Subject, m.Text, m.HTML); link != "" {
		out = append(out, Tag{Kind: "open-url", Label: kind, Text: mail.LinkHost(link), Value: link})
	}
	return out
}
