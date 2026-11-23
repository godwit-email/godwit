package app

import (
	"bytes"
	"context"
	"fmt"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/emersion/go-imap/v2"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// shotEnv is a mail harness shaped like the design's sample data: two accounts,
// today = Fri 10/09 10:00 (the fake clock), a fake model service.
type shotEnv struct {
	*harness
	you, work core.Account
	llm       *coretest.LLM
	design    bool             // shotDesignMail's: threads, labels and gists as the design draws them
	gists     map[int64]string // a folded message's AI gist, by message
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
func shotMail(t *testing.T, set func(*core.Settings)) *shotEnv { return shotMailWith(t, set, false) }

// shotDesignMail is shotMail with the reader's states the design draws on its
// main artboards: the proposal a thread of three, a long thread under a
// history card, labels on the rows, the folded messages' gists.
func shotDesignMail(t *testing.T, set func(*core.Settings)) *shotEnv {
	return shotMailWith(t, set, true)
}

func shotMailWith(t *testing.T, set func(*core.Settings), design bool) *shotEnv {
	t.Helper()
	s := &shotEnv{design: design}
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
		st.SummaryMode, st.QuickReplies = core.SummaryOpen, true
		if design {
			st.AutoLabel = false // the labels are put by hand: the classifier would take the scripted model turns
		}
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
	lena := []coretest.MsgOption{coretest.Attachment("Q4-proposal.pdf", "application/pdf", make([]byte, 218*1024*3/4))}
	if design {
		lena = append(lena, coretest.InReplyTo("alex@x"))
		h.putDesignThreads(you, m)
	}
	h.putSeen(you, "INBOX", m("Lena Fischer <lena@partner.example>", "Q4 partnership proposal",
		"Hi,\n\nFollowing up on our call last week, I put together a draft proposal for the Q4 partnership. The main points are a shared launch in November and a joint webinar.\n\nCould you take a look and let me know by Friday whether the timeline works on your side?\n\nBest, Lena",
		"lena@x", day(10, 8, 21, 3), lena...))
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
	if design {
		s.labelDesignMail()
	}
	return s
}

// putDesignThreads puts the proposal's two earlier messages and the long
// thread, before the proposal that answers them (m builds a message to you).
func (h *harness) putDesignThreads(you core.Account, m func(from, subj, body, id string, when time.Time, o ...coretest.MsgOption) []byte) {
	lena, alex := "Lena Fischer <lena@partner.example>", "Alex Kim <alex@partner.example>"
	h.putSeen(you, "INBOX", m(lena, "Q4 partnership proposal", "提议 10 月底联合发布，附初版方案。", "lena0@x", day(10, 2, 10, 0)))
	h.putSeen(you, "INBOX", m(alex, "Re: Q4 partnership proposal", "10 月排不开，建议改到 11 月。", "alex@x", day(10, 3, 15, 0), coretest.InReplyTo("lena0@x")))
	people := []string{"Mark Chen <mark@partner.example>", lena, alex, "Mark Chen <mark@partner.example>", lena, alex, "Mark Chen <mark@partner.example>", "Sofia Rossi <sofia@partner.example>"}
	notes := []string{"法务审完了第一版协议", "研讨会的嘉宾名单有变动", "预算表更新了，见附件", "新闻稿第二稿，请批注", "落地页文案定稿", "发布日期待定", "合同条款已确认", "汇总一下目前的结论"}
	parent := ""
	for i, from := range people {
		id := fmt.Sprintf("long%d@x", i)
		body := notes[i] + "。"
		if i == len(people)-1 {
			body = "Hi all,\n\n汇总一下目前的结论，按模块列在下面。每一块后面写了负责人，请各自确认，有出入的直接回复这封。\n\n发布日期：定在 11 月的第二周。具体哪一天，等场地确认之后再定，两边都留了机动时间。\n\n下一步：请各位在周五前回复确认自己负责的部分。"
		}
		opts := []coretest.MsgOption{}
		if parent != "" {
			opts = append(opts, coretest.InReplyTo(parent))
		}
		h.putSeen(you, "INBOX", m(from, "11 月联合发布：计划与分工", body, id, day(10, 1+i, 18, 40), opts...))
		parent = id
	}
}

// idOf is the inbox message whose text starts with phrase.
func (s *shotEnv) idOf(phrase string) int64 {
	h := s.harness
	h.t.Helper()
	inbox, err := h.env.Svc.ListMessages(context.Background(), core.View{Unified: "inbox"}, "", 200)
	if err != nil {
		h.t.Fatal(err)
	}
	i := slices.IndexFunc(inbox.Items, func(m core.MessageSummary) bool { return strings.Contains(m.Snippet, phrase) })
	if i < 0 {
		h.t.Fatalf("no message with %q among %d", phrase, len(inbox.Items))
	}
	return inbox.Items[i].ID
}

// labelDesignMail puts the design's labels on the rows, and the gists on the
// proposal's folded messages, which the reader asks the model for otherwise.
func (s *shotEnv) labelDesignMail() {
	h := s.harness
	ctx := context.Background()
	ids := map[string]int64{} // by a phrase of the message's start
	for _, phrase := range []string{"验证码 482913", "现已开放在线值机", "提议 10 月底", "10 月排不开", "Following up on our call", "汇总一下目前", "报销单已审批通过", "新闻稿第二稿", "落地页文案定稿", "发布日期待定", "合同条款已确认"} {
		ids[phrase] = s.idOf(phrase)
	}
	labels, err := h.env.Svc.ListLabels(ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	named := map[string]int64{}
	for _, l := range labels {
		named[l.Slug] = l.ID
	}
	for name, in := range map[string]core.LabelInput{
		"q4":      {Name: "Q4 项目", Color: "violet", Icon: "briefcase-4"},
		"expense": {Name: "报销", Color: "pink", Icon: "money-dollar-circle"},
	} {
		l, err := h.env.Svc.CreateLabel(ctx, in)
		if err != nil {
			h.t.Fatal(err)
		}
		named[name] = l.ID
	}
	for _, put := range []struct {
		phrase string
		labels []string
	}{
		{"验证码 482913", []string{"otp"}},
		{"现已开放在线值机", []string{"travel", "calendar", "order"}}, // not notice: core gives a notice no quick replies
		{"Following up on our call", []string{"q4"}},
		{"汇总一下目前", []string{"q4"}},
		{"报销单已审批通过", []string{"expense", "notice"}},
	} {
		for _, slug := range put.labels {
			if err := h.env.Svc.SetLabel(ctx, []int64{ids[put.phrase]}, named[slug], true); err != nil {
				h.t.Fatal(err)
			}
		}
	}
	s.gists = map[int64]string{
		ids["提议 10 月底"]: "提议 10 月底联合发布，附初版方案",
		ids["10 月排不开"]:  "10 月排不开，建议改到 11 月",
		ids["新闻稿第二稿"]:   "新闻稿第二稿，请批注",
		ids["落地页文案定稿"]:  "落地页文案定稿",
		ids["发布日期待定"]:   "发布日期待定",
		ids["合同条款已确认"]:  "合同条款已确认",
	}
	s.app.mail.reader.gistFn = func(_ context.Context, id int64) (string, error) { return s.gists[id], nil }
	h.waitMail("the labels", func() bool { return h.tt.HasText("Q4 项目") && h.tt.HasText("航班与出行") })
}

// addQQ adds the default account as a QQ mailbox on fake servers, so its
// settings show QQ's logo and meta as the design does.
func addQQ(t *testing.T, e *coretest.Env, email string) core.Account {
	t.Helper()
	ih, ip, sh, sp, user, pass := e.StartServers(t)
	acc, err := e.Svc.AddAccount(context.Background(), core.AccountInput{Email: email, Name: "you", Username: user, Password: pass, IsDefault: true,
		Receiving: core.Server{Host: ih, Port: ip, Security: core.SecurityNone}, Sending: core.Server{Host: sh, Port: sp, Security: core.SecurityNone}, Provider: "qq"})
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
		writeSkill(t, s.env, "brief") // the design's 更多 Skill
		todo := "需要你在周五前确认"
		if s.design { // the thread's digest, each step citing its message
			cite := func(phrase string) string { return fmt.Sprintf(` <mail id="%d"/>`, s.idOf(phrase)) }
			todo = "周五前回复，确认 11 月的时间是否可行"
			s.llm.Script(coretest.Turn{Text: []string{"- 提议 10 月底联合发布" + cite("提议 10 月底") + "\n- 你回复 10 月排不开，希望改到 11 月" + cite("10 月排不开") +
				"\n- 发来修订提案：11 月联合发布，加一场联合研讨会（附件 PDF）" + cite("Following up on our call") + "\nTODO: " + todo + "\n"}})
		} else {
			s.llm.Script(coretest.Turn{Text: []string{"- 对方发来 Q4 合作提案草稿（附件 PDF）。\n- 要点：11 月联合发布、一场联合网络研讨会。\n- 需要你在周五前确认时间安排是否可行。\n"}})
		}
		s.click("Q4 partnership proposal")
		s.see(todo)
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
	noAuto := func(st *core.Settings) { st.QuickReplies, st.SummaryMode = false, core.SummaryOff }

	t.Run("SettingsRulesForward", func(t *testing.T) {
		s := shotMail(t, noAuto)
		s.openSettings("rules")
		s.click("新建规则")
		s.click("值")
		s.typeText("billing@")
		s.click("动作 1")
		s.click("转发给")
		s.click("收件人邮箱")
		s.typeText("accountant@company.example")
		s.click("保存")
		s.see("第一次发往这个地址")
		shot(s.harness, "SettingsRulesForward")
	})
	t.Run("Scheduled", func(t *testing.T) {
		s := shotMail(t, noAuto)
		ctx := context.Background()
		d, err := s.env.Svc.NewDraft(ctx, s.you.ID, "new", 0)
		if err != nil {
			t.Fatal(err)
		}
		d.To, d.Subject, d.HTML = []string{"Lena Fischer <lena@partner.example>"}, "Re: Q4 partnership proposal", "<p>November works.</p>"
		if d, err = s.env.Svc.SaveDraft(ctx, d); err != nil {
			t.Fatal(err)
		}
		if err := s.env.Svc.SendLater(ctx, d.ID, d.Revision, day(10, 12, 8, 0), ""); err != nil {
			t.Fatal(err)
		}
		s.waitMail("listed under 定时邮件", func() bool { return len(s.app.mail.scheduled) == 1 })
		s.click("定时邮件")
		s.see("Re: Q4 partnership proposal")
		shot(s.harness, "Scheduled")
	})

	t.Run("Main", func(t *testing.T) {
		s := shotDesignMail(t, noQuick)
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
	t.Run("MainDefault", func(t *testing.T) { // the design's default window
		s := shotDesignMail(t, noQuick)
		s.resize(1280, 820)
		openLena(s)
		shot(s.harness, "MainDefault")
	})
	t.Run("MainCompact", func(t *testing.T) { // the least window, with the sidebar folded (surface mail-compact)
		s := shotDesignMail(t, noQuick)
		s.resize(960, 600)
		s.app.sidebarHidden = true
		openLena(s)
		shot(s.harness, "MainCompact")
	})
	t.Run("MainLongThread", func(t *testing.T) { // a long thread under its history card, no summary (129:303)
		s := shotDesignMail(t, noQuick)
		todo := "周五前确认自己负责的部分"
		s.llm.Script(coretest.Turn{Text: []string{"- 目前的结论已按模块汇总\nTODO: " + todo + "\n"}})
		s.click("11 月联合发布：计划与分工")
		s.see(todo)
		s.see("3 封较早的邮件")
		s.post(func() { s.app.mail.reader.toggleSummary() }) // the design shows the thread without it
		s.until("the summary hidden", func() bool { s.tt.Frame(); return !s.tt.HasText(todo) })
		shot(s.harness, "MainLongThread")
		// Scrolled past the header: slim sticky bar pinned at the top (666:2751).
		r := s.app.mail.reader
		last, _ := r.last()
		s.seeBody(last.ID)
		s.post(func() {
			if b := r.bodies[last.ID]; b != nil && b.view != nil {
				s.app.BodyHeight(b.view, last.ID, 1200)
			}
		})
		s.tt.Frame()
		s.tt.Advance(2 * theme.MotionBase)
		s.post(func() { r.scroll.Y = r.headerH + 220 })
		s.until("sticky header collapsed", func() bool { s.tt.Frame(); return r.barTop <= 45 })
		shot(s.harness, "MainScrolled")
	})
	t.Run("MainAttachments", func(t *testing.T) { // a message with several attachments, one chosen (215:2091)
		s := shotDesignMail(t, noQuick)
		file := func(name, kind string, kb int) coretest.MsgOption {
			return coretest.Attachment(name, kind, make([]byte, kb*1024*3/4))
		}
		s.putSeen(s.you, "INBOX", coretest.Msg("Lena Fischer <lena@partner.example>", s.you.Email, "Q4 revised proposal",
			"Hi,\n\nThanks for the quick reply. November works on our side. I have attached a revised proposal and the webinar dates.",
			coretest.MessageID("rev@x"), coretest.Date(day(10, 8, 22, 0)),
			file("proposal-revised.pdf", "application/pdf", 218), file("webinar-dates.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", 86)))
		s.syncNow(s.you)
		s.waitMail("the revised proposal", func() bool { return s.tt.HasText("Q4 revised proposal") })
		s.click("Q4 revised proposal")
		s.see("proposal-revised.pdf")
		s.click(i18n.Tf("reader.attachments.select_item", "proposal-revised.pdf"))
		s.see(i18n.Tf("reader.attachments.selected", 1, 2))
		shot(s.harness, "MainAttachments")
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
	t.Run("MainProgramDraft", func(t *testing.T) { // a reply godwit mcp wrote, named in its thread's reply bar (ADR 0043; no artboard)
		s := shotMail(t, noAuto)
		ctx := context.Background()
		page, err := s.env.Svc.Search(ctx, s.you.ID, "Q4 partnership proposal", 1)
		if err != nil || len(page) == 0 {
			t.Fatalf("search %v, %v", page, err)
		}
		mcp := core.WithExternalSource(ctx, core.AuditMCP)
		d, err := s.env.Svc.NewDraft(mcp, s.you.ID, "reply", page[0].ID)
		if err == nil {
			d.HTML = "<p>Hi Lena,</p><p>Thanks for the proposal. November works for us, happy to move ahead.</p>"
			d, err = s.env.Svc.SaveDraft(mcp, d)
		}
		if err != nil {
			t.Fatal(err)
		}
		s.click("Q4 partnership proposal")
		s.see("程序写好了一封回复：Hi Lena,")
		shot(s.harness, "MainProgramDraft")
		s.shotDrafts()
		s.post(func() { s.app.mail.draftSel = d.ID })
		s.see("由 AI 工具、脚本、插件或 Wit 写入")
		shot(s.harness, "MainProgramDraftFolder")
		// open in a compose window too: both notices, as wide as each other
		s.plat.setComposeOpen(d.ID, true)
		s.post(func() { s.app.mail.setDrafts() })
		s.see("来自程序的草稿")
		shot(s.harness, "MainProgramDraftEditing")
	})
	t.Run("MainOlder", func(t *testing.T) { // the row while older mail loads (no artboard: compared by eye)
		s := shotDesignMail(t, noAuto)
		f := s.folderOf(s.you, "inbox")
		s.post(func() { s.app.navigate(Route{Page: PageMail, View: core.View{FolderID: f.ID}}) })
		s.until("the folder", func() bool { return s.app.mail.view.FolderID == f.ID && !s.app.mail.list.loading })
		s.post(func() { s.app.mail.stopOlder(); s.app.mail.older.read.loading = true })
		shot(s.harness, "MainOlder")
	})
	t.Run("Palette", func(t *testing.T) {
		s := shotMail(t, noAuto)
		s.tt.Key(ui.Cmd, ui.KeyK)
		s.until("the palette", func() bool { return paletteShown(s.harness) })
		s.typeText("/")
		s.until("the filter list", func() bool { return filtersShown(s.harness) })
		shot(s.harness, "PaletteFilters")
		s.tt.Key(0, ui.KeyEscape) // closes the list, not the palette
		s.until("the list closed", func() bool { return !filtersShown(s.harness) && paletteShown(s.harness) })
		s.tt.Key(ui.Cmd, ui.KeyA)
		s.typeText("proposal")
		s.advance(debounce)
		s.until("the results", func() bool { return s.app.palette.res.Mail.Total > 0 })
		shot(s.harness, "PaletteResults")
		s.tt.Key(ui.Alt, ui.KeyEnter)
		s.until("the actions", func() bool { return s.app.palette.m.open })
		shot(s.harness, "PaletteMenu")
		s.post(func() { s.app.palette.m.open, s.app.palette.open = false, false })
		s.post(s.app.openPalette)
		s.until("the palette again", func() bool { return paletteShown(s.harness) })
		s.tt.Key(0, ui.KeyTab)
		s.until("the question box", func() bool { return !paletteShown(s.harness) && s.app.palette.open })
		shot(s.harness, "PaletteWit")
	})
	t.Run("MainSearch", func(t *testing.T) {
		s := shotMail(t, noAuto)
		search := func(q string) {
			s.post(func() { s.app.mail.searchFor(q, searchFilters{}) }) // the palette's 查看全部 opens the list's search with its query
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
		s := shotDesignMail(t, noAuto)
		writeSkill(t, s.env, "brief")
		// the built-in sign-in codes plugin draws the card
		s.click("[Code Host] 你的登录验证码")
		s.see("复制")
		shot(s.harness, "MainOTP")
	})
	t.Run("MainFlight", func(t *testing.T) {
		s := shotDesignMail(t, noAuto)
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
	// scrolled shoots a long settings page as the design's frames do: where its state is (the section titled at), then its header, then its last item.
	scrolled := func(s *shotEnv, at, name string) {
		scroll := func(dy float32) { s.tt.Scroll(900, 400, 0, dy); s.tt.Frame() }
		scroll(-100000)
		r, ok := s.tt.Find(at)
		if !ok {
			t.Fatalf("no %q on the page; texts %q", at, s.tt.Texts())
		}
		scroll(r.Y - 80)
		shot(s.harness, name)
		scroll(-100000)
		shot(s.harness, name+"Top")
		scroll(100000)
		shot(s.harness, name+"End")
		scroll(-100000)
	}
	t.Run("Settings", func(t *testing.T) {
		s := shotMail(t, noAuto)
		modelsSetup(s, "normal")
		// The design's account has the 个人签名 signature as its default and a Wit instruction.
		sig, err := s.env.Svc.CreateSignature(context.Background(), "个人签名")
		if err != nil {
			t.Fatal(err)
		}
		html, ai := "you\nyou@example.com", "姓名：you\n提示：回复同事时简短直接，给财务部的邮件附上报销单号。"
		if _, err := s.env.Svc.UpdateSignature(context.Background(), sig.ID, core.SignaturePatch{HTML: &html}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.env.Svc.UpdateAccount(context.Background(), s.you.ID, core.AccountPatch{SignatureID: &sig.ID, AIInstructions: &ai}); err != nil {
			t.Fatal(err)
		}
		s.post(s.app.load)
		s.openSettings(accountRoute(s.you.ID))
		s.see("签名")
		s.see("预览")
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
					acc.Receiving.Host, acc.Receiving.Port, acc.Receiving.Security = "outlook.office365.com", 993, core.SecurityTLS
					acc.Sending.Host, acc.Sending.Port, acc.Sending.Security = "smtp.office365.com", 587, core.SecurityStartTLS
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
		// The design's sending-refused account: QQ, receive-only.
		outlook := s.app.fetch
		s.app.fetch = func(ctx context.Context) (core.Settings, []core.Account, error) {
			st, accs, err := outlook(ctx)
			for i, acc := range accs {
				if acc.ID == s.you.ID {
					acc.ReceiveOnly = true
					acc.Receiving.Host, acc.Receiving.Port, acc.Receiving.Security = "imap.qq.com", 993, core.SecurityTLS
					acc.Sending.Host, acc.Sending.Port, acc.Sending.Security = "smtp.qq.com", 465, core.SecurityTLS
					accs[i] = acc
				}
			}
			return st, accs, err
		}
		s.openSettings(accountRoute(s.you.ID))
		s.post(s.app.load) // no event comes: the page reads the account again
		s.until("the receive-only rows", func() bool { return s.tt.HasText("重新检查发信") })
		scrolled(s, "登录与服务器", "SettingsAccountsReceiveOnly")
		// Fastmail over JMAP: the one server row is the session's address.
		s.app.fetch = func(ctx context.Context) (core.Settings, []core.Account, error) {
			st, accs, err := outlook(ctx)
			for i, acc := range accs {
				if acc.ID == s.you.ID {
					acc.Provider, acc.ReceiveOnly = "fastmail", false
					acc.Receiving = core.Server{Protocol: auth.JMAP, Host: "api.fastmail.com", Port: 443, Security: core.SecurityTLS}
					acc.Sending = acc.Receiving
					accs[i] = acc
				}
			}
			return st, accs, err
		}
		s.post(s.app.load)
		s.until("the JMAP rows", func() bool { return s.tt.HasText("https://api.fastmail.com:443/.well-known/jmap") })
		// The design's Fastmail account has its address book linked.
		s.post(func() {
			s.app.loads.Stop()
			s.app.addressBooks = []core.AddressBook{{
				ID: 7, Preset: core.PresetFastmail, Name: "Fastmail", URL: "https://carddav.fastmail.com", AuthType: "password", Username: "me@fastmail.com",
				Status: "ok", LastSyncedAt: s.clock.Now().Add(-5 * time.Minute), Account: s.you.ID,
				Collections: []core.AddressBookCollection{{ID: 1, AddressBookID: 7, Name: "Contacts", Enabled: true, CardCount: 212}},
			}}
		})
		s.tt.Frame()
		scrolled(s, "登录与服务器", "SettingsAccountsJMAP")
		s.app.fetch = outlook
		s.openSettings("labels")
		s.see("回扫已有邮件")
		s.post(func() { // a rescan under way, as the design has it
			v := s.app.pages[PageSettings].(*settingsPage).labels
			v.started, v.total, v.pending = true, 3560, 3036
		})
		s.tt.Frame()
		s.see("正在回扫")
		scrolled(s, "回扫已有邮件", "SettingsLabelsRescan")
		s.openSettings("general")
		s.see("界面密度")
		shot(s.harness, "SettingsGeneral")
		s.openSettings("rules")
		s.click("新建规则")
		s.click("添加条件")
		shot(s.harness, "SettingsRulesEditor")
		s.click("添加条件组")
		shot(s.harness, "SettingsRulesOr")
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
		s.see("本地模型")
		scrolled(s, "本地模型", "SettingsModelsLocal")
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
			f.key, f.offered, f.models, f.list = strings.Repeat("k", 20), []string{"[MODEL A]", "[MODEL B]", "[MODEL C]"}, []string{"[MODEL A]", "[MODEL B]"}, listing{state: "ok", n: 3}
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
				f.name, f.baseURL, f.list, f.offered, f.models = "公司网关", "https://llm.example.com/v1", listing{state: "fail", err: "404"}, []string{"[MODEL X]"}, []string{"[MODEL X]"}
			},
			"SettingsModelsAddOther": func(m *modelsView) {
				f := newForm(core.ProviderOther)
				m.form, m.open, m.group = f, true, 1
				f.pickGateway("OpenRouter", "https://openrouter.ai/api/v1")
				f.key, f.kind, f.list = strings.Repeat("k", 18), ai.KindAnthropic, listing{state: "ok", n: 3}
				f.offered, f.models = []string{"[VENDOR/MODEL A]", "[VENDOR/MODEL B]", "[VENDOR/MODEL C]"}, []string{"[VENDOR/MODEL A]", "[VENDOR/MODEL B]"}
				f.judgeOn, f.judgeFormat, f.judgeModel = true, string(ai.FormatSystemOne), "jev-latest"
			},
			"SettingsModelsAddTypeSafe": func(m *modelsView) {
				m.form, m.open, m.group = newForm(core.ProviderTypeSafe), true, 1
				m.form.key, m.form.judgeModels, m.form.judgeList = strings.Repeat("k", 15), []string{"jev-latest", "jev-preview", "jev-1.13.0"}, listing{state: "ok", n: 3}
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
				return core.ServiceCheck{Models: []string{"[MODEL A]", "[MODEL B]", "[MODEL C]"}, ListSupported: true}, nil
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
	t.Run("SettingsPluginDirectory", func(t *testing.T) {
		rs := coretest.NewRegistryServer(t)
		dlx := regPlugin("dlx", rs.NativeRelease(t, "dlx", "0.1.0", nil))
		dlx["title"], dlx["publisher"], dlx["category"], dlx["dataFlow"] = "DLX", "Godwit Community", "translate", "third-party"
		dlx["summary"] = "翻译邮件与选中文本，无需设置。文本会通过非官方接口发送到第三方翻译服务，可能被限流或失效。"
		lux := regPlugin("startlux", rs.NativeRelease(t, "startlux", "0.1.0", nil))
		lux["title"], lux["publisher"], lux["category"], lux["dataFlow"] = "StartLux", "Godwit", "models", "local"
		lux["summary"] = "本地判断模型：在本机给邮件打标签、回答是非题，邮件内容不发往任何地方。"
		zen := regPlugin("zen-free", rs.ScriptRelease("zen-free", "0.1.0", `{"net":["opencode.ai","models.opencode.ai","raw.githubusercontent.com"]}`, `export default function () {}`))
		zen["title"], zen["publisher"], zen["category"], zen["dataFlow"] = "Zen Free", "Godwit", "models", "third-party"
		zen["summary"] = "OpenCode Zen 免费模型。提示、上下文与工具会通过非官方接口发送到第三方服务。"
		zen["homepage"] = "https://github.com/godwit-email/plugins/tree/main/packages/zen-free"
		rs.Publish(t, coretest.Registry(regT0, dlx, lux, zen))
		rs.Src.Client = &http.Client{Transport: &rawGitHub{base: rs.Src.Client.Transport, files: map[string]string{
			"/godwit-email/plugins/main/packages/zen-free/README.zh-Hans.md": "# Zen Free for Godwit\n\n在 Godwit 中使用 OpenCode Zen 的免费模型，无需 API Key。\n\n## 使用\n\n1. 打开 Godwit「设置 → 扩展」，在插件目录中安装 Zen Free。\n2. 启用插件，允许它声明的网络权限。\n",
		}}}
		h := newHarnessWith(t, []coretest.Option{coretest.WithRegistry(rs.Src)}, func(e *coretest.Env) { e.AddAccount(t, "alice@example.com") })
		h.until("initial load", func() bool { return h.app.loaded })
		h.resize(1440, 900)
		h.openSettings("plugins")
		h.see("浏览插件目录")
		shot(h, "SettingsExtensionsPlugins")
		h.click("浏览插件目录")
		h.see("Zen Free")
		shot(h, "SettingsPluginDirectory")
		ext := h.app.pages[PageSettings].(*settingsPage).ext
		entries := ext.reg.Entries
		ext.installing, ext.installProgress = "dlx", core.PluginInstallProgress{Stage: "download", Done: 64, Total: 100}
		shot(h, "SettingsPluginDirectoryInstalling")
		ext.installing, ext.reg.Stale = "", "offline"
		shot(h, "SettingsPluginDirectoryStale")
		ext.reg.Entries = nil
		shot(h, "SettingsPluginDirectoryOffline")
		ext.reg.Stale = ""
		shot(h, "SettingsPluginDirectoryEmpty")
		ext.reg.Entries = entries
		h.click("安装 zen-free")
		h.see("以下说明来自插件的 GitHub 主页，不在审核范围内。")
		shot(h, "SettingsPluginInstallDialog")
	})
	t.Run("SettingsExtensions", func(t *testing.T) {
		s := shotMail(t, noAuto)
		shotInstall(t, s.harness, "deepl-translate", `{"name":"deepl-translate","version":"0.1.0","description":"用 DeepL 翻译邮件，不经过大模型","main":"index.ts",
			"permissions":{"mail":["read"],"net":["api-free.deepl.com","api.deepl.com"]},
			"config":[{"key":"apiKey","type":"secret","label":"DeepL API Key"},
				{"key":"plan","type":"select","label":"套餐","options":[{"value":"free","label":"Free"},{"value":"pro","label":"Pro"}],"default":"free"}]}`,
			`export default function (_ctx: any) {}`, false)
		shotPlugin(t, s.harness, "flight-cards", "NW", `{ title: "航班", fields: [] }`)
		shotPlugin(t, s.harness, "attachment-check", "-", `{ title: "附件", fields: [] }`)
		s.openSettings("plugins")
		s.see("deepl-translate")
		s.see("attachment-check")
		shot(s.harness, "SettingsExtensions")

		p := s.aiTab(aiFeatures)
		s.see("阅读语言")
		shot(s.harness, "SettingsAIFeatures")
		s.aiTab(aiMCP)
		s.see("添加服务器")
		shot(s.harness, "SettingsAIMCP")
		s.aiTab(aiApps)
		s.see("允许外部应用和脚本接入")
		shot(s.harness, "SettingsAIApps")
		e, _ := s.app.mcpEnv() // Claude connected, Codex, Antigravity and Grok there but not
		for _, dir := range []string{".codex", ".gemini/antigravity", ".grok"} {
			if err := os.MkdirAll(filepath.Join(e.Home, dir), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.MkdirAll(filepath.Join(e.Home, "Library", "Application Support", "Claude"), 0o755); err != nil {
			t.Fatal(err)
		}
		installClaudeExtension(t, e.Home, s.app.mcpServer())
		s.post(func() { // serving: the rows to connect a client show
			s.app.settings.LocalAPI = true
			s.plat.mu.Lock()
			s.plat.dataDir = "/Users/you/Library/Application Support/Godwit"
			s.plat.mu.Unlock()
		})
		s.openSettings("general") // back: the AI apps are read again
		s.openSettings("clients")
		s.see("ChatGPT (Codex)")
		shot(s.harness, "SettingsAIAppsOn")
		for i, name := range []string{"SettingsPrivacy", "SettingsPrivacyImages", "SettingsPrivacyBlocked"} {
			s.openSettings([]string{"masking", "images", "blocked"}[i])
			shot(s.harness, name)
		}
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
	t.Run("SettingsStates", func(t *testing.T) {
		page := func(s *shotEnv) *settingsPage { return s.app.pages[PageSettings].(*settingsPage) }
		ctx := context.Background()
		// shotNow shoots at once: the shot's wait for refreshes would read the page's lists again over what the fixture set on them.
		shotNow := func(h *harness, name string) {
			h.t.Helper()
			h.drain()
			h.tt.Frame()
			write(h, dir, name)
			if dark != "" {
				h.tt.SetDark(true)
				write(h, dark, name)
				h.tt.SetDark(false)
				h.tt.Frame()
			}
		}
		t.Run("SettingsGeneralProxy", func(t *testing.T) {
			s := shotMail(t, func(st *core.Settings) {
				noAuto(st)
				st.ProxyMode, st.ProxyURL, st.ProxyBypass = core.ProxyCustom, "socks5://127.0.0.1:1080", []string{"localhost", "127.0.0.1", "*.corp.example"}
			})
			s.openSettings("general")
			s.see("网络代理")
			for range 3 { // the page's height is only known as it is laid out
				s.tt.Frame()
				s.tt.Scroll(900, 400, 0, 100000)
			}
			s.tt.Frame()
			shot(s.harness, "SettingsGeneralProxy")
		})
		t.Run("SettingsLabelsEdit", func(t *testing.T) {
			s := shotDesignMail(t, noAuto)
			s.openSettings("labels")
			s.see("Q4 项目")
			p := page(s)
			s.post(func() {
				i := slices.IndexFunc(s.app.labels, func(l core.Label) bool { return l.Name == "Q4 项目" })
				l := s.app.labels[i]
				l.Description = "和 Q4 项目有关的排期、评审和交付，Lena Fischer 和 Mark Chen 常发"
				p.labels.open(l)
			})
			shot(s.harness, "SettingsLabelsEdit")
		})
		rulesPage := func(t *testing.T) (*shotEnv, []core.Rule) {
			s := shotMail(t, noAuto)
			var out []core.Rule
			for _, r := range []core.Rule{
				{Enabled: true, Background: true, Doc: core.RuleDoc{Name: "账单归档", Conditions: []core.RuleCondition{{Field: core.FieldSubject, Op: core.OpContains, Value: "invoice"}, {Field: core.FieldAttachments, Op: core.OpIs, Value: core.RuleYes}},
					Actions: []core.RuleAction{{Kind: core.ActionLabel, Label: "bill"}, {Kind: core.ActionPDF, Dir: "/Users/you/账单", Naming: core.NamingPresets[0]}, {Kind: core.ActionArchive}}}},
				{Enabled: true, Doc: core.RuleDoc{Name: "订阅邮件不进收件箱", Trigger: core.TriggerLabel, TriggerArg: "newsletter", Actions: []core.RuleAction{{Kind: core.ActionRead}, {Kind: core.ActionArchive}}}},
				{Enabled: false, Doc: core.RuleDoc{Name: "发票转给会计", Conditions: []core.RuleCondition{{Field: core.FieldFrom, Op: core.OpContains, Value: "acme.example"}, {Field: core.FieldSubject, Op: core.OpContains, Value: "invoice"}},
					Actions: []core.RuleAction{{Kind: core.ActionForward, To: "accountant@company.example"}}}},
			} {
				r.Doc.Version, r.Doc.Match = core.RuleVersion, core.MatchAll
				if r.Doc.Trigger == "" {
					r.Doc.Trigger = core.TriggerReceive
				}
				id, err := s.env.Svc.SaveRule(ctx, r)
				if err != nil {
					t.Fatal(err)
				}
				r.ID = id
				out = append(out, r)
			}
			s.openSettings("rules")
			s.see("订阅邮件不进收件箱")
			now := s.clock.Now()
			act := func(kind, state, failure string, a core.RuleAction) core.RuleActionResult {
				return core.RuleActionResult{Kind: kind, State: state, Failure: failure, Action: a}
			}
			ok := []core.RuleActionResult{
				act(core.ActionLabel, core.RuleActionDone, "", core.RuleAction{Kind: core.ActionLabel, Label: "账单"}),
				act(core.ActionPDF, core.RuleActionDone, "", core.RuleAction{Kind: core.ActionPDF, Dir: "/Users/you/账单", Naming: core.NamingPresets[0]}),
				act(core.ActionArchive, core.RuleActionDone, "", core.RuleAction{Kind: core.ActionArchive}),
			}
			s.post(func() {
				v := page(s).rules
				v.counts = map[int64]int{out[0].ID: 4, out[1].ID: 12}
				v.open = out[0].ID
				v.hits = []core.RuleHit{
					{RunID: 1, MessageID: 1, From: "Acme Cloud", Subject: "Invoice #1261 for October", At: now.Add(-48 * time.Minute), State: core.RuleRunDone, Actions: ok},
					{RunID: 2, MessageID: 2, From: "Northwind Airlines", Subject: "Invoice for booking NW-58213", At: now.Add(-26 * time.Hour), State: core.RuleRunDone, Actions: ok},
					{RunID: 3, MessageID: 3, From: "财务部", Subject: "10 月 invoice 已开具，请查收", At: now.Add(-72 * time.Hour), State: core.RuleRunFailed, Actions: []core.RuleActionResult{ok[0],
						act(core.ActionPDF, core.RuleActionFailed, core.RuleFailWrite, core.RuleAction{Kind: core.ActionPDF, Dir: "/Users/you/账单", Naming: core.NamingPresets[0]}), act(core.ActionArchive, core.RuleActionNotRun, "", core.RuleAction{Kind: core.ActionArchive})}},
					{RunID: 4, MessageID: 0, From: "Acme Cloud", Subject: "Invoice #1198", At: now.Add(-120 * time.Hour), State: core.RuleRunDone, Actions: ok},
				}
			})
			s.tt.Frame()
			return s, out
		}
		t.Run("SettingsRulesList", func(t *testing.T) {
			s, _ := rulesPage(t)
			shotNow(s.harness, "SettingsRulesList")
		})
		t.Run("SettingsRulesDelete", func(t *testing.T) {
			s, rules := rulesPage(t)
			s.post(func() { v := page(s).rules; v.deleting, v.deleteOpen = &rules[0], true })
			shotNow(s.harness, "SettingsRulesDelete")
		})
		editor := func(t *testing.T, name string, doc core.RuleDoc) {
			s := shotMail(t, noAuto)
			s.openSettings("rules")
			s.see("新建规则")
			doc.Version, doc.Name, doc.Trigger, doc.Match = core.RuleVersion, name, core.TriggerReceive, core.MatchAll
			s.post(func() { page(s).rules.openEditor(core.Rule{Enabled: true, Doc: doc}) })
			s.see("现有邮件里")
			s.advance(5 * time.Second) // the preview's debounce
			s.until("the preview", func() bool { return !s.tt.HasText("正在查找符合的邮件…") })
			shot(s.harness, "SettingsRulesEditor"+strings.ReplaceAll(name, " ", ""))
		}
		t.Run("SettingsRulesEditorFilled", func(t *testing.T) {
			editor(t, "Acme 账单", core.RuleDoc{Conditions: []core.RuleCondition{{Field: core.FieldFrom, Op: core.OpContains, Value: "acme.example"}, {Field: core.FieldSubject, Op: core.OpContains, Value: "receipt"}},
				Actions: []core.RuleAction{{Kind: core.ActionLabel, Label: "bill"}, {Kind: core.ActionArchive}}})
		})
		t.Run("SettingsRulesEditorGroups", func(t *testing.T) {
			editor(t, "Acme 账单分组", core.RuleDoc{Conditions: []core.RuleCondition{
				{Match: core.MatchAny, Group: []core.RuleCondition{{Field: core.FieldFrom, Op: core.OpContains, Value: "acme.example"}, {Field: core.FieldFrom, Op: core.OpContains, Value: "acme-billing.example"}}},
				{Match: core.MatchAny, Group: []core.RuleCondition{{Field: core.FieldSubject, Op: core.OpContains, Value: "invoice"}, {Field: core.FieldSubject, Op: core.OpContains, Value: "发票"}}}},
				Actions: []core.RuleAction{{Kind: core.ActionLabel, Label: "bill"}, {Kind: core.ActionArchive}}})
		})
		t.Run("SettingsRulesEditorFile", func(t *testing.T) {
			editor(t, "合同附件存到本机", core.RuleDoc{Conditions: []core.RuleCondition{{Field: core.FieldSubject, Op: core.OpContains, Value: "合同"}},
				Actions: []core.RuleAction{{Kind: core.ActionAttachments, Dir: "/Users/you/Documents/合同", Subfolder: true, Naming: "{date} - {subject}_备份"}}})
		})
		t.Run("SettingsSignatureLinkBad", func(t *testing.T) {
			s := shotMail(t, noAuto)
			var ids []int64
			for _, sig := range []struct{ name, html string }{
				{"工作", "<p><b>王晨</b></p><p>产品经理 · Company Inc.</p><p><a href=\"https://www.company.example\">www.company.example</a></p>"},
				{"个人", "<p>祝好，</p><p>王晨</p>"},
			} {
				created, err := s.env.Svc.CreateSignature(ctx, sig.name)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.env.Svc.UpdateSignature(ctx, created.ID, core.SignaturePatch{HTML: &sig.html}); err != nil {
					t.Fatal(err)
				}
				ids = append(ids, created.ID)
			}
			for acc, id := range map[int64]int64{s.work.ID: ids[0], s.you.ID: ids[1]} {
				if _, err := s.env.Svc.UpdateAccount(ctx, acc, core.AccountPatch{SignatureID: &id}); err != nil {
					t.Fatal(err)
				}
			}
			s.app.load()
			s.until("the signatures", func() bool { return len(s.app.signatures) == 2 })
			s.openSettings("signatures")
			s.see("个人")
			shot(s.harness, "SettingsSignatures")
			s.openSettings(signatureRoute(ids[0]))
			s.see("默认用于")
			shot(s.harness, "SettingsSignatureDetail")
			s.post(func() {
				s.app.SignatureLink("https://www.company.example", func(string, bool) {})
				l := page(s).sigLink
				l.url, l.bad, l.focused = "www.company.example", true, true
			})
			shot(s.harness, "SettingsSignatureLinkBad")
		})
		t.Run("SettingsPluginsNative", func(t *testing.T) {
			s := shotMail(t, noAuto)
			shotInstall(t, s.harness, "deepl-translate", `{"name":"deepl-translate","version":"0.1.0","description":"用 DeepL 翻译邮件，不经过大模型","main":"index.ts","permissions":{"mail":["read"],"net":["api.deepl.com"]}}`, `export default function (_ctx: any) {}`, false)
			shotPlugin(t, s.harness, "flight-cards", "NW", `{ title: "航班", fields: [] }`)
			shotPlugin(t, s.harness, "attachment-check", "-", `{ title: "附件", fields: [] }`)
			s.openSettings("plugins")
			s.see("flight-cards")
			native := func(name, desc, reason string, enabled, grant bool) core.PluginInfo {
				return core.PluginInfo{Name: name, Version: "0.3.0", Description: plugins.Text{Default: desc}, Native: true, Enabled: enabled, NeedsGrant: grant, DisabledReason: reason,
					Trust: map[bool]string{true: "", false: "user"}[grant], LibraryHash: "9f86d081884c7d659a2feaa0c55ad0150a08"}
			}
			ext := page(s).ext
			s.post(func() {
				ext.plugins = append(ext.plugins, native("antigravity", "通过 Antigravity 使用 Gemini 模型", "", false, true), native("startlux", "本地判断模型", "", true, false))
				ext.plugin = ""
				ext.later = map[string]bool{"antigravity": false, "deepl-translate": true}
			})
			shotNow(s.harness, "SettingsPluginsNative")
			s.post(func() {
				i := slices.IndexFunc(ext.plugins, func(p core.PluginInfo) bool { return p.Name == "startlux" })
				ext.plugins[i].Enabled, ext.plugins[i].DisabledReason = false, "trust_revoked"
				ext.plugin = "startlux"
				ext.later["antigravity"] = true
				native := &ext.plugins[i]
				native.Trust = ""
			})
			shotNow(s.harness, "SettingsPluginsRevoked")
			s.post(func() {
				ext.plugin, ext.loginPage, ext.loginPageOpen = "", "https://accounts.google.com/o/oauth2/auth", true
			})
			shotNow(s.harness, "SettingsPluginsLogin")
		})
		t.Run("SettingsMagpie", func(t *testing.T) {
			s := shotMail(t, noAuto)
			modelsSetup(s, "normal")
			s.openSettings("models")
			s.see("Anthropic")
			modelsStatus(s, "normal")
			m := page(s).models
			s.post(func() {
				m.services = append(m.services[:3:3], core.ModelService{ID: 98, Name: "magpie", Slug: "magpie", Provider: core.ProviderOther, Kind: ai.KindOpenAI, BaseURL: "http://127.0.0.1:47811/v1", ManagedBy: "magpie",
					Models: []string{"claude-sonnet-4.5", "gpt-5", "gemini-2.5"}, JudgeFormat: "decisions", JudgeModel: "magpie-judge",
					Status: map[string]core.CapStatus{core.CapGen: {State: "ok", LatencyMs: 96}}})
				s.app.settings.DefaultModel, s.app.settings.DefaultModelBy = "magpie/claude-sonnet-4.5", "magpie"
			})
			s.tt.Frame()
			shotNow(s.harness, "SettingsModelsManaged")
			s.openSettings("mcp")
			ext := page(s).ext
			tools := []core.MCPTool{{Name: "List issues in the workspace", ReadOnlyHint: true, Trusted: true}, {Name: "Get an issue by its ID", ReadOnlyHint: true, Trusted: true}, {Name: "Search Linear's documentation", ReadOnlyHint: true},
				{Name: "Create a new issue"}, {Name: "Update an existing issue"}, {Name: "Comment on an issue"}}
			linear := core.MCPServer{ID: 1, Name: "Linear", Transport: "HTTP", URL: "https://mcp.linear.app/mcp", Enabled: true, Granted: true, Tools: tools}
			s.post(func() { ext.servers, ext.mcp = []core.MCPServer{linear}, 1 })
			s.tt.Frame()
			shotNow(s.harness, "SettingsMCPLinear")
			s.post(func() {
				ext.servers = []core.MCPServer{linear, {ID: 2, Name: "magpie", Transport: "HTTP", URL: "http://127.0.0.1:47811/mcp", ManagedBy: "magpie"}}
				ext.mcp = 2
			})
			s.tt.Frame()
			shotNow(s.harness, "SettingsMCPManaged")
			s.openSettings("skills")
			s.post(func() {
				ext.skills = append([]core.SkillInfo{{Name: "research-brief", Icon: "sparkles", ManagedBy: "magpie", Enabled: true, Description: "Turn a thread into a short research brief with sources"},
					{Name: "meeting-notes", Icon: "sparkles", ManagedBy: "magpie", Description: "Pull decisions and action items out of a meeting thread"}}, ext.skills...)
			})
			s.tt.Frame()
			shotNow(s.harness, "SettingsSkillsManaged")
		})
		t.Run("SettingsAIAppsDirect", func(t *testing.T) {
			s := shotMail(t, noAuto)
			s.openSettings("clients")
			s.post(func() { s.app.settings.LocalAPI, s.app.settings.MCPSend = true, core.MCPSendDirect })
			s.tt.Frame()
			shotNow(s.harness, "SettingsAIAppsDirect")
		})
		t.Run("SettingsAbout", func(t *testing.T) {
			s := shotMail(t, noAuto)
			s.plat.mu.Lock()
			s.plat.latest = "1.40.0"
			s.plat.mu.Unlock()
			s.openSettings("about")
			s.see("升级到 1.40.0")
			shot(s.harness, "SettingsAboutUpdate")
		})
		recognition := func(t *testing.T, name string, serve http.HandlerFunc) {
			h := recognitionModelHarness(t, serve)
			h.resize(1440, 900)
			h.openSettings("masking")
			h.see(i18n.T("settings.masking.recognition_model.missing"))
			h.click(i18n.T("settings.masking.recognition_model"))
			shot(h, name)
		}
		t.Run("SettingsMaskingDownloading", func(t *testing.T) {
			recognition(t, "SettingsMaskingDownloading", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", "330781")
				w.Write(make([]byte, 1000))
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			})
		})
		t.Run("SettingsMaskingFailed", func(t *testing.T) {
			recognition(t, "SettingsMaskingFailed", func(w http.ResponseWriter, r *http.Request) { w.Write(make([]byte, 330781)) })
		})
		t.Run("SettingsMaskLogStates", func(t *testing.T) {
			s := shotMail(t, noAuto)
			s.openSettings("masklog")
			s.see("脱敏记录")
			shotNow(s.harness, "SettingsMaskLogCleared")
			s.post(func() { // the design's sample, as SettingsMaskLog has it: two requests, one blocked
				at := s.clock.Now().Add(-time.Hour)
				p := page(s)
				p.maskLog = []core.MaskRecord{
					{At: at, Request: 2, Feature: "skill", Kind: "EMAIL", Category: "emails", First: "h", Len: 16, Action: core.MaskMask, Source: core.MaskSourceRule},
					{At: at, Request: 2, Feature: "skill", Kind: "ID_CARD", Category: "ids", First: "3", Len: 18, Action: core.MaskBlock, Source: core.MaskSourceRule},
					{At: at.Add(-time.Hour), Request: 1, Feature: "wit", Kind: "NAME", Category: "names", Len: 2, Action: core.MaskMask, Source: core.MaskSourceRecognizer},
				}
				p.logFilter.result, p.logFilter.open = core.MaskBlock, map[string]bool{"2": true}
			})
			shotNow(s.harness, "SettingsMaskLogBlocked")
		})
		t.Run("SettingsPages", func(t *testing.T) {
			s := shotMail(t, noAuto)
			for _, v := range []struct{ sec, see, name string }{
				{"security", "发件人安全", "SettingsSecurity"}, {"labs", "JMAP 账户", "SettingsLabs"}, {"about", "检查更新", "SettingsAbout"},
				{"skills", "Format", "SettingsSkills"}, {"mcp", "添加服务器", "SettingsMCP"}, {"features", "邮件总结", "SettingsFeatures"},
			} {
				s.openSettings(v.sec)
				s.see(v.see)
				shot(s.harness, v.name)
			}
		})
	})
	t.Run("Toasts", func(t *testing.T) {
		s := shotMail(t, noQuick)
		openLena(s)
		s.app.toastAs(kit.ToastSuccess, "邮件已发送")
		s.app.toastAs(kit.ToastWarning, "work@company.example 没有归档文件夹，已跳过")
		s.app.toasts = append(s.app.toasts, ui.Toast{ID: "archived", Title: "已归档 3 封", Action: "撤销", Data: kit.ToastData{Deadline: s.clock.Now().Add(core.UndoArchiveWindow)}})
		s.see("撤销")
		s.tt.Advance(2 * theme.MotionBase)
		shot(s.harness, "Toasts")
		front, _ := s.tt.Find("撤销")
		s.tt.Move(front.X+front.W/2, front.Y+front.H/2)
		s.tt.Advance(2 * theme.MotionBase)
		shot(s.harness, "ToastsExpanded")
	})
	t.Run("SettingsAccountDialogs", func(t *testing.T) {
		s := shotMail(t, noAuto)
		s.openSettings(accountRoute(s.you.ID))
		s.see("签名")
		page := func() *settingsPage { return s.app.pages[PageSettings].(*settingsPage) }
		open := func(kind, name string, setup func(*settingsPage)) {
			s.post(func() {
				acc, _ := s.app.accountByID(s.you.ID)
				p := page()
				p.openDialog(kind, acc)
				if setup != nil {
					setup(p)
				}
			})
			s.tt.Advance(2 * theme.MotionBase)
			shot(s.harness, name)
			s.post(page().closeDialog)
			s.tt.Advance(2 * theme.MotionBase)
		}
		open("password", "SettingsAccountPassword", func(p *settingsPage) { p.secret = "abcdefghijklmnop" })
		open("servers", "SettingsAccountServers", nil)
		open("cache", "SettingsAccountCache", nil)
		open("remove", "SettingsAccountRemove", nil)
		s.post(func() {
			p := page()
			p.importPath, p.importOpen = "accounts.json", true
			p.importPreview = core.AccountImportPreview{Accounts: []core.AccountImportEntry{
				{Email: "lisa@163.com", Name: "Lisa Wang", Signature: "工作", ReceivingProtocol: "imap", ReceivingHost: "imap.163.com", ReceivingPort: 993,
					SendingProtocol: "smtp", SendingHost: "smtp.163.com", SendingPort: 465, Username: "lisa@163.com"},
				{Email: "ops@company.example", Name: "运维值班", ReceiveOnly: true, AIInstructions: true, Username: "ops", ReceivingProtocol: "imap",
					ReceivingHost: "mail.company.example", ReceivingPort: 993, SendingProtocol: "smtp", SendingHost: "mail.company.example", SendingPort: 587},
				{Email: "you@example.com", Existing: true, Username: "you@example.com", ReceivingProtocol: "imap", ReceivingHost: "imap.qq.com", ReceivingPort: 993,
					SendingProtocol: "smtp", SendingHost: "smtp.qq.com", SendingPort: 465},
			}}
		})
		s.tt.Advance(2 * theme.MotionBase)
		shot(s.harness, "SettingsAccountImport")
	})
	t.Run("SettingsAccountKinds", func(t *testing.T) {
		s := shotMail(t, noAuto)
		arch, err := s.env.Svc.AddLocalArchive(context.Background(), "归档 2023")
		if err != nil {
			t.Fatal(err)
		}
		s.post(s.app.load)
		s.until("the archive", func() bool { _, ok := s.app.accountByID(arch.ID); return ok })
		s.openSettings(accountRoute(arch.ID))
		s.tt.Frame()
		shot(s.harness, "SettingsAccountArchive")
		s.post(func() {
			s.app.loads.Stop()
			s.app.addressBooks = []core.AddressBook{{
				ID: 9, Preset: core.PresetCustom, Name: "Nextcloud", URL: "https://cloud.example.org/remote.php/dav", AuthType: "password", Username: "me",
				Status: "ok", LastSyncedAt: s.clock.Now().Add(-5 * time.Minute),
				Collections: []core.AddressBookCollection{{ID: 2, AddressBookID: 9, Href: "/contacts/", Name: "联系人", Enabled: true, CardCount: 248},
					{ID: 3, AddressBookID: 9, Href: "/colleagues/", Name: "同事", Enabled: true, CardCount: 36}},
			}}
			s.app.navigate(Route{Page: PageSettings, Settings: addressBookRoute(9)})
		})
		s.tt.Frame()
		shot(s.harness, "SettingsAddressBook")
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
	t.Run("AddAccountConnected", func(t *testing.T) {
		plaintext(t)
		s := shotMail(t, noAuto)
		s.openSettings(accountRoute(s.you.ID))
		s.app.openAddAccount()
		s.see("添加账户")
		s.fill("邮箱地址", "12345678@qq.com")
		s.advance(debounce)
		s.see("识别为 QQ 邮箱")
		s.click("继续")
		s.see("授权码")
		s.fill("授权码", "abcdefghijklmnop")
		shot(s.harness, "AddAccountPassword")
		s.manualServers()
		s.fill("授权码", "secret") // the test servers' password
		shot(s.harness, "AddAccountManual")
		s.click("连接")
		s.see("连接成功")
		s.tt.Advance(confettiLife)
		shot(s.harness, "AddAccountConnected")
		s.click("完成")
		s.until("the new account's settings", func() bool { return s.app.addAccount == nil && s.app.route.Settings != accountRoute(s.you.ID) })
		s.see("设为默认发件账户")
		shot(s.harness, "AddAccountNewAccount")
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
		shot(h, "OnboardingManualServers")
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
	t.Run("OnboardingOwnOAuthClient", func(t *testing.T) {
		h := onboarding(t)
		h.click("Gmail")
		h.see("应用专用密码")
		shot(h, "OnboardingGmail")
		h.fill("邮箱地址", "me@gmail.com")
		h.click("浏览器登录")
		h.see("把下载的 client_secret_….json 拖到这里")
		shot(h, "OnboardingOwnOAuthClient")
		h.click("Thunderbird 的公开客户端")
		h.see("Godwit 与 Mozilla 没有关联")
		shot(h, "OnboardingPublicOAuthClient")
	})
}
