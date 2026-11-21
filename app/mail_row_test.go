package app

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/internal/theme"
)

// A row is the sender's avatar beside one column of text: who, the subject,
// the snippet, and the labels under it, all at the column's left edge.
func TestMailRowAvatar(t *testing.T) {
	h, _, m := labeledMail(t)
	h.autoLabel(m.ID, "otp")
	h.waitMail("the row's chip", func() bool { return h.tt.HasText("验证码") && h.tt.HasText("Check-in open") })
	// The sidebar's labels group lists the label too, and Find takes the first
	// of two: fold the group (a refresh brings its counts back, not its fold),
	// then wait for the row's own chips to load.
	h.post(func() { h.app.mail.labelsOpen = false })
	h.until("the row's chip alone", func() bool {
		h.drain()
		h.tt.Frame()
		items := h.items()
		return len(items) > 0 && len(items[0].Labels) > 0 && h.countExact("验证码") == 1
	})
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

// Mail list rows show real sender avatars under the remote-image policy
// (deduplicated per sender address), leave initials while remote images are
// asked for or in Junk, and reload when the policy changes.
func TestMailRowAvatarPolicy(t *testing.T) {
	for _, tc := range []struct {
		mode    string
		fetched bool
	}{{"ask", false}, {"auto", true}} {
		t.Run(tc.mode, func(t *testing.T) {
			var hits atomic.Int64
			h, acc := avatarHarness(t, tc.mode, func(w http.ResponseWriter, req *http.Request) {
				hits.Add(1)
				w.Write(readerAvatarSVG)
			})
			h.deliverOne(acc, coretest.Msg("Bob <bob@example.com>", acc.Email, "First", "plain body"))
			h.deliverOne(acc, coretest.Msg("Bob <bob@example.com>", acc.Email, "Second", "another body"))
			h.waitMail("both rows", func() bool { return len(h.items()) == 2 })
			initial := func() bool { return slices.Contains(h.tt.Texts(), "B") }
			if tc.fetched {
				h.until("list avatar loaded", func() bool { h.drain(); h.tt.Frame(); return hits.Load() > 0 && !initial() })
				if hits.Load() != 1 {
					t.Fatalf("expected 1 deduplicated fetch for two rows from bob@example.com, got %d", hits.Load())
				}
				return
			}
			h.until("initial shown while blocked", initial)
			for range 5 {
				h.tt.Frame()
				h.drain()
			}
			if hits.Load() != 0 {
				t.Fatalf("fetched %d avatars while remote images are asked for", hits.Load())
			}
			// Enabling auto reloads visible list rows immediately.
			st := h.app.settings
			st.RemoteImages = "auto"
			h.app.changeSettings(st)
			h.until("list avatar after switching to auto", func() bool { h.drain(); h.tt.Frame(); return hits.Load() > 0 && !initial() })
		})
	}

	// Junk rows keep initial fallbacks even when RemoteImages is auto.
	t.Run("junk", func(t *testing.T) {
		var hits atomic.Int64
		h, acc := avatarHarness(t, "auto", func(w http.ResponseWriter, req *http.Request) {
			hits.Add(1)
			w.Write(readerAvatarSVG)
		})
		if err := h.env.IMAP(acc.ID).User.Create("Junk", nil); err != nil {
			t.Fatal(err)
		}
		h.syncNow(acc)
		h.env.Deliver(t, acc.ID, "Junk", coretest.Msg("Spammer <spam@example.org>", acc.Email, "Offer", "spam"))
		junk := h.folderOf(acc, "junk")
		h.post(func() { h.app.navigate(Route{Page: PageMail, View: core.View{FolderID: junk.ID}}) })
		h.waitMail("junk row", func() bool { return len(h.items()) == 1 })
		for range 5 {
			h.tt.Frame()
			h.drain()
		}
		if hits.Load() != 0 || !slices.Contains(h.tt.Texts(), "S") {
			t.Fatalf("junk row fetched %d avatars, texts %q", hits.Load(), h.tt.Texts())
		}
	})
}
