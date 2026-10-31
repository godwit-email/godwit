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
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
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
		s.you, s.work = addQQ(t, e, "you@example.com"), e.AddAccount(t, "work@company.example")
		def := true // the second account took the default
		if _, err := e.Svc.UpdateAccount(context.Background(), s.you.ID, core.AccountPatch{IsDefault: &def}); err != nil {
			t.Fatal(err)
		}
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
	for _, f := range []string{"项目/Q4/合同", "项目/Q4/发票", "项目/报销"} { // the design's nested folders
		if err := h.env.IMAP(work.ID).User.Create(f, nil); err != nil {
			t.Fatal(err)
		}
	}
	h.put(work, "项目/Q4/合同", coretest.Msg("Mark Chen <mark@partner.example>", work.Email, "合同终稿", "请查收合同终稿。", coretest.MessageID("contract@x"), coretest.Date(day(10, 7, 16, 0))))
	h.syncNow(you)
	h.syncNow(work)
	h.waitMail("the sample mail", func() bool {
		return h.tt.HasText("Q4 partnership proposal") && h.tt.HasText("Your receipt for October")
	})
	return s
}

// addQQ adds the default account as a QQ mailbox on fake servers, so its
// settings show QQ's logo and meta as the design does.
func addQQ(t *testing.T, e *coretest.Env, email string) core.Account {
	t.Helper()
	ih, ip, sh, sp, user, pass := e.StartServers(t)
	acc, err := e.Svc.AddAccount(context.Background(), core.AccountInput{Email: email, Name: "you", Username: user, Password: pass, IsDefault: true,
		IMAPHost: ih, IMAPPort: ip, IMAPSecurity: core.SecurityNone, SMTPHost: sh, SMTPPort: sp, SMTPSecurity: core.SecurityNone, Provider: "qq"})
	if err != nil {
		t.Fatal(err)
	}
	return acc
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
		{"财务部 <finance@company.example>", "差旅报销补充材料", "<p>附件是 10 月出差的酒店发票，请查收。</p>"},
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
	f := h.folderOf(s.you, "drafts")
	h.post(func() { h.app.navigate(Route{Page: PageMail, View: core.View{FolderID: f.ID}}) })
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

// shotDescs are the design's plugin descriptions.
var shotDescs = map[string]string{"flight-cards": "航班邮件显示行程卡片", "attachment-check": "发送前检查遗漏的附件"}

// shotPlugin installs an enabled plugin with a card on the messages whose
// subject has match.
func shotPlugin(t *testing.T, h *harness, name, match, card string) {
	t.Helper()
	shotInstall(t, h, name, fmt.Sprintf(`{"name":%q,"version":"0.1.0","description":%q,"main":"index.ts","permissions":{"mail":["read"]}}`, name, shotDescs[name]),
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
	write := func(h *harness, dir, name string) {
		h.t.Helper()
		h.tt.Advance(2 * theme.MotionBase)
		f, err := os.Create(filepath.Join(dir, name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, h.tt.Image()); err != nil {
			t.Fatal(err)
		}
	}
	dark := os.Getenv("GODWIT_SHOTS_DARK") // also each shot in dark mode, there (the design has no dark artboards)
	if dark != "" {
		if err := os.MkdirAll(dark, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	shot := func(h *harness, name string) {
		h.t.Helper()
		h.tt.Frame()
		h.advance(refreshEvery)
		write(h, dir, name)
		if dark != "" {
			h.tt.SetDark(true)
			write(h, dark, name)
			h.tt.SetDark(false)
			h.tt.Frame()
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
		writeSkill(t, s.env, "brief") // the design's 更多 skill
		s.llm.Script(coretest.Turn{Text: []string{"- 对方发来 Q4 合作提案草稿（附件 PDF）。\n- 要点：11 月联合发布、一场联合网络研讨会。\n- 需要你在周五前确认时间安排是否可行。\n"}})
		s.click("Q4 partnership proposal")
		s.see("需要你在周五前确认")
		s.llm.Script(coretest.Turn{Text: []string{`{"replies":[{"label":"同意","html":"<p>Hi Lena, thanks for the proposal. November works for us, happy to move ahead.</p>"},{"label":"同意但调整","html":"<p>Hi Lena, November works, but could we move the webinar one week later?</p>"},{"label":"稍后答复","html":"<p>Hi Lena, thanks! I will review it with the team and get back to you by Friday.</p>"}]}`}})
		quick(s)
		s.see("Hi Lena, thanks! I will review it")
	}
	noQuick := func(st *core.Settings) { st.QuickReplies = false }
	t.Run("Contacts", func(t *testing.T) {
		s := shotMail(t, noAuto)
		unsub := coretest.Header("List-Unsubscribe: <https://news.northwind.example/u?t=1>")
		for i, subj := range []string{"本周特价机票", "会员积分即将过期", "秋季目的地推荐"} {
			s.put(s.you, "INBOX", coretest.Msg("Northwind Airlines <news@northwind.example>", s.you.Email, subj, "精选优惠。",
				coretest.MessageID(fmt.Sprint("nw", i, "@x")), coretest.Date(day(10, 9-i, 7, 30)), unsub))
		}
		s.syncNow(s.you)
		s.click("联系人")
		s.see("news@northwind.example")
		shot(s.harness, "Contacts")
		s.app.contacts().choose("lena@partner.example")
		s.see("最近往来")
		shot(s.harness, "ContactsPerson")
	})
	noAuto := func(st *core.Settings) { st.QuickReplies, st.Summary = false, false }

	t.Run("Main", func(t *testing.T) {
		s := shotMail(t, noQuick)
		openLena(s)
		shot(s.harness, "Main")
		*s.app.mail.accountOpen(s.work.ID) = true
		shot(s.harness, "MainFolders")
		*s.app.mail.accountOpen(s.work.ID) = false
		// Translation keeps the message's body page, below the summary.
		s.llm.Script(coretest.Turn{Text: []string{`{"texts":["你好，\n\n接着上周的通话，我整理了一份 Q4 合作的提案草稿。要点是 11 月联合发布，以及一场联合网络研讨会。\n\n能否在周五前看一下，告诉我时间安排对你们来说是否可行？\n\n祝好，Lena"],"source":"en"}`}})
		s.click("翻译")
		s.see("显示原文")
		m, _ := s.app.mail.reader.last()
		s.seeTranslation(m.ID, "能否在周五前看一下")
		shot(s.harness, "MainTranslated")
	})
	for _, v := range []struct {
		name string
		evs  func(s *shotEnv) []events.Event
		wait time.Duration
	}{
		{"MainSyncOk", func(s *shotEnv) []events.Event { return nil }, 0},
		{"MainSyncRetrying", func(s *shotEnv) []events.Event {
			return []events.Event{{Type: events.SyncFailed, AccountID: s.you.ID, Kind: "connect", Error: "dial tcp: connection refused"}}
		}, 0},
		{"MainSyncPassword", func(s *shotEnv) []events.Event {
			return []events.Event{{Type: events.SyncFailed, AccountID: s.you.ID, Kind: "auth", Error: "login refused"},
				{Type: events.SyncFailed, AccountID: s.work.ID, Kind: "timeout", Error: "i/o timeout"}}
		}, 0},
		{"MainSyncFailed", func(s *shotEnv) []events.Event {
			return []events.Event{{Type: events.SyncFailed, AccountID: s.you.ID, Kind: "timeout", Error: "i/o timeout"}}
		}, 2 * time.Hour},
	} {
		t.Run(v.name, func(t *testing.T) {
			s := shotMail(t, noAuto)
			s.settle()
			s.post(func() {
				for _, ev := range v.evs(s) {
					s.app.onEvent(ev)
				}
			})
			s.drain()
			s.advance(v.wait)
			s.app.syncOpen = true
			shot(s.harness, v.name)
		})
	}
	t.Run("MainWindows", func(t *testing.T) {
		old := goos
		goos = "windows"
		defer func() { goos = old }()
		s := shotMail(t, noQuick)
		s.tt.SetTitleBar(ui.TitleBar{Height: 40, Right: 138}) // 3 caption buttons of 46 px
		openLena(s)
		shot(s.harness, "MainWindows")
	})
	t.Run("MainDrafts", func(t *testing.T) {
		s := shotMail(t, noAuto)
		s.shotDrafts()
		s.click("Re: Q4 partnership proposal")
		s.see("继续编辑")
		s.plat.setComposeOpen(s.app.mail.draftSel, true) // the design's editing state
		s.app.ComposeWindowsChanged()
		s.see("正在撰写窗口里编辑")
		shot(s.harness, "MainDrafts")
	})
	t.Run("MainOlder", func(t *testing.T) { // the row while older mail loads (no artboard: compared by eye)
		s := shotMail(t, noAuto)
		f := s.folderOf(s.you, "inbox")
		s.post(func() { s.app.navigate(Route{Page: PageMail, View: core.View{FolderID: f.ID}}) })
		s.until("the folder", func() bool { return s.app.mail.view.FolderID == f.ID && !s.app.mail.loading })
		s.post(func() { s.app.mail.stopOlder(); s.app.mail.older.busy = true })
		shot(s.harness, "MainOlder")
	})
	t.Run("MainSearch", func(t *testing.T) {
		s := shotMail(t, noAuto)
		search := func(q string) {
			s.post(func() { s.app.mail.searchOpen = true }) // ⌘F opens the palette; the list's search is its 查看全部
			s.tt.Key(ui.Cmd, ui.KeyA)
			s.typeText(q)
			s.tt.Key(0, ui.KeyEnter)
			s.advance(debounce)
		}
		search("Q3 预算 v4")
		s.waitMail("no results", func() bool { return s.tt.HasText("没有找到「Q3 预算 v4」") })
		shot(s.harness, "MainSearchEmpty")
		// the design's results: the proposal open, and chosen among them
		s.tt.Key(0, ui.KeyEscape)
		s.click("Q4 partnership proposal")
		s.see("Q4-proposal.pdf")
		search("proposal")
		s.waitMail("the results", func() bool {
			return s.tt.HasText("Partnership proposal — revised deck") && s.tt.HasText("Re: proposal timeline")
		})
		shot(s.harness, "MainSearch")
	})
	t.Run("MainOTP", func(t *testing.T) {
		s := shotMail(t, noAuto)
		writeSkill(t, s.env, "brief")
		// the built-in sign-in codes plugin draws the card
		s.click("[Code Host] 你的登录验证码")
		s.see("复制")
		shot(s.harness, "MainOTP")
	})
	t.Run("MainFlight", func(t *testing.T) {
		s := shotMail(t, noAuto)
		writeSkill(t, s.env, "brief")
		shotPlugin(t, s.harness, "flight-cards", "NW 1858", `{ title: "航班 NW 1858 · 明天 14:20 起飞 · 登机口 [待定]", fields: [{ label: "", value: "PVG → CTU" }, { label: "座位", value: "[待选] · 行李额 [待确认]" }], buttons: [{ label: "在线值机", kind: "open-url", value: "https://northwind.example/checkin" }] }`)
		s.click("值机提醒：NW 1858 上海 → 成都")
		s.see("在线值机")
		s.llm.Script(coretest.Turn{Text: []string{`{"replies":[{"label":"确认","html":"<p>Thanks, checked in. See you on board.</p>"},{"label":"改签","html":"<p>你好，我想把航班改到后一天，请问怎么操作？</p>"}]}`}})
		quick(s)
		s.see("Thanks, checked in. See you on board.")
		shot(s.harness, "MainFlight")
	})
	// modelsSetup swaps the fake model service for the design's: the views
	// (normal, broken, gen-only) pick which of the three and their tests.
	modelsSetup := func(s *shotEnv, view string) {
		ctx := context.Background()
		list, err := s.env.Svc.ListModelServices(ctx)
		if err != nil {
			t.Fatal(err)
		}
		s.setSettings(func(st *core.Settings) { st.DefaultModel, st.SmallModel, st.JudgeService = "", "", "" })
		for _, ms := range list {
			if err := s.env.Svc.RemoveModelService(ctx, ms.ID); err != nil {
				t.Fatal(err)
			}
		}
		defer func() { // a load still under way read the sample's settings: the newest applies
			want := s.app.settings.DefaultModel
			s.app.load()
			s.until("the app reads the design's settings", func() bool { return s.app.settings.DefaultModel == want })
		}()
		if view == "empty" {
			return
		}
		all := []core.ModelServiceInput{
			{Name: "OpenAI", Kind: "openai-responses", BaseURL: "https://api.openai.com/v1", APIKey: "k1", Models: []string{"[MODEL A]", "[MODEL B]"}, JudgeFormat: "decisions", JudgeModel: "gpt-6-luna"},
			{Name: "Anthropic", Kind: "anthropic", BaseURL: "https://api.anthropic.com/v1", APIKey: "k2", Models: []string{"[MODEL D]"}},
			// the design's address is http://127.0.0.1:[PORT]/v1, which no service can store
			{Name: "本地网关", Kind: "openai", BaseURL: "http://127.0.0.1:8080/v1", APIKey: "k3", Models: []string{"[MODEL F]", "[MODEL G]", "[MODEL H]"}},
		}
		if view == "gen-only" {
			all = all[1:]
		}
		for _, in := range all {
			if _, err := s.env.Svc.AddModelService(ctx, in); err != nil {
				t.Fatal(err)
			}
		}
		s.setSettings(func(st *core.Settings) {
			st.DefaultModel, st.JudgeService = "openai/[MODEL A]", "openai"
			if view == "gen-only" {
				st.DefaultModel, st.JudgeService = "anthropic/[MODEL D]", ""
			}
		})
	}
	// modelsStatus gives the listed services the design's test results:
	// stored tests need a reachable service, which the design's addresses aren't.
	modelsStatus := func(s *shotEnv, view string) {
		m := s.app.pages[PageSettings].(*settingsPage).models
		m.serviceAvatar = func(context.Context, int64) (core.Avatar, error) { return core.Avatar{}, nil } // no favicons off the network
		ok := func(ms int) core.CapStatus { return core.CapStatus{State: "ok", LatencyMs: ms} }
		auth := core.CapStatus{State: "fail", Code: "auth", HTTPStatus: 401}
		for i, svc := range m.services {
			switch {
			case svc.Name == "OpenAI" && view == "broken":
				m.services[i].Status = map[string]core.CapStatus{core.CapGen: auth, core.CapJudge: auth}
			case svc.Name == "OpenAI":
				m.services[i].Status = map[string]core.CapStatus{core.CapGen: ok(812), core.CapJudge: ok(143)}
			case svc.Name == "Anthropic":
				m.services[i].Status = map[string]core.CapStatus{core.CapGen: ok(812)}
			}
		}
		// A signed-in subscription, as the design lists one; adding one for real needs a browser.
		m.services = append(m.services, core.ModelService{ID: 99, Name: "ChatGPT", Provider: core.ProviderChatGPT, Email: "you@example.com", BaseURL: "https://api.openai.com/v1",
			Kind: ai.KindResponses, Models: []string{"gpt-5.2"}, Status: map[string]core.CapStatus{core.CapGen: ok(640)}})
	}
	t.Run("Settings", func(t *testing.T) {
		s := shotMail(t, noAuto)
		modelsSetup(s, "normal")
		s.openSettings(accountRoute(s.you.ID))
		s.see("签名")
		shot(s.harness, "SettingsAccounts")
		// The whole account page, as SettingsAccountsReauth draws it (邮箱行为 below the fold).
		for _, n := range []string{"Drafts", "Junk Email"} {
			if err := s.env.IMAP(s.work.ID).User.Create(n, nil); err != nil {
				t.Fatal(err)
			}
		}
		s.syncNow(s.work)
		name, alias := "[你的名字]", "工作"
		if _, err := s.env.Svc.UpdateAccount(context.Background(), s.work.ID, core.AccountPatch{Name: &name, Alias: &alias}); err != nil {
			t.Fatal(err)
		}
		s.env.IMAP(s.work.ID).SetPassword("expired")
		_ = s.env.Svc.SyncNow(context.Background(), s.work.ID) // rejected: the account needs signing in again
		// The design's account is Outlook, signed in with OAuth, on Microsoft's
		// servers: the local test servers can't be those, so the page is shown
		// the account as the design has it.
		fetch := s.app.fetch
		s.app.fetch = func(ctx context.Context) (core.Settings, []core.Account, error) {
			st, accs, err := fetch(ctx)
			for i, acc := range accs {
				if acc.ID == s.work.ID {
					acc.Provider, acc.AuthType, acc.StatusDetail = "outlook", "oauth", ""
					acc.IMAPHost, acc.IMAPPort, acc.IMAPSecurity = "outlook.office365.com", 993, core.SecurityTLS
					acc.SMTPHost, acc.SMTPPort, acc.SMTPSecurity = "smtp.office365.com", 587, core.SecurityStartTLS
					accs[i] = acc
				}
			}
			return st, accs, err
		}
		s.resize(1440, 2010)
		s.openSettings(accountRoute(s.work.ID))
		s.see("Junk Email")
		s.until("the reauth banner", func() bool {
			s.advance(refreshEvery) // the throttled refresh reloads the accounts
			return s.tt.HasText("需要重新登录")
		})
		s.advance(3 * time.Hour) // the design's 3 小时前起停止同步
		shot(s.harness, "SettingsAccountsReauth")
		s.resize(1440, 900)
		s.openSettings("general")
		s.see("界面密度")
		shot(s.harness, "SettingsGeneral")
		s.openSettings("models")
		s.see("OpenAI")
		modelsStatus(s, "normal")
		shot(s.harness, "SettingsModels")
		s.post(func() { // a local judge plugin's models: one downloading, one downloaded, one too large here
			m := s.app.pages[PageSettings].(*settingsPage).models
			judge := func(id, name string) plugins.ProviderModel {
				return plugins.ProviderModel{ID: id, Name: name, Transport: "plugin", Operation: "judge", Assets: map[string]string{"model": id}}
			}
			m.plugins = append(m.plugins, core.PluginModels{Plugin: "startlux",
				Models: []plugins.ProviderModel{judge("4b", "StartLux Decision 4B Q4"), func() plugins.ProviderModel {
					m := judge("2b", "StartLux Decision 2B Q8")
					m.Description = plugins.Text{Default: "Slow on this computer; best for labeling new mail only", ByLang: map[string]string{"zh-CN": "在这台电脑上较慢，建议只给新邮件打标签"}}
					return m
				}(), judge("9b", "StartLux Decision 9B")},
				Files: map[string]core.ModelStatus{
					"4b": {State: core.ModelNotDownloaded, TotalBytes: 2_710_000_000},
					"2b": {State: core.ModelDownloaded, TotalBytes: 2_010_000_000, DownloadedBytes: 2_010_000_000},
					"9b": {State: core.ModelErrorState, TotalBytes: 9_000_000_000, Error: &core.ModelError{Code: "insufficient_memory"}},
				}})
			s.app.settings.JudgeService = "plugin/startlux/2b" // a long name in the judge's select
			m.downloads = map[string]*modelDownload{"plugin/startlux/4b": {cancel: func() {},
				status: core.ModelStatus{State: core.ModelDownloading, DownloadedBytes: 1_220_000_000, TotalBytes: 2_710_000_000}}}
		})
		s.resize(1440, 1240) // the section is below the fold
		s.see("本地模型")
		shot(s.harness, "SettingsModelsLocal")
		s.resize(1440, 900)
		s.post(func() {
			m := s.app.pages[PageSettings].(*settingsPage).models
			m.plugins, m.downloads = m.plugins[:len(m.plugins)-1], nil
		})
		s.click("添加服务")
		s.see("Continue with ChatGPT")
		shot(s.harness, "SettingsModelsSheetSubscription")
		s.click("API 密钥")
		s.see("其他服务")
		s.see("https://api.openai.com/v1")
		shot(s.harness, "SettingsModelsSheet")
	})
	// The dialog's states, as the design's preset() makes them: the form is
	// set as the user would have left it; nothing reaches a real service.
	dlg := func(state string) func(m *modelsView) {
		ready := func(m *modelsView) *modelForm {
			m.add()
			m.group, m.form = 1, newForm(core.ProviderOpenAI)
			f := m.form
			f.key, f.offered, f.models, f.fetch, f.fetched = strings.Repeat("k", 20), []string{"[MODEL A]", "[MODEL B]", "[MODEL C]"}, []string{"[MODEL A]", "[MODEL B]"}, "ok", 3
			return f
		}
		tested := func(f *modelForm, gen, judge capTest) { f.edited(); f.gen, f.judge = gen, judge }
		ok := func(ms int) capTest {
			return capResult(ai.KindResponses, core.CapStatus{State: "ok", LatencyMs: ms}, nil)
		}
		edit := func(m *modelsView) *modelForm { m.openEdit(m.services[0]); return m.form }
		return map[string]func(m *modelsView){
			"SettingsModelsAddReady":   func(m *modelsView) { tested(ready(m), ok(812), ok(143)) },
			"SettingsModelsAddTesting": func(m *modelsView) { tested(ready(m), capTest{state: "testing"}, capTest{state: "testing"}) },
			"SettingsModelsAddFailed": func(m *modelsView) {
				tested(ready(m), ok(812), capResult(string(ai.FormatDecisions), core.CapStatus{State: "fail", Code: "forbidden", HTTPStatus: 403}, ai.ErrModelForbidden))
			},
			"SettingsModelsAddFetchFailed": func(m *modelsView) {
				m.addLocal()
				f := m.form
				f.name, f.baseURL, f.fetch, f.fetchErr, f.offered, f.models = "公司网关", "https://llm.example.com/v1", "fail", "404", []string{"[MODEL X]"}, []string{"[MODEL X]"}
			},
			"SettingsModelsAddOther": func(m *modelsView) {
				f := newForm(core.ProviderOther)
				m.form, m.open = f, true
				f.pickGateway("OpenRouter", "https://openrouter.ai/api/v1")
				f.key, f.kind, f.fetch, f.fetched = strings.Repeat("k", 18), ai.KindAnthropic, "ok", 3
				f.offered, f.models = []string{"[VENDOR/MODEL A]", "[VENDOR/MODEL B]", "[VENDOR/MODEL C]"}, []string{"[VENDOR/MODEL A]", "[VENDOR/MODEL B]"}
				f.judgeOn, f.judgeFormat, f.judgeModel = true, string(ai.FormatSystemOne), "jev-latest"
			},
			"SettingsModelsAddTypeSafe": func(m *modelsView) {
				m.form, m.open = newForm(core.ProviderTypeSafe), true
				m.form.key, m.form.judgeModels = strings.Repeat("k", 15), []string{"jev-latest", "jev-preview", "jev-1.13.0"}
			},
			"SettingsModelsAddNoCap":           func(m *modelsView) { f := ready(m); f.genOn, f.judgeOn = false, false },
			"SettingsModelsEdit":               func(m *modelsView) { edit(m) },
			"SettingsModelsEditDisableDefault": func(m *modelsView) { edit(m).genOn = false },
			"SettingsModelsEditDelete":         func(m *modelsView) { edit(m).confirmDel = true },
		}[state]
	}
	for _, name := range []string{"SettingsModelsAddReady", "SettingsModelsAddTesting", "SettingsModelsAddFailed", "SettingsModelsAddFetchFailed", "SettingsModelsAddOther",
		"SettingsModelsAddTypeSafe", "SettingsModelsAddNoCap", "SettingsModelsEdit", "SettingsModelsEditDisableDefault", "SettingsModelsEditDelete"} {
		t.Run(name, func(t *testing.T) {
			s := shotMail(t, noAuto)
			modelsSetup(s, "normal")
			s.openSettings("models")
			s.see("Anthropic")
			modelsStatus(s, "normal")
			m := s.app.pages[PageSettings].(*settingsPage).models
			m.checkService = func(context.Context, int64, core.ModelServiceInput) (core.ServiceCheck, error) {
				return core.ServiceCheck{Models: []string{"[MODEL A]", "[MODEL B]", "[MODEL C]"}, ListSupported: true, JudgeModels: []string{"gpt-6-luna"}}, nil
			}
			dlg(name)(m)
			s.drain()
			shot(s.harness, name)
		})
	}
	for _, v := range []struct{ view, name string }{{"broken", "SettingsModelsBroken"}, {"gen-only", "SettingsModelsGenOnly"}, {"empty", "SettingsModelsEmpty"}} {
		t.Run(v.name, func(t *testing.T) {
			s := shotMail(t, noAuto)
			modelsSetup(s, v.view)
			s.openSettings("models")
			s.see("模型服务")
			if v.view != "empty" {
				s.see("Anthropic")
				modelsStatus(s, v.view)
			} else {
				s.see("还没有接入模型服务")
			}
			shot(s.harness, v.name)
		})
	}
	t.Run("SettingsExtensions", func(t *testing.T) {
		s := shotMail(t, noAuto)
		shotInstall(t, s.harness, "deepl-translate", `{"name":"deepl-translate","version":"0.1.0","description":"用 DeepL 翻译邮件，不经过大模型","main":"index.ts",
			"permissions":{"mail":["read"],"net":["api-free.deepl.com","api.deepl.com"]},
			"config":[{"key":"apiKey","type":"secret","label":"DeepL API Key"},
				{"key":"plan","type":"select","label":"套餐","options":[{"value":"free","label":"Free"},{"value":"pro","label":"Pro"}],"default":"free"}]}`,
			`export default function (_ctx: any) {}`, false)
		shotPlugin(t, s.harness, "flight-cards", "NW", `{ title: "航班", fields: [] }`)
		shotPlugin(t, s.harness, "attachment-check", "-", `{ title: "附件", fields: [] }`)
		s.openSettings("extensions")
		s.click("插件 · 4")
		s.see("deepl-translate")
		s.see("attachment-check")
		shot(s.harness, "SettingsExtensions")

		p := s.aiTab(aiFeatures)
		s.see("阅读语言")
		shot(s.harness, "SettingsAIFeatures")
		p.aiTab = aiMCP
		s.see("添加服务器")
		shot(s.harness, "SettingsAIMCP")
		p.aiTab = aiApps
		s.see("启用本机 API")
		shot(s.harness, "SettingsAIApps")
		e, _ := s.app.mcpEnv() // Claude connected, Codex there but not
		if err := os.MkdirAll(filepath.Join(e.Home, ".codex"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(e.Home, "Library", "Application Support", "Claude"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := e.Connect("claude", s.app.mcpServer()); err != nil {
			t.Fatal(err)
		}
		s.post(func() { // serving: the rows to connect a client show
			s.app.settings.LocalAPI = true
			s.app.localAPIPath = "/Users/you/Library/Application Support/Godwit/godwit.sock"
		})
		s.openSettings("general") // back: the AI apps are read again
		s.openSettings("models")
		s.see("Codex")
		shot(s.harness, "SettingsAIAppsOn")
		p.aiTab = aiModels
		s.openSettings("privacy")
		for i, name := range []string{"SettingsPrivacy", "SettingsPrivacyImages", "SettingsPrivacyBlocked"} {
			p.privacyTab = i
			s.tt.Frame()
			shot(s.harness, name)
		}
		p.privacyTab = 0
		s.openSettings("masklog")
		s.post(func() { // the design's sample: two requests, one blocked
			at := s.clock.Now().Add(-time.Hour)
			p.maskLog = []core.MaskRecord{
				{At: at, Request: 2, Feature: "skill", Kind: "EMAIL", Category: "emails", First: "h", Len: 16, Action: core.MaskMask, Source: core.MaskSourceRule},
				{At: at, Request: 2, Feature: "skill", Kind: "ID_CARD", Category: "ids", First: "3", Len: 18, Action: core.MaskBlock, Source: core.MaskSourceRule},
				{At: at.Add(-time.Hour), Request: 1, Feature: "wit", Kind: "NAME", Category: "names", Len: 2, Action: core.MaskMask, Source: core.MaskSourceRecognizer},
				{At: at.Add(-time.Hour), Request: 1, Feature: "wit", Kind: "PHONE", Category: "phones", First: "1", Len: 11, Action: core.MaskWarn, Source: core.MaskSourceRule},
			}
			p.logFilter.open = map[string]bool{"2": true}
		})
		s.see("被拦截的请求")
		shot(s.harness, "SettingsMaskLog")
	})
	t.Run("Toasts", func(t *testing.T) {
		s := shotMail(t, noQuick)
		openLena(s)
		s.app.toastAs(kit.ToastSuccess, "邮件已发送")
		s.app.toastAs(kit.ToastWarning, "work@company.example 没有归档文件夹，已跳过")
		s.app.toast("链接已复制")
		s.see("链接已复制")
		s.tt.Advance(2 * theme.MotionBase)
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
		h.app.oauth = &fakeOAuth{flows: map[string]chan flowResult{}}
		h.click("Outlook / Hotmail")
		h.see("已在浏览器中打开登录页")
		shot(h, "OnboardingOAuth")
	})
}
