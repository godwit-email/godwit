package mail_test

import (
	"testing"

	"github.com/godwit-email/godwit/core/mail"
)

func TestSignInLink(t *testing.T) {
	for _, tt := range []struct{ name, subject, text, html, want string }{
		{"button", "Sign in to Linear", "", `<p>Hi</p><a href="https://linear.app/auth/abc">Log in to Linear</a><a href="https://linear.app/help">Help</a>`, "https://linear.app/auth/abc"},
		{"click here", "Your magic link", "", `<a href="https://ct.sendgrid.net/ls/click?x=1">Click here to sign in</a>`, "https://ct.sendgrid.net/ls/click?x=1"},
		{"Chinese", "登录 Acme", "", `<a href="https://acme.example/l/1"><b>立即登录</b></a>`, "https://acme.example/l/1"},
		{"repeated", "Sign in", "", `<a href="https://a.example/x">Sign in</a><a href="https://a.example/x">Sign in</a>`, "https://a.example/x"},
		{"plain text", "Log in to Acme", "Use this link to log in:\nhttps://acme.example/l/2\n\nThanks", "", "https://acme.example/l/2"},
		{"not a sign-in mail", "Weekly digest", "", `<a href="https://a.example/x">Sign in</a>`, ""},
		{"security alert", "New sign-in to your account", "", `<a href="https://a.example/activity">Review sign-in activity</a>`, ""},
		{"activity button", "New sign-in to your account", "", `<a href="https://a.example/activity">Sign in activity</a>`, ""},
		{"Chinese activity", "新的登录", "", `<a href="https://a.example/activity">登录活动</a>`, ""},
		{"sentence end", "Log in to Acme", "Log in here: https://acme.example/auth/abc.", "", "https://acme.example/auth/abc"},
		{"caveat", "Sign in to Acme", "Log in here: https://acme.example/l/3\nIf you did not sign in, visit https://acme.example/security", "", "https://acme.example/l/3"},
		{"caveat next line", "Sign in to Acme", "If you did not sign in\nhttps://acme.example/security", "", ""},
		{"quoted next line", "Sign in to Acme", "Log in:\n> https://acme.example/old", "", ""},
		{"two links", "Sign in", "", `<a href="https://a.example/1">Sign in</a><a href="https://a.example/2">Log in</a>`, ""},
		{"http", "Sign in", "", `<a href="http://a.example/x">Sign in</a>`, ""},
		{"javascript", "Sign in", "", `<a href="javascript:alert(1)">Sign in</a>`, ""},
		{"userinfo", "Sign in", "", `<a href="https://bank.example@evil.example/x">Sign in</a>`, ""},
		{"quoted", "Re: Sign in", "", `<p>Thanks</p><blockquote><a href="https://a.example/x">Sign in</a></blockquote>`, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := mail.SignInLink(tt.subject, tt.text, tt.html); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
