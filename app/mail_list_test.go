package app

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/emersion/go-imap/v2"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
)

// mailHarness is a harness with an account per email, on the mail page.
func mailHarness(t *testing.T, opts []coretest.Option, emails ...string) (*harness, []core.Account) {
	t.Helper()
	var accs []core.Account
	h := newHarnessWith(t, opts, func(e *coretest.Env) {
		for _, em := range emails {
			accs = append(accs, e.AddAccount(t, em))
		}
	})
	h.until("the mail page", func() bool { return h.app.loaded && h.app.route.Page == PageMail })
	return h, accs
}

// put appends raw to the account's folder on its server, without a sync.
func (h *harness) put(acc core.Account, folder string, raw []byte) {
	h.t.Helper()
	if _, err := h.env.IMAP(acc.ID).User.Append(folder, bytes.NewReader(raw), &imap.AppendOptions{}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) syncNow(acc core.Account) {
	h.t.Helper()
	if err := h.env.Svc.SyncNow(context.Background(), acc.ID); err != nil {
		h.t.Fatal(err)
	}
}

// waitMail fires throttled refreshes until cond holds.
func (h *harness) waitMail(desc string, cond func() bool) {
	h.t.Helper()
	h.until(desc, func() bool {
		h.advance(refreshEvery)
		return cond()
	})
}

func (h *harness) folderOf(acc core.Account, role string) core.Folder {
	h.t.Helper()
	fs, err := h.env.Svc.ListFolders(context.Background(), acc.ID)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, f := range fs {
		if f.Role == role {
			return f
		}
	}
	h.t.Fatalf("account %s has no %s folder", acc.Email, role)
	return core.Folder{}
}

func (h *harness) countIn(f core.Folder) int {
	h.t.Helper()
	page, err := h.env.Svc.ListMessages(context.Background(), core.View{FolderID: f.ID}, "", 200)
	if err != nil {
		h.t.Fatal(err)
	}
	return len(page.Items)
}

func at(base time.Time, hoursAgo int) coretest.MsgOption {
	return coretest.Date(base.Add(-time.Duration(hoursAgo) * time.Hour))
}

// putMany puts n messages "msg 000" (newest) … into the INBOX and syncs.
func (h *harness) putMany(acc core.Account, n int) {
	base := h.clock.Now()
	for i := range n {
		h.put(acc, "INBOX", coretest.Msg("bob@example.com", acc.Email, fmt.Sprintf("msg %03d", i), "body", at(base, i)))
	}
	h.syncNow(acc)
}

func (h *harness) items() []mailItem { return h.app.mail.items }

func noDuplicates(t *testing.T, items []mailItem) {
	t.Helper()
	seen := map[int64]bool{}
	for _, it := range items {
		if seen[it.ID] {
			t.Fatalf("item %d listed twice", it.ID)
		}
		seen[it.ID] = true
	}
}

// scrollLoad scrolls to the end until n items are loaded.
func (h *harness) scrollLoad(n int) {
	h.t.Helper()
	h.until(fmt.Sprintf("%d items", n), func() bool {
		h.app.mail.main.list.ScrollToEnd()
		h.tt.Frame()
		return len(h.items()) == n
	})
}

func TestSidebarAccountsAndViews(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com", "carol@example.com")
	alias := "Work"
	if _, err := h.env.Svc.UpdateAccount(context.Background(), accs[0].ID, core.AccountPatch{Alias: &alias}); err != nil {
		t.Fatal(err)
	}
	h.put(accs[0], "INBOX", coretest.Msg("bob@example.com", accs[0].Email, "to alice", "x"))
	h.syncNow(accs[0])
	h.put(accs[1], "INBOX", coretest.Msg("bob@example.com", accs[1].Email, "to carol", "x"))
	h.syncNow(accs[1])
	h.waitMail("both accounts and their mail", func() bool {
		return h.tt.HasText("Work") && h.tt.HasText("carol@example.com") && h.app.mail.inboxUnread() == 2
	})
	for _, s := range []string{"写邮件", "全部收件箱", "已标星", "添加账户", "2"} {
		h.see(s)
	}
	// Folders by role: inbox, sent, archive, trash (the fake server has no drafts or junk).
	var ys []float32
	for _, name := range []string{"收件箱", "已发送", "归档", "废纸篓"} {
		r, ok := h.tt.Find(name)
		if !ok {
			t.Fatalf("no folder %s: %q", name, h.tt.Texts())
		}
		ys = append(ys, r.Y)
	}
	if !slices.IsSorted(ys) {
		t.Fatalf("folders out of order: %v", ys)
	}
	count := func(s string) int {
		n := 0
		for _, x := range h.tt.Texts() {
			if x == s {
				n++
			}
		}
		return n
	}
	if n := count("已发送"); n != 2 {
		t.Fatalf("%d 已发送 folders, want one per account", n)
	}
	h.click("Work") // collapses the first account, animated: wait for its end
	h.until("one 已发送 folder after collapsing", func() bool { return count("已发送") == 1 })
	h.click("已标星")
	if v := h.app.route.View; v.Unified != "starred" {
		t.Fatalf("view %+v", v)
	}
	h.click("归档") // carol's: alice's group is collapsed
	if v := h.app.route.View; v.FolderID != h.folderOf(accs[1], "archive").ID {
		t.Fatalf("view %+v", v)
	}
	h.click("添加账户")
	if h.app.route.Page != PageSettings || h.app.addAccount == nil {
		t.Fatalf("route %+v, sheet %v", h.app.route, h.app.addAccount != nil)
	}
}

func TestThreadListShowsThreads(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	base := h.clock.Now()
	h.put(accs[0], "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Plan", "one", coretest.MessageID("a@x"), at(base, 3)))
	h.put(accs[0], "INBOX", coretest.Msg("carol@example.com", "alice@example.com", "Re: Plan", "two", coretest.MessageID("b@x"), coretest.InReplyTo("a@x"), at(base, 2)))
	h.put(accs[0], "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Re: Plan", "three", coretest.MessageID("c@x"), coretest.InReplyTo("b@x"), at(base, 1)))
	h.syncNow(accs[0])
	h.waitMail("one thread of three", func() bool {
		return len(h.items()) == 1 && h.items()[0].Count == 3
	})
	h.see("3")
	h.see("08:00")             // today: HH:mm
	h.see("alice@example.com") // a lone account is grouped too, as in the design
	h.see("收件箱")
	h.click("three")
	if h.app.route.ThreadID != h.items()[0].ID {
		t.Fatalf("route %+v", h.app.route)
	}
}

func TestListTime(t *testing.T) {
	now := time.Date(2026, 10, 9, 9, 0, 0, 0, time.Local) // a Friday
	for _, c := range []struct {
		t    time.Time
		want string
	}{
		{now.Add(-time.Hour), "08:00"},
		{now.Add(-10 * time.Hour), "昨天"},
		{now.AddDate(0, 0, -4), "周一"},
		{now.AddDate(0, 0, -5), "10/4"}, // last week's Sunday
		{now.AddDate(0, 0, -7), "10/2"},
	} {
		if got := listTime(now, c.t); got != c.want {
			t.Errorf("listTime(%v) = %q, want %q", c.t, got, c.want)
		}
	}
	monday := time.Date(2026, 10, 12, 9, 0, 0, 0, time.Local)
	for _, c := range []struct {
		t    time.Time
		want string
	}{
		{monday.AddDate(0, 0, -1), "昨天"},
		{monday.AddDate(0, 0, -2), "10/10"}, // Saturday: the week before
	} {
		if got := listTime(monday, c.t); got != c.want {
			t.Errorf("on Monday listTime(%v) = %q, want %q", c.t, got, c.want)
		}
	}
}

func TestThreadViewOff(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.put(accs[0], "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Plan", "one", coretest.MessageID("a@x")))
	h.put(accs[0], "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Re: Plan", "two", coretest.MessageID("b@x"), coretest.InReplyTo("a@x")))
	h.put(accs[0], "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Re: Plan", "three", coretest.MessageID("c@x"), coretest.InReplyTo("b@x")))
	h.syncNow(accs[0])
	h.waitMail("one thread", func() bool { return len(h.items()) == 1 })
	ctx := context.Background()
	st, err := h.env.Svc.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	st.ThreadView = false
	if _, err := h.env.Svc.SetSettings(ctx, st); err != nil {
		t.Fatal(err)
	}
	h.waitMail("three messages", func() bool { return len(h.items()) == 3 && !h.app.mail.threads })
	h.click("two")
	if h.app.route.MessageID == 0 || h.app.route.ThreadID != h.items()[0].Msg.ThreadID {
		t.Fatalf("route %+v", h.app.route)
	}
}

func TestThreadListPagesOnScroll(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.putMany(accs[0], 120)
	h.waitMail("the first page", func() bool { return len(h.items()) >= 50 })
	for range 5 {
		h.advance(refreshEvery)
	}
	if n := len(h.items()); n != 50 {
		t.Fatalf("%d items before scrolling, want 50", n)
	}
	h.scrollLoad(100)
	h.scrollLoad(120)
	noDuplicates(t, h.items())
	h.app.mail.main.list.ScrollToEnd()
	h.see("msg 119")
}

func TestRefreshKeepsLoadedRange(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.putMany(accs[0], 120)
	h.waitMail("the first page", func() bool { return len(h.items()) == 50 })
	h.click("msg 000")
	first := h.items()[0]
	if h.app.route.ThreadID != first.ID {
		t.Fatalf("route %+v", h.app.route)
	}
	h.scrollLoad(100)
	h.scrollLoad(120)
	h.app.mail.main.list.ScrollTo(60, ui.Start)
	h.until("row 60 on top", func() bool { top, _ := h.app.mail.main.list.Visible(); return top == 60 })
	anchor := h.items()[60].ID

	// The next refresh stops once it has read the list, until the new mail is in.
	entered, release := make(chan struct{}), make(chan struct{})
	var held atomic.Bool
	h.app.mail.fetched = func() {
		if held.CompareAndSwap(false, true) { // only the first refresh waits
			close(entered)
			<-release
		}
	}
	target := h.items()[110] // on the third page
	th, err := h.env.Svc.GetThread(context.Background(), target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.env.Svc.SetFlags(context.Background(), []int64{th.Messages[0].ID}, []string{`\Seen`}, nil); err != nil {
		t.Fatal(err)
	}
	h.until("a refresh in flight", func() bool {
		h.advance(refreshEvery)
		select {
		case <-entered:
			return true
		default:
			return false
		}
	})
	h.put(accs[0], "INBOX", coretest.Msg("dan@example.com", "alice@example.com", "newcomer", "x"))
	h.syncNow(accs[0])
	h.settle() // the new mail's events are in: the next refresh supersedes the one held
	close(release)
	h.waitMail("the new mail and the read row", func() bool {
		i := slices.IndexFunc(h.items(), func(it mailItem) bool { return it.ID == target.ID })
		return len(h.items()) == 121 && i >= 0 && h.items()[i].Unread == 0
	})
	for range 3 {
		h.advance(refreshEvery)
	}
	noDuplicates(t, h.items())
	if n := len(h.items()); n != 121 || h.items()[0].Subject != "newcomer" {
		t.Fatalf("%d items, first %q", n, h.items()[0].Subject)
	}
	if h.app.route.ThreadID != first.ID || !h.app.mail.main.sel.Has(first.key()) || h.app.mail.main.sel.Len() != 1 {
		t.Fatalf("the choice moved: route %+v", h.app.route)
	}
	if top, _ := h.app.mail.main.list.Visible(); h.items()[top].ID != anchor {
		t.Fatalf("the anchor moved: row %d is %q", top, h.items()[top].Subject)
	}
}

func TestUnreadFilter(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	base := h.clock.Now()
	h.put(accs[0], "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "old news", "x", at(base, 2)))
	h.put(accs[0], "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "fresh", "x", at(base, 1)))
	h.syncNow(accs[0])
	h.waitMail("two threads", func() bool { return len(h.items()) == 2 })
	th, err := h.env.Svc.GetThread(context.Background(), h.items()[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.env.Svc.SetFlags(context.Background(), []int64{th.Messages[0].ID}, []string{`\Seen`}, nil); err != nil {
		t.Fatal(err)
	}
	h.waitMail("one read", func() bool { return h.items()[1].Unread == 0 })
	h.click("未读")
	if h.tt.HasText("old news") || !h.tt.HasText("fresh") {
		t.Fatalf("unread filter shows %q", h.tt.Texts())
	}
	h.click("全部")
	h.see("old news")
}

func TestSearch(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.put(accs[0], "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Invoice March", "pay"))
	h.put(accs[0], "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Lunch", "eat"))
	h.syncNow(accs[0])
	h.waitMail("two threads", func() bool { return len(h.items()) == 2 })
	h.tt.Key(ui.Cmd, ui.KeyF)
	h.see("只搜索已同步的邮件")
	h.typeText("Invoice")
	h.tt.Key(0, ui.KeyEnter)
	h.until("the search result", func() bool { return h.app.mail.searched && len(h.app.mail.results) == 1 })
	if h.tt.HasText("Lunch") || !h.tt.HasText("Invoice March") {
		t.Fatalf("search shows %q", h.tt.Texts())
	}
	h.tt.Key(0, ui.KeyEscape)
	h.see("Lunch")
	if h.app.mail.searched || h.tt.HasText("只搜索已同步的邮件") {
		t.Fatal("Escape did not leave the search")
	}
	h.click("搜索")
	h.see("只搜索已同步的邮件")
}

func TestKeyboardArchiveDelete(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	base := h.clock.Now()
	h.put(accs[0], "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "first", "x", at(base, 1)))
	h.put(accs[0], "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "second", "x", at(base, 2)))
	h.syncNow(accs[0])
	h.waitMail("two threads", func() bool { return len(h.items()) == 2 })
	h.click("first")
	h.waitMail("first read by the reader", func() bool { return h.items()[0].Unread == 0 })
	h.click("first") // the list has the focus again
	h.tt.Key(0, ui.KeyU)
	h.waitMail("first unread", func() bool { return h.items()[0].Unread == 1 })
	h.tt.Key(0, ui.KeyS)
	h.until("first starred", func() bool {
		page, err := h.env.Svc.ListMessages(context.Background(), core.View{Unified: "starred"}, "", 10)
		return err == nil && len(page.Items) == 1
	})
	h.tt.Key(0, ui.KeyE)
	h.waitMail("first archived", func() bool { return len(h.items()) == 1 })
	if n := h.countIn(h.folderOf(accs[0], "archive")); n != 1 {
		t.Fatalf("%d in Archive", n)
	}
	h.click("second")
	h.tt.TypeKey(ui.Shift, ui.Key3, "#")
	h.waitMail("second deleted", func() bool { return len(h.items()) == 0 })
	if n := h.countIn(h.folderOf(accs[0], "trash")); n != 1 {
		t.Fatalf("%d in Trash", n)
	}
}

func TestBatchAcrossAccounts(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com", "carol@example.com", "dave@example.com")
	if err := h.env.IMAP(accs[2].ID).User.Delete("Archive"); err != nil {
		t.Fatal(err)
	}
	base := h.clock.Now()
	for i, acc := range accs {
		h.put(acc, "INBOX", coretest.Msg("bob@example.com", acc.Email, "hello "+acc.Email, "x", at(base, i+1)))
		h.syncNow(acc)
	}
	h.waitMail("three threads", func() bool { return len(h.items()) == 3 })
	h.click("hello alice@example.com")
	h.tt.Key(ui.Cmd, ui.KeyA)
	h.tt.Frame()
	if n := h.app.mail.main.sel.Len(); n != 3 {
		t.Fatalf("%d chosen, want 3", n)
	}
	if err := h.tt.RightClick("hello carol@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("归档"); err != nil {
		t.Fatalf("%v; menu %q", err, h.tt.Menu())
	}
	h.tt.Frame()
	h.until("both archives filled", func() bool {
		return h.countIn(h.folderOf(accs[0], "archive")) == 1 && h.countIn(h.folderOf(accs[1], "archive")) == 1
	})
	h.until("the skip told", func() bool {
		return slices.ContainsFunc(h.tt.Texts(), func(s string) bool {
			return strings.Contains(s, "dave@example.com") && strings.Contains(s, "归档")
		})
	})
	h.waitMail("dave's left", func() bool { return len(h.items()) == 1 })
}

func TestDraftsListRoute(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	if err := h.env.IMAP(accs[0].ID).User.Create("Drafts", nil); err != nil {
		t.Fatal(err)
	}
	h.syncNow(accs[0])
	ctx := context.Background()
	var keep core.Draft
	for _, subj := range []string{"Draft bye", "Draft hello"} {
		d, err := h.env.Svc.NewDraft(ctx, accs[0].ID, "new", 0)
		if err != nil {
			t.Fatal(err)
		}
		d.Subject, d.To = subj, []string{"bob@example.com"}
		if keep, err = h.env.Svc.SaveDraft(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	h.waitMail("the drafts folder", func() bool { return h.tt.HasText("草稿") })
	if n := slices.Index(h.tt.Texts(), "草稿"); n < 0 || slices.Contains(h.tt.Texts()[n+1:], "草稿") {
		t.Fatalf("one 草稿 entry expected: %q", h.tt.Texts())
	}
	h.click("草稿")
	h.see("Draft hello")
	h.see("bob@example.com")
	if err := h.tt.RightClick("Draft bye"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("删除草稿"); err != nil {
		t.Fatalf("%v; menu %q", err, h.tt.Menu())
	}
	h.tt.Frame()
	h.until("the draft deleted", func() bool { return !h.tt.HasText("Draft bye") })
	h.click("Draft hello")
	if id := h.composeOpened(0); id != keep.ID {
		t.Fatalf("opened draft %d, want %d", id, keep.ID)
	}
	h.click("写邮件")
	if id := h.composeOpened(keep.ID); id <= keep.ID {
		t.Fatalf("写邮件 opened draft %d", id)
	}
}

func TestRowUnreadCount(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	base := h.clock.Now()
	h.put(accs[0], "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Plan", "one", coretest.MessageID("a@x"), at(base, 3)))
	h.put(accs[0], "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Re: Plan", "two", coretest.MessageID("b@x"), coretest.InReplyTo("a@x"), at(base, 2)))
	h.put(accs[0], "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Re: Plan", "three", coretest.MessageID("c@x"), coretest.InReplyTo("b@x"), at(base, 1)))
	h.syncNow(accs[0])
	h.waitMail("one thread", func() bool { return len(h.items()) == 1 })
	th, err := h.env.Svc.GetThread(context.Background(), h.items()[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.env.Svc.SetFlags(context.Background(), []int64{th.Messages[0].ID}, []string{`\Seen`}, nil); err != nil {
		t.Fatal(err)
	}
	h.waitMail("two unread of three", func() bool { return h.items()[0].Unread == 2 })
	h.until("the dot labeled 2 unread", func() bool { _, ok := h.tt.Find("2 未读"); return ok })
	h.see("3")
}

func TestSearchDropsStaleResults(t *testing.T) {
	h, accs := mailHarness(t, []coretest.Option{coretest.WithoutAutoSync()}, "alice@example.com")
	h.put(accs[0], "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Invoice March", "pay"))
	h.put(accs[0], "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Lunch Plans", "eat"))
	h.syncNow(accs[0])
	h.waitMail("two threads", func() bool { return len(h.items()) == 2 })
	h.settle() // nothing else is posted from here on
	p := h.app.mail
	h.tt.Key(ui.Cmd, ui.KeyF)
	h.tt.Frame()
	p.query = "Invoice"
	p.runSearch() // A
	h.queued("search A's answer", 1)
	p.query = "Lunch"
	p.runSearch() // B
	h.queued("search B's answer", 2)
	h.mu.Lock()
	slices.Reverse(h.queue) // B answers first, A late
	h.mu.Unlock()
	h.drain()
	h.tt.Frame()
	if !p.searched || len(p.results) != 1 || p.results[0].Subject != "Lunch Plans" {
		t.Fatalf("searched %v, results %+v", p.searched, p.results)
	}
	// A late answer after leaving the search, reopened before it lands.
	p.query = "Invoice"
	p.runSearch()
	h.queued("the late answer", 1)
	h.tt.Key(0, ui.KeyEscape)
	if p.searchOpen {
		t.Fatal("Escape did not leave the search")
	}
	p.searchOpen = true
	h.drain()
	h.tt.Frame()
	if p.searched || p.results != nil {
		t.Fatalf("a late answer showed: %+v", p.results)
	}
}

func TestSelectionKeepsToMode(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	base := h.clock.Now()
	h.put(accs[0], "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "note alpha", "x", at(base, 1)))
	h.put(accs[0], "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "note beta", "x", at(base, 2)))
	h.syncNow(accs[0])
	h.waitMail("two threads", func() bool { return len(h.items()) == 2 })
	p := h.app.mail
	h.click("note alpha")
	h.tt.Key(ui.Cmd, ui.KeyA)
	h.tt.Frame()
	if p.main.sel.Len() != 2 {
		t.Fatalf("%d chosen", p.main.sel.Len())
	}
	threads, msgs := map[int64]bool{}, map[int64]bool{}
	for _, it := range h.items() {
		threads[it.ID] = true
		th, err := h.env.Svc.GetThread(context.Background(), it.ID)
		if err != nil {
			t.Fatal(err)
		}
		msgs[th.Messages[0].ID] = true
	}
	overlap := false
	for id := range msgs {
		overlap = overlap || threads[id]
	}
	if !overlap {
		t.Fatalf("fixture: thread IDs %v and message IDs %v do not overlap", threads, msgs)
	}
	h.tt.Key(ui.Cmd, ui.KeyF)
	h.typeText("note")
	h.tt.Key(0, ui.KeyEnter)
	h.until("both found", func() bool { return p.searched && len(p.results) == 2 })
	if got := p.chosen(p.shown()); len(got) != 0 {
		t.Fatalf("search results chosen by the thread list's choice: %+v", got)
	}
	h.tt.Key(0, ui.KeyEscape)
	h.tt.Frame()
	if p.main.sel.Len() != 2 || len(p.chosen(p.shown())) != 2 {
		t.Fatalf("the thread list lost its choice: %d", p.main.sel.Len())
	}
}

func TestBatchWritesInOrder(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.put(accs[0], "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "toggle me", "x"))
	h.syncNow(accs[0])
	h.waitMail("one thread", func() bool { return len(h.items()) == 1 })
	p := h.app.mail
	exec := p.exec
	entered, release := make(chan struct{}), make(chan struct{})
	var calls, done atomic.Int32
	p.exec = func(ctx context.Context, op string, ids []int64, folder int64) error {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		defer done.Add(1)
		return exec(ctx, op, ids, folder)
	}
	rows := h.items()
	p.batch("read", rows)
	h.until("the first write blocked", func() bool {
		select {
		case <-entered:
			return true
		default:
			return false
		}
	})
	p.batch("unread", rows)
	wait := time.Now().Add(200 * time.Millisecond)
	h.until("the second write's chance to overtake", func() bool { return time.Now().After(wait) })
	close(release)
	h.until("both written", func() bool { return done.Load() == 2 })
	th, err := h.env.Svc.GetThread(context.Background(), rows[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(th.Messages[0].Flags, `\Seen`) {
		t.Fatalf("read after read then unread: %v", th.Messages[0].Flags)
	}
}

func TestLocalDraftsEntry(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com") // the fake server has no Drafts folder
	d, err := h.env.Svc.NewDraft(context.Background(), accs[0].ID, "new", 0)
	if err != nil {
		t.Fatal(err)
	}
	d.Subject = "local only"
	if _, err := h.env.Svc.SaveDraft(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	h.click("草稿")
	h.see("local only")
	h.click("local only")
	if id := h.composeOpened(0); id != d.ID {
		t.Fatalf("opened draft %d, want %d", id, d.ID)
	}
}

func TestAccountFoldPersists(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com", "carol@example.com")
	h.tt.SetPreferences(ui.Preferences{ReduceMotion: true}) // the fold opens and closes at once
	count := func() int { return h.countTexts("已发送") }
	h.waitMail("both accounts' folders", func() bool { return count() == 2 })
	h.click("alice@example.com")
	if n := count(); n != 1 || *h.app.mail.accountOpen(accs[0].ID) {
		t.Fatalf("%d 已发送 folders after folding alice, want 1", n)
	}
	h.app.navigate(Route{Page: PageSettings})
	h.tt.Frame()
	h.app.navigate(Route{Page: PageMail})
	h.tt.Frame()
	h.tt.Frame()
	if n := count(); n != 1 || *h.app.mail.accountOpen(accs[0].ID) {
		t.Fatalf("%d 已发送 folders back on the mail page, want alice's still folded", n)
	}
	h.click("alice@example.com")
	if n := count(); n != 2 {
		t.Fatalf("%d 已发送 folders after unfolding alice, want 2", n)
	}
}
