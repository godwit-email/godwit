package app

import (
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core/coretest"
)

// A mail chosen in the palette shows a card of it: its sender, subject,
// the start of its body and its attachments. A row of another kind, an
// attachment or a command, shows none.
func TestPalettePreview(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.put(accs[0], "INBOX", coretest.Msg("Bob <bob@example.com>", "alice@example.com", "Quarterly report",
		"The numbers for review are attached.", coretest.Attachment("q3.xlsx", "application/vnd.ms-excel", []byte("x"))))
	h.syncNow(accs[0])
	h.waitMail("a thread", func() bool { return len(h.items()) == 1 })
	h.tt.Key(ui.Cmd, ui.KeyK)
	h.until("the palette", func() bool { return paletteShown(h) })

	h.typeText("report")
	h.until("the card", func() bool {
		return inPalette(h, "The numbers for review are attached.") && inPalette(h, "Bob") && inPalette(h, "q3.xlsx")
	})
	h.see("Quarterly report")
	att, _ := h.tt.Find("q3.xlsx") // only the card has it
	tab, _ := h.tt.Find("联系人")
	if att.X < 1280/2+640/2+8 || att.Y < tab.Y {
		t.Fatalf("the card at %+v is not right of the panel, below its tabs at %+v", att, tab)
	}

	h.tt.Key(ui.Cmd, ui.KeyA)
	h.typeText("q3") // the attachment alone
	h.until("no card on an attachment", func() bool {
		return inPalette(h, "附件 · 1") && !inPalette(h, "The numbers for review are attached.")
	})

	h.tt.Key(ui.Cmd, ui.KeyA)
	h.tt.Key(0, ui.KeyBackspace) // nothing typed: the mail, its attachment, the commands
	h.until("the card again", func() bool { return inPalette(h, "The numbers for review are attached.") })
	h.tt.Key(0, ui.KeyUp) // wraps to the last command
	h.until("no card on a command", func() bool { return !inPalette(h, "The numbers for review are attached.") })
}

// Opening the filter list with / hides the preview card.
func TestPalettePreviewHiddenWhileFiltering(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.put(accs[0], "INBOX", coretest.Msg("Bob <bob@example.com>", "alice@example.com", "Quarterly report", "The numbers for review are attached."))
	h.syncNow(accs[0])
	h.waitMail("a thread", func() bool { return len(h.items()) == 1 })
	h.tt.Key(ui.Cmd, ui.KeyK)
	h.until("the palette", func() bool { return paletteShown(h) })
	h.until("the preview card", func() bool { return inPalette(h, "The numbers for review are attached.") })

	h.typeText("/")
	h.until("the filter list", func() bool { return filtersShown(h) })
	if inPalette(h, "The numbers for review are attached.") {
		t.Fatal("preview card still shown while filter list is open")
	}

	h.tt.Key(0, ui.KeyEscape)
	h.until("the card restored once filters close", func() bool { return inPalette(h, "The numbers for review are attached.") })
}

// The card's avatar keeps to the remote-image setting: none is fetched
// while remote images are asked for, the initial shows instead.
func TestPalettePreviewAvatarPolicy(t *testing.T) {
	for _, tc := range []struct {
		mode    string
		fetched bool
	}{{"ask", false}, {"auto", true}} {
		t.Run(tc.mode, func(t *testing.T) {
			var hits atomic.Int64
			h, acc := avatarHarness(t, tc.mode, func(w http.ResponseWriter, req *http.Request) { hits.Add(1); w.Write(readerAvatarSVG) })
			h.deliverOne(acc, coretest.Msg("Bob <bob@example.com>", acc.Email, "Avatar", "plain body"))
			h.tt.Key(ui.Cmd, ui.KeyK)
			h.until("the palette", func() bool { return paletteShown(h) })
			h.typeText("avatar")
			h.until("the card", func() bool { return inPalette(h, "plain body") })
			if tc.fetched {
				h.until("the avatar", func() bool { return hits.Load() > 0 && !inPalette(h, "B") })
				return
			}
			h.until("the initial", func() bool { return inPalette(h, "B") })
			for range 5 {
				h.tt.Frame()
				h.drain()
			}
			if hits.Load() != 0 {
				t.Fatalf("fetched %d avatars", hits.Load())
			}
		})
	}
}
