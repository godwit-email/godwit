package app

import (
	"testing"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/internal/theme"
)

func TestHighlightSpans(t *testing.T) {
	th := theme.Light()
	count := func(sp []ui.Span) (n int) {
		for _, s := range sp {
			if s.Background == th.Highlight {
				n++
			}
		}
		return n
	}
	join := func(sp []ui.Span) (s string) {
		for _, x := range sp {
			s += x.Text
		}
		return s
	}
	for _, c := range []struct {
		text, q string
		hits    int
	}{
		{"Learn C++ now", "C++", 1},
		{"Learn C++ now", "", 0},
		{"邮件邮件", "邮件", 2},
		{"ACME corp", "acme", 1},
		{"İstanbul", "i", 0}, // no simple fold pairs İ with i
		{"Kelvin", "k", 1},
		{"Straße", "ß", 1},
	} {
		sp := highlight(th, c.text, c.q)
		if c.q == "" {
			if sp != nil {
				t.Errorf("%q: spans for an empty query", c.text)
			}
			continue
		}
		for _, x := range sp {
			if !utf8.ValidString(x.Text) {
				t.Errorf("highlight(%q, %q): span %q cuts a rune", c.text, c.q, x.Text)
			}
		}
		if got := count(sp); got != c.hits || join(sp) != c.text {
			t.Errorf("highlight(%q, %q): %d hits, text %q", c.text, c.q, got, join(sp))
		}
	}
	if sp := highlight(th, "邮件邮件", "邮件"); len(sp) != 2 {
		t.Errorf("邮件邮件: %d spans, want 2", len(sp))
	}
}

func TestHighlightDark(t *testing.T) {
	th := theme.Dark()
	sp := highlight(th, "a b", "b")
	if len(sp) != 2 || sp[1].Background != th.Highlight || sp[1].Color != th.HighlightText {
		t.Fatalf("spans %+v are not the dark tokens", sp)
	}
}

func TestDateGroups(t *testing.T) {
	loc := time.FixedZone("x", 8*3600)
	now := time.Date(2026, 10, 14, 15, 0, 0, 0, loc) // a Wednesday
	for _, c := range []struct {
		t    time.Time
		want string
	}{
		{time.Date(2026, 10, 14, 0, 0, 0, 0, loc), "今天"},
		{time.Date(2026, 10, 13, 23, 59, 0, 0, loc), "昨天"},
		{time.Date(2026, 10, 12, 0, 0, 0, 0, loc), "本周"},      // Monday
		{time.Date(2026, 10, 11, 23, 0, 0, 0, loc), "更早"},     // Sunday before
		{time.Date(2026, 10, 14, 1, 0, 0, 0, time.UTC), "今天"}, // 09:00 local
		{time.Date(2026, 1, 1, 0, 0, 0, 0, loc), "更早"},
	} {
		if got := dateGroup(now, c.t); got != c.want {
			t.Errorf("%v: %q, want %q", c.t, got, c.want)
		}
	}
	mon := time.Date(2026, 10, 12, 9, 0, 0, 0, loc)
	if got := dateGroup(mon, time.Date(2026, 10, 11, 9, 0, 0, 0, loc)); got != "昨天" {
		t.Errorf("Sunday seen from Monday: %q", got)
	}
}

func TestSearchResultFolderLine(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com", "carol@example.com")
	p := h.app.mail
	a0, a1 := accs[0].ID, accs[1].ID
	p.folders = map[int64][]core.Folder{
		a0: {{ID: 1, AccountID: a0, Role: "inbox"}, {ID: 2, AccountID: a0, Role: "sent"}},
		a1: {{ID: 3, AccountID: a1, Role: "", Name: "Receipts"}},
	}
	h.app.accounts = []core.Account{{ID: a0, Email: "alice@example.com"}, {ID: a1, Email: "carol@example.com"}}
	in := messageItem(core.MessageSummary{FolderID: 1, From: "Bob <bob@example.com>"})
	other := messageItem(core.MessageSummary{FolderID: 3, From: "bob@example.com"})
	sent := messageItem(core.MessageSummary{FolderID: 2, From: "alice@example.com", To: []string{"Lena Fischer <lena@example.com>"}})
	if got := p.folderLine(in); got != "收件箱 · alice@example.com" {
		t.Errorf("inbox line %q", got)
	}
	if got := p.folderLine(other); got != "Receipts · carol@example.com" {
		t.Errorf("other account's line %q", got)
	}
	if got := p.resultWho(sent); got != "我 → Lena Fischer" {
		t.Errorf("sent who %q", got)
	}
	if got := p.resultWho(in); got != "Bob" {
		t.Errorf("received who %q", got)
	}
}

func searchEmpty(h *harness) {
	p := h.app.mail
	p.searchOpen, p.searched, p.query, p.hlQuery, p.results = true, true, "zzz", "zzz", nil
	h.tt.Frame()
}

func TestSearchEmptyHandsToWit(t *testing.T) {
	h, _, _ := askHarness(t, true)
	var asks []string
	h.app.SetWitAsk(func(q string) { asks = append(asks, q) })
	h.app.navigate(Route{Page: PageMail, View: core.View{Unified: "inbox"}})
	h.tt.Frame()
	searchEmpty(h)
	h.see("没有找到「zzz」")
	h.see("扩大同步范围")
	h.click("让 Wit 帮你找")
	if h.app.route.Page != PageWit || len(asks) != 1 || asks[0] != "zzz" {
		t.Fatalf("route %+v, asks %q", h.app.route, asks)
	}
}

func TestSearchEmptyNoModelOnlyNavigates(t *testing.T) {
	h, _, _ := askHarness(t, false)
	var asks []string
	h.app.SetWitAsk(func(q string) { asks = append(asks, q) })
	h.app.navigate(Route{Page: PageMail, View: core.View{Unified: "inbox"}})
	h.tt.Frame()
	searchEmpty(h)
	h.click("让 Wit 帮你找")
	if h.app.route.Page != PageWit || len(asks) != 0 {
		t.Fatalf("route %+v, asks %q", h.app.route, asks)
	}
	h.see("添加模型服务")
}

func TestSearchHandoffRowAndHint(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.put(accs[0], "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Invoice March", "pay"))
	h.syncNow(accs[0])
	h.waitMail("a thread", func() bool { return len(h.items()) == 1 })
	var asks []string
	h.app.SetWitAsk(func(q string) { asks = append(asks, q) })
	h.tt.Key(ui.Cmd, ui.KeyF)
	h.see("只搜索已同步到本机的邮件")
	h.typeText("Invoice")
	h.tt.Key(0, ui.KeyEnter)
	h.until("found", func() bool { return h.app.mail.searched && len(h.app.mail.results) == 1 })
	h.see("找到 1 封 · 全部账户 · 只搜索已同步到本机的邮件")
	h.see("让 Wit 帮你找：「Invoice」")
	h.see("收件箱 · alice@example.com")
}

// The wording follows the query searched, not what the box holds since.
func TestSearchEmptyUsesSearchedQuery(t *testing.T) {
	h, _, _ := askHarness(t, true)
	var asks []string
	h.app.SetWitAsk(func(q string) { asks = append(asks, q) })
	h.app.navigate(Route{Page: PageMail, View: core.View{Unified: "inbox"}})
	h.tt.Frame()
	searchEmpty(h)
	h.app.mail.query = "edited"
	h.tt.Frame()
	h.see("没有找到「zzz」")
	h.click("让 Wit 帮你找")
	if len(asks) != 1 || asks[0] != "zzz" {
		t.Fatalf("asks %q", asks)
	}
}

// A date heading is no row: clicking it chooses nothing, and the choice
// (counted in list lines) opens the right thread. The first group's heading
// sits in the list header, the later ones in the list.
func TestDateHeadingIsNotARow(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	base := h.clock.Now()
	h.put(accs[0], "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Lunch today", "x", at(base, 0)))
	h.put(accs[0], "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Invoice March", "pay", at(base, 24)))
	h.syncNow(accs[0])
	h.waitMail("two threads", func() bool { return len(h.items()) == 2 })
	h.tt.Frame()
	p := h.app.mail
	h.see("今天")
	before := h.app.route
	h.click("昨天")
	if p.main.sel.Len() != 0 || p.main.cur != -1 || h.app.route != before {
		t.Fatalf("a heading was chosen: sel %d, cur %d, route %+v", p.main.sel.Len(), p.main.cur, h.app.route)
	}
	h.click("Invoice March")
	if p.main.cur != 2 || h.app.route.ThreadID != h.items()[1].ID {
		t.Fatalf("cur %d, route %+v", p.main.cur, h.app.route)
	}
	if got := p.chosen(p.shown()); len(got) != 1 || got[0].Subject != "Invoice March" {
		t.Fatalf("chosen %+v", got)
	}
}
