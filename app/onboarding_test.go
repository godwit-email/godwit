package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/internal/kit"
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
	h.see("new@example.com · 4 个文件夹") // INBOX, Sent, Trash, Archive
	o := h.app.pages[PageOnboarding].(*onboarding)
	if !o.report.OK() {
		t.Fatalf("report %+v", o.report)
	}
	if text, detail, _ := o.checkRow(o.report.Steps[1]); text != "IMAP 登录成功" || detail != "4 个文件夹" {
		t.Fatalf("imap row %q, %q", text, detail)
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

func TestOnboardingProviderEmail(t *testing.T) {
	for _, p := range append(auth.ListProviders(), otherProvider) {
		if p.Login == "oauth" || !listed(p) {
			continue
		}
		t.Run(p.ID, func(t *testing.T) {
			h := newHarness(t)
			o := h.firstRun()
			h.click(tileName(p))
			want := ""
			if len(p.Domains) > 0 {
				want = "@" + p.Domains[0]
			}
			if o.email != want || !o.askEmail {
				t.Fatalf("email %q, askEmail %v; want %q, true", o.email, o.askEmail, want)
			}
			if want != "" {
				h.typeText("alice")
				if o.email != "alice"+want {
					t.Fatalf("typing before @: got %q", o.email)
				}
				h.typeText("2")
				if o.email != "alice2"+want {
					t.Fatalf("caret moved after typing: got %q", o.email)
				}
			}
		})
	}
}

func TestOnboardingProviderEmailPreserved(t *testing.T) {
	for _, email := range []string{"alice@company.example", "alice"} {
		t.Run(email, func(t *testing.T) {
			h := newHarness(t)
			o := h.firstRun()
			h.fill("邮箱地址", email)
			if !validEmail(email) {
				h.tt.Key(0, ui.KeyEscape)
				h.tt.Frame()
			}
			h.click("Gmail")
			if o.email != email || o.askEmail == validEmail(email) {
				t.Fatalf("email %q, askEmail %v", o.email, o.askEmail)
			}
		})
	}
}

func TestOnboardingSwitchUneditedProviderEmail(t *testing.T) {
	h := newHarness(t)
	o := h.firstRun()
	h.click("Gmail")
	h.click("← 换一个邮箱")
	h.click("iCloud")
	h.typeText("alice")
	if o.email != "alice@icloud.com" {
		t.Fatalf("switching provider: got %q", o.email)
	}
}

func TestOnboardingEmailAutocomplete(t *testing.T) {
	for _, keyboard := range []bool{false, true} {
		t.Run(strconv.FormatBool(keyboard), func(t *testing.T) {
			h := newHarness(t)
			o := h.firstRun()
			h.fill("邮箱地址", "alice@g")
			h.see("alice@gmail.com")
			if h.tt.HasText("alice@qq.com") {
				t.Fatal("suggested a domain that does not match")
			}
			if keyboard {
				h.tt.Key(0, ui.KeyDown)
				h.tt.Key(0, ui.KeyEnter)
				h.drain()
				h.tt.Frame()
			} else {
				h.click("alice@gmail.com")
			}
			if o.email != "alice@gmail.com" || o.step != stepPick {
				t.Fatalf("completion: email %q, step %v", o.email, o.step)
			}
			h.advance(debounce)
			h.see("识别为 Gmail")
			h.click("继续")
			if o.prov.ID != "gmail" || o.step != stepPassword || o.askEmail {
				t.Fatalf("continue: provider %q, step %v, askEmail %v", o.prov.ID, o.step, o.askEmail)
			}
		})
	}
}

func TestOnboardingSecretWhitespace(t *testing.T) {
	for _, provider := range []string{"icloud", "gmail", "qq", "exmail", "other"} {
		t.Run(provider, func(t *testing.T) {
			h := newHarness(t)
			o := h.firstRun()
			o.prov = otherProvider
			if p, ok := auth.ByID(provider); ok {
				o.prov = p
			}
			o.email = "alice@example.com"
			o.goTo(stepPassword)
			const pasted = "\r\n abcd-efgh\tijkl\u00a0mnop \n"
			o.secret = pasted
			h.tt.Frame()
			if o.input().Password != pasted {
				t.Fatal("password changed without confirmation")
			}
			h.see("检测到空格、换行或零宽字符，可能是复制时带入的。是否清除？")
			h.click("清除空白字符")
			if o.input().Password != "abcd-efghijklmnop" {
				t.Fatalf("password after cleanup %q", o.input().Password)
			}
			if h.tt.HasText("检测到空格、换行或零宽字符") {
				t.Fatal("cleanup hint stayed after cleanup")
			}
		})
	}
}

func TestSecretZeroWidthCharacters(t *testing.T) {
	for _, invisible := range []string{"\u200b", "\ufeff", " \u200b\t\ufeff\u00a0"} {
		t.Run(strconv.Quote(invisible), func(t *testing.T) {
			secret := invisible + "abc\u200c\u200d-def" + invisible
			pasted := secret
			tt := ui.NewTester(func(c *ui.Context) {
				theme.Apply(c, theme.Light())
				secretWhitespaceHint(c, &secret)
			}, 800, 200)
			if secret != pasted {
				t.Fatal("password changed without confirmation")
			}
			if err := tt.Click("清除空白字符"); err != nil {
				t.Fatal(err)
			}
			if secret != "abc\u200c\u200d-def" {
				t.Fatalf("password after cleanup %q", secret)
			}
			if tt.HasText("清除空白字符") {
				t.Fatal("cleanup hint stayed for meaningful joiners")
			}
		})
	}
}

func TestOnboardingPasswordVisibility(t *testing.T) {
	h := newHarness(t)
	o := h.firstRun()
	h.click("其他 IMAP 邮箱")
	h.fill("密码", "typed-secret")
	h.click("显示密码")
	h.see("隐藏密码")
	if o.secret != "typed-secret" {
		t.Fatal("showing the password changed its value")
	}
	h.click("隐藏密码")
	h.see("显示密码")
	if o.secret != "typed-secret" {
		t.Fatal("hiding the password changed its value")
	}
	h.click("显示密码")
	o.goTo(stepPassword)
	h.tt.Frame()
	h.see("显示密码")
}

func TestAccountInputOuterHeights(t *testing.T) {
	var email, password string
	var shown bool
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, theme.Light())
		ui.Column(c).Fill().Padding(20).Gap(12).Children(func() {
			ui.Box(c).Label("email-frame").Children(func() {
				kit.Input(c, &email, kit.InputOpts{}).Height(inputHeight(c))
			})
			ui.Box(c).Label("password-frame").Children(func() {
				secretInput(c, &password, &shown, "密码")
			})
		})
	}, 400, 200)
	for _, toggle := range []bool{false, true} {
		if toggle {
			if err := tt.Click("显示密码"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
		}
		for _, label := range []string{"email-frame", "password-frame"} {
			r, ok := tt.Find(label)
			if !ok || r.H != 36 {
				t.Fatalf("%s outer height %v, want 36 (shown=%v)", label, r.H, shown)
			}
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

type flowResult struct {
	email string
	err   error
}

// fakeOAuth hands out flows f1, f2, …; Await returns once the test finishes the flow.
type fakeOAuth struct {
	mu       sync.Mutex
	canceled []string
	flows    map[string]chan flowResult
	clients  []core.OAuthClient // each Begin's
}

func (f *fakeOAuth) Begin(_ context.Context, provider, hint string, client core.OAuthClient) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clients = append(f.clients, client)
	n := strconv.Itoa(len(f.flows) + 1)
	f.flows["f"+n] = make(chan flowResult, 1)
	return "f" + n, "https://login.example/authorize?n=" + n, nil
}

func (f *fakeOAuth) Await(ctx context.Context, flowID string) (string, error) {
	f.mu.Lock()
	ch := f.flows[flowID]
	f.mu.Unlock()
	select {
	case r := <-ch:
		return r.email, r.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// finish signs flowID in as email.
func (f *fakeOAuth) finish(flowID, email string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.flows[flowID] <- flowResult{email: email}
}

func (f *fakeOAuth) fail(flowID string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.flows[flowID] <- flowResult{err: err}
}

func (f *fakeOAuth) Cancel(flowID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.canceled = append(f.canceled, flowID)
	if ch, ok := f.flows[flowID]; ok {
		select {
		case ch <- flowResult{err: context.Canceled}:
		default:
		}
	}
}

func (f *fakeOAuth) Canceled() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.canceled)
}

func TestOnboardingOAuth(t *testing.T) {
	h, _ := withAccount(t)
	fo := &fakeOAuth{flows: map[string]chan flowResult{}}
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
	h.until("the window in front", func() bool { return h.hasCall("ShowWindow") })
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

// Without an app password, Gmail signs in with the user's own OAuth client.
func TestOnboardingOwnOAuthClient(t *testing.T) {
	h, _ := withAccount(t)
	fo := &fakeOAuth{flows: map[string]chan flowResult{}}
	h.app.oauth = fo
	h.app.openAddAccount()
	h.click("Gmail")
	h.see("应用专用密码")
	h.see("没开两步验证、加入了高级保护计划，或者管理员禁用了应用专用密码，就生成不了，改用浏览器登录。")
	h.fill("邮箱地址", "me@gmail.com")
	o := h.app.addAccount
	o.secret = "abcd" // the field shares its label with its tab
	h.click("浏览器登录")
	h.click("手动填写客户端 ID 和密钥")
	h.fill("客户端 ID", " 123.apps.googleusercontent.com ")
	o.client.secret = "GOCSPX-s3cret" // below the sheet's fold
	o.tab = 0                         // back to the app password and over again: each tab keeps what was typed in it
	h.tt.Frame()
	if o.secret != "abcd" {
		t.Fatalf("app password %q after switching tabs", o.secret)
	}
	h.click("浏览器登录")
	h.click("在浏览器中登录")
	h.see("需要在浏览器里登录 Google 账户")
	h.until("OpenURL of the sign-in page", func() bool {
		return slices.Contains(h.plat.Calls(), "OpenURL https://login.example/authorize?n=1")
	})
	fo.mu.Lock()
	got := fo.clients
	fo.mu.Unlock()
	if want := []core.OAuthClient{{ID: "123.apps.googleusercontent.com", Secret: "GOCSPX-s3cret"}}; !slices.Equal(got, want) {
		t.Fatalf("clients %+v, want %+v", got, want)
	}
	fo.finish("f1", "me@gmail.com")
	h.until("the check", func() bool { return o.step == stepChecking })
	if in := o.input(); in.OAuthFlowID != "f1" || in.Password != "" {
		t.Fatalf("input %+v signs in with neither the flow nor only it", in)
	}
	h.see("连接失败") // the fake flow left no grant in the Service
	h.click("重新登录")
	h.until("the second flow, with the same client", func() bool {
		fo.mu.Lock()
		defer fo.mu.Unlock()
		return len(fo.clients) == 2 && fo.clients[1] == fo.clients[0]
	})
	h.click("← 换一个邮箱")
	h.click("Gmail")
	h.see("应用专用密码")
	if o.tab != 0 {
		t.Fatal("a fresh pick starts on the browser sign-in, not the app password")
	}
}

// The client_secret_….json Google Cloud offers fills the own client in, chosen or dropped.
func TestOnboardingClientFile(t *testing.T) {
	h, _ := withAccount(t)
	fo := &fakeOAuth{flows: map[string]chan flowResult{}}
	h.app.oauth = fo
	dir := t.TempDir()
	file := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	desktop := file("client_secret_123.json", `{"installed":{"client_id":"123-abc.apps.googleusercontent.com","client_secret":"GOCSPX-s3cret"}}`)
	web := file("client_secret_web.json", `{"web":{"client_id":"9-web.apps.googleusercontent.com","client_secret":"GOCSPX-web"}}`)
	other := file("notes.txt", "hello")

	h.app.openAddAccount()
	h.click("Gmail")
	h.fill("邮箱地址", "me@company.com")
	h.click("浏览器登录")
	drop := "把下载的 client_secret_….json 拖到这里"
	h.see(drop)
	dropOn := func(path string) {
		h.t.Helper()
		r, _ := h.tt.Find(drop)
		if !h.tt.DropFiles(r.X+r.W/2, r.Y+r.H/2, path) {
			t.Fatal("the drop row took no file")
		}
		h.drain()
		h.tt.Frame()
	}

	dropOn(web)
	h.see("这是「Web 应用」客户端，要用「桌面应用」")
	h.click("换一个")
	dropOn(other)
	h.see("这不是 OAuth 客户端文件")
	h.click("换一个")

	h.plat.mu.Lock()
	h.plat.files = []string{desktop}
	h.plat.mu.Unlock()
	h.click("选择文件…")
	h.see("已导入")
	h.click("换一个")
	h.see(drop)
	h.click("选择文件…")
	h.see("123-abc.apps.googleusercontent.com")
	h.click("在浏览器中登录")
	h.until("the sign-in, with the file's client", func() bool {
		fo.mu.Lock()
		defer fo.mu.Unlock()
		return len(fo.clients) == 1 && fo.clients[0] == core.OAuthClient{ID: "123-abc.apps.googleusercontent.com", Secret: "GOCSPX-s3cret"}
	})
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
	o := h.app.addAccount
	b := o.confetti
	if b == nil {
		t.Fatal("no confetti on success")
	}
	h.tt.Frame()
	if o.confetti != b || !b.launched {
		t.Fatalf("confetti %p (launched %v), want %p launched", o.confetti, b.launched, b)
	}
	h.advance(confettiLife)
	h.tt.Frame()
	if o.confetti != nil {
		t.Fatal("confetti still in the air after its life")
	}
	h.fill("发件人名称", "Second") // the pieces never covered the form
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
	if want := "account:" + strconv.FormatInt(added.ID, 10); added.ID == 0 || h.app.route.Settings != want || added.Name != "Second" || added.Sync != (core.SyncRange{Limit: 2000}) {
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

// The range chosen is saved with the account, in one write: a sync started
// by the add never sees the default first.
func TestOnboardingSyncRangeRetry(t *testing.T) {
	plaintext(t)
	h := newHarness(t)
	o := h.toDone("new@example.com")
	o.sync = core.SyncRange{Months: 3}
	adds, fails := 0, 1
	add := o.add
	o.add = func(ctx context.Context, in core.AccountInput) (core.Account, error) {
		adds++
		if in.Sync == nil || *in.Sync != (core.SyncRange{Months: 3}) {
			t.Errorf("added with %+v", in.Sync)
		}
		if fails > 0 {
			fails--
			return core.Account{}, errors.New("boom")
		}
		return add(ctx, in)
	}
	h.click("开始使用")
	h.see("出错了：boom")
	if h.app.route.Page != PageOnboarding || o.step != stepDone {
		t.Fatalf("finished after a failed add: route %+v, step %d", h.app.route, o.step)
	}
	h.click("重试")
	h.until("the mail page", func() bool { return h.app.route.Page == PageMail })
	accs, err := h.env.Svc.ListAccounts(context.Background())
	if err != nil || len(accs) != 1 || accs[0].Sync != (core.SyncRange{Months: 3}) || adds != 2 {
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
	h.tt.Advance(2 * theme.MotionFast)
	in, ok1 := h.tt.Find("邮箱地址")
	badge, ok2 := h.tt.Find("识别为 QQ 邮箱")
	if !ok1 || !ok2 {
		t.Fatalf("input %v, badge %v", ok1, ok2)
	}
	if in.H != 34 {
		t.Fatalf("email input is %v high, want 34 inside its 36-high frame", in.H)
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
	h.tt.Advance(2 * theme.MotionFast)
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

func TestOnboardingFeishuLarkTile(t *testing.T) {
	h := newHarness(t)
	o := h.firstRun()
	h.see("飞书/Lark 邮箱")
	if lark, _ := auth.ByID("lark"); listed(lark) {
		t.Fatal("Lark has a tile of its own")
	}
	h.click("飞书/Lark 邮箱")
	h.see("飞书/Lark 邮箱")
	if o.prov.ID != "feishu" || o.imapHost != "imap.feishu.cn" {
		t.Fatalf("tile chose %s on %s", o.prov.ID, o.imapHost)
	}
	lark, _ := auth.ByID("lark")
	o.email = "a@corp.example"
	o.found(lark, o.email)
	if o.prov.ID != "lark" || o.imapHost != "imap.larksuite.com" || o.smtpHost != "smtp.larksuite.com" {
		t.Fatalf("a Lark address left %s on %s/%s", o.prov.ID, o.imapHost, o.smtpHost)
	}
	h.see("Lark 邮箱")

	feishu, _ := auth.ByID("feishu")
	o.smtpHost = "smtp.custom.example"
	o.found(feishu, o.email)
	if o.prov.ID != "lark" || o.imapHost != "imap.larksuite.com" || o.smtpHost != "smtp.custom.example" {
		t.Fatalf("edited servers were replaced: %s on %s/%s", o.prov.ID, o.imapHost, o.smtpHost)
	}
}

func TestOnboardingFeishuLarkTileAfterDetection(t *testing.T) {
	h := newHarness(t)
	o := h.firstRun()
	lark, _ := auth.ByID("lark")
	o.email = "a@corp.example"
	o.found(lark, o.email) // detected on the pick screen
	h.click("飞书/Lark 邮箱")
	if o.prov.ID != "lark" || o.imapHost != "imap.larksuite.com" {
		t.Fatalf("tile chose %s on %s for a Lark address", o.prov.ID, o.imapHost)
	}
}
