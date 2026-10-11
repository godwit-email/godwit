package app

import (
	"bytes"
	"context"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/emersion/go-imap/v2"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/internal/kit"
)

// shotEnv is a mail harness shaped like the design's sample data: two accounts,
// today = Fri 10/09 10:00 (the fake clock), a fake model service.
type shotEnv struct {
	*harness
	you, work core.Account
	llm       *coretest.LLM
}

func day(m, d, hh, mm int) time.Time {
	return time.Date(2026, time.Month(m), d, hh, mm, 0, 0, time.Local)
}

// putSeen is put with the \Seen flag.
func (h *harness) putSeen(acc core.Account, folder string, raw []byte) {
	h.t.Helper()
	if _, err := h.env.IMAP(acc.ID).User.Append(folder, bytes.NewReader(raw), &imap.AppendOptions{Flags: []imap.Flag{imap.FlagSeen}}); err != nil {
		h.t.Fatal(err)
	}
}

// shotMail seeds the design's inbox. set adjusts the settings (summary and
// quick replies are on unless it turns them off).
func shotMail(t *testing.T, set func(*core.Settings)) *shotEnv {
	t.Helper()
	s := &shotEnv{}
	h := newHarnessWith(t, nil, func(e *coretest.Env) {
		s.you, s.work = e.AddAccount(t, "you@example.com"), e.AddAccount(t, "work@company.example")
		s.llm = e.LLM(t)
		t.Cleanup(s.llm.Release)
		st, err := e.Svc.GetSettings(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		st.Summary, st.QuickReplies = true, true
		if set != nil {
			set(&st)
		}
		if _, err := e.Svc.SetSettings(context.Background(), st); err != nil {
			t.Fatal(err)
		}
	})
	s.harness = h
	h.until("the mail page", func() bool { return h.app.loaded && h.app.route.Page == PageMail })
	h.resize(1440, 900)
	h.advance(time.Hour) // 10:00
	you, work := s.you, s.work
	m := func(from, subj, body, id string, when time.Time, o ...coretest.MsgOption) []byte {
		return coretest.Msg(from, you.Email, subj, body, append([]coretest.MsgOption{coretest.MessageID(id), coretest.Date(when)}, o...)...)
	}
	flightHTML := `<p>尊敬的旅客：</p><p>你预订的 NW 1858 航班将于明天 14:20 起飞。</p>` + strings.Repeat(`<img src="https://img.northwind.example/p.png">`, 3)
	h.put(you, "INBOX", m("Code Host <noreply@codehost.example>", "[Code Host] 你的登录验证码", "有人正在尝试登录你的账户，验证码 482913，10 分钟内有效。", "otp@x", day(10, 9, 9, 41)))
	h.put(you, "INBOX", m("Northwind Airlines <checkin@northwind.example>", "值机提醒：NW 1858 上海 → 成都", "你的航班将于明天 14:20 起飞，现已开放在线值机。", "fl@x", day(10, 9, 8, 15), coretest.HTML(flightHTML)))
	h.putSeen(you, "INBOX", m("Lena Fischer <lena@partner.example>", "Q4 partnership proposal",
		"Hi,\n\nFollowing up on our call last week, I put together a draft proposal for the Q4 partnership. The main points are a shared launch in November and a joint webinar.\n\nCould you take a look and let me know by Friday whether the timeline works on your side?\n\nBest, Lena",
		"lena@x", day(10, 8, 21, 3), coretest.Attachment("Q4-proposal.pdf", "application/pdf", make([]byte, 218*1024*3/4))))
	h.putSeen(you, "INBOX", m("财务部 <finance@company.example>", "10 月报销单已审批通过", "你提交的 10 月差旅报销单已审批通过，款项将于 3 个工作日内到账。", "fin@x", day(10, 5, 11, 0)))
	h.putSeen(you, "INBOX", m("Acme Cloud <billing@acme.example>", "Your receipt for October", "Thanks for your payment. Amount: [AMOUNT]", "acme@x", day(10, 2, 9, 0)))
	h.putSeen(you, "Sent", coretest.Msg("Me <you@example.com>", "Lena Fischer <lena@partner.example>", "Re: proposal timeline", "Sounds good. Let us aim to finalize the proposal by Friday.", coretest.MessageID("sent@x"), coretest.Date(day(10, 3, 10, 0))))
	h.putSeen(work, "Archive", coretest.Msg("Mark Chen <mark@partner.example>", work.Email, "Partnership proposal — revised deck", "Attached is the revised proposal deck with the pricing.", coretest.MessageID("mark@x"), coretest.Date(day(9, 28, 15, 0))))
	h.syncNow(you)
	h.syncNow(work)
	h.waitMail("the sample mail", func() bool {
		return h.tt.HasText("Q4 partnership proposal") && h.tt.HasText("Your receipt for October")
	})
	return s
}

// shotDrafts adds the design's three drafts and opens the drafts folder.
func (s *shotEnv) shotDrafts() {
	h := s.harness
	if err := h.env.IMAP(s.you.ID).User.Create("Drafts", nil); err != nil {
		h.t.Fatal(err)
	}
	h.syncNow(s.you)
	for _, d := range []struct{ to, subj, html string }{
		{"", "", "<p>周会要点：发布时间、研讨会议程、预算……</p>"},
		{"finance@company.example", "差旅报销补充材料", "<p>附件是 10 月出差的酒店发票，请查收。</p>"},
		{"Lena Fischer <lena@partner.example>", "Re: Q4 partnership proposal", "<p>Hi Lena,</p><p>Thank you for the proposal. November works well for us; would it be possible to move the webinar back by one week?</p><p>Best,</p>"},
	} {
		dr, err := h.env.Svc.NewDraft(context.Background(), s.you.ID, "new", 0)
		if err != nil {
			h.t.Fatal(err)
		}
		dr.Subject, dr.HTML = d.subj, d.html
		if d.to != "" {
			dr.To = []string{d.to}
		}
		if _, err = h.env.Svc.SaveDraft(context.Background(), dr); err != nil {
			h.t.Fatal(err)
		}
	}
	h.waitMail("the drafts folder", func() bool { return h.tt.HasText("草稿") })
	h.click("草稿")
	h.see("差旅报销补充材料")
}

// shotInstall installs a plugin made on the spot; grant also approves its
// permissions and enables it.
func shotInstall(t *testing.T, h *harness, name, manifest, index string, grant bool) {
	t.Helper()
	dir := t.TempDir()
	for f, body := range map[string]string{"plugin.json": manifest, "index.ts": index} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	if _, err := h.env.Svc.InstallPlugin(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if !grant {
		return
	}
	if err := h.env.Svc.GrantPermission(ctx, name); err != nil {
		t.Fatal(err)
	}
	if err := h.env.Svc.SetPluginEnabled(ctx, name, true); err != nil {
		t.Fatal(err)
	}
}

// shotPlugin installs an enabled plugin with a card on the messages whose
// subject has match.
func shotPlugin(t *testing.T, h *harness, name, match, card string) {
	t.Helper()
	shotInstall(t, h, name, fmt.Sprintf(`{"name":%q,"version":"0.1.0","main":"index.ts","permissions":{"mail":["read"]}}`, name),
		fmt.Sprintf(`export default function (ctx: any) { ctx.cards.register({ match: (m: any) => m.subject.includes(%q), render: (m: any) => (%s) }) }`, match, card), true)
}

// TestShots writes screenshots named after the design's artboards to
// $GODWIT_SHOTS, to compare with them by eye. Skipped unless the variable is set.
func TestShots(t *testing.T) {
	dir := os.Getenv("GODWIT_SHOTS")
	if dir == "" {
		t.Skip("GODWIT_SHOTS not set")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	shot := func(h *harness, name string) {
		h.t.Helper()
		h.tt.Frame()
		h.advance(refreshEvery)
		h.tt.Frame()
		f, err := os.Create(filepath.Join(dir, name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, h.tt.Image()); err != nil {
			t.Fatal(err)
		}
	}
	// quick turns quick replies on once the open message is there (they and the
	// summary would race for the scripted turns otherwise).
	quick := func(s *shotEnv) {
		s.setSettings(func(st *core.Settings) { st.QuickReplies = true })
		r := s.app.mail.reader
		m, _ := r.last()
		r.loadQuick(m.ID, false)
	}
	// openLena opens the proposal thread and waits for its summary and quick
	// replies (the fake model answers in the order scripted).
	openLena := func(s *shotEnv) {
		s.llm.Script(coretest.Turn{Text: []string{"- 对方发来 Q4 合作提案草稿（附件 PDF）。\n- 要点：11 月联合发布、一场联合网络研讨会。\n- 需要你在周五前确认时间安排是否可行。\n"}})
		s.click("Q4 partnership proposal")
		s.see("需要你在周五前确认")
		s.llm.Script(coretest.Turn{Text: []string{`{"replies":[{"html":"<p>Hi Lena, thanks for the proposal. November works for us, happy to move ahead.</p>"},{"html":"<p>Hi Lena, November works, but could we move the webinar one week later?</p>"},{"html":"<p>Hi Lena, thanks! I will review it with the team and get back to you by Friday.</p>"}]}`}})
		quick(s)
		s.see("Hi Lena, thanks! I will review it")
	}
	noQuick := func(st *core.Settings) { st.QuickReplies = false }
	noAuto := func(st *core.Settings) { st.QuickReplies, st.Summary = false, false }

	t.Run("Main", func(t *testing.T) {
		s := shotMail(t, noQuick)
		openLena(s)
		shot(s.harness, "Main")
		// the translation shows over the body, under the summary
		s.llm.Script(coretest.Turn{Text: []string{"你好，\n\n接着上周的通话，我整理了一份 Q4 合作的提案草稿。要点是 11 月联合发布，以及一场联合网络研讨会。\n\n能否在周五前看一下，告诉我时间安排对你们来说是否可行？\n\n祝好，Lena"}})
		s.click("翻译")
		s.see("显示原文")
		s.see("能否在周五前看一下")
		shot(s.harness, "MainTranslated")
	})
	t.Run("MainDrafts", func(t *testing.T) {
		s := shotMail(t, noAuto)
		s.shotDrafts()
		s.click("Re: Q4 partnership proposal")
		s.see("继续编辑")
		shot(s.harness, "MainDrafts")
	})
	t.Run("MainSearch", func(t *testing.T) {
		s := shotMail(t, noAuto)
		s.tt.Key(ui.Cmd, ui.KeyF)
		s.typeText("proposal")
		s.tt.Key(0, ui.KeyEnter)
		s.advance(debounce)
		s.waitMail("the results", func() bool {
			return s.tt.HasText("Partnership proposal — revised deck") && s.tt.HasText("Re: proposal timeline")
		})
		shot(s.harness, "MainSearch")
		s.tt.Key(ui.Cmd, ui.KeyA)
		s.typeText("Q3 预算 v4")
		s.tt.Key(0, ui.KeyEnter)
		s.advance(debounce)
		s.waitMail("no results", func() bool { return s.tt.HasText("没有结果") || s.tt.HasText("没有找到") })
		shot(s.harness, "MainSearchEmpty")
	})
	t.Run("MainOTP", func(t *testing.T) {
		s := shotMail(t, noAuto)
		shotPlugin(t, s.harness, "otp-card", "验证码", `{ title: "验证码", fields: [{ label: "", value: "482 913" }, { label: "有效期", value: "10 分钟" }], buttons: [{ label: "复制", kind: "copy", value: "482913" }] }`)
		s.click("[Code Host] 你的登录验证码")
		s.see("复制")
		shot(s.harness, "MainOTP")
	})
	t.Run("MainFlight", func(t *testing.T) {
		s := shotMail(t, noAuto)
		shotPlugin(t, s.harness, "flight-cards", "NW 1858", `{ title: "航班 NW 1858 · 明天 14:20 起飞 · 登机口 [待定]", fields: [{ label: "", value: "PVG → CTU" }, { label: "座位", value: "[待选] · 行李额 [待确认]" }], buttons: [{ label: "在线值机", kind: "open-url", value: "https://northwind.example/checkin" }] }`)
		s.click("值机提醒：NW 1858 上海 → 成都")
		s.see("在线值机")
		s.llm.Script(coretest.Turn{Text: []string{`{"replies":[{"html":"<p>Thanks, checked in. See you on board.</p>"},{"html":"<p>你好，我想把航班改到后一天，请问怎么操作？</p>"}]}`}})
		quick(s)
		s.see("Thanks, checked in. See you on board.")
		shot(s.harness, "MainFlight")
	})
	// modelsSetup swaps the fake model service for the design's three.
	modelsSetup := func(s *shotEnv, keep bool) {
		ctx := context.Background()
		list, err := s.env.Svc.ListModelServices(ctx)
		if err != nil {
			t.Fatal(err)
		}
		s.setSettings(func(st *core.Settings) { st.DefaultModel, st.SmallModel = "", "" })
		for _, ms := range list {
			if err := s.env.Svc.RemoveModelService(ctx, ms.ID); err != nil {
				t.Fatal(err)
			}
		}
		if !keep {
			return
		}
		for _, in := range []core.ModelServiceInput{
			{Name: "OpenAI", Kind: "openai-responses", BaseURL: "https://api.openai.com/v1", APIKey: "k1", Models: []string{"[MODEL A]", "gpt-6-luna"}},
			{Name: "Anthropic", Kind: "anthropic", BaseURL: "https://api.anthropic.com/v1", APIKey: "k2", Models: []string{"[MODEL B]"}},
			{Name: "本地网关", Kind: "openai", BaseURL: "http://127.0.0.1:8080/v1", APIKey: "k3", Models: []string{"[MODEL C]", "[MODEL D]", "[MODEL E]"}},
		} {
			if _, err := s.env.Svc.AddModelService(ctx, in); err != nil {
				t.Fatal(err)
			}
		}
		s.setSettings(func(st *core.Settings) { st.DefaultModel, st.SmallModel = "openai/[MODEL A]", "openai/gpt-6-luna" })
	}
	t.Run("Settings", func(t *testing.T) {
		s := shotMail(t, noAuto)
		modelsSetup(s, true)
		s.openSettings(accountRoute(s.you.ID))
		s.see("签名")
		shot(s.harness, "SettingsAccounts")
		s.openSettings("general")
		s.see("界面密度")
		shot(s.harness, "SettingsGeneral")
		s.openSettings("models")
		s.see("OpenAI")
		shot(s.harness, "SettingsModels")
		s.click("添加服务")
		s.see("其他服务")
		s.click("OpenAI")
		s.see("https://api.openai.com/v1")
		shot(s.harness, "SettingsModelsSheet")
	})
	t.Run("SettingsModelsEmpty", func(t *testing.T) {
		s := shotMail(t, noAuto)
		modelsSetup(s, false)
		s.openSettings("models")
		s.see("添加服务")
		shot(s.harness, "SettingsModelsEmpty")
	})
	t.Run("SettingsExtensions", func(t *testing.T) {
		s := shotMail(t, noAuto)
		shotInstall(t, s.harness, "deepl-translate", `{"name":"deepl-translate","version":"0.1.0","main":"index.ts",
			"permissions":{"mail":["read"],"net":["api-free.deepl.com","api.deepl.com"]},
			"config":[{"key":"apiKey","type":"secret","label":"DeepL API Key"},
				{"key":"plan","type":"select","label":"套餐","options":[{"value":"free","label":"Free"},{"value":"pro","label":"Pro"}],"default":"free"}]}`,
			`export default function (_ctx: any) {}`, false)
		shotPlugin(t, s.harness, "otp-card", "验证码", `{ title: "验证码", fields: [] }`)
		shotPlugin(t, s.harness, "flight-cards", "NW", `{ title: "航班", fields: [] }`)
		shotPlugin(t, s.harness, "attachment-check", "-", `{ title: "附件", fields: [] }`)
		s.openSettings("extensions")
		s.click("插件 · 4")
		s.see("deepl-translate")
		s.see("attachment-check")
		shot(s.harness, "SettingsExtensions")
	})
	t.Run("Toasts", func(t *testing.T) {
		s := shotMail(t, noQuick)
		openLena(s)
		s.app.toastAs(kit.ToastSuccess, "邮件已发送")
		s.app.toastAs(kit.ToastWarning, "work@company.example 没有归档文件夹，已跳过")
		s.app.toast("链接已复制")
		s.see("链接已复制")
		shot(s.harness, "Toasts")
	})
	t.Run("AddAccountSheet", func(t *testing.T) {
		s := shotMail(t, noAuto)
		s.openSettings(accountRoute(s.you.ID))
		s.app.openAddAccount()
		s.see("添加账户")
		s.fill("邮箱地址", "12345678@qq.com")
		s.advance(debounce)
		s.see("识别为 QQ 邮箱")
		shot(s.harness, "AddAccountSheet")
	})
	// onboarding starts the first run, on fake servers for the checks.
	onboarding := func(t *testing.T) *harness {
		plaintext(t)
		h := newHarness(t)
		h.resize(1440, 900)
		h.firstRun()
		return h
	}
	qq := func(h *harness) {
		h.fill("邮箱地址", "12345678@qq.com")
		h.advance(debounce)
		h.see("识别为 QQ 邮箱")
	}
	t.Run("Onboarding", func(t *testing.T) {
		h := onboarding(t)
		qq(h)
		shot(h, "Onboarding")
		h.click("继续")
		h.see("授权码")
		h.fill("授权码", "abcdefghijk")
		shot(h, "OnboardingPassword")
		h.manualServers()
		h.fill("授权码", "wrong")
		h.click("连接")
		h.see("连接失败")
		h.see("等待上一步")
		shot(h, "OnboardingError")
	})
	t.Run("OnboardingDone", func(t *testing.T) {
		h := onboarding(t)
		qq(h)
		h.click("继续")
		h.see("授权码")
		h.manualServers()
		h.fill("授权码", "secret")
		h.click("连接")
		h.see("连接成功")
		shot(h, "OnboardingDone")
	})
	t.Run("OnboardingOAuth", func(t *testing.T) {
		h := onboarding(t)
		h.app.oauth = &fakeOAuth{flows: map[string]chan string{}}
		h.click("Outlook / Hotmail")
		h.see("已在浏览器中打开登录页")
		shot(h, "OnboardingOAuth")
	})
}
