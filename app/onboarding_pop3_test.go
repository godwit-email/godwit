package app

import (
	"context"
	"strconv"
	"testing"

	"github.com/godwit-email/godwit/backend/pop3"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/stretchr/testify/assert"
)

func experimentalPOP3(e *coretest.Env) {
	ctx := context.Background()
	st, err := e.Svc.GetSettings(ctx)
	if err == nil {
		st.ExperimentalPOP3 = true
		_, err = e.Svc.SetSettings(ctx, st)
	}
	if err != nil {
		panic(err)
	}
}

// openServers opens the server form for a new address.
func (h *harness) openServers(email string) *onboarding {
	o := h.firstRun()
	h.click("其他邮箱")
	h.fill("邮箱地址", email)
	if _, open := h.tt.Find("IMAP 服务器"); !open {
		h.click("手动设置服务器")
	}
	return o
}

// With the POP3 experiment on, the server form offers POP3: a POP3 server
// in place of the IMAP one, 995 over SSL/TLS by default.
func TestOnboardingPOP3(t *testing.T) {
	plaintext(t)
	h := newHarness(t, experimentalPOP3)
	o := h.openServers("new@example.com")
	in := h.env.StartPOP3Servers(h.t, "new@example.com")
	h.see("IMAP 服务器") // IMAP stays the default
	h.click("POP3（实验性）")
	h.see("POP3 服务器")
	if o.imapPort != "995" || o.imapSec != core.SecurityTLS {
		t.Fatalf("pop3 defaults %s %s", o.imapPort, o.imapSec)
	}
	h.fill("POP3 服务器", in.Receiving.Host)
	h.fill("POP3 端口", strconv.Itoa(in.Receiving.Port))
	h.fill("SMTP 服务器", in.Sending.Host)
	h.fill("SMTP 端口", strconv.Itoa(in.Sending.Port))
	h.fill("用户名", "alice")
	h.fill("密码", "secret")
	h.click("连接")
	h.see("连接成功")
	if text, _, _ := o.checkRow(o.report.Steps[1]); text != "POP3 登录成功" {
		t.Fatalf("pop3 row %q", text)
	}
	h.click("开始使用")
	h.until("the mail page", func() bool { return h.app.route.Page == PageMail })
	accs, err := h.env.Svc.ListAccounts(context.Background())
	if err != nil || len(accs) != 1 || accs[0].Receiving.Protocol != "pop3" {
		t.Fatalf("accounts %+v, %v", accs, err)
	}
}

// POP3 over STARTTLS defaults to port 110.
func TestOnboardingPOP3PortFollowsSecurity(t *testing.T) {
	h := newHarness(t, experimentalPOP3)
	o := h.openServers("new@example.com")
	h.click("POP3（实验性）")
	h.click("SSL/TLS") // POP3's, above SMTP's
	h.click("STARTTLS")
	h.until("port 110", func() bool { return o.imapPort == "110" })
}

// With the experiment off, the form offers IMAP only.
func TestOnboardingNoPOP3ByDefault(t *testing.T) {
	h := newHarness(t)
	h.openServers("new@example.com")
	h.see("IMAP 服务器")
	if h.tt.HasText("POP3（实验性）") {
		t.Fatal("POP3 offered with the experiment off")
	}
}

// A server without UIDL fails the check with words, not the reason key.
func TestOnboardingPOP3NoUIDL(t *testing.T) {
	o := &onboarding{}
	text, _, status := o.checkRow(core.CheckStep{Name: "pop3", Kind: "protocol", Detail: pop3.ReasonNoUIDL})
	assert.Equal(t, 2, status)
	assert.Equal(t, "POP3 登录失败："+i18n.T("feature.reason.pop3.no_uidl"), text)
	assert.NotEqual(t, "feature.reason.pop3.no_uidl", i18n.T("feature.reason.pop3.no_uidl"))
}

// Every POP3 reason key has text in the catalog.
func TestPOP3FeatureReasonsHaveText(t *testing.T) {
	for _, r := range []string{pop3.ReasonLocalMove, pop3.ReasonLocalFlags, pop3.ReasonInboxOnly, pop3.ReasonNone, pop3.ReasonNoUIDL} {
		assert.NotEqual(t, "feature.reason."+r, i18n.T("feature.reason."+r), "no text for reason %s", r)
	}
}

// The labs page has the POP3 switch beside JMAP's.
func TestLabsPOP3Switch(t *testing.T) {
	h := newHarness(t)
	h.until("initial load", func() bool { return h.app.loaded })
	h.openSettings("labs")
	h.click("POP3 账户")
	h.until("the experiment on", func() bool {
		st, err := h.env.Svc.GetSettings(context.Background())
		return err == nil && st.ExperimentalPOP3
	})
}
