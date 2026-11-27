package core

import (
	"fmt"
	"sync"
	"testing"

	"github.com/godwit-email/godwit/core/internal/testmail"
)

// BenchmarkFirstScreenAvatars loads one first screen of the mail list (issue
// #864): pageSize rows from senders no address book knows, each asking for its
// avatar at once as app/mail_list.go does, against several large enabled books.
// Remote images are "ask", so only the address-book lookup runs.
func BenchmarkFirstScreenAvatars(b *testing.B) {
	const books, cardsPerBook, rows = 3, 2000, 50
	s := newCardDAVService(b, b.TempDir(), false)
	ctx := b.Context()
	if _, err := s.db.Execute(ctx, "INSERT INTO accounts(id, email) VALUES (1, 'me@example.com')"); err != nil {
		b.Fatal(err)
	}
	if _, err := s.db.Execute(ctx, "INSERT INTO folders(id, account_id, name, role) VALUES (1, 1, 'INBOX', 'inbox')"); err != nil {
		b.Fatal(err)
	}
	st, err := s.GetSettings(ctx)
	if err != nil {
		b.Fatal(err)
	}
	st.RemoteImages = "ask"
	if _, err := s.SetSettings(ctx, st); err != nil {
		b.Fatal(err)
	}
	photo := encodeB64(makePNGBytes(b, 8, 8))
	for bk := range books {
		dav := testmail.StartCardDAV(b)
		dav.PutCollection("contacts", "Contacts")
		for i := range cardsPerBook {
			// Every tenth card has a photo; every card has two addresses, the
			// second shared with the same card in the next book, as merged contacts are.
			p := ""
			if i%10 == 0 {
				p = photo
			}
			emails := []string{fmt.Sprintf("p%d.b%d@contacts.example", i, bk), fmt.Sprintf("p%d.b%d@contacts.example", i, bk+1)}
			dav.PutCard("contacts", fmt.Sprintf("c%d", i), vcardWithPhoto(fmt.Sprintf("Person %d", i), emails, p, ""))
		}
		if _, err := s.AddAddressBook(ctx, AddressBookInput{Preset: PresetCustom, URL: dav.URL(), Username: "alice", Password: "secret"}); err != nil {
			b.Fatal(err)
		}
	}
	ids := make([]int64, rows)
	for i := range ids {
		res, err := s.db.Execute(ctx, `INSERT INTO messages(account_id, subject, from_addr, date, body_text, body_html, snippet, has_attachments)
			VALUES (1, 'hi', ?, ?, 'hi', '', 'snip', 0)`, fmt.Sprintf("Stranger %d <stranger%d@unknown.example>", i, i), i)
		if err != nil {
			b.Fatal(err)
		}
		ids[i] = res.LastInsertID
		if _, err := s.db.Execute(ctx, `INSERT INTO message_locations(message_id, folder_id, shown, entered_at, date) VALUES (?, 1, 1, 0, ?)`, ids[i], i); err != nil {
			b.Fatal(err)
		}
	}
	// A known contact's avatar still comes from the book (sanity check on the fixture).
	if _, err := s.db.Execute(ctx, `INSERT INTO messages(account_id, subject, from_addr, date, body_text, body_html, snippet, has_attachments)
		VALUES (1, 'hi', 'p10.b1@contacts.example', 0, 'hi', '', 'snip', 0)`); err != nil {
		b.Fatal(err)
	}
	if av, err := s.ContactAvatar(ctx, "p10.b1@contacts.example"); err != nil || av.Source != "carddav" {
		b.Fatalf("known contact avatar = %q, %v", av.Source, err)
	}

	screen := func(b *testing.B, load func(i int) (Avatar, error)) {
		b.ReportAllocs()
		for b.Loop() {
			var wg sync.WaitGroup
			for i := range rows {
				wg.Go(func() {
					if av, err := load(i); err != nil || len(av.Data) > 0 {
						b.Errorf("row %d: avatar %q, %v", i, av.Source, err)
					}
				})
			}
			wg.Wait()
		}
	}
	b.Run("messages", func(b *testing.B) {
		screen(b, func(i int) (Avatar, error) { return s.SenderAvatar(ctx, ids[i], false) })
	})
	b.Run("threads", func(b *testing.B) {
		screen(b, func(i int) (Avatar, error) { return s.ContactAvatar(ctx, fmt.Sprintf("stranger%d@unknown.example", i)) })
	})
	// One full read of every enabled book, for scale against a screen above.
	b.Run("one_full_read", func(b *testing.B) {
		own, err := s.ownAddrSet(ctx)
		if err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		for b.Loop() {
			if _, _, err := s.enabledCardComponents(ctx, own); err != nil {
				b.Fatal(err)
			}
		}
	})
}
