package mail_test

import (
	"testing"

	"github.com/godwit-email/godwit/core/mail"
)

func TestSignInCode(t *testing.T) {
	for _, tt := range []struct{ name, subject, body, want string }{
		{"leading zero", "Your verification code: 001234", "Expires in 10 minutes.", "001234"},
		{"letters and case", "Sign-in code", "Your code is aB12Cd34", "aB12Cd34"},
		{"letters only", "Verification code: XQWERT", "", "XQWERT"},
		{"Chinese", "登录验证码", "验证码为：012345，5 分钟内有效。", "012345"},
		{"code first", "482913 is your verification code", "", "482913"},
		{"heading", "Your one-time code", "\nAb12Cd\n\nDo not share it.", "Ab12Cd"},
		{"grouped", "Your security code", "123 456", "123456"},
		{"repeated", "验证码：482913", "验证码：482913", "482913"},
		{"ambiguous", "Verification code: 123456", "Verification code: 654321", ""},
		{"ordinary number", "Order 123456", "Total 1000, phone 1234567890", ""},
		{"promotion", "Discount code: SALE2026", "", ""},
		{"link", "Your verification code", "https://example.com/123456", ""},
		{"email address", "Verification code: 123456@example.com", "", ""},
		{"long number", "Verification code: 1234567890123456", "", ""},
		{"long code first", "1234567890123456 is your verification code", "", ""},
		{"lowercase otp", "Your OTP is x7Yz09", "", "x7Yz09"},
		{"prose", "Your verification code expires in 1000 seconds", "", ""},
		{"quoted", "Re: login", "> Your verification code: 123456", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := mail.SignInCode(tt.subject, tt.body, ""); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSignInCodeExcludesHTMLQuotes(t *testing.T) {
	if got := mail.SignInCode("Re: Your verification code", "Your verification code: 123456", `<p>Thanks, the login worked.</p><blockquote>Your verification code: 123456</blockquote>`); got != "" {
		t.Fatalf("quoted code %q", got)
	}
	if got := mail.SignInCode("Your verification code", "Your verification code: 654321\nYour verification code: 123456", `<p>Your verification code: <b>654321</b></p><div class="gmail_quote">Your verification code: 123456</div>`); got != "654321" {
		t.Fatalf("new code outside quote %q", got)
	}
}
