package app

import (
	"context"
	"fmt"
	"math"
	"testing"

	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/internal/theme"
)

// A row is the sender's avatar beside one column of text: who, the subject,
// the snippet, and the labels under it, all at the column's left edge.
func TestMailRowAvatar(t *testing.T) {
	h, _, m := labeledMail(t)
	h.autoLabel(m.ID, "otp")
	h.waitMail("the row's chip", func() bool { return h.tt.HasText("验证码") && h.tt.HasText("Check-in open") })
	// The sidebar lists the label too, and Find takes the first of two: with
	// no counts the sidebar drops its labels section; the row's chips stay.
	h.post(func() {
		for i := range h.app.labels {
			h.app.labels[i].Count = 0
		}
	})
	h.drain()
	h.tt.Frame()
	initial, ok := h.tt.Find("B")
	if !ok {
		t.Fatalf("no avatar: %q", h.tt.Texts())
	}
	who, _ := h.tt.Find("bob@example.com")
	subject, _ := h.tt.Find("Check-in open")
	snippet, _ := h.tt.Find("your flight")
	chip, _ := h.tt.Find("验证码")
	if initial.X+initial.W > who.X {
		t.Errorf("avatar at %v overlaps the text at %v", initial, who)
	}
	for name, x := range map[string]float32{"subject": subject.X, "snippet": snippet.X} {
		if math.Abs(float64(x-who.X)) > 1 {
			t.Errorf("%s at x %v, sender at x %v: want one text column", name, x, who.X)
		}
	}
	// The chip starts the column; its name follows its padding and icon.
	if chip.X < who.X || chip.X-who.X > theme.LabelChip+theme.IconXs {
		t.Errorf("chip name at x %v, sender at x %v: want the chip at the text column", chip.X, who.X)
	}
	if !(who.Y < subject.Y && subject.Y < snippet.Y && snippet.Y < chip.Y) {
		t.Errorf("lines out of order: %v %v %v %v", who, subject, snippet, chip)
	}
}

// The avatar decorates: it neither names its sender (the first line does) nor
// takes the row's click.
func TestMailRowAvatarIsDecoration(t *testing.T) {
	h, _, m := labeledMail(t)
	initial, ok := h.tt.Find("B")
	if !ok {
		t.Fatalf("no avatar: %q", h.tt.Texts())
	}
	if h.countExact("bob@example.com") != 1 {
		t.Fatalf("the sender is named %d times: %q", h.countExact("bob@example.com"), h.tt.Texts())
	}
	h.tt.ClickAt(initial.X+initial.W/2, initial.Y+initial.H/2)
	h.until("the thread opened", func() bool {
		h.tt.Frame()
		return h.app.mail.reader.key[0] == m.ThreadID
	})
}

// A search result is a row too: avatar and one column.
func TestSearchResultAvatar(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.deliverOne(accs[0], coretest.Msg("Bob <bob@example.com>", accs[0].Email, "Plan", "first words"))
	p := h.app.mail
	p.searchOpen = true
	p.query = "Plan"
	p.runSearch()
	h.until("the result", func() bool { h.tt.Frame(); return p.searched && len(p.results) > 0 && h.tt.HasText("first words") })
	initial, ok := h.tt.Find("B")
	if !ok {
		t.Fatalf("no avatar: %q", h.tt.Texts())
	}
	who, _ := h.tt.Find("Bob")
	if initial.X+initial.W > who.X {
		t.Errorf("avatar at %v overlaps the sender at %v", initial, who)
	}
}

// The date and the icons at a row's right end never shrink: a long sender
// truncates first, so the date keeps its one line at the list's width.
func TestMailRowDateStaysOnOneLine(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	acc := accs[0]
	base := h.clock.Now()
	senders := []string{
		"Alexandria Montgomery-Fitzgerald <alexandria@example.com>",
		"Bartholomew Wolfeschlegelstein <bart@example.com>",
		"Cornelius Featherstonehaugh <cornelius@example.com>",
	}
	for i, from := range senders {
		opts := []coretest.MsgOption{coretest.MessageID(fmt.Sprintf("l%d@x", i)), at(base, 60-i)}
		if i > 0 {
			opts = append(opts, coretest.InReplyTo(fmt.Sprintf("l%d@x", i-1)))
		} else {
			opts = append(opts, coretest.Attachment("plan.pdf", "application/pdf", make([]byte, 1024)))
		}
		h.put(acc, "INBOX", coretest.Msg(from, acc.Email, "Long thread", "body", opts...))
	}
	h.put(acc, "INBOX", coretest.Msg("bob@example.com", acc.Email, "Short", "body", coretest.MessageID("s@x"), at(base, 0)))
	h.syncNow(acc)
	h.waitMail("both rows", func() bool { return len(h.items()) == 2 })
	var long, short mailItem
	for _, it := range h.items() {
		if it.Subject == "Long thread" {
			long = it
		} else {
			short = it
		}
	}
	var ids []int64
	for _, m := range h.messages(long.ID) {
		ids = append(ids, m.ID)
	}
	if err := h.env.Svc.SetFlags(context.Background(), ids, []string{`\Flagged`}, nil); err != nil {
		t.Fatal(err)
	}
	h.waitMail("the long row starred", func() bool {
		for _, it := range h.items() {
			if it.Subject == "Long thread" {
				return it.Flagged > 0 && it.Attach && it.Count == len(senders)
			}
		}
		return false
	})
	now := h.app.clock.Now()
	longDate, ok := h.tt.Find(listTime(now, long.Date))
	if !ok {
		t.Fatalf("no date for the long row: %q", h.tt.Texts())
	}
	shortDate, ok := h.tt.Find(listTime(now, short.Date))
	if !ok {
		t.Fatalf("no date for the short row: %q", h.tt.Texts())
	}
	if math.Abs(float64(longDate.H-shortDate.H)) > 1 || math.Abs(float64(longDate.W)) < 1 {
		t.Errorf("the long row's date is %v, the short row's one line %v: it wrapped", longDate, shortDate)
	}
}
