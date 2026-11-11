package app

import (
	"context"
	"testing"

	"github.com/godwit-email/godwit/backend/gmailapi"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/stretchr/testify/assert"
)

func experimentalGmailAPI(e *coretest.Env) {
	ctx := context.Background()
	st, err := e.Svc.GetSettings(ctx)
	if err == nil {
		st.ExperimentalGmailAPI = true
		_, err = e.Svc.SetSettings(ctx, st)
	}
	if err != nil {
		panic(err)
	}
}

func TestLabsGmailAPISwitch(t *testing.T) {
	h := newHarness(t)
	h.until("initial load", func() bool { return h.app.loaded })
	h.openSettings("labs")
	h.click("Gmail API 账户")
	h.until("the experiment on", func() bool {
		st, err := h.env.Svc.GetSettings(context.Background())
		return err == nil && st.ExperimentalGmailAPI
	})
}

func TestOnboardingNoGmailAPIByDefault(t *testing.T) {
	h := newHarness(t)
	o := h.firstRun()
	_ = o
	h.click("Gmail")
	if h.tt.HasText("Gmail API（实验性）") {
		t.Fatal("Gmail API offered on the app-password tab")
	}
	h.click("浏览器登录")
	if h.tt.HasText("Gmail API（实验性）") {
		t.Fatal("Gmail API offered with the experiment off")
	}
}

// Selecting Gmail API on Gmail's browser tab requires the user's own OAuth
// client (hiding Thunderbird's public client) and shows the Gmail API enablement step.
func TestOnboardingGmailAPIOwnClientOnly(t *testing.T) {
	h := newHarness(t, experimentalGmailAPI)
	o := h.firstRun()
	h.click("Gmail")
	if h.tt.HasText("Gmail API（实验性）") {
		t.Fatal("Gmail API offered on the app-password tab")
	}
	h.click("浏览器登录")
	h.see("Thunderbird 的公开客户端")
	h.click("Thunderbird 的公开客户端")
	h.see("使用前请知悉")
	if !o.client.public {
		t.Fatal("expected public client selected under IMAP")
	}

	h.click("Gmail API（实验性）")
	h.tt.Frame()
	if o.proto != auth.GmailAPI || !o.gmailAPI() {
		t.Fatalf("proto = %q, gmailAPI = %v", o.proto, o.gmailAPI())
	}
	if o.client.public {
		t.Fatal("public client stayed selected under Gmail API")
	}
	if h.tt.HasText("使用前请知悉") {
		t.Fatal("public client notice still shown under Gmail API")
	}
	h.see(i18n.T("onboarding.gmailapi.own_client_only"))
	h.see("搜索启用 Gmail API")
}

func TestOnboardingGmailAPIDisabledReason(t *testing.T) {
	o := &onboarding{}
	text, _, status := o.checkRow(core.CheckStep{Name: "gmailapi", Kind: "protocol", Detail: gmailapi.ReasonAPIDisabled})
	assert.Equal(t, 2, status)
	assert.Equal(t, "Gmail API 登录失败："+i18n.T("feature.reason.gmailapi.api_disabled"), text)
	assert.NotEqual(t, "feature.reason.gmailapi.api_disabled", i18n.T("feature.reason.gmailapi.api_disabled"))
}

func TestGmailAPIFeatureReasonsHaveText(t *testing.T) {
	for _, r := range []string{
		gmailapi.ReasonAPIDisabled,
		gmailapi.ReasonNoCopy,
		gmailapi.ReasonNoAnswered,
		gmailapi.ReasonNoDraft,
		gmailapi.ReasonNoKeywords,
		gmailapi.ReasonNoPush,
		gmailapi.ReasonFoldersPending,
	} {
		assert.NotEqual(t, "feature.reason."+r, i18n.T("feature.reason."+r), "no text for reason %s", r)
	}
}
