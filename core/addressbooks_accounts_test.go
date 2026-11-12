package core

import (
	"errors"
	"testing"

	"github.com/godwit-email/godwit/core/internal/testmail"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

// addLinkableAccount adds a mail account of provider on fake servers, signing
// in as alice with password secret.
func addLinkableAccount(t *testing.T, s *Service, provider, email string) Account {
	t.Helper()
	smtpAddr, _ := testmail.StartSMTP(t)
	im := testmail.StartIMAP(t)
	in := internalInput(t, im.Addr, smtpAddr, email, false)
	in.Provider = provider
	acc, err := s.AddAccount(bg, in)
	require.NoError(t, err)
	return acc
}

// reorderBooks puts the standalone address books in ids' order after the mail accounts.
func reorderBooks(t *testing.T, s *Service, ids ...int64) {
	t.Helper()
	accs, err := s.ListAccounts(bg)
	require.NoError(t, err)
	var es []AccountEntry
	for _, a := range accs {
		es = append(es, AccountEntry{Account: a.ID})
	}
	for _, id := range ids {
		es = append(es, AccountEntry{AddressBook: id})
	}
	require.NoError(t, s.ReorderAccountEntries(bg, es))
}

func TestLinkedAddressBookReusesAccountPassword(t *testing.T) {
	s := newCardDAVService(t, t.TempDir(), false)
	srv := testmail.StartCardDAV(t) // alice / secret, as the account
	srv.PutCard("contacts", "bob", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Bob\r\nEMAIL:bob@example.com\r\nEND:VCARD\r\n")
	SetAddressBookPresetURLForTest(t, PresetICloud, srv.URL())

	other := addLinkableAccount(t, s, "other", "a@example.com")
	_, err := s.AddAddressBook(bg, AddressBookInput{Account: other.ID, Preset: PresetCustom, URL: srv.URL()})
	require.Error(t, err, "an account whose provider has no CardDAV cannot link")

	acc := addLinkableAccount(t, s, "icloud", "alice@icloud.com")
	assert.Equal(t, PresetICloud, LinkedAddressBookPreset(acc))
	_, err = s.AddAddressBook(bg, AddressBookInput{Account: acc.ID, Preset: PresetFastmail})
	require.Error(t, err, "the preset must be the account's")

	ab, err := s.AddAddressBook(bg, AddressBookInput{Account: acc.ID, Preset: PresetICloud})
	require.NoError(t, err)
	assert.Equal(t, acc.ID, ab.Account)
	assert.Equal(t, "ok", ab.Status)
	require.Len(t, ab.Collections, 1, "one collection: the UI shows its count, not a list")
	assert.Equal(t, 1, ab.Collections[0].CardCount)
	_, err = keychainGet(s.addressBookKeyringUser(ab.ID))
	assert.ErrorIs(t, err, keyring.ErrNotFound, "a linked address book reads the account's password, no copy")

	// A second link to the same account is refused.
	_, err = s.AddAddressBook(bg, AddressBookInput{Account: acc.ID, Preset: PresetICloud})
	require.Error(t, err)

	require.NoError(t, s.SyncAddressBook(bg, ab.ID))

	require.NoError(t, s.RenameAddressBook(bg, ab.ID, " 家里的 iCloud "))
	require.Error(t, s.RenameAddressBook(bg, ab.ID, " "))
	books, err := s.ListAddressBooks(bg)
	require.NoError(t, err)
	assert.Equal(t, "家里的 iCloud", books[0].Name)
}

func TestLinkedAddressBookRejectedPasswordWaitsForItsOwn(t *testing.T) {
	s := newCardDAVService(t, t.TempDir(), false)
	srv := testmail.StartCardDAV(t)
	srv.SetBasicAuth("alice", "carddav-only-pass")
	srv.PutCollection("work", "Work")
	srv.PutCollection("family", "Family")
	srv.PutCard("work", "bob", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Bob\r\nEMAIL:bob@example.com\r\nEND:VCARD\r\n")
	SetAddressBookPresetURLForTest(t, PresetFastmail, srv.URL())
	acc := addLinkableAccount(t, s, "fastmail", "alice@fastmail.com")

	ab, err := s.AddAddressBook(bg, AddressBookInput{Account: acc.ID, Preset: PresetFastmail})
	require.NoError(t, err, "a rejected reused password still links, waiting for one of its own")
	assert.Equal(t, "reauth_required", ab.Status)
	assert.Empty(t, ab.Collections)

	_, err = s.UpdateAddressBookAuth(bg, ab.ID, "carddav-only-pass", "", OAuthClient{})
	require.NoError(t, err)
	books, err := s.ListAddressBooks(bg)
	require.NoError(t, err)
	require.Len(t, books, 1)
	assert.Equal(t, "ok", books[0].Status)
	assert.Equal(t, srv.HomeSetURL(), books[0].URL)
	require.Len(t, books[0].Collections, 2)
	assert.Equal(t, 1, books[0].Collections[0].CardCount)
	v, err := keychainGet(s.addressBookKeyringUser(ab.ID))
	require.NoError(t, err)
	assert.Equal(t, "carddav-only-pass", v)
}

func TestAddressBookPriorityFollowsAccountList(t *testing.T) {
	s := newCardDAVService(t, t.TempDir(), false)
	linked := testmail.StartCardDAV(t)
	linked.PutCard("contacts", "bob", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Bob iCloud\r\nEMAIL:bob@example.com\r\nEND:VCARD\r\n")
	SetAddressBookPresetURLForTest(t, PresetICloud, linked.URL())
	standalone := testmail.StartCardDAV(t)
	standalone.PutCard("contacts", "bob", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Bob Nextcloud\r\nEMAIL:bob@example.com\r\nEND:VCARD\r\n")

	plain := addLinkableAccount(t, s, "other", "a@example.com")
	acc := addLinkableAccount(t, s, "icloud", "alice@icloud.com")
	sa, err := s.AddAddressBook(bg, AddressBookInput{Preset: PresetCustom, Name: "Company", URL: standalone.URL(), Username: "alice", Password: "secret"})
	require.NoError(t, err)
	la, err := s.AddAddressBook(bg, AddressBookInput{Account: acc.ID, Preset: PresetICloud})
	require.NoError(t, err)

	order := func() []int64 {
		books, err := s.ListAddressBooks(bg)
		require.NoError(t, err)
		var ids []int64
		for _, b := range books {
			ids = append(ids, b.ID)
		}
		return ids
	}
	name := func() string {
		m, err := s.SearchContacts(bg, "bob@example.com", 5)
		require.NoError(t, err)
		require.Len(t, m, 1)
		return m[0].Name
	}
	// The account was added before the standalone address book: it comes first.
	assert.Equal(t, []int64{la.ID, sa.ID}, order())
	assert.Equal(t, "Bob iCloud", name())

	require.NoError(t, s.ReorderAccountEntries(bg, []AccountEntry{{AddressBook: sa.ID}, {Account: plain.ID}, {Account: acc.ID}}))
	assert.Equal(t, []int64{sa.ID, la.ID}, order())
	assert.Equal(t, "Bob Nextcloud", name())
	accs, err := s.ListAccounts(bg)
	require.NoError(t, err)
	assert.Equal(t, []int64{plain.ID, acc.ID}, []int64{accs[0].ID, accs[1].ID})

	// The list must name every account and standalone address book once.
	require.Error(t, s.ReorderAccountEntries(bg, []AccountEntry{{Account: plain.ID}, {Account: acc.ID}}))
	require.Error(t, s.ReorderAccountEntries(bg, []AccountEntry{{AddressBook: la.ID}, {AddressBook: sa.ID}, {Account: plain.ID}, {Account: acc.ID}}))

	// A new account goes after everything in the list.
	late := addLinkableAccount(t, s, "other", "c@example.com")
	accs, err = s.ListAccounts(bg)
	require.NoError(t, err)
	assert.Equal(t, late.ID, accs[2].ID)
	books, err := s.ListAddressBooks(bg)
	require.NoError(t, err)
	assert.Greater(t, accs[2].Position, books[1].SortOrder)
}

func TestRemoveAccountRemovesLinkedAddressBook(t *testing.T) {
	s := newCardDAVService(t, t.TempDir(), false)
	srv := testmail.StartCardDAV(t)
	srv.SetBasicAuth("alice", "own-pass")
	srv.PutCard("contacts", "bob", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Bob\r\nEMAIL:bob@example.com\r\nEND:VCARD\r\n")
	SetAddressBookPresetURLForTest(t, PresetICloud, srv.URL())
	acc := addLinkableAccount(t, s, "icloud", "alice@icloud.com")
	ab, err := s.AddAddressBook(bg, AddressBookInput{Account: acc.ID, Preset: PresetICloud})
	require.NoError(t, err)
	_, err = s.UpdateAddressBookAuth(bg, ab.ID, "own-pass", "", OAuthClient{})
	require.NoError(t, err)

	require.NoError(t, s.RemoveAccount(bg, acc.ID))
	books, err := s.ListAddressBooks(bg)
	require.NoError(t, err)
	assert.Empty(t, books)
	_, err = keychainGet(s.addressBookKeyringUser(ab.ID))
	assert.True(t, errors.Is(err, keyring.ErrNotFound), "the linked address book's keychain entry goes with the account: %v", err)
	m, err := s.SearchContacts(bg, "bob@example.com", 5)
	require.NoError(t, err)
	assert.Empty(t, m)
}
