package app

import (
	"context"
	"net"
	"strconv"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
)

func experimentalJMAP(e *coretest.Env) {
	ctx := context.Background()
	st, err := e.Svc.GetSettings(ctx)
	if err == nil {
		st.ExperimentalJMAP = true
		_, err = e.Svc.SetSettings(ctx, st)
	}
	if err != nil {
		panic(err)
	}
}

// With the JMAP experiment on, the server form offers JMAP: a session URL
// in place of IMAP and SMTP, and the check names the session's host.
func TestOnboardingJMAP(t *testing.T) {
	h := newHarness(t, experimentalJMAP)
	o := h.firstRun()
	h.click("其他 IMAP 邮箱")
	h.fill("邮箱地址", "new@example.com")
	in := h.env.StartJMAPServers(h.t, "new@example.com")
	if _, open := h.tt.Find("IMAP 服务器"); !open {
		h.click("手动设置服务器")
	}
	h.click("JMAP（实验性）")
	h.see("会话 URL")
	if h.tt.HasText("SMTP 服务器") {
		t.Fatal("SMTP server fields shown for JMAP")
	}
	jmapHost := net.JoinHostPort(in.Receiving.Host, strconv.Itoa(in.Receiving.Port))
	h.fill("会话 URL", "http://"+jmapHost+"/.well-known/jmap")
	h.fill("用户名", "alice")
	h.fill("密码", "secret")
	h.click("连接")
	h.see("连接成功")
	if len(o.report.Steps) != 2 {
		t.Fatalf("steps %+v", o.report.Steps)
	}
	if text, detail, _ := o.checkRow(o.report.Steps[1]); text != "JMAP 登录成功" || detail != "会话主机："+jmapHost+" · 1 个文件夹" {
		t.Fatalf("jmap row %q, %q", text, detail)
	}
	h.click("开始使用")
	h.until("the mail page", func() bool { return h.app.route.Page == PageMail })
	accs, err := h.env.Svc.ListAccounts(context.Background())
	if err != nil || len(accs) != 1 || accs[0].Receiving.Protocol != "jmap" || accs[0].Sending.Protocol != "jmap" || accs[0].Receiving.Path != "" {
		t.Fatalf("accounts %+v, %v", accs, err)
	}
}

// JMAP can sign in with an API token, used for both receiving and sending
// without any SMTP server or separate SMTP password.
func TestOnboardingJMAPAPIToken(t *testing.T) {
	h := newHarness(t, experimentalJMAP)
	h.firstRun()
	h.see("Fastmail") // listed, with its JMAP entry's API token preset
	h.click("其他 IMAP 邮箱")
	h.fill("邮箱地址", "new@example.com")
	in := h.env.StartJMAPServers(h.t, "new@example.com")
	if _, open := h.tt.Find("IMAP 服务器"); !open {
		h.click("手动设置服务器")
	}
	h.click("JMAP（实验性）")
	jmapHost := net.JoinHostPort(in.Receiving.Host, strconv.Itoa(in.Receiving.Port))
	h.fill("会话 URL", "http://"+jmapHost+"/.well-known/jmap")
	h.click("API 令牌")
	if h.tt.HasText("SMTP 密码") || h.tt.HasText("SMTP 服务器") {
		t.Fatal("SMTP fields shown for JMAP API token")
	}
	h.fill("用户名", "alice")
	h.fill("API 令牌", "secret")
	h.click("连接")
	h.see("连接成功")
	h.click("开始使用")
	h.until("the mail page", func() bool { return h.app.route.Page == PageMail })
	accs, err := h.env.Svc.ListAccounts(context.Background())
	if err != nil || len(accs) != 1 || accs[0].Receiving.Auth != core.AuthAPIToken || accs[0].Sending.Protocol != "jmap" || accs[0].Sending.Auth != core.AuthAPIToken {
		t.Fatalf("accounts %+v, %v", accs, err)
	}
}

// A JMAP server without urn:ietf:params:jmap:submission fails the sign-in
// check with its localized reason.
func TestOnboardingJMAPNoSubmission(t *testing.T) {
	h := newHarness(t, experimentalJMAP)
	h.firstRun()
	// Add one account first to grab the underlying JMAP server handle, then
	// disable submission and try onboarding another address on it.
	in := h.env.StartJMAPServers(h.t, "seed@example.com")
	seed, err := h.env.Svc.AddAccount(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	h.env.JMAP(seed.ID).DisableSubmission()

	h.click("其他 IMAP 邮箱")
	h.fill("邮箱地址", "nosend@example.com")
	if _, open := h.tt.Find("IMAP 服务器"); !open {
		h.click("手动设置服务器")
	}
	h.click("JMAP（实验性）")
	jmapHost := net.JoinHostPort(in.Receiving.Host, strconv.Itoa(in.Receiving.Port))
	h.fill("会话 URL", "http://"+jmapHost+"/.well-known/jmap")
	h.fill("用户名", "alice")
	h.fill("密码", "secret")
	h.click("连接")
	h.see("服务器（或此 API 令牌）未开启 JMAP 发信权限。")
}

// An account whose JMAP signs in with an API token updates its token in
// settings and shows no SMTP server or separate SMTP password.
func TestSettingsJMAPAPITokenAccount(t *testing.T) {
	var acc core.Account
	h := newHarness(t, func(e *coretest.Env) {
		experimentalJMAP(e)
		in := e.StartJMAPServers(t, "tok@example.com")
		in.Receiving.Auth = core.AuthAPIToken
		var err error
		if acc, err = e.Svc.AddAccount(context.Background(), in); err != nil {
			t.Fatal(err)
		}
	})
	h.until("initial load", func() bool { return h.app.loaded })
	p := h.openSettings(accountRoute(acc.ID))
	h.see("更新API 令牌")
	if h.tt.HasText("SMTP") {
		t.Fatal("SMTP shown for JMAP account in settings")
	}
	h.env.JMAP(acc.ID).SetPassword("token-new")
	h.click("更新API 令牌")
	h.fill("API 令牌", "token-new")
	h.click("保存")
	h.see("已更新API 令牌")

	h.click("编辑")
	h.see("会话 URL")
	if h.tt.HasText("IMAP 服务器") || h.tt.HasText("SMTP 服务器") {
		t.Fatal("IMAP or SMTP server fields shown in JMAP edit-servers dialog")
	}
	h.click("保存")
	h.until("the dialog closes", func() bool { return p.dialog == "" })
}

// With the experiment off, the form offers IMAP only.
func TestOnboardingNoJMAPByDefault(t *testing.T) {
	h := newHarness(t)
	h.firstRun()
	h.click("其他 IMAP 邮箱")
	if _, open := h.tt.Find("IMAP 服务器"); !open {
		h.click("手动设置服务器")
	}
	h.see("IMAP 服务器")
	if h.tt.HasText("JMAP（实验性）") {
		t.Fatal("JMAP offered with the experiment off")
	}
}
