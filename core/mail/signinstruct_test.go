package mail_test

import (
	"testing"

	"github.com/godwit-email/godwit/core/mail"
)

func TestEmphasizedCode(t *testing.T) {
	for _, tt := range []struct{ name, subject, html, want string }{
		{"large cell", "Acme", `<p>Utilisez le code ci-dessous.</p><table><tr><td style="font-size:32px">482913</td></tr></table>`, "482913"},
		{"bold", "Acme", `<p>Il tuo codice:</p><p><strong>482913</strong></p>`, "482913"},
		{"letter-spaced", "Acme", `<p>Kodunuz</p><div style="letter-spacing: 6px">AB12CD</div>`, "AB12CD"},
		{"monospaced", "Acme", `<p>Mã của bạn</p><div style="font-family: 'Courier New', monospace">731 904</div>`, "731904"},
		{"letter boxes", "Acme", `<p>Код</p><table><tr><td><b>4</b></td><td><b>8</b></td><td><b>2</b></td><td><b>9</b></td></tr></table>`, "4829"},
		{"heading", "Acme", `<h1>482913</h1><p>Expires soon.</p>`, "482913"},
		{"pt size", "Acme", `<span style="FONT-SIZE: 18pt">482913</span>`, "482913"},
		{"alone in the body", "Acme", `<b>482913</b>`, "482913"},
		{"plain", "Acme", `<p>Your code</p><p>482913</p>`, ""},
		{"inside a sentence", "Acme", `<p>Use <b>482913</b> to continue.</p>`, "482913"},
		{"bold sentence", "Acme", `<p><b>Order 482913 shipped</b></p>`, ""},
		{"two codes", "Acme", `<b>482913</b><b>731904</b>`, ""},
		{"same code twice", "Acme", `<b>482913</b><p>again</p><b>482913</b>`, "482913"},
		{"word", "Acme", `<b>WELCOME</b>`, ""},
		{"too short", "Acme", `<b>123</b>`, ""},
		{"too long", "Acme", `<b>1234567890123</b>`, ""},
		{"in a link", "Acme", `<a href="https://x.example"><b>482913</b></a>`, ""},
		{"hidden preheader", "Acme", `<div style="display:none"><b>111111</b></div><b>482913</b>`, "482913"},
		{"transparent preheader", "Acme", `<div style="opacity:0"><b>111111</b></div><b>482913</b>`, "482913"},
		{"Outlook-hidden preheader", "Acme", `<div style="mso-hide:all"><b>111111</b></div><b>482913</b>`, "482913"},
		{"quoted", "Acme", `<p>Thanks</p><blockquote><b>482913</b></blockquote>`, ""},
		{"reply", "Re: Acme", `<b>482913</b>`, ""},
		{"no html", "Acme", ``, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := mail.EmphasizedCode(tt.subject, "", tt.html); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPrimaryButton(t *testing.T) {
	for _, tt := range []struct{ name, subject, html, want, kind string }{
		{"background cell", "Acme", `<p>Klik hieronder.</p><table><tr><td bgcolor="#1a73e8"><a href="https://acme.example/l/1">Doorgaan</a></td></tr></table><p><a href="https://acme.example/help">Help</a></p>`, "https://acme.example/l/1", mail.LinkSignIn},
		{"styled link", "Acme", `<a style="background-color:#000;color:#fff;padding:12px" href="https://acme.example/l/1">Continuar</a>`, "https://acme.example/l/1", mail.LinkSignIn},
		{"button class", "Acme", `<a class="btn btn-primary" href="https://acme.example/l/1">Weiter</a>`, "https://acme.example/l/1", mail.LinkSignIn},
		{"image button", "Acme", `<a class="button" href="https://acme.example/l/1"><img src="cid:b" alt=""></a>`, "https://acme.example/l/1", mail.LinkSignIn},
		{"confirm by context", "Email verification", `<a style="background:#06c" href="https://acme.example/c/1">OK</a>`, "https://acme.example/c/1", mail.LinkConfirm},
		{"reset by context", "Password reset", `<a style="background:#06c" href="https://acme.example/r/1">OK</a>`, "https://acme.example/r/1", mail.LinkReset},
		{"same link twice", "Acme", `<a class="button" href="https://acme.example/l/1">Go</a><a class="button" href="https://acme.example/l/1">Go</a>`, "https://acme.example/l/1", mail.LinkSignIn},
		{"two buttons", "Acme", `<a class="button" href="https://acme.example/l/1">Yes</a><a class="button" href="https://acme.example/l/2">No</a>`, "", ""},
		{"plain links", "Acme", `<a href="https://acme.example/l/1">Go</a>`, "", ""},
		{"white background", "Acme", `<a style="background-color:#ffffff" href="https://acme.example/l/1">Go</a>`, "", ""},
		{"container color", "Acme", `<table bgcolor="#eee"><tr><td><p>Hello</p><a href="https://acme.example/l/1">Go</a></td></tr></table>`, "", ""},
		{"http", "Acme", `<a class="button" href="http://acme.example/l/1">Go</a>`, "", ""},
		{"alert", "New sign-in to your account", `<a class="button" href="https://acme.example/a">Review</a>`, "", ""},
		{"quoted", "Acme", `<p>Thanks</p><blockquote><a class="button" href="https://acme.example/l/1">Go</a></blockquote>`, "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if l, k := mail.PrimaryButton(tt.subject, "", tt.html); l != tt.want || k != tt.kind {
				t.Fatalf("got %q %q, want %q %q", l, k, tt.want, tt.kind)
			}
		})
	}
}
