package coretest_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAddressBookDiscoveryCRUDAndRedact(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	ctx := t.Context()

	presets := e.Svc.ListAddressBookPresets()
	require.Len(t, presets, 4)
	assert.Equal(t, []string{"icloud", "fastmail", "google", "custom"}, []string{
		presets[0].ID, presets[1].ID, presets[2].ID, presets[3].ID,
	})
	assert.Equal(t, "https://contacts.icloud.com", presets[0].DefaultURL)
	assert.Equal(t, "https://carddav.fastmail.com", presets[1].DefaultURL)
	assert.Equal(t, "oauth", presets[2].AuthType)

	srv1 := e.StartCardDAV(t)
	srv1.PutCollection("work", "Work")
	srv1.PutCollection("family", "Family")
	srv1.PutCard("work", "bob", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Bob Chen\r\nEMAIL:bob@example.com\r\nEND:VCARD\r\n")
	srv1.PutCard("work", "carol", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Carol Wu\r\nEMAIL:carol@example.com\r\nEND:VCARD\r\n")
	srv1.PutCard("family", "mom", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Mom\r\nEMAIL:mom@example.com\r\nEND:VCARD\r\n")

	// Non-loopback HTTP is rejected; http:// redirect downgrade is rejected.
	_, _, err := e.Svc.CheckAddressBook(ctx, core.AddressBookInput{
		Preset: "custom", URL: "http://nextcloud.example.com", Username: "alice", Password: "secret",
	})
	require.ErrorIs(t, err, core.ErrBaseURLScheme)

	// Wrong credentials return backend.ErrAuthFailed without saving.
	_, _, err = e.Svc.CheckAddressBook(ctx, core.AddressBookInput{
		Preset: "custom", URL: srv1.URL(), Username: "alice", Password: "wrong-password",
	})
	require.ErrorIs(t, err, backend.ErrAuthFailed)

	// Discovery via /.well-known/carddav lists sub-collections and counts without saving.
	cols, homeURL, err := e.Svc.CheckAddressBook(ctx, core.AddressBookInput{
		Preset: "custom", URL: srv1.URL(), Username: "alice", Password: "secret",
	})
	require.NoError(t, err)
	assert.Equal(t, srv1.HomeSetURL(), homeURL)
	require.Len(t, cols, 2)
	assert.Equal(t, "Work", cols[0].Name)
	assert.Equal(t, 2, cols[0].CardCount)
	assert.True(t, cols[0].Enabled)
	assert.Equal(t, "Family", cols[1].Name)
	assert.Equal(t, 1, cols[1].CardCount)

	list, err := e.Svc.ListAddressBooks(ctx)
	require.NoError(t, err)
	assert.Empty(t, list)

	// Direct principal URL and direct home-set URL also discover cleanly.
	cols, _, err = e.Svc.CheckAddressBook(ctx, core.AddressBookInput{
		Preset: "custom", URL: srv1.PrincipalURL(), Username: "alice", Password: "secret",
	})
	require.NoError(t, err)
	assert.Len(t, cols, 2)

	ch, unsub := e.Svc.Subscribe()
	defer unsub()

	const appPass = "app-pass-9876543210"
	srv1.SetBasicAuth("alice", appPass)
	ab1, err := e.Svc.AddAddressBook(ctx, core.AddressBookInput{
		Preset: "custom", Name: "Company", URL: srv1.URL(), Username: "alice", Password: appPass,
	})
	require.NoError(t, err)
	assert.Equal(t, "Company", ab1.Name)
	assert.Equal(t, "ok", ab1.Status)
	assert.False(t, ab1.LastSyncedAt.IsZero())
	require.Len(t, ab1.Collections, 2)
	assert.Contains(t, e.Svc.Redact("err="+appPass), "[redacted]")
	assert.NotContains(t, e.Svc.Redact("err="+appPass), appPass)

	srv2 := e.StartCardDAV(t)
	srv2.PutCard("contacts", "dan", "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Dan Li\r\nEMAIL:dan@example.com\r\nEND:VCARD\r\n")
	ab2, err := e.Svc.AddAddressBook(ctx, core.AddressBookInput{
		Preset: "fastmail", Name: "Personal", URL: srv2.HomeSetURL(), Username: "alice", Password: "secret",
	})
	require.NoError(t, err)

	// Reordering the accounts list swaps priority.
	require.NoError(t, e.Svc.ReorderAccountEntries(ctx, []core.AccountEntry{{AddressBook: ab2.ID}, {AddressBook: ab1.ID}}))
	books, err := e.Svc.ListAddressBooks(ctx)
	require.NoError(t, err)
	require.Len(t, books, 2)
	assert.Equal(t, []int64{ab2.ID, ab1.ID}, []int64{books[0].ID, books[1].ID})
	assert.Equal(t, 0, books[0].SortOrder)
	assert.Equal(t, 1, books[1].SortOrder)

	// RemoveAddressBook deletes DB rows and emits AddressBooksChanged.
	require.NoError(t, e.Svc.RemoveAddressBook(ctx, ab1.ID))
	books, err = e.Svc.ListAddressBooks(ctx)
	require.NoError(t, err)
	require.Len(t, books, 1)
	assert.Equal(t, ab2.ID, books[0].ID)
	require.ErrorIs(t, e.Svc.RemoveAddressBook(ctx, ab1.ID), core.ErrAddressBookNotFound)

	sawEvent := false
	for len(ch) > 0 {
		if (<-ch).Type == events.AddressBooksChanged {
			sawEvent = true
		}
	}
	assert.True(t, sawEvent, "expected AddressBooksChanged event")
}

func TestAddressBookIncrementalPollingAndSyncTokenRecovery(t *testing.T) {
	// AutoSync enabled (default): tests SetClockForTest re-arming and 30m polling.
	e := coretest.New(t)
	ctx := t.Context()

	srv := e.StartCardDAV(t)
	srv.PutCard("contacts", "alice", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Alice\r\nEMAIL:alice@example.com\r\nEND:VCARD\r\n")
	srv.PutCard("contacts", "bob", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Bob\r\nEMAIL:bob@example.com\r\nEND:VCARD\r\n")

	ab, err := e.Svc.AddAddressBook(ctx, core.AddressBookInput{
		Preset: core.PresetCustom, Name: "Team", URL: srv.URL(), Username: "alice", Password: "secret",
	})
	require.NoError(t, err)
	require.Len(t, ab.Collections, 1)
	assert.Equal(t, 2, ab.Collections[0].CardCount)

	// Mutate remote cards: add carol, update bob, delete alice.
	srv.ResetRequests()
	srv.PutCard("contacts", "carol", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Carol\r\nEMAIL:carol@example.com\r\nEND:VCARD\r\n")
	srv.PutCard("contacts", "bob", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Bob Updated\r\nEMAIL:bob@example.com\r\nEND:VCARD\r\n")
	srv.DeleteCard("contacts", "alice")

	// Advance clock by 30 minutes -> background scheduler runs incremental sync-collection.
	e.AdvanceClock(core.AddressBookSyncInterval)
	require.Eventually(t, func() bool {
		reqs := srv.Requests()
		return slices.ContainsFunc(reqs, func(r coretest.CardDAVRequest) bool { return r.ReportKind == "sync-collection" }) &&
			slices.ContainsFunc(reqs, func(r coretest.CardDAVRequest) bool { return r.ReportKind == "addressbook-multiget" })
	}, 5*time.Second, 20*time.Millisecond)

	require.Eventually(t, func() bool {
		books, err := e.Svc.ListAddressBooks(ctx)
		return err == nil && len(books) == 1 && books[0].Collections[0].CardCount == 2 && books[0].LastSyncedAt.After(ab.LastSyncedAt)
	}, 5*time.Second, 20*time.Millisecond)

	// Verify sync-collection did NOT fall back to collection-level PROPFIND on a valid token.
	for _, r := range srv.Requests() {
		assert.False(t, r.Method == "PROPFIND" && r.Path == "/dav/addressbooks/alice/contacts/",
			"incremental sync should use sync-collection, not full PROPFIND")
	}

	// Expire all sync-tokens on server, add dan, delete bob -> next 30m poll self-heals via PROPFIND fallback.
	srv.ResetRequests()
	srv.InvalidateSyncTokens()
	srv.PutCard("contacts", "dan", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Dan\r\nEMAIL:dan@example.com\r\nEND:VCARD\r\n")
	srv.DeleteCard("contacts", "bob")

	e.AdvanceClock(core.AddressBookSyncInterval)
	require.Eventually(t, func() bool {
		reqs := srv.Requests()
		sawSync := slices.ContainsFunc(reqs, func(r coretest.CardDAVRequest) bool { return r.ReportKind == "sync-collection" })
		sawPropfind := slices.ContainsFunc(reqs, func(r coretest.CardDAVRequest) bool {
			return r.Method == "PROPFIND" && r.Path == "/dav/addressbooks/alice/contacts/"
		})
		books, err := e.Svc.ListAddressBooks(ctx)
		return sawSync && sawPropfind && err == nil && len(books) == 1 && books[0].Collections[0].CardCount == 2
	}, 5*time.Second, 20*time.Millisecond)

	// Password rotation -> auth_rejected pauses background retries until UpdateAddressBookAuth.
	srv.SetBasicAuth("alice", "rotated-pass-9999")
	e.AdvanceClock(core.AddressBookSyncInterval)
	require.Eventually(t, func() bool {
		books, err := e.Svc.ListAddressBooks(ctx)
		return err == nil && len(books) == 1 && books[0].Status == "reauth_required"
	}, 5*time.Second, 20*time.Millisecond)

	srv.ResetRequests()
	e.AdvanceClock(core.AddressBookSyncInterval)
	time.Sleep(50 * time.Millisecond)
	assert.Empty(t, srv.Requests(), "reauth_required address book pauses background polling")

	fixed, err := e.Svc.UpdateAddressBookAuth(ctx, ab.ID, "rotated-pass-9999", "", core.OAuthClient{})
	require.NoError(t, err)
	assert.Equal(t, "ok", fixed.Status)
	assert.Contains(t, e.Svc.Redact("pw=rotated-pass-9999"), "[redacted]")
}

func TestAddressBookRedirectSecurityAndStartupSync(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	ctx := t.Context()

	front := e.StartCardDAV(t)
	back := e.StartCardDAV(t)
	back.PutCard("contacts", "one", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:One\r\nEMAIL:one@example.com\r\nEND:VCARD\r\n")

	// Downgrade redirect from https -> http is rejected without sending credentials to http.
	front.SetWellKnownRedirect("http://127.0.0.1:9/dav/")
	_, _, err := e.Svc.CheckAddressBook(ctx, core.AddressBookInput{
		Preset: core.PresetCustom, URL: front.URL(), Username: "alice", Password: "secret",
	})
	require.ErrorIs(t, err, core.ErrBaseURLScheme)

	// Cross-host HTTPS redirect during /.well-known/carddav discovery (like Google CardDAV) succeeds.
	front.SetWellKnownRedirect(back.PrincipalURL())
	ab, err := e.Svc.AddAddressBook(ctx, core.AddressBookInput{
		Preset: core.PresetCustom, URL: front.URL(), Username: "alice", Password: "secret",
	})
	require.NoError(t, err)
	assert.Equal(t, back.HomeSetURL(), ab.URL)
	assert.Equal(t, 1, ab.Collections[0].CardCount)

	// Empty server with zero card:addressbook collections returns ErrNoAddressBooks.
	empty := e.StartCardDAV(t)
	empty.SetNoCollections(true)
	_, _, err = e.Svc.CheckAddressBook(ctx, core.AddressBookInput{
		Preset: core.PresetCustom, URL: empty.URL(), Username: "alice", Password: "secret",
	})
	require.ErrorIs(t, err, core.ErrNoAddressBooks)

	// Close service, add a second card while offline, reopen with AutoSync: true -> startup sync pulls it.
	require.NoError(t, e.Svc.Close())
	back.PutCard("contacts", "two", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Two\r\nEMAIL:two@example.com\r\nEND:VCARD\r\n")

	reopened, err := core.New(context.Background(), core.Options{DataDir: e.DataDir, AutoSync: true})
	require.NoError(t, err)
	defer reopened.Close()
	require.Eventually(t, func() bool {
		books, err := reopened.ListAddressBooks(ctx)
		return err == nil && len(books) == 1 && books[0].Collections[0].CardCount == 2
	}, 5*time.Second, 20*time.Millisecond)
}
