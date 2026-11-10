package app

import (
	"context"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
)

func experimentalEWS(e *coretest.Env) {
	ctx := context.Background()
	st, err := e.Svc.GetSettings(ctx)
	if err == nil {
		st.ExperimentalEWS = true
		_, err = e.Svc.SetSettings(ctx, st)
	}
	if err != nil {
		panic(err)
	}
}

// With the EWS experiment on, the server form offers Exchange (EWS): the
// EWS URL and the login name in place of the IMAP and SMTP servers, and the
// check names the EWS host.
func TestOnboardingEWS(t *testing.T) {
	h := newHarness(t, experimentalEWS)
	o := h.firstRun()
	h.click("其他 IMAP 邮箱")
	h.fill("邮箱地址", "new@example.com")
	_, srv := h.env.StartEWSServers(h.t, "new@example.com")
	if _, open := h.tt.Find("IMAP 服务器"); !open {
		h.click("手动设置服务器")
	}
	h.click("Exchange（EWS）")
	h.click("高级：手动填写 EWS URL")
	h.see("EWS URL")
	if h.tt.HasText("SMTP 服务器") {
		t.Fatal("an EWS account asked for an SMTP server")
	}
	h.fill("EWS URL", srv.URL())
	h.fill("登录名", "alice")
	h.fill("密码", "secret")

	srv.SetVersion(15, 0)
	h.click("连接")
	h.see("服务器版本过低")
	srv.SetVersion(15, 2)
	h.click("手动设置服务器")
	h.click("连接")

	h.see("连接成功")
	host := srv.URL()[len("http://"):]
	host = host[:len(host)-len("/EWS/Exchange.asmx")]
	if text, detail, _ := o.checkRow(o.report.Steps[1]); text != "EWS 登录成功" || detail != "EWS 主机："+host+" · 6 个文件夹" {
		t.Fatalf("ews row %q, %q", text, detail)
	}
	h.click("开始使用")
	h.until("the mail page", func() bool { return h.app.route.Page == PageMail })
	accs, err := h.env.Svc.ListAccounts(context.Background())
	if err != nil || len(accs) != 1 || accs[0].Receiving.Protocol != "ews" || accs[0].Sending.Protocol != "ews" || accs[0].Username != "alice" {
		t.Fatalf("accounts %+v, %v", accs, err)
	}
}

// Without an EWS URL the check finds it by Autodiscover, asking before the
// password goes to a host outside the address's domain; the check then
// names the EWS host found.
func TestOnboardingEWSAutodiscover(t *testing.T) {
	h := newHarness(t, experimentalEWS)
	o := h.firstRun()
	h.click("其他 IMAP 邮箱")
	h.fill("邮箱地址", "new@example.com")
	_, srv, ad := h.env.StartEWSAutodiscover(h.t, "new@example.com")
	ad.RedirectURL("autodiscover.example.com", "ad.example.net")
	ad.Serve("ad.example.net")
	if _, open := h.tt.Find("IMAP 服务器"); !open {
		h.click("手动设置服务器")
	}
	h.click("Exchange（EWS）")
	h.fill("登录名", "alice")
	h.fill("密码", "secret")
	h.click("连接")
	h.see("自动发现要把密码发送到 ad.example.net")
	if ad.Credentialed("ad.example.net") {
		t.Fatal("the password went to ad.example.net before the user confirmed it")
	}
	h.click("发送到 ad.example.net")

	h.see("连接成功")
	if o.report.URL != srv.URL() {
		t.Fatalf("found %q, want %q", o.report.URL, srv.URL())
	}
	h.click("开始使用")
	h.until("the mail page", func() bool { return h.app.route.Page == PageMail })
	accs, err := h.env.Svc.ListAccounts(context.Background())
	if err != nil || len(accs) != 1 || accs[0].Receiving.Path != "/EWS/Exchange.asmx" {
		t.Fatalf("accounts %+v, %v", accs, err)
	}
}

// When Autodiscover finds nothing, the form opens the EWS URL field.
func TestOnboardingEWSAutodiscoverFails(t *testing.T) {
	h := newHarness(t, experimentalEWS)
	h.firstRun()
	h.click("其他 IMAP 邮箱")
	h.fill("邮箱地址", "new@example.com")
	h.env.StartEWSAutodiscover(h.t, "new@example.com")
	if _, open := h.tt.Find("IMAP 服务器"); !open {
		h.click("手动设置服务器")
	}
	h.click("Exchange（EWS）")
	if h.tt.HasText("通常以 /EWS/Exchange.asmx 结尾") { // the field's help
		t.Fatal("the EWS URL field shows before Autodiscover failed")
	}
	h.fill("登录名", "alice")
	h.fill("密码", "secret")
	h.click("连接")
	h.see("自动发现没有找到 EWS 地址")
	h.click("手动设置服务器")
	h.see("通常以 /EWS/Exchange.asmx 结尾")
}

// With the experiment off, the form offers no Exchange (EWS).
func TestOnboardingNoEWSByDefault(t *testing.T) {
	h := newHarness(t)
	h.firstRun()
	h.click("其他 IMAP 邮箱")
	if _, open := h.tt.Find("IMAP 服务器"); !open {
		h.click("手动设置服务器")
	}
	h.see("IMAP 服务器")
	if h.tt.HasText("Exchange（EWS）") {
		t.Fatal("EWS offered with the experiment off")
	}
}

// An EWS account's servers dialog edits its EWS URL, with no IMAP or SMTP
// server, and saves it.
func TestSettingsEWSServers(t *testing.T) {
	var acc core.Account
	h := newHarness(t, func(e *coretest.Env) {
		experimentalEWS(e)
		in, _ := e.StartEWSServers(t, "alice@example.com")
		var err error
		if acc, err = e.Svc.AddAccount(context.Background(), in); err != nil {
			t.Fatal(err)
		}
	})
	h.until("initial load", func() bool { return h.app.loaded })
	p := h.openSettings(accountRoute(acc.ID))
	h.click("编辑")
	h.see("EWS URL")
	if h.tt.HasText("IMAP 服务器") || h.tt.HasText("SMTP 服务器") {
		t.Fatal("an EWS account's servers dialog asks for IMAP or SMTP")
	}
	h.click("保存")
	h.until("the dialog closes", func() bool { return p.dialog == "" })
	if got := h.account(acc.ID); got.Receiving.Path != "/EWS/Exchange.asmx" || got.Receiving.Host != acc.Receiving.Host {
		t.Fatalf("stored %+v", got.Receiving)
	}
}

// A host the user let Autodiscover send the password to for one address is
// asked about again once the address changes, even when the new address's
// Autodiscover sends the client to the same host.
func TestOnboardingEWSConfirmedHostPerAddress(t *testing.T) {
	h := newHarness(t, experimentalEWS)
	h.firstRun()
	h.click("其他 IMAP 邮箱")
	h.fill("邮箱地址", "a@example.com")
	_, srv, ad := h.env.StartEWSAutodiscover(h.t, "a@example.com")
	ad.RedirectURL("autodiscover.example.com", "ad.example.net")
	ad.Serve("ad.example.net")
	if _, open := h.tt.Find("IMAP 服务器"); !open {
		h.click("手动设置服务器")
	}
	h.click("Exchange（EWS）")
	h.fill("登录名", "alice")
	h.fill("密码", "secret")
	srv.SetVersion(15, 0) // the check fails after Autodiscover, back to the form
	h.click("连接")
	h.click("发送到 ad.example.net")
	h.see("服务器版本过低")
	h.click("手动设置服务器")

	_, _, ad = h.env.StartEWSAutodiscover(h.t, "b@example.org")
	ad.RedirectURL("autodiscover.example.org", "ad.example.net")
	ad.Serve("ad.example.net")
	h.fill("邮箱地址", "b@example.org")
	h.click("连接")
	h.see("自动发现要把密码发送到 ad.example.net")
	if ad.Credentialed("ad.example.net") {
		t.Fatal("the password for b@example.org went to ad.example.net, confirmed for a@example.com only")
	}
}
