package app

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/theme"
)

// plaintext lets the onboarding reach the loopback fake servers, which speak
// no TLS; the form itself offers only TLS and STARTTLS.
func plaintext(t *testing.T) {
	testSecurity = core.SecurityNone
	t.Cleanup(func() { testSecurity = "" })
}

// fill replaces the text of the field labelled label with value.
func (h *harness) fill(label, value string) {
	h.t.Helper()
	h.click(label)
	h.tt.Command("selectAll")
	h.typeText(value)
}

func (h *harness) firstRun() *onboarding {
	h.t.Helper()
	h.until("first-run onboarding", func() bool { return h.app.route.Page == PageOnboarding })
	return h.app.pages[PageOnboarding].(*onboarding)
}

// manualServers opens the manual server settings and fills in the fake servers.
func (h *harness) manualServers() {
	h.t.Helper()
	ih, ip, sh, sp, user, _ := h.env.StartServers(h.t)
	if _, open := h.tt.Find("IMAP 服务器"); !open {
		h.click("手动设置服务器")
	}
	h.fill("IMAP 服务器", ih)
	h.fill("IMAP 端口", strconv.Itoa(ip))
	h.fill("SMTP 服务器", sh)
	h.fill("SMTP 端口", strconv.Itoa(sp))
	h.fill("用户名", user)
}

func TestOnboardingPasswordFlow(t *testing.T) {
	plaintext(t)
	h := newHarness(t)
	h.firstRun()
	h.see("先把邮箱接进来")
	h.see("密码只存在系统钥匙串")
	h.click("其他 IMAP 邮箱")
	h.fill("邮箱地址", "new@example.com")
	h.manualServers()
	h.fill("密码", "secret")
	h.click("连接")
	h.see("连接成功")
	if o := h.app.pages[PageOnboarding].(*onboarding); !o.report.OK() {
		t.Fatalf("report %+v", o.report)
	}
	h.fill("发件人名称", "New Person")
	h.click("开始使用")
	h.until("the mail page", func() bool { return h.app.route.Page == PageMail })
	if h.app.route.View.Unified != "inbox" {
		t.Fatalf("route %+v", h.app.route)
	}
	accs, err := h.env.Svc.ListAccounts(context.Background())
	if err != nil || len(accs) != 1 || accs[0].Email != "new@example.com" || accs[0].Name != "New Person" || accs[0].Username != "alice" {
		t.Fatalf("accounts %+v, %v", accs, err)
	}
	for _, s := range securities {
		if s.Value == core.SecurityNone {
			t.Fatal("the form offers plaintext")
		}
	}
}

func TestOnboardingCheckFailure(t *testing.T) {
	plaintext(t)
	h := newHarness(t)
	o := h.firstRun()
	h.fill("邮箱地址", "x@qq.com")
	h.advance(debounce)
	h.see("识别为 QQ 邮箱")
	h.click("继续")
	h.see("授权码")
	h.manualServers()
	h.fill("授权码", "wrong")
	h.click("连接")
	h.see("连接失败")
	h.see("IMAP 登录失败：授权码不正确")
	h.see("检查是否复制完整，或者在 QQ 邮箱里重新生成一个")
	h.see("等待上一步") // SMTP was skipped
	h.click("重新输入授权码")
	h.see("连接")
	if o.step != stepPassword || o.secret != "wrong" || o.username != "alice" || o.imapHost == "" {
		t.Fatalf("step %d, secret %q, user %q, host %q", o.step, o.secret, o.username, o.imapHost)
	}
	if !h.tt.HasText("QQ 邮箱要用「授权码」登录，不是 QQ 密码") {
		t.Fatalf("texts %q", h.tt.Texts())
	}
}

func TestOnboardingDetect(t *testing.T) {
	h := newHarness(t)
	h.firstRun()
	h.fill("邮箱地址", "x@qq.com")
	if h.tt.HasText("识别为") {
		t.Fatal("detected before the debounce")
	}
	h.advance(debounce)
	h.see("识别为 QQ 邮箱")
	h.click("继续")
	h.see("授权码")
	h.see("在网页版 QQ 邮箱打开「设置 → 账号」")
	h.click("打开 QQ 邮箱设置 ↗")
	h.until("OpenURL", func() bool {
		return slices.Contains(h.plat.Calls(), "OpenURL https://service.mail.qq.com/detail/0/75")
	})
}

// fakeOAuth hands out flows f1, f2, …; Await returns once the test finishes the flow.
type fakeOAuth struct {
	mu       sync.Mutex
	canceled []string
	flows    map[string]chan string
}

func (f *fakeOAuth) Begin(_ context.Context, provider, hint string) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := strconv.Itoa(len(f.flows) + 1)
	f.flows["f"+n] = make(chan string, 1)
	return "f" + n, "https://login.example/authorize?n=" + n, nil
}

func (f *fakeOAuth) Await(ctx context.Context, flowID string) (string, error) {
	f.mu.Lock()
	ch := f.flows[flowID]
	f.mu.Unlock()
	select {
	case e := <-ch:
		return e, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// finish signs flowID in as email.
func (f *fakeOAuth) finish(flowID, email string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.flows[flowID] <- email
}

func (f *fakeOAuth) Cancel(flowID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.canceled = append(f.canceled, flowID)
}

func (f *fakeOAuth) Canceled() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.canceled)
}

func TestOnboardingOAuth(t *testing.T) {
	h, _ := withAccount(t)
	fo := &fakeOAuth{flows: map[string]chan string{}}
	h.app.oauth = fo
	h.app.openAddAccount()
	h.see("添加账户")
	h.click("Outlook / Hotmail")
	h.see("已在浏览器中打开登录页")
	h.until("OpenURL of the sign-in page", func() bool {
		return slices.Contains(h.plat.Calls(), "OpenURL https://login.example/authorize?n=1")
	})
	// Leaving the step cancels its flow.
	h.click("← 换一个邮箱")
	h.until("Cancel f1", func() bool { return slices.Equal(fo.Canceled(), []string{"f1"}) })

	h.click("Outlook / Hotmail")
	h.until("OpenURL of the second flow", func() bool {
		return slices.Contains(h.plat.Calls(), "OpenURL https://login.example/authorize?n=2")
	})
	h.click("重新打开浏览器")
	h.until("the sign-in page again", func() bool {
		n := 0
		for _, c := range h.plat.Calls() {
			if c == "OpenURL https://login.example/authorize?n=2" {
				n++
			}
		}
		return n == 2
	})
	fo.finish("f2", "me@outlook.com")
	o := h.app.addAccount
	h.until("the check", func() bool { return o.step == stepChecking })
	h.see("me@outlook.com")
	// The fake flow left no grant in the Service, so the check fails at the settings step.
	h.see("连接失败")
	h.click("关闭")
	if h.app.addAccount != nil {
		t.Fatal("sheet still open")
	}
	h.until("Cancel f2", func() bool { return slices.Equal(fo.Canceled(), []string{"f1", "f2"}) })
}

func TestAddAccountSheet(t *testing.T) {
	plaintext(t)
	h, _ := withAccount(t)
	h.app.openAddAccount()
	h.see("添加账户")
	h.click("其他 IMAP 邮箱")
	h.fill("邮箱地址", "second@example.com")
	h.manualServers()
	h.fill("密码", "secret")
	h.click("连接")
	h.see("连接成功")
	h.fill("发件人名称", "Second")
	h.click("完成")
	h.until("the new account's settings", func() bool { return h.app.route.Page == PageSettings })
	if h.app.addAccount != nil {
		t.Fatal("sheet still open")
	}
	accs, err := h.env.Svc.ListAccounts(context.Background())
	if err != nil || len(accs) != 2 {
		t.Fatalf("accounts %+v, %v", accs, err)
	}
	var added core.Account
	for _, a := range accs {
		if a.Email == "second@example.com" {
			added = a
		}
	}
	if want := "account:" + strconv.FormatInt(added.ID, 10); added.ID == 0 || h.app.route.Settings != want || added.Name != "Second" || added.SyncLimit != 2000 {
		t.Fatalf("route %+v, added %+v", h.app.route, added)
	}
}

// toDone takes the first-run onboarding through the check of a new account
// on fake servers, to the done page.
func (h *harness) toDone(email string) *onboarding {
	h.t.Helper()
	o := h.firstRun()
	h.click("其他 IMAP 邮箱")
	h.fill("邮箱地址", email)
	h.manualServers()
	h.fill("密码", "secret")
	h.click("连接")
	h.see("连接成功")
	h.fill("发件人名称", "Saved Name")
	return o
}

func TestOnboardingSaveDisablesEdits(t *testing.T) {
	plaintext(t)
	h := newHarness(t)
	o := h.toDone("new@example.com")
	release := make(chan struct{})
	add := o.add
	o.add = func(ctx context.Context, in core.AccountInput) (core.Account, error) {
		<-release
		return add(ctx, in)
	}
	h.click("开始使用")
	h.until("saving", func() bool { return o.adding })
	h.click("发件人名称")
	h.typeText("Changed")
	if o.name != "Saved Name" {
		t.Fatalf("name edited while saving: %q", o.name)
	}
	close(release)
	h.until("the mail page", func() bool { return h.app.route.Page == PageMail })
	accs, err := h.env.Svc.ListAccounts(context.Background())
	if err != nil || len(accs) != 1 || accs[0].Name != "Saved Name" {
		t.Fatalf("accounts %+v, %v", accs, err)
	}
}

func TestOnboardingSyncLimitRetry(t *testing.T) {
	plaintext(t)
	h := newHarness(t)
	o := h.toDone("new@example.com")
	o.syncLimit = 10000
	adds, fails := 0, 1
	add, update := o.add, o.update
	o.add = func(ctx context.Context, in core.AccountInput) (core.Account, error) { adds++; return add(ctx, in) }
	o.update = func(ctx context.Context, id int64, p core.AccountPatch) (core.Account, error) {
		if fails > 0 {
			fails--
			return core.Account{}, errors.New("boom")
		}
		return update(ctx, id, p)
	}
	h.click("开始使用")
	h.see("出错了：boom")
	if h.app.route.Page != PageOnboarding || o.step != stepDone {
		t.Fatalf("finished after a failed update: route %+v, step %d", h.app.route, o.step)
	}
	h.click("重试")
	h.until("the mail page", func() bool { return h.app.route.Page == PageMail })
	accs, err := h.env.Svc.ListAccounts(context.Background())
	if err != nil || len(accs) != 1 || accs[0].SyncLimit != 10000 || adds != 1 {
		t.Fatalf("accounts %+v, %v, %d adds", accs, err, adds)
	}
}

func TestOnboardingSettingsErrors(t *testing.T) {
	h := newHarness(t)
	h.firstRun()
	h.click("其他 IMAP 邮箱")
	h.fill("邮箱地址", "new@example.com")
	h.fill("密码", "secret")
	h.click("连接")
	h.see("服务器设置有误：没有填写 IMAP 服务器")
	h.see("在「手动设置服务器」里填写服务器地址")
	h.click("手动设置服务器")
	h.fill("IMAP 服务器", "127.0.0.1")
	h.fill("SMTP 服务器", "127.0.0.1")
	h.fill("IMAP 端口", "99999")
	h.click("连接")
	h.see("服务器设置有误：IMAP 端口「99999」无效")
	h.see("端口是 1 到 65535 之间的数字，例如 993 或 465")
	for _, s := range h.tt.Texts() {
		if strings.Contains(s, "required") || strings.Contains(s, "range") {
			t.Fatalf("raw core error shown: %q", s)
		}
	}
}

func TestOnboardingDetectedBadgeInInput(t *testing.T) {
	h := newHarness(t)
	h.firstRun()
	h.fill("邮箱地址", "x@qq.com")
	h.advance(debounce)
	h.see("识别为 QQ 邮箱")
	in, ok1 := h.tt.Find("邮箱地址")
	badge, ok2 := h.tt.Find("识别为 QQ 邮箱")
	if !ok1 || !ok2 {
		t.Fatalf("input %v, badge %v", ok1, ok2)
	}
	if in.H != 40 {
		t.Fatalf("email input is %v high, want 40", in.H)
	}
	if badge.X < in.X+in.W || badge.Y < in.Y || badge.Y+badge.H > in.Y+in.H {
		t.Fatalf("badge %v is not trailing the input %v", badge, in)
	}
	// The row around them draws the box: its right border sits just past the badge.
	img, want, found := h.tt.Image(), theme.Light().Ring.SRGB(), false // focused after the fill
	y := int(badge.Y + badge.H/2)
	for x := int(badge.X + badge.W); x < int(badge.X+badge.W)+24 && !found; x++ { // the badge's and the box's padding
		got := img.RGBAAt(x, y)
		found = max(diff(got.R, want.R), diff(got.G, want.G), diff(got.B, want.B)) <= 4
	}
	if !found {
		t.Fatal("no input border right of the badge")
	}
}

func TestOnboardingDoneBadgeUsesSuccessTokens(t *testing.T) {
	plaintext(t)
	h := newHarness(t)
	h.toDone("done@example.com")
	title, ok := h.tt.Find("连接成功")
	if !ok {
		t.Fatal("no done title")
	}
	img, want := h.tt.Image(), theme.Light().SuccessBg.SRGB()
	x, found := int(title.X)+theme.DoneBadge/2, false
	for y := int(title.Y) - 60; y < int(title.Y) && !found; y++ {
		got := img.RGBAAt(x, y)
		found = max(diff(got.R, want.R), diff(got.G, want.G), diff(got.B, want.B)) <= 4
	}
	if !found {
		t.Fatal("no SuccessBg badge above the done title")
	}
}

func TestOnboardingManualLabels(t *testing.T) {
	h := newHarness(t)
	h.firstRun()
	h.click("其他 IMAP 邮箱")
	if _, open := h.tt.Find("IMAP 服务器"); !open {
		h.click("手动设置服务器")
	}
	h.see("IMAP 服务器")
	if _, ok := h.tt.Find("端口"); !ok {
		t.Fatalf("no bare 端口 label: %q", h.tt.Texts())
	}
}

func TestOnboardingEmailRowRingsOnFocus(t *testing.T) {
	h := newHarness(t)
	h.firstRun()
	h.click("邮箱地址")
	in, _ := h.tt.Find("邮箱地址")
	img, want := h.tt.Image(), theme.Light().Ring.SRGB()
	y, found := int(in.Y+in.H/2), false
	for x := int(in.X) - 16; x < int(in.X) && !found; x++ { // the row's left border, left of its padding
		got := img.RGBAAt(x, y)
		found = max(diff(got.R, want.R), diff(got.G, want.G), diff(got.B, want.B)) <= 4
	}
	if !found {
		t.Fatal("focused email row has no ring-coloured border")
	}
}
