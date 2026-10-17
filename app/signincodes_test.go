package app

import (
	"testing"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
)

func TestSignInCodesArrivalAndCopy(t *testing.T) {
	h, acc := notifyHarness(t, nil)
	h.clock.mu.Lock()
	h.clock.now = time.Now().Truncate(time.Second)
	h.clock.mu.Unlock()
	h.app.refreshSignInCodes()
	h.until("initial code scan", func() bool { return h.app.codesReady })
	h.env.Deliver(t, acc.ID, "INBOX", coretest.Msg("Login <login@example.com>", acc.Email, "Your verification code: aB0123", "Your verification code: aB0123"))
	h.until("the mail arrival", func() bool { return h.app.newMail[acc.ID] != nil })
	h.advance(notifyWait)
	h.until("the recent code", func() bool { return len(h.plat.codes) == 1 && len(h.plat.codeBanners) == 1 })
	if got := h.plat.codes[0].Code; got != "aB0123" {
		t.Fatalf("recent code %q", got)
	}
	if notes := h.notesAfter(0); len(notes) != 0 {
		t.Fatalf("duplicate ordinary notification alongside the code card: %+v", notes)
	}
	h.app.refreshSignInCodes()
	h.until("code refresh", func() bool { return h.app.codesReady && !h.app.codesLoading })
	if len(h.plat.codeBanners) != 1 {
		t.Fatal("refresh displayed the same banner again")
	}
	h.app.OpenMessage(h.plat.codes[0].MessageID)
	h.until("the reader copy button", func() bool { return h.tt.HasText("复制验证码 aB0123") })
	h.click("复制验证码 aB0123")
	if got := h.tt.Clipboard(); got != "aB0123" {
		t.Fatalf("clipboard %q", got)
	}
	h.advance(15 * time.Minute)
	h.until("expired code removed", func() bool { return len(h.plat.codes) == 0 })
}

func TestSignInCodeBannerRespectsPrivacy(t *testing.T) {
	for _, mode := range []string{"foreground", "notifications off", "preview off"} {
		t.Run(mode, func(t *testing.T) {
			h, acc := notifyHarness(t, func(st *core.Settings) {
				if mode == "notifications off" {
					st.NotifyNewMail = false
				}
				if mode == "preview off" {
					st.NotifyPreview = false
				}
			})
			h.clock.mu.Lock()
			h.clock.now = time.Now().Truncate(time.Second)
			h.clock.mu.Unlock()
			h.app.refreshSignInCodes()
			h.until("initial scan", func() bool { return h.app.codesReady && !h.app.codesLoading })
			h.app.SetForeground(mode == "foreground")
			h.arrive(acc, "验证码：001234")
			h.advance(notifyWait)
			h.until("recent code", func() bool { return len(h.plat.codes) == 1 })
			if len(h.plat.codeBanners) != 0 {
				t.Fatal("banner exposed the code despite the notification setting")
			}
		})
	}
}

func TestSignInCodeWithoutTrayKeepsNotification(t *testing.T) {
	h, acc := notifyHarness(t, nil)
	h.plat.noCodeBanner = true
	h.clock.mu.Lock()
	h.clock.now = time.Now().Truncate(time.Second)
	h.clock.mu.Unlock()
	h.app.refreshSignInCodes()
	h.until("initial scan", func() bool { return h.app.codesReady && !h.app.codesLoading })
	h.arrive(acc, "验证码：001234")
	if notes := h.notesAfter(1); len(notes) != 1 {
		t.Fatalf("notifications %+v", notes)
	}
	if len(h.plat.codeBanners) != 0 {
		t.Fatal("banner without a tray")
	}
}

func TestSignInCodeArrivingBeforeFirstScan(t *testing.T) {
	h, acc := notifyHarness(t, nil)
	h.until("idle initial scan", func() bool { return h.app.codesReady && !h.app.codesLoading })
	h.clock.mu.Lock()
	h.clock.now = time.Now().Truncate(time.Second)
	h.clock.mu.Unlock()
	h.app.codesReady = false // the initial scan has not completed yet
	h.env.Deliver(t, acc.ID, "INBOX", coretest.Msg("login@example.com", acc.Email, "Verification code: 001234", ""))
	h.app.refreshSignInCodes()
	h.until("startup arrival banner", func() bool { return len(h.plat.codeBanners) == 1 })
}
