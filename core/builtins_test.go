package core

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSignInCodesBuiltinPlugin(t *testing.T) {
	e := newSyncEnv(t)
	now := time.Now().Truncate(time.Second)
	e.sync(t)
	e.put(t, "INBOX", eml("code@x", "Login <login@example.com>", e.acc.Email, "Verification code: 482913", now.Format(time.RFC1123Z), "Do not share it."))
	e.put(t, "INBOX", eml("link@x", "Acme <id@acme.example>", e.acc.Email, "Sign in to Acme", now.Format(time.RFC1123Z), "Log in here: https://acme.example/l/1\nThis link expires in 10 minutes."))
	e.sync(t)
	codeID, linkID := e.idOf(t, "code@x"), e.idOf(t, "link@x")

	builtinOf := func() PluginInfo {
		list, err := e.s.ListPlugins(bg)
		require.NoError(t, err)
		i := slices.IndexFunc(list, func(p PluginInfo) bool { return p.Name == SignInCodesPlugin })
		require.GreaterOrEqual(t, i, 0, "the built-in plugin is listed")
		return list[i]
	}
	p := builtinOf()
	require.True(t, p.Builtin)
	require.True(t, p.Enabled, "on by default")
	require.Equal(t, "验证码与登录链接", p.Title.For("zh-Hans"))

	tags, err := e.s.PluginTags(bg, codeID)
	require.NoError(t, err)
	require.Equal(t, []Tag{{Plugin: SignInCodesPlugin, Builtin: true, Kind: "copy", Text: "482 913", Value: "482913"}}, tags)
	tags, err = e.s.PluginTags(bg, linkID)
	require.NoError(t, err)
	require.Equal(t, []Tag{{Plugin: SignInCodesPlugin, Builtin: true, Kind: "open-url", Label: "signin", Text: "acme.example", Value: "https://acme.example/l/1"}}, tags)
	cards, err := e.s.PluginCards(bg, codeID)
	require.NoError(t, err)
	require.Empty(t, cards, "the plugin adds tags, not a card")
	e.put(t, "INBOX", eml("both@x", "ID.me <hello@id.me>", e.acc.Email, "欢迎来到 ID.me!", now.Format(time.RFC1123Z),
		"确认您的电子邮件：https://account.id.me/confirm/abc\n\n复制此代码并在浏览器中输入以完成确认。\n\n118169"))
	e.sync(t)
	tags, err = e.s.PluginTags(bg, e.idOf(t, "both@x"))
	require.NoError(t, err)
	require.Equal(t, []Tag{{Plugin: SignInCodesPlugin, Builtin: true, Kind: "copy", Text: "118 169", Value: "118169"},
		{Plugin: SignInCodesPlugin, Builtin: true, Kind: "open-url", Label: "confirm", Text: "account.id.me", Value: "https://account.id.me/confirm/abc"}}, tags)
	codes, err := e.s.RecentSignInCodes(bg, now)
	require.NoError(t, err)
	require.Len(t, codes, 3)
	both := codes[slices.IndexFunc(codes, func(c SignInCode) bool { return c.Code == "118169" })]
	require.Equal(t, "confirm", both.LinkKind)
	require.Equal(t, "https://account.id.me/confirm/abc", both.Link)

	require.NoError(t, e.s.SetPluginEnabled(bg, SignInCodesPlugin, false))
	require.False(t, builtinOf().Enabled)
	tags, err = e.s.PluginTags(bg, codeID)
	require.NoError(t, err)
	require.Empty(t, tags, "off: no tag")
	codes, err = e.s.RecentSignInCodes(bg, now)
	require.NoError(t, err)
	require.Empty(t, codes, "off: no banner, tray entry or held-back notification")

	st, err := e.s.GetSettings(bg)
	require.NoError(t, err)
	st.DisabledBuiltins = nil // a page's stale snapshot
	_, err = e.s.SetSettings(bg, st)
	require.NoError(t, err)
	require.False(t, builtinOf().Enabled, "saving other settings keeps the switch")

	require.NoError(t, e.s.SetPluginEnabled(bg, SignInCodesPlugin, true))
	require.True(t, builtinOf().Enabled)
	require.ErrorIs(t, e.s.UninstallPlugin(bg, SignInCodesPlugin), ErrPluginNotFound, "a built-in plugin only switches off")
}

func TestPluginCannotTakeABuiltinName(t *testing.T) {
	e := newSyncEnv(t)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"name":"`+SignInCodesPlugin+`","version":"0.1.0","description":"x","main":"index.ts","permissions":{}}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.ts"), []byte(`export default function () {}`), 0o600))
	_, err := e.s.InstallPlugin(bg, dir)
	require.ErrorContains(t, err, "built into Godwit")
}
