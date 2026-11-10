package app

import (
	"context"
	"testing"

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
