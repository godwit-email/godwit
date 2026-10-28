package app

import (
	"context"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/events"
)

// fakeWitView stands for the Wit page's *mygo.WebView.
type fakeWitView struct{ ui.NativeWebView }

// witOpens records what the App tells the Wit page, as WitPage.SetConversation.
type witOpens struct{ convs []int64 }

func (w *witOpens) open(conv int64) int64 {
	w.convs = append(w.convs, conv)
	return int64(len(w.convs))
}

// askHarness is aiHarness on the Ask page, with a Wit page.
func askHarness(t *testing.T, llm bool) (*harness, *fakeWitView, *witOpens) {
	t.Helper()
	h, _, _ := aiHarness(t, llm, noAuto)
	view, opens := &fakeWitView{}, &witOpens{}
	h.app.SetWitPage(view, opens.open, opens.open)
	h.app.navigate(Route{Page: PageWit})
	h.tt.Frame()
	return h, view, opens
}

func (h *harness) ask() *askPage { return h.app.pages[PageWit].(*askPage) }

// Review Focus 4: with a model the content is the Wit page, told the
// route's conversation on every navigation.
func TestAskPageHostsWebView(t *testing.T) {
	h, view, opens := askHarness(t, true)
	if got := h.tt.WebViewAt(1000, 400); got != view {
		t.Fatalf("the content shows %v, want the Wit page", got)
	}
	h.app.navigate(Route{Page: PageWit, ConversationID: 7})
	h.app.navigate(Route{Page: PageMail, View: core.View{Unified: "inbox"}})
	h.tt.Frame()
	if got := h.tt.WebViewAt(1000, 400); got != nil {
		t.Fatal("the Wit page shows on the mail page")
	}
	h.app.Navigate(-1)
	if !slices.Equal(opens.convs, []int64{0, 7, 7}) {
		t.Fatalf("the page was told %v, want [0 7 7]", opens.convs)
	}
	if h.ask().version != 3 {
		t.Fatalf("version %d, want the last one told", h.ask().version)
	}
}

func TestAskNoModel(t *testing.T) {
	h, _, _ := askHarness(t, false)
	h.see("添加模型服务")
	if h.tt.WebViewAt(1000, 400) != nil {
		t.Fatal("the Wit page shows without a model")
	}
	h.click("添加模型服务")
	if r := h.app.route; r.Page != PageSettings || r.Settings != "models" {
		t.Fatalf("route %+v", r)
	}
}

// Review Focus 2: the first question's conversation takes the route in
// place, and back and forward keep it; one asked under an older snapshot
// leaves the route alone.
func TestAskConversationCreated(t *testing.T) {
	h, _, opens := askHarness(t, true)
	ctx := context.Background()
	c, err := h.env.Svc.NewConversation(ctx, "ask", 0)
	if err != nil {
		t.Fatal(err)
	}
	h.app.ConversationCreated(c.ID, h.ask().version)
	h.drain()
	h.tt.Frame()
	if r := h.app.route; r.Page != PageWit || r.ConversationID != c.ID {
		t.Fatalf("route %+v", r)
	}
	// The page is told it as a new version: one reloaded meanwhile shows it.
	if last := opens.convs[len(opens.convs)-1]; last != c.ID || h.ask().version != int64(len(opens.convs)) {
		t.Fatalf("the page was told %v, version %d", opens.convs, h.ask().version)
	}
	h.see("未命名对话") // listed
	h.app.navigate(Route{Page: PageSettings})
	h.app.Navigate(-1)
	if r := h.app.route; r.ConversationID != c.ID {
		t.Fatalf("back to %+v", r)
	}

	h.app.navigate(Route{Page: PageWit})
	stale := h.ask().version - 1
	c2, err := h.env.Svc.NewConversation(ctx, "ask", 0)
	if err != nil {
		t.Fatal(err)
	}
	told := len(opens.convs)
	h.app.ConversationCreated(c2.ID, stale)
	if r := h.app.route; r.ConversationID != 0 || len(opens.convs) != told {
		t.Fatalf("a stale creation took the route: %+v, told %v", r, opens.convs)
	}
	h.until("both listed", func() bool { return len(h.ask().convs) == 2 })
}

func TestAskConversationsList(t *testing.T) {
	h, _, opens := askHarness(t, true)
	ctx := context.Background()
	var ids []int64
	for _, q := range []string{"上周谁在等我回复", "找一下去年的发票"} {
		c, err := h.env.Svc.NewConversation(ctx, "ask", 0)
		if err != nil {
			t.Fatal(err)
		}
		ch, err := h.env.Svc.Ask(ctx, c.ID, q)
		if err != nil {
			t.Fatal(err)
		}
		for range ch {
		}
		ids = append(ids, c.ID)
	}
	h.app.refresh(nil)
	h.see("最近")
	h.see("上周谁在等我回复")
	h.click("找一下去年的发票")
	h.until("the conversation open", func() bool {
		return h.app.route.ConversationID == ids[1] && opens.convs[len(opens.convs)-1] == ids[1]
	})
	if title, sub := h.ask().Title(); title != "找一下去年的发票" || sub != "权限：只读 + 写草稿" {
		t.Fatalf("title %q · %q", title, sub)
	}
	if err := h.tt.RightClick("上周谁在等我回复"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("删除"); err != nil {
		t.Fatal(err)
	}
	h.until("the conversation gone", func() bool { return !h.tt.HasText("上周谁在等我回复") })
	cs, err := h.env.Svc.ListConversations(ctx, "ask")
	if err != nil || len(cs) != 1 || cs[0].ID != ids[1] {
		t.Fatalf("conversations %v, %v", cs, err)
	}
	h.click("新对话")
	h.until("a new conversation", func() bool {
		return h.app.route == Route{Page: PageWit} && opens.convs[len(opens.convs)-1] == 0
	})
	if title, _ := h.ask().Title(); title != "" {
		t.Fatalf("a new conversation titled %q", title)
	}
}

// A conversation waiting on the user's decision says so in the sidebar.
func TestAskConversationAwaiting(t *testing.T) {
	h, _, _ := askHarness(t, true)
	h.app.refresh(nil)
	p := h.ask()
	p.convs = []core.Conversation{{ID: 1, Kind: "ask", Title: "回复 Lena", Awaiting: true}, {ID: 2, Kind: "ask", Title: "这个月的订阅", Answering: true}}
	p.listGen++ // the refresh's read, still under way, is dropped
	h.see("回复 Lena")
	h.see("待确认")
	h.ask().convs[0].Awaiting = false
	h.until("the badge gone once decided", func() bool { return !h.tt.HasText("待确认") })
}

// A run starting or ending re-reads the Wit sidebar alone, not the whole window.
func TestAskRunsChangedReloadsTheList(t *testing.T) {
	h, _, _ := askHarness(t, true)
	h.app.refresh(nil)
	before, gen := h.app.refreshes, h.ask().listGen
	h.app.onEvent(events.Event{Type: events.RunsChanged, ConversationID: 1})
	if h.ask().listGen == gen {
		t.Fatal("the list was not read again")
	}
	if slices.ContainsFunc(h.app.dirty, func(e events.Event) bool { return e.Type == events.RunsChanged }) || h.app.refreshes != before {
		t.Fatalf("a window refresh was queued: %v dirty, %d refreshes", h.app.dirty, h.app.refreshes-before)
	}
}

// Deleting the conversation shown opens a new one and drops it from the history.
func TestAskDeleteShown(t *testing.T) {
	h, _, opens := askHarness(t, true)
	c, err := h.env.Svc.NewConversation(context.Background(), "ask", 0)
	if err != nil {
		t.Fatal(err)
	}
	h.app.navigate(Route{Page: PageWit, ConversationID: c.ID})
	h.until("listed", func() bool { h.tt.Frame(); return h.tt.HasText("未命名对话") })
	h.app.deleteConversation(c.ID)
	h.until("a new conversation", func() bool { return h.app.route == Route{Page: PageWit} })
	if opens.convs[len(opens.convs)-1] != 0 {
		t.Fatalf("the page was told %v", opens.convs)
	}
	if slices.ContainsFunc(h.app.history, func(r Route) bool { return r.ConversationID == c.ID }) {
		t.Fatalf("history keeps it: %+v", h.app.history)
	}
}

// The Wit page's sidebar has the search button too: it opens the palette.
func TestAskSidebarSearch(t *testing.T) {
	h, _, _ := askHarness(t, true)
	h.click("搜索")
	h.until("the palette", func() bool { return paletteShown(h) })
}
