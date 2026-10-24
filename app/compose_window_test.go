package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
)

// lastCompose is the draft of the newest compose window the platform was
// asked to open; ok false for none.
func (h *harness) lastCompose() (id int64, ok bool) {
	calls := h.plat.Calls()
	for i := len(calls) - 1; i >= 0; i-- {
		if s, found := strings.CutPrefix(calls[i], "OpenCompose "); found {
			id, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				h.t.Fatal(err)
			}
			return id, true
		}
	}
	return 0, false
}

// composeOpened waits until the platform is asked to open a compose window
// for a draft other than not, and returns that draft.
func (h *harness) composeOpened(not int64) int64 {
	h.t.Helper()
	var id int64
	h.until("a compose window", func() bool {
		var ok bool
		id, ok = h.lastCompose()
		return ok && id != not
	})
	return id
}

func (h *harness) draft(id int64) core.Draft {
	h.t.Helper()
	d, err := h.env.Svc.GetDraft(context.Background(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	return d
}

// sent lets the undo window pass, waits until the account's SMTP server has n
// messages and returns them.
func (h *harness) sent(acc core.Account, n int) []string {
	h.t.Helper()
	h.env.AdvanceClock(core.UndoSendWindow)
	h.until("sent mail", func() bool { return len(h.env.Sent(acc.ID)) >= n })
	var out []string
	for _, m := range h.env.Sent(acc.ID) {
		out = append(out, string(m))
	}
	return out
}

func TestSidebarComposeOpensWindow(t *testing.T) {
	h, accs := mailHarness(t, nil, "me@example.com")
	route := h.app.route
	h.click("写邮件")
	id := h.composeOpened(0)
	if d := h.draft(id); d.AccountID != accs[0].ID || d.Mode != "new" {
		t.Fatalf("draft %+v", d)
	}
	if h.app.route != route {
		t.Fatalf("the main window navigated to %+v", h.app.route)
	}
	h.app.Compose() // ⌘N and the tray
	if next := h.composeOpened(id); next <= id {
		t.Fatalf("⌘N opened draft %d after %d", next, id)
	}
}

func TestMailtoOpensWindow(t *testing.T) {
	h, accs := mailHarness(t, nil, "me@example.com")
	h.app.openLink("mailto:lena@example.com?cc=max@example.com&subject=Hi+there%21")
	d := h.draft(h.composeOpened(0))
	if d.AccountID != accs[0].ID || len(d.To) != 1 || d.To[0] != "lena@example.com" || len(d.Cc) != 1 || d.Subject != "Hi+there!" {
		t.Fatalf("draft %+v", d)
	}
}

// A page's mailto (the Wit page's) drafts from the default account, not the
// account of the message the reader last showed.
func TestPageMailtoFromDefaultAccount(t *testing.T) {
	h, accs := mailHarness(t, nil, "me@example.com", "work@example.com")
	m := h.deliverOne(accs[1], coretest.Msg("Bob <bob@example.com>", accs[1].Email, "Hi", "hi"))
	h.openThread(m.ThreadID)
	h.until("the reader showing the work account's message", func() bool { return h.app.mail.reader.account() == accs[1].ID })
	h.app.OpenLink("mailto:lena@example.com")
	if d := h.draft(h.composeOpened(0)); d.AccountID != h.app.defaultAccount() || d.AccountID == accs[1].ID {
		t.Fatalf("draft from account %d, default %d", d.AccountID, h.app.defaultAccount())
	}
}

func TestSettingsChangedReachesPlatform(t *testing.T) {
	h, _ := withAccount(t)
	h.openSettings("general")
	h.click("深色")
	h.called("SettingsChanged dark")
}

// A draft deleted from the drafts list closes its compose window: the
// platform hears DraftDeleted once the draft is gone.
func TestDeleteDraftNotifiesPlatform(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	if err := h.env.IMAP(accs[0].ID).User.Create("Drafts", nil); err != nil {
		t.Fatal(err)
	}
	h.syncNow(accs[0])
	d, err := h.env.Svc.NewDraft(context.Background(), accs[0].ID, "new", 0)
	if err != nil {
		t.Fatal(err)
	}
	d.Subject = "Draft bye"
	if _, err := h.env.Svc.SaveDraft(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	h.waitMail("the drafts folder", func() bool { return h.tt.HasText("草稿") })
	h.click("草稿")
	h.see("Draft bye")
	if err := h.tt.RightClick("Draft bye"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("删除草稿"); err != nil {
		t.Fatal(err)
	}
	h.tt.Frame()
	h.called(fmt.Sprint("DraftDeleted ", d.ID))
	if _, err := h.env.Svc.GetDraft(context.Background(), d.ID); err == nil {
		t.Fatalf("DraftDeleted %d told while the draft is still there", d.ID)
	}
}
