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
		{"prose word next line", "Your verification code", "\nIMPORTANT: do not share it", ""},
		{"prose word same line", "Your verification code SECURITY notice", "", ""},
		{"original message", "RE: login", "Thanks!\n-----Original Message-----\nYour verification code: 123456", ""},
		{"outlook header", "RE: login", "Thanks!\n\nFrom: Login <login@example.com>\nSent: Friday\nSubject: code\n\nYour verification code: 123456", ""},
		{"wrote", "Re: login", "Thanks!\n\nOn Fri, Oct 17, 2026 at 10:00 Login wrote:\nYour verification code: 123456", ""},
		{"sender line kept", "Your verification code", "From: Acme Security\nYour verification code: 123456", "123456"},
		{"coupon", "Thanks for your order", "Your code is SAVE20 for 20% off", ""},
		{"coupon with sign-in footer", "Thanks for your order", "Your code is SAVE20 for 20% off\n\nLots of deals inside.\n\nSign in to manage your account", ""},
		{"booking", "Your booking", "Booking confirmation code: AB12CD", ""},
		{"card", "Payment details", "Your card security code: 1234", ""},
		{"security code OTP", "Your security code", "Your security code is 482913", "482913"},
		{"coupon digits short", "Thanks for your order", "Your code is 2026", ""},
		{"confirm email", "Confirm your email", "Your confirmation code: 731904", "731904"},
		{"verified payment", "Your payment was verified", "Your code is SAVE20 for 20% off", ""},
		{"your code at sign-in", "Sign in to Acme", "Your code is 482913", "482913"},
		{"Microsoft one-time code", "你的一次性代码", "我们已收到你要求获得 Microsoft 帐户所用的一次性代码的申请。\n\n你的一次性代码为: 188223", "188223"},
		{"Microsoft single-use code", "Your single-use code", "Your single-use code is: 188223", "188223"},
		{"passcode", "Sign in", "Your passcode is 731904", "731904"},
		{"Traditional (M-Team)", "[M-Team - TP] 驗證碼 856035", "驗證碼: 856035, 20分鐘內有效\n時間: 2026-09-13 15:55:51", "856035"},
		{"activation code (V.PS)", "Your account needs activation", "If asked, your activation code is: r23wb5.", "r23wb5"},
		{"reverse coupon", "Thanks for your order", "SAVE20 is your code for 20% off", ""},
		{"reverse booking", "Your booking", "AB12CD is your confirmation code", ""},
		{"reply subject", "Re: Your verification code: 123456", "Thanks, it worked.", ""},
		{"coupon alone on its line", "Weekend sale", "Use this coupon code at checkout:\n\n123456", ""},
		{"licence key", "Your licence", "Your activation code: XQ12RT", ""},
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
	outlook := `<div>Thanks, it worked.</div><div id="appendonsend"></div><hr><div id="divRplyFwdMsg"><b>From:</b> Login</div><div>Your verification code: 123456</div>`
	if got := mail.SignInCode("RE: Your verification code", "", outlook); got != "" {
		t.Fatalf("Outlook-quoted code %q", got)
	}
	if got := mail.SignInCode("Re: Your verification code", "Your verification code: 123456", `<p>Thanks, the login worked.</p><blockquote>Your verification code: 123456</blockquote>`); got != "" {
		t.Fatalf("quoted code %q", got)
	}
	if got := mail.SignInCode("Your verification code", "Your verification code: 654321\nYour verification code: 123456", `<p>Your verification code: <b>654321</b></p><div class="gmail_quote">Your verification code: 123456</div>`); got != "654321" {
		t.Fatalf("new code outside quote %q", got)
	}
}

func TestGroupCode(t *testing.T) {
	for in, want := range map[string]string{"482913": "482 913", "001234": "001 234", "aB0123": "aB0123", "12345678": "12345678"} {
		if got := mail.GroupCode(in); got != want {
			t.Errorf("GroupCode(%q) = %q, want %q", in, got, want)
		}
	}
}

const idme = `<p>感谢您创建 ID.me Wallet!</p><p>您可以通过单击下面的链接来确认您的电子邮件地址。</p>
<a href="https://account.id.me/confirm/abc">确认您的电子邮件</a><p>请注意：此链接将在 15 分钟后过期。</p>
<p>无法点击此电子邮件中的按钮?</p><p>复制此代码并在浏览器中输入以完成确认。</p><div>118169</div>`

func TestSignInCodeAloneOnItsLine(t *testing.T) {
	if got := mail.SignInCode("欢迎来到 ID.me!", "", idme); got != "118169" {
		t.Fatalf("ID.me code %q", got)
	}
	for _, tt := range []struct{ name, body string }{
		{"no code words above", "Order shipped\n\n482913"},
		{"too short", "Your card code\n\n1234"},
		{"phone-like", "Call this code line\n\n13800138000"},
	} {
		if got := mail.SignInCode("Hello", tt.body, ""); got != "" {
			t.Errorf("%s: %q", tt.name, got)
		}
	}
}
