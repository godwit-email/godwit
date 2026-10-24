package app

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core/coretest"
)

// unsubHarness is a mail harness whose one-click unsubscribes reach a TLS
// server answering status; posts counts what it got.
func unsubHarness(t *testing.T, status int) (h *harness, url string, posts *atomic.Int32) {
	posts = &atomic.Int32{}
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		w.WriteHeader(status)
	}))
	t.Cleanup(ts.Close)
	h, _ = mailHarness(t, []coretest.Option{coretest.WithUnsubscribeClient(ts.Client())}, "alice@example.com")
	return h, ts.URL + "/u?t=1", posts
}

// showNewsletter delivers a message with hdr (List-* lines) and shows it.
func (h *harness) showNewsletter(hdr ...string) {
	h.t.Helper()
	var opts []coretest.MsgOption
	for _, l := range hdr {
		opts = append(opts, coretest.Header(l))
	}
	acc := h.app.accounts[0]
	m := h.deliverOne(acc, coretest.Msg("Shop <news@shop.example>", acc.Email, "Deals", "Hello.", opts...))
	h.openThread(m.ThreadID)
	h.seeBody(m.ID)
}

func TestReaderOneClickUnsubscribe(t *testing.T) {
	h, link, posts := unsubHarness(t, http.StatusOK)
	h.showNewsletter("List-Unsubscribe: <"+link+">", "List-Unsubscribe-Post: List-Unsubscribe=One-Click", "List-Id: Shop deals <deals.shop.example>")
	h.click("退订")
	h.see("127.0.0.1")
	h.see("Shop deals")
	if posts.Load() != 0 {
		t.Fatal("posted before confirming")
	}
	h.tt.Key(0, ui.KeyEnter) // the dialog's 退订
	h.see("已向 127.0.0.1 发送退订请求")
	if n := posts.Load(); n != 1 {
		t.Fatalf("%d posts", n)
	}
}

func TestReaderOneClickUnsubscribeFailureOpensPage(t *testing.T) {
	h, link, _ := unsubHarness(t, http.StatusFound)
	h.showNewsletter("List-Unsubscribe: <"+link+">", "List-Unsubscribe-Post: List-Unsubscribe=One-Click")
	h.click("退订")
	h.tt.Key(0, ui.KeyEnter)
	h.see("要求跳转")
	h.click("在浏览器中打开退订页")
	h.see("/u?t=1") // the link confirmation, as a body link's
	h.click("打开")
	if got := h.callArg("OpenURL"); got != link {
		t.Fatalf("opened %q", got)
	}
}

func TestReaderWebUnsubscribeConfirmsLink(t *testing.T) {
	h, link, posts := unsubHarness(t, http.StatusOK)
	h.showNewsletter("List-Unsubscribe: <" + link + ">")
	h.click("退订")
	h.see("/u?t=1")
	h.click("打开")
	if got := h.callArg("OpenURL"); got != link || posts.Load() != 0 {
		t.Fatalf("opened %q, %d posts", got, posts.Load())
	}
}

func TestReaderMailtoUnsubscribeOpensDraft(t *testing.T) {
	h, _, _ := unsubHarness(t, http.StatusOK)
	h.showNewsletter("List-Unsubscribe: <mailto:leave@shop.example?subject=unsubscribe>")
	h.click("退订")
	d := h.draft(h.composeOpened(0))
	if len(d.To) != 1 || d.To[0] != "leave@shop.example" || d.Subject != "unsubscribe" || d.AccountID != h.app.accounts[0].ID {
		t.Fatalf("draft %+v", d)
	}
}

func TestReaderNoUnsubscribeWithoutHeader(t *testing.T) {
	h, _, _ := unsubHarness(t, http.StatusOK)
	h.showNewsletter()
	h.drain()
	h.tt.Frame()
	if _, ok := h.tt.Find("退订"); ok {
		t.Fatal("退订 shown for a message without List-Unsubscribe")
	}
}
