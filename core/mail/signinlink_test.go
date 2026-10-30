package mail_test

import (
	"testing"

	"github.com/godwit-email/godwit/core/mail"
)

func TestSignInLink(t *testing.T) {
	for _, tt := range []struct{ name, subject, text, html, want string }{
		{"button", "Sign in to Linear", "", `<p>Hi</p><a href="https://linear.app/auth/abc">Log in to Linear</a><a href="https://linear.app/help">Help</a><p>This link expires in 10 minutes.</p>`, "https://linear.app/auth/abc"},
		{"click here", "Your magic link", "", `<a href="https://ct.sendgrid.net/ls/click?x=1">Click here to sign in</a>`, "https://ct.sendgrid.net/ls/click?x=1"},
		{"Chinese", "登录 Acme", "", `<a href="https://acme.example/l/1"><b>立即登录</b></a><p>此登录链接 15 分钟内有效。</p>`, "https://acme.example/l/1"},
		{"repeated", "Sign in", "", `<a href="https://a.example/x">Sign in</a><a href="https://a.example/x">Sign in</a><p>This link expires in 10 minutes.</p>`, "https://a.example/x"},
		{"plain text", "Log in to Acme", "Use this link to log in:\nhttps://acme.example/l/2\n\nThanks", "", "https://acme.example/l/2"},
		{"button and written-out address", "Sign in to Acme", "", `<a href="https://track.example/c/1">Sign in to Acme</a><p>Or log in here:</p><p>https://acme.example/l/9</p><p>This link expires in 10 minutes.</p>`, "https://track.example/c/1"},
		{"a reminder to sign in (Google Photos)", "请登录您的 Google 相册账号以便继续使用面孔分组功能", "", `<p>如果想保留您的面孔分组内容，请使用下方按钮登录 Google 相册。</p><a href="https://accounts.google.com/AccountChooser?continue=https://photos.google.com/reactivate">登录</a>`, ""},
		{"not a sign-in mail", "Weekly digest", "", `<a href="https://a.example/x">Sign in</a>`, ""},
		{"security alert", "New sign-in to your account", "", `<a href="https://a.example/activity">Review sign-in activity</a>`, ""},
		{"activity button", "New sign-in to your account", "", `<a href="https://a.example/activity">Sign in activity</a>`, ""},
		{"Chinese activity", "新的登录", "", `<a href="https://a.example/activity">登录活动</a>`, ""},
		{"sentence end", "Log in to Acme", "Log in here: https://acme.example/auth/abc.\nThis link expires in 10 minutes.", "", "https://acme.example/auth/abc"},
		{"caveat", "Sign in to Acme", "Log in here: https://acme.example/l/3\nIf you did not sign in, visit https://acme.example/security\nThis link expires in 10 minutes.", "", "https://acme.example/l/3"},
		{"angle brackets", "Log in to Acme", "Log in here: <https://acme.example/l/4>\nThis link expires in 10 minutes.", "", "https://acme.example/l/4"},
		{"parentheses", "登录 Acme", "点击登录：（https://acme.example/l/5）\n此登录链接 15 分钟内有效。", "", "https://acme.example/l/5"},
		{"CJK brackets", "登录 Acme", "登录链接：【https://acme.example/l/6】", "", "https://acme.example/l/6"},
		{"plain reply", "RE: Sign in to Acme", "Thanks\n-----Original Message-----\nLog in here: https://acme.example/old", "", ""},
		{"activity line", "New sign-in to your account", "Review sign-in activity: https://acme.example/activity", "", ""},
		{"square brackets", "Log in to Acme", "Log in here: [https://acme.example/auth/abc]\nThis link expires in 10 minutes.", "", "https://acme.example/auth/abc"},
		{"Chinese sentence end", "登录 Acme", "登录链接：https://acme.example/auth/abc。", "", "https://acme.example/auth/abc"},
		{"secure link", "Your secure link to Claude.ai is here", "", `<p>Sign in to Claude.ai</p><a href="https://claude.ai/magic-link#abc">Sign in to Claude.ai</a>`, "https://claude.ai/magic-link#abc"},
		{"caveat next line", "Sign in to Acme", "If you did not sign in\nhttps://acme.example/security", "", ""},
		{"quoted next line", "Sign in to Acme", "Log in:\n> https://acme.example/old", "", ""},
		{"two links", "Sign in", "", `<a href="https://a.example/1">Sign in</a><a href="https://a.example/2">Log in</a>`, ""},
		{"http", "Sign in", "", `<a href="http://a.example/x">Sign in</a>`, ""},
		{"javascript", "Sign in", "", `<a href="javascript:alert(1)">Sign in</a>`, ""},
		{"userinfo", "Sign in", "", `<a href="https://bank.example@evil.example/x">Sign in</a>`, ""},
		{"quoted", "Re: Sign in", "", `<p>Thanks</p><blockquote><a href="https://a.example/x">Sign in</a></blockquote>`, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := linkOf(mail.LinkSignIn, tt.subject, tt.text, tt.html); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestConfirmLink(t *testing.T) {
	for _, tt := range []struct{ name, text, html, want string }{
		{"ID.me", "", idme, "https://account.id.me/confirm/abc"},
		{"English", "", `<a href="https://x.example/v/1">Verify email address</a>`, "https://x.example/v/1"},
		{"plain text", "Confirm your email: https://x.example/v/2", "", "https://x.example/v/2"},
		{"Hugging Face", "", `<div><p><b>Confirm your email address by clicking on this link:</b></p><p><a href="https://huggingface.co/email_confirmation/BUrUCuhKkJGfUBKhTYHASUpMLTdm">https://huggingface.co/email_confirmation/BUrUCuhKkJGfUBKhTYHASUpMLTdm</a></p><p>If you didn't create a Hugging Face account, you can ignore this email.</p></div>`, "https://huggingface.co/email_confirmation/BUrUCuhKkJGfUBKhTYHASUpMLTdm"},
		{"query in an HTML link", "", `<p>Verify your email:</p><p><a href="https://x.example/v?a=1&amp;b=2">https://x.example/v?a=1&amp;b=2</a></p>`, "https://x.example/v?a=1&b=2"},
		{"not a confirm button", "", `<a href="https://x.example/s">Email settings</a>`, ""},
		{"two", "", `<a href="https://x.example/1">Confirm your email</a><a href="https://x.example/2">Verify email</a>`, ""},
	} {
		if got := linkOf(mail.LinkConfirm, "", tt.text, tt.html); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
	if got := linkOf(mail.LinkSignIn, "欢迎来到 ID.me!", "", idme); got != "" {
		t.Fatalf("a confirm link is not a sign-in link: %q", got)
	}
}

// linkOf is ActionLink's link when it is of kind, else "".
func linkOf(kind, subject, text, html string) string {
	if l, k := mail.ActionLink(subject, text, html); k == kind {
		return l
	}
	return ""
}

func TestActionLinkBadCases(t *testing.T) {
	for _, tt := range []struct{ name, subject, text, html, want, kind string }{
		{"image button with a fallback line (H2NEXUS)", "Confirm your email to finish registration",
			"Confirm your email address to finish registration and open the client area.\n (https://my.h2.nexus?func=register.activate&key=4A31) \nIf the button does not work, use this link:\nhttps://my.h2.nexus?func=register.activate&key=4A31 (https://my.h2.nexus?func=register.activate&key=4A31) \n",
			"", "https://my.h2.nexus?func=register.activate&key=4A31", mail.LinkConfirm},
		{"Use this link (TypeSafe)", "Sign in to TypeSafe", "",
			`<a href="https://login.example/magic?t=1"><img alt=""></a><p>Button not working? <a href="https://login.example/magic?t=1">Use this link</a> instead.</p>`,
			"https://login.example/magic?t=1", mail.LinkSignIn},
		{"Traditional password reset (DMIT)", "重置密碼的驗證", "",
			`<p>若要重置密碼，請訪問下面的連結：</p><p><a href="https://track.example/x">https://www.dmit.io/index.php?rp=/password/reset/use/key/4c6d</a></p>`,
			"https://www.dmit.io/index.php?rp=/password/reset/use/key/4c6d", mail.LinkReset},
		{"security alert with a fallback link", "New sign-in to your account", "", `<p>We noticed a new sign-in. If this link expires, sign in again.</p><a href="https://a.example/activity">Use this link</a>`, "", ""},
		{"reset button without a reset subject", "Forgot your password?", "", `<a href="https://a.example/reset/1">Reset password</a>`, "https://a.example/reset/1", mail.LinkReset},
		{"two URLs after a lead", "Sign in to Acme", "Log in here:\nhttps://a.example/auth https://b.example/auth\nThis link expires in 10 minutes.", "", "", ""},
		{"Traditional sign-in link", "登入 Acme", "登入連結：https://acme.example/l/7\n此連結 10 分鐘內有效。", "", "https://acme.example/l/7", mail.LinkSignIn},
		{"reply to a magic link", "Re: Your magic link", "Thanks!\n-- \nSign in: https://me.example/login\nThis link is my site.", "", "", ""},
		{"reset by its fallback line", "Password reset", "Button does not work, use this link:\nhttps://a.example/reset/2", "", "https://a.example/reset/2", mail.LinkReset},
		{"no fallback outside a known kind", "Your invoice", "If the button does not work, use this link:\nhttps://x.example/invoice", "", "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if l, k := mail.ActionLink(tt.subject, tt.text, tt.html); l != tt.want || k != tt.kind {
				t.Fatalf("got %q %q, want %q %q", l, k, tt.want, tt.kind)
			}
		})
	}
}

// A look-alike host shows as punycode, however the link spells it.
func TestLinkHostLookAlike(t *testing.T) {
	for link, want := range map[string]string{
		"https://www.apple.com/x":      "apple.com",
		"https://аpple.com/login":      "xn--pple-43d.com",
		"https://%D0%B0pple.com/login": "xn--pple-43d.com",
		"https://例子.测试/a":              "xn--fsqu00a.xn--0zwm56d",
	} {
		if got := mail.LinkHost(link); got != want {
			t.Errorf("LinkHost(%q) = %q, want %q", link, got, want)
		}
	}
}
