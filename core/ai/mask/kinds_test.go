package mask_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/godwit-email/godwit/core/ai/mask"
)

// A sign-in code and a link that signs in or resets a password go as
// placeholders, as mail.SignInCode, EmphasizedCode and ActionLink find them
// in the text; the user sees them as they were.
func TestFindsCodesAndOneTimeLinks(t *testing.T) {
	for in, want := range map[string]string{
		"Subject: Your verification code\n\nYour verification code is 482913. It expires in 10 minutes.": "Subject: Your verification code\n\nYour verification code is <CODE>. It expires in 10 minutes.",
		"Subject: 登录验证码\n\n您的验证码：731 559，请勿告诉他人。":                                                        "Subject: 登录验证码\n\n您的验证码：<CODE>，请勿告诉他人。",
		"Subject: Password reset\n\nButton does not work, use this link:\nhttps://a.example/reset/2":     "Subject: Password reset\n\nButton does not work, use this link:\n<LINK>",
		`<p>Sign in to Acme</p><p style="font-size:32px;font-weight:bold">583920</p>`:                    `<p>Sign in to Acme</p><p style="font-size:32px;font-weight:bold"><CODE></p>`,
		// one mail's code stays its own in a body that quotes several
		"<untrusted_email source=\"message:1\">\nSubject: Login code\nYour login code is 118169\n</untrusted_email>\n" +
			"<untrusted_email source=\"message:2\">\nSubject: Login code\nYour login code is 220733\n</untrusted_email>": "<untrusted_email source=\"message:1\">\nSubject: Login code\nYour login code is <CODE>\n</untrusted_email>\n" +
			"<untrusted_email source=\"message:2\">\nSubject: Login code\nYour login code is <CODE>\n</untrusted_email>",
		// and what is no code
		"Use code SAVE20 at checkout":      "Use code SAVE20 at checkout",
		"See https://a.example/docs/intro": "See https://a.example/docs/intro",
	} {
		m := mask.New(key)
		out, err := m.Text(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, shown(out), in)
		assert.Equal(t, in, m.Restore(out), in)
	}
}

// A mainland China mobile or landline number, or one written with + and its
// country code; a number with neither is no phone's, nor are the order
// numbers, dates, amounts and versions mail is full of.
func TestFindsPhoneNumbers(t *testing.T) {
	for in, want := range map[string]string{
		"手机 13812345678，谢谢":                    "手机 <PHONE>，谢谢",
		"Call 138-1234-5678 or 138 1234 5678.": "Call <PHONE> or <PHONE>.",
		"电话：010-12345678":                      "电话：<PHONE>",
		"座机 (0571) 8765 4321":                  "座机 <PHONE>",
		"Tel +86 138 1234 5678":                "Tel <PHONE>",
		"+8613812345678":                       "<PHONE>",
		"Call +1 (415) 555-2671 today":         "Call <PHONE> today",
		"+44 20 7946 0958":                     "<PHONE>",
		// and what is not one
		"Call (415) 555-2671":                "Call (415) 555-2671",
		"Order 20261027143000123 shipped":    "Order 20261027143000123 shipped",
		"Order #1381234567890":               "Order #1381234567890",
		"订单号 SO-021-12345678":                "订单号 SO-021-12345678",
		"Date 2026-10-27 at 2026/10/27":      "Date 2026-10-27 at 2026/10/27",
		"Total ¥13,812.50 or $1,380,000.00":  "Total ¥13,812.50 or $1,380,000.00",
		"Amount 13812345678.00":              "Amount 13812345678.00",
		"Version 10.15.7, build 1.2.3456789": "Version 10.15.7, build 1.2.3456789",
		"Up +12.5% to 1234567":               "Up +12.5% to 1234567",
	} {
		m := mask.New(key)
		out, err := m.Text(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, shown(out), in)
		assert.Equal(t, in, m.Restore(out), in)
	}
}

// An address goes with its user name as a placeholder and its domain as it
// was, for labels and sorting go by the domain; restored, it is the address
// it was, to the letter.
func TestMasksTheUserNameOfAnAddress(t *testing.T) {
	m := mask.New(key)
	in := "From: Alice <Alice.Smith+news@Example.co.uk>, cc bob_99@mail.example.com."
	out, err := m.Text(in)
	require.NoError(t, err)
	assert.Equal(t, "From: Alice <<EMAIL>@Example.co.uk>, cc <EMAIL>@mail.example.com.", shown(out))
	assert.Equal(t, in, m.Restore(out))

	out, err = m.Text("postgres://app:hunter2pass@db.example.com/x and @mention, a@b")
	require.NoError(t, err)
	assert.Equal(t, "postgres://app:<PASSWORD>@db.example.com/x and @mention, a@b", shown(out))
}

// Each category goes as it is once turned off; what was masked before still
// restores.
func TestACategoryTurnedOffIsSentAsItIs(t *testing.T) {
	in := "Code 482913 is your verification code. Call +86 138 1234 5678 or mail ann@example.com, card 4111 1111 1111 1111"
	m := mask.New(key)
	before, err := m.Text(in)
	require.NoError(t, err)
	assert.Equal(t, "Code <CODE> is your verification code. Call <PHONE> or mail <EMAIL>@example.com, card <BANK_CARD>", shown(before))
	for cat, want := range map[string]string{
		mask.CatCode:   "Code 482913 is your verification code. Call <PHONE> or mail <EMAIL>@example.com, card <BANK_CARD>",
		mask.CatPhone:  "Code <CODE> is your verification code. Call +86 138 1234 5678 or mail <EMAIL>@example.com, card <BANK_CARD>",
		mask.CatEmail:  "Code <CODE> is your verification code. Call <PHONE> or mail ann@example.com, card <BANK_CARD>",
		mask.CatSecret: "Code <CODE> is your verification code. Call <PHONE> or mail <EMAIL>@example.com, card 4111 1111 1111 1111",
	} {
		m.SetCategories(map[string]bool{cat: false})
		out, err := m.Text(in)
		require.NoError(t, err)
		assert.Equal(t, want, shown(out), cat)
		assert.Equal(t, in, m.Restore(before), cat)
	}
}
