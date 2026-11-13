package app

import (
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
)

// The tray's recent codes and the code banner are the backend's (ADR 0038,
// cmd/godwit TestHeadlessTraySignInCodesAndBanner); the reader tags a code
// mail with its code, and a click copies it.
func TestSignInCodeTagCopies(t *testing.T) {
	h, acc := notifyHarness(t, nil)
	h.arrive(acc, "Your verification code: aB0123")
	h.app.OpenMessage(h.newMail(acc.ID).msgs[0])
	h.until("the plugin's tag after the subject", func() bool { return h.tt.HasText("aB0123") && !h.tt.HasText("验证码 · 内置插件") })
	h.click("aB0123")
	if got := h.tt.Clipboard(); got != "aB0123" {
		t.Fatalf("clipboard %q", got)
	}
}

// With the sign-in codes plugin off a code mail is ordinary mail: it
// notifies, and the reader shows no code tag until the plugin is on again.
func TestSignInCodesPluginOffStopsEverySurface(t *testing.T) {
	h, acc := notifyHarness(t, nil)
	if err := h.env.Svc.SetPluginEnabled(t.Context(), core.SignInCodesPlugin, false); err != nil {
		t.Fatal(err)
	}
	h.arrive(acc, "验证码：001234")
	notes := h.notesAfter(1)
	if len(notes) != 1 {
		t.Fatalf("with the plugin off the code mail is ordinary mail: %+v", notes)
	}
	h.app.OpenMessage(notes[0].MessageID)
	h.until("the message", func() bool { return h.tt.HasText("验证码：001234") })
	tags := func() []core.Tag {
		if st := h.app.mail.reader.ai[notes[0].MessageID]; st != nil {
			return st.tags.v
		}
		return nil
	}
	h.until("the tags read", func() bool { st := h.app.mail.reader.ai[notes[0].MessageID]; return st != nil && st.tags.done })
	if len(tags()) != 0 {
		t.Fatalf("tags with the plugin off: %+v", tags())
	}
	if err := h.env.Svc.SetPluginEnabled(t.Context(), core.SignInCodesPlugin, true); err != nil {
		t.Fatal(err)
	}
	h.waitMail("the tag back", func() bool { return len(tags()) == 1 })
}

func TestSignInLinkTagAsksFirst(t *testing.T) {
	h, acc := withAccount(t)
	h.env.Deliver(t, acc.ID, "INBOX", coretest.Msg("Acme <id@acme.example>", acc.Email, "Sign in to Acme", "Log in here: https://acme.example/l/1\nThis link expires in 10 minutes."))
	h.waitMail("the mail", func() bool { return h.tt.HasText("Sign in to Acme") })
	h.click("Sign in to Acme")
	h.until("the link tag", func() bool { return h.tt.HasText("登录链接") })
	h.click("登录链接")
	if !h.app.linkOpen || h.app.link != "https://acme.example/l/1" {
		t.Fatalf("link dialog %v %q, want it asking for the link", h.app.linkOpen, h.app.link)
	}
}
