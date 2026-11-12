package core

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/conn"
	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/core/events"
	gomail "github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/store"
	"github.com/godwit-email/godwit/internal/carddav"
	"github.com/godwit-email/godwit/internal/imagefetch"
	"github.com/zalando/go-keyring"
)

const (
	PresetICloud   = "icloud"
	PresetFastmail = "fastmail"
	PresetGoogle   = "google"
	PresetCustom   = "custom"

	ProviderGoogleCardDAV = "google_carddav"

	AddressBookSyncInterval = 30 * time.Minute
)

var (
	ErrAddressBookNotFound = errors.New("address book not found")
	ErrNoAddressBooks      = errors.New("no address books found on this server")
)

// onAddressBooksChanged is an internal hook invoked before AddressBooksChanged
// is published on the event bus (used by masking to refresh known card names).
var onAddressBooksChanged func(*Service, context.Context)

var addressBookPresetURLs = map[string]string{
	PresetICloud:   "https://contacts.icloud.com",
	PresetFastmail: "https://carddav.fastmail.com",
	PresetGoogle:   "https://www.googleapis.com/.well-known/carddav",
}

// SetAddressBookPresetURLForTest overrides a preset's default URL until t ends.
func SetAddressBookPresetURLForTest(t interface{ Cleanup(func()) }, preset, rawURL string) {
	prev := maps.Clone(addressBookPresetURLs)
	addressBookPresetURLs[preset] = rawURL
	t.Cleanup(func() { addressBookPresetURLs = prev })
}

type AddressBookPreset struct {
	ID, Name, DefaultURL, AuthType, HelpURL string
}

// AddressBookInput adds an address book. With Account set it is that mail
// account's (Preset must be LinkedAddressBookPreset's): a password preset signs
// in with the account's username and keychain password, Google with OAuthFlowID.
type AddressBookInput struct {
	Preset, Name, URL, Username, Password, OAuthFlowID string
	Client                                             OAuthClient
	Account                                            int64
}

type AddressBookCollection struct {
	ID, AddressBookID int64
	Href, Name        string
	Enabled           bool
	CardCount         int
}

// AddressBook is one sign-in to a CardDAV server and its cached collections.
// Account is the mail account it is linked to, 0 for a standalone one. SortOrder is its place in the
// accounts list (a linked one's is its account's Position), which is its priority.
type AddressBook struct {
	ID                                                          int64
	Preset, Name, URL, AuthType, Username, Status, StatusDetail string
	SortOrder                                                   int
	LastSyncedAt                                                time.Time
	Collections                                                 []AddressBookCollection
	Account                                                     int64
}

type addressBookRecord struct {
	AddressBook
	OAuthClientID string
}

// abPriority orders address books (as ab) by the accounts list: a linked one at
// its account's position, a standalone one at its own, the account first on a tie.
const abPriority = `COALESCE((SELECT position FROM accounts WHERE id = ab.account_id), ab.sort_order), ab.account_id IS NULL, COALESCE(ab.account_id, ab.id)`

// abCols are scanAddressBookRecord's columns of address_books ab.
const abCols = `ab.id, ab.preset, ab.name, ab.url, ab.auth_type, ab.username, ab.oauth_client_id,
	COALESCE((SELECT position FROM accounts WHERE id = ab.account_id), ab.sort_order), ab.status, ab.status_detail, ab.last_synced_at, COALESCE(ab.account_id, 0)`

// LinkedAddressBookPreset is the preset of the address book acc's credentials
// reach, "" when its provider has no CardDAV of its own.
func LinkedAddressBookPreset(acc Account) string {
	switch acc.Provider {
	case "icloud":
		return PresetICloud
	case "fastmail":
		return PresetFastmail
	case "gmail":
		return PresetGoogle
	}
	return ""
}

func (s *Service) addressBookKeyringUser(id int64) string {
	return s.installID + "/addressbook:" + strconv.FormatInt(id, 10)
}

func (s *Service) addressBookOAuthClientUser(id int64) string {
	return s.addressBookKeyringUser(id) + "/oauth-client"
}

func (s *Service) deleteAddressBookCredentials(id int64) error {
	var errs []error
	for _, u := range []string{s.addressBookKeyringUser(id), s.addressBookOAuthClientUser(id)} {
		if err := keychainDelete(u); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Service) resolveAddressBookClient(ctx context.Context, id int64) (OAuthClient, error) {
	rec, err := s.addressBookRecord(ctx, id)
	if err != nil || rec.OAuthClientID == "" {
		return OAuthClient{}, err
	}
	secret, err := keychainGet(s.addressBookOAuthClientUser(id))
	if err != nil {
		return OAuthClient{}, fmt.Errorf("read the OAuth client secret from the keychain: %w", err)
	}
	return OAuthClient{ID: rec.OAuthClientID, Secret: secret}, nil
}

func (s *Service) syncAddressBookMaskNames(ctx context.Context) {
	if onAddressBooksChanged != nil {
		onAddressBooksChanged(s, ctx)
	}
	if s.addressBooksHook != nil {
		s.addressBooksHook(ctx)
	}
}

func (s *Service) notifyAddressBooksChanged(ctx context.Context) {
	s.syncAddressBookMaskNames(ctx)
	s.bus.Publish(events.Event{Type: events.AddressBooksChanged})
}

func mapCardDAVErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, carddav.ErrInvalidURL):
		return ErrBaseURLInvalid
	case errors.Is(err, carddav.ErrScheme):
		return ErrBaseURLScheme
	case errors.Is(err, carddav.ErrAuthFailed):
		return backend.ErrAuthFailed
	case errors.Is(err, carddav.ErrNoAddressBooks):
		return ErrNoAddressBooks
	default:
		return err
	}
}

func (s *Service) carddavClient() *carddav.Client {
	return carddav.NewClient(conn.HTTPTransport())
}

func (s *Service) photoClient() *http.Client {
	base := s.opts.ImageClient
	if base == nil {
		base = imagefetch.NewClient(false)
	}
	if conn.TLSRoots == nil {
		return base
	}
	if tr, ok := base.Transport.(*imagefetch.Transport); ok && tr.TLSClientConfig == nil {
		cloned := tr.Transport.Clone()
		cloned.TLSClientConfig = &tls.Config{RootCAs: conn.TLSRoots}
		cp := *base
		cp.Transport = &imagefetch.Transport{Transport: cloned}
		return &cp
	}
	return base
}

// ListAddressBookPresets returns the built-in CardDAV presets and custom option.
func (s *Service) ListAddressBookPresets() []AddressBookPreset {
	return []AddressBookPreset{
		{
			ID:         PresetICloud,
			Name:       "iCloud",
			DefaultURL: addressBookPresetURLs[PresetICloud],
			AuthType:   "password",
			HelpURL:    "https://support.apple.com/102654",
		},
		{
			ID:         PresetFastmail,
			Name:       "Fastmail",
			DefaultURL: addressBookPresetURLs[PresetFastmail],
			AuthType:   "password",
			HelpURL:    "https://www.fastmail.help/hc/en-us/articles/360058752854",
		},
		{
			ID:         PresetGoogle,
			Name:       "Google",
			DefaultURL: addressBookPresetURLs[PresetGoogle],
			AuthType:   "oauth",
			HelpURL:    "https://developers.google.com/people/carddav",
		},
		{
			ID:         PresetCustom,
			Name:       "CardDAV",
			DefaultURL: "",
			AuthType:   "password",
			HelpURL:    "",
		},
	}
}

func normalizeAddressBookPreset(in AddressBookInput) (preset, authType, rawURL string, err error) {
	preset = strings.ToLower(strings.TrimSpace(in.Preset))
	if preset == "" {
		if in.OAuthFlowID != "" {
			preset = PresetGoogle
		} else {
			preset = PresetCustom
		}
	}
	switch preset {
	case PresetICloud, PresetFastmail:
		authType = "password"
	case PresetGoogle:
		authType = "oauth"
	case PresetCustom:
		if in.OAuthFlowID != "" {
			authType = "oauth"
		} else {
			authType = "password"
		}
	default:
		return "", "", "", fmt.Errorf("unknown address book preset %q", in.Preset)
	}
	rawURL = strings.TrimSpace(in.URL)
	if rawURL == "" {
		rawURL = addressBookPresetURLs[preset]
	}
	return preset, authType, rawURL, nil
}

// AddressBookEffectiveURL returns the normalized start URL that CheckAddressBook
// and AddAddressBook dial for in, or "" if in does not resolve to a valid URL.
func AddressBookEffectiveURL(in AddressBookInput) string {
	_, _, rawURL, err := normalizeAddressBookPreset(in)
	if err != nil {
		return ""
	}
	u, err := carddav.NormalizeURL(rawURL)
	if err != nil {
		return ""
	}
	return u.String()
}

func defaultAddressBookName(preset string, homeURL *url.URL) string {
	switch preset {
	case PresetICloud:
		return "iCloud"
	case PresetFastmail:
		return "Fastmail"
	case PresetGoogle:
		return "Google"
	default:
		if homeURL != nil && homeURL.Hostname() != "" {
			return homeURL.Hostname()
		}
		return "CardDAV"
	}
}

func (s *Service) addressBookFlowGrant(flowID, wantEmail string) (oauthGrant, error) {
	s.oauthMu.Lock()
	g, ok := s.grants[flowID]
	s.oauthMu.Unlock()
	if !ok {
		return oauthGrant{}, ErrUnknownFlow
	}
	if g.provider != ProviderGoogleCardDAV {
		return oauthGrant{}, ErrProviderMismatch
	}
	if wantEmail != "" && g.Email != "" && !strings.EqualFold(g.Email, strings.TrimSpace(wantEmail)) {
		return oauthGrant{}, ErrWrongAccount
	}
	return g, nil
}

// CheckAddressBook discovers the CardDAV addressbook-home-set and verifies
// credentials without saving anything to the database or keychain.
func (s *Service) CheckAddressBook(ctx context.Context, in AddressBookInput) ([]AddressBookCollection, string, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, "", err
	}
	defer end()

	_, authType, rawURL, err := normalizeAddressBookPreset(in)
	if err != nil {
		return nil, "", err
	}
	startURL, err := carddav.NormalizeURL(rawURL)
	if err != nil {
		return nil, "", mapCardDAVErr(err)
	}

	var creds carddav.Credentials
	if authType == "oauth" {
		g, err := s.addressBookFlowGrant(in.OAuthFlowID, in.Username)
		if err != nil {
			return nil, "", err
		}
		keychainValues.Add(g.AccessToken)
		s.secrets.Add(g.AccessToken)
		creds.BearerToken = g.AccessToken
	} else {
		if strings.TrimSpace(in.Username) == "" || in.Password == "" {
			return nil, "", backend.ErrAuthFailed
		}
		s.secrets.Add(in.Password)
		creds.Username = strings.TrimSpace(in.Username)
		creds.Password = in.Password
	}

	homeURL, disc, err := s.carddavClient().Discover(ctx, startURL, creds)
	if err != nil {
		return nil, "", mapCardDAVErr(err)
	}
	cols := make([]AddressBookCollection, len(disc))
	for i, d := range disc {
		cols[i] = AddressBookCollection{
			Href:      d.Href,
			Name:      d.Name,
			Enabled:   true,
			CardCount: d.CardCount,
		}
	}
	return cols, homeURL, nil
}

// AddAddressBook discovers, verifies, stores credentials in the keychain, saves
// the address book and its collections, and performs an initial sync.
func (s *Service) AddAddressBook(ctx context.Context, in AddressBookInput) (AddressBook, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return AddressBook{}, err
	}
	defer end()

	if err := s.KeychainStatus(ctx); err != nil {
		return AddressBook{}, err
	}

	preset, authType, rawURL, err := normalizeAddressBookPreset(in)
	if err != nil {
		return AddressBook{}, err
	}
	startURL, err := carddav.NormalizeURL(rawURL)
	if err != nil {
		return AddressBook{}, mapCardDAVErr(err)
	}
	// linked reuses the account's password: no copy is kept, and the server
	// refusing it leaves the address book waiting for one of its own.
	linked := in.Account != 0 && authType == "password"
	if in.Account != 0 {
		acc, err := s.account(ctx, in.Account)
		if err != nil {
			return AddressBook{}, err
		}
		if p := LinkedAddressBookPreset(acc); p == "" || p != preset {
			return AddressBook{}, fmt.Errorf("account %d has no %q address book", in.Account, preset)
		}
		in.Username = cmp.Or(acc.Username, acc.Email)
		if linked {
			if in.Password, err = keychainGet(s.keyringUser(acc.ID)); err != nil {
				return AddressBook{}, err
			}
		}
	}

	var (
		creds    carddav.Credentials
		grant    oauthGrant
		username string
		secret   string
		client   OAuthClient
	)
	if authType == "oauth" {
		grant, err = s.addressBookFlowGrant(in.OAuthFlowID, in.Username)
		if err != nil {
			return AddressBook{}, err
		}
		if grant.RefreshToken == "" {
			return AddressBook{}, errors.New("the provider granted no refresh token")
		}
		keychainValues.Add(grant.AccessToken)
		keychainValues.Add(grant.RefreshToken)
		s.secrets.Add(grant.AccessToken)
		s.secrets.Add(grant.RefreshToken)
		creds.BearerToken = grant.AccessToken
		username = cmp.Or(strings.TrimSpace(in.Username), grant.Email)
		secret = grant.RefreshToken
		client = grant.client
		if in.Client.ID != "" {
			client = in.Client
		}
	} else {
		username = strings.TrimSpace(in.Username)
		secret = in.Password
		if username == "" || secret == "" {
			return AddressBook{}, backend.ErrAuthFailed
		}
		s.secrets.Add(secret)
		creds.Username = username
		creds.Password = secret
	}

	homeStr, disc, err := s.carddavClient().Discover(ctx, startURL, creds)
	refused := linked && errors.Is(err, carddav.ErrAuthFailed)
	if err != nil && !refused {
		return AddressBook{}, mapCardDAVErr(err)
	}
	status, detail := "ok", ""
	if refused {
		homeStr, status, detail = startURL.String(), "reauth_required", "auth_rejected"
	}
	homeURL, _ := url.Parse(homeStr)

	if authType == "oauth" {
		if _, ok := s.takeGrant(in.OAuthFlowID); !ok {
			return AddressBook{}, ErrUnknownFlow
		}
	}

	name := cmp.Or(strings.TrimSpace(in.Name), defaultAddressBookName(preset, homeURL))
	now := s.clk().Now().Unix()

	var account any // NULL: standalone, last in the accounts list
	if in.Account != 0 {
		account = in.Account
	}
	s.abWriteMu.Lock()
	res, err := s.db.Execute(ctx, `INSERT INTO address_books
		(preset, name, url, auth_type, username, oauth_client_id, sort_order, status, status_detail, last_synced_at, created_at, account_id)
		VALUES (?, ?, ?, ?, ?, ?, (`+nextListPosition+`), ?, ?, 0, ?, ?)`,
		preset, name, homeStr, authType, username, client.ID, status, detail, now, account)
	if err != nil {
		s.abWriteMu.Unlock()
		return AddressBook{}, err
	}
	abID := res.LastInsertID

	if !linked { // a linked one signs in with the account's entry (addressBookCredentials)
		if err := keychainSet(s.addressBookKeyringUser(abID), secret); err != nil {
			_, _ = s.db.Execute(context.WithoutCancel(ctx), "DELETE FROM address_books WHERE id = ?", abID)
			s.abWriteMu.Unlock()
			return AddressBook{}, fmt.Errorf("save credentials to keychain: %w", err)
		}
	}
	if client.ID != "" && client.Secret != "" {
		s.secrets.Add(client.Secret)
		if err := keychainSet(s.addressBookOAuthClientUser(abID), client.Secret); err != nil {
			_ = s.deleteAddressBookCredentials(abID)
			_, _ = s.db.Execute(context.WithoutCancel(ctx), "DELETE FROM address_books WHERE id = ?", abID)
			s.abWriteMu.Unlock()
			return AddressBook{}, fmt.Errorf("save OAuth client secret to keychain: %w", err)
		}
	}
	if authType == "oauth" {
		s.cacheAddressBookToken(abID, grant.Grant)
	}

	stmts := make([]sqlite.Statement, 0, len(disc))
	for _, d := range disc {
		stmts = append(stmts, sqlite.Statement{
			SQL:  `INSERT INTO address_book_collections (address_book_id, href, name, enabled, sync_token, card_count) VALUES (?, ?, ?, 1, '', ?)`,
			Args: []any{abID, d.Href, d.Name, d.CardCount},
		})
	}
	if len(stmts) > 0 {
		if _, err := s.db.Transaction(context.WithoutCancel(ctx), stmts); err != nil {
			if authType == "oauth" {
				s.abTokMu.Lock()
				delete(s.abTokens, abID)
				s.abTokMu.Unlock()
			}
			_ = s.deleteAddressBookCredentials(abID)
			_, _ = s.db.Execute(context.WithoutCancel(ctx), "DELETE FROM address_books WHERE id = ?", abID)
			s.abWriteMu.Unlock()
			return AddressBook{}, err
		}
	}
	s.abWriteMu.Unlock()

	if !refused {
		_ = s.syncOneAddressBook(ctx, abID)
	}
	if s.opts.AutoSync {
		s.armAddressBookTimer(AddressBookSyncInterval)
	}
	s.notifyAddressBooksChanged(ctx)
	return s.getAddressBook(ctx, abID)
}

func (s *Service) addressBookRecord(ctx context.Context, id int64) (addressBookRecord, error) {
	rows, err := s.db.Query(ctx, `SELECT `+abCols+` FROM address_books ab WHERE ab.id = ?`, id)
	if err != nil {
		return addressBookRecord{}, err
	}
	if len(rows.Values) == 0 {
		return addressBookRecord{}, ErrAddressBookNotFound
	}
	return scanAddressBookRecord(rows.Values[0]), nil
}

func scanAddressBookRecord(r []any) addressBookRecord {
	var synced time.Time
	if ts := store.Int(r[10]); ts > 0 {
		synced = time.Unix(ts, 0).UTC()
	}
	return addressBookRecord{
		AddressBook: AddressBook{
			ID:           store.Int(r[0]),
			Preset:       store.Str(r[1]),
			Name:         store.Str(r[2]),
			URL:          store.Str(r[3]),
			AuthType:     store.Str(r[4]),
			Username:     store.Str(r[5]),
			SortOrder:    int(store.Int(r[7])),
			Status:       store.Str(r[8]),
			StatusDetail: store.Str(r[9]),
			LastSyncedAt: synced,
			Account:      store.Int(r[11]),
		},
		OAuthClientID: store.Str(r[6]),
	}
}

func (s *Service) getAddressBook(ctx context.Context, id int64) (AddressBook, error) {
	rec, err := s.addressBookRecord(ctx, id)
	if err != nil {
		return AddressBook{}, err
	}
	cols, err := s.listCollectionsForBooks(ctx, []int64{id})
	if err != nil {
		return AddressBook{}, err
	}
	rec.Collections = cols[id]
	return rec.AddressBook, nil
}

func (s *Service) listCollectionsForBooks(ctx context.Context, ids []int64) (map[int64][]AddressBookCollection, error) {
	out := map[int64][]AddressBookCollection{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT id, address_book_id, href, name, enabled, card_count
		FROM address_book_collections ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	want := map[int64]bool{}
	for _, id := range ids {
		want[id] = true
	}
	for _, r := range rows.Values {
		abID := store.Int(r[1])
		if !want[abID] {
			continue
		}
		out[abID] = append(out[abID], AddressBookCollection{
			ID:            store.Int(r[0]),
			AddressBookID: abID,
			Href:          store.Str(r[2]),
			Name:          store.Str(r[3]),
			Enabled:       store.Int(r[4]) != 0,
			CardCount:     int(store.Int(r[5])),
		})
	}
	return out, nil
}

// ListAddressBooks returns all configured CardDAV address books in priority
// order: the accounts list's (ReorderAccountEntries).
func (s *Service) ListAddressBooks(ctx context.Context) ([]AddressBook, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	defer end()

	rows, err := s.db.Query(ctx, `SELECT `+abCols+` FROM address_books ab ORDER BY `+abPriority)
	if err != nil {
		return nil, err
	}
	if len(rows.Values) == 0 {
		return nil, nil
	}
	books := make([]AddressBook, len(rows.Values))
	ids := make([]int64, len(rows.Values))
	for i, r := range rows.Values {
		rec := scanAddressBookRecord(r)
		books[i] = rec.AddressBook
		ids[i] = rec.ID
	}
	cols, err := s.listCollectionsForBooks(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range books {
		books[i].Collections = cols[books[i].ID]
	}
	return books, nil
}

// RemoveAddressBook deletes the address book, cascades all cached collections,
// cards, emails and avatars, removes keychain credentials, and emits AddressBooksChanged.
func (s *Service) RemoveAddressBook(ctx context.Context, id int64) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()

	s.abWriteMu.Lock()
	res, err := s.db.Execute(ctx, "DELETE FROM address_books WHERE id = ?", id)
	s.abWriteMu.Unlock()
	if err != nil {
		return err
	}
	if res.Changes == 0 {
		return ErrAddressBookNotFound
	}
	kerr := s.forgetAddressBook(id)
	s.notifyAddressBooksChanged(ctx)
	return kerr
}

// RenameAddressBook sets the name the address book shows under; blank names are refused.
func (s *Service) RenameAddressBook(ctx context.Context, id int64, name string) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("an address book needs a name")
	}
	res, err := s.db.Execute(ctx, "UPDATE address_books SET name = ? WHERE id = ?", name, id)
	if err != nil {
		return err
	}
	if res.Changes == 0 {
		return ErrAddressBookNotFound
	}
	s.notifyAddressBooksChanged(ctx)
	return nil
}

// forgetAddressBook drops a deleted address book's tokens and keychain entries.
func (s *Service) forgetAddressBook(id int64) error {
	s.abTokMu.Lock()
	delete(s.abTokens, id)
	delete(s.abRefreshLocks, id)
	s.abTokMu.Unlock()
	return s.deleteAddressBookCredentials(id)
}

// AccountEntry is one row of the accounts list: a mail account (Account) or a
// standalone address book (AddressBook).
type AccountEntry struct{ Account, AddressBook int64 }

// ReorderAccountEntries puts the accounts list in the order of entries, which
// names every mail account and standalone address book once. The order is
// also the address books' priority: a linked one goes with its account.
func (s *Service) ReorderAccountEntries(ctx context.Context, entries []AccountEntry) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()

	if err := s.reorderAccountEntries(ctx, entries); err != nil {
		return err
	}
	s.notifyAddressBooksChanged(ctx)
	return nil
}

func (s *Service) reorderAccountEntries(ctx context.Context, entries []AccountEntry) error {
	s.abWriteMu.Lock()
	defer s.abWriteMu.Unlock()
	rows, err := s.db.Query(ctx, "SELECT id, 0 FROM accounts UNION ALL SELECT 0, id FROM address_books WHERE account_id IS NULL")
	if err != nil {
		return err
	}
	have := map[AccountEntry]bool{}
	for _, r := range rows.Values {
		have[AccountEntry{store.Int(r[0]), store.Int(r[1])}] = true
	}
	stmts := make([]sqlite.Statement, len(entries))
	for i, e := range entries {
		if !have[e] {
			return errBadAccountOrder
		}
		delete(have, e)
		stmts[i] = sqlite.Statement{SQL: "UPDATE accounts SET position = ? WHERE id = ?", Args: []any{i, e.Account}}
		if e.Account == 0 {
			stmts[i] = sqlite.Statement{SQL: "UPDATE address_books SET sort_order = ? WHERE id = ?", Args: []any{i, e.AddressBook}}
		}
	}
	if len(have) > 0 {
		return errBadAccountOrder
	}
	_, err = s.db.Transaction(ctx, stmts)
	return err
}

var errBadAccountOrder = errors.New("the new order must name every account and standalone address book once")

// SetAddressBookCollectionEnabled toggles a sub-collection; disabling immediately
// deletes its cached cards, emails, and avatars and resets sync_token in the same transaction.
func (s *Service) SetAddressBookCollectionEnabled(ctx context.Context, collectionID int64, enabled bool) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()

	s.abWriteMu.Lock()
	rows, err := s.db.Query(ctx, "SELECT address_book_id FROM address_book_collections WHERE id = ?", collectionID)
	if err != nil {
		s.abWriteMu.Unlock()
		return err
	}
	if len(rows.Values) == 0 {
		s.abWriteMu.Unlock()
		return ErrAddressBookNotFound
	}
	var stmts []sqlite.Statement
	if enabled {
		stmts = []sqlite.Statement{{
			SQL:  "UPDATE address_book_collections SET enabled = 1 WHERE id = ?",
			Args: []any{collectionID},
		}}
	} else {
		// Odd sync_gen marks a reset collection awaiting a fresh full sync; commits advance to even.
		stmts = []sqlite.Statement{
			{SQL: "DELETE FROM address_book_cards WHERE collection_id = ?", Args: []any{collectionID}},
			{SQL: "UPDATE address_book_collections SET enabled = 0, sync_token = '', sync_gen = CASE WHEN sync_gen % 2 = 0 THEN sync_gen + 1 ELSE sync_gen + 2 END, card_count = 0 WHERE id = ?", Args: []any{collectionID}},
		}
	}
	_, err = s.db.Transaction(ctx, stmts)
	s.abWriteMu.Unlock()
	if err != nil {
		return err
	}
	if enabled && s.opts.AutoSync {
		s.wakeAddressBooks()
	}
	s.notifyAddressBooksChanged(ctx)
	return nil
}

// UpdateAddressBookAuth updates the password or OAuth grant for an existing address book,
// clears error/reauth status, and syncs immediately.
func (s *Service) UpdateAddressBookAuth(ctx context.Context, id int64, password, oauthFlowID string, client OAuthClient) (AddressBook, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return AddressBook{}, err
	}
	defer end()

	rec, err := s.addressBookRecord(ctx, id)
	if err != nil {
		return AddressBook{}, err
	}
	homeURL, err := carddav.NormalizeURL(rec.URL)
	if err != nil {
		return AddressBook{}, mapCardDAVErr(err)
	}

	if rec.AuthType == "oauth" {
		g, err := s.addressBookFlowGrant(oauthFlowID, rec.Username)
		if err != nil {
			return AddressBook{}, err
		}
		if g.RefreshToken == "" {
			return AddressBook{}, errors.New("the provider granted no refresh token")
		}
		keychainValues.Add(g.AccessToken)
		keychainValues.Add(g.RefreshToken)
		s.secrets.Add(g.AccessToken)
		s.secrets.Add(g.RefreshToken)

		if _, err := s.carddavClient().ListCollections(ctx, homeURL, carddav.Credentials{BearerToken: g.AccessToken}, false); err != nil {
			return AddressBook{}, mapCardDAVErr(err)
		}

		sem := s.addressBookRefreshLock(id)
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return AddressBook{}, ctx.Err()
		}
		release := sync.OnceFunc(func() { <-sem })
		defer release()

		if _, ok := s.takeGrant(oauthFlowID); !ok {
			return AddressBook{}, ErrUnknownFlow
		}
		chosen := g.client
		if client.ID != "" {
			chosen = client
		}
		if err := keychainSet(s.addressBookKeyringUser(id), g.RefreshToken); err != nil {
			return AddressBook{}, fmt.Errorf("save credentials to keychain: %w", err)
		}
		if chosen.ID != "" && chosen.Secret != "" {
			s.secrets.Add(chosen.Secret)
			if err := keychainSet(s.addressBookOAuthClientUser(id), chosen.Secret); err != nil {
				return AddressBook{}, fmt.Errorf("save OAuth client secret to keychain: %w", err)
			}
		} else {
			_ = keychainDelete(s.addressBookOAuthClientUser(id))
		}
		s.cacheAddressBookToken(id, g.Grant)
		if _, err := s.db.Execute(ctx, "UPDATE address_books SET oauth_client_id = ?, status = 'ok', status_detail = '' WHERE id = ?", chosen.ID, id); err != nil {
			return AddressBook{}, err
		}
		release()
	} else {
		if password == "" {
			return AddressBook{}, backend.ErrAuthFailed
		}
		s.secrets.Add(password)
		creds := carddav.Credentials{Username: rec.Username, Password: password}
		home := rec.URL
		if cols, err := s.listCollectionsForBooks(ctx, []int64{id}); err != nil {
			return AddressBook{}, err
		} else if len(cols[id]) == 0 { // linked with a refused password: never discovered
			if home, _, err = s.carddavClient().Discover(ctx, homeURL, creds); err != nil {
				return AddressBook{}, mapCardDAVErr(err)
			}
		} else if _, err := s.carddavClient().ListCollections(ctx, homeURL, creds, false); err != nil {
			return AddressBook{}, mapCardDAVErr(err)
		}
		if err := keychainSet(s.addressBookKeyringUser(id), password); err != nil {
			return AddressBook{}, fmt.Errorf("save credentials to keychain: %w", err)
		}
		if _, err := s.db.Execute(ctx, "UPDATE address_books SET url = ?, status = 'ok', status_detail = '' WHERE id = ?", home, id); err != nil {
			return AddressBook{}, err
		}
	}

	_ = s.syncOneAddressBook(ctx, id)
	if s.opts.AutoSync {
		s.armAddressBookTimer(AddressBookSyncInterval)
	}
	s.notifyAddressBooksChanged(ctx)
	return s.getAddressBook(ctx, id)
}

// SyncAddressBook immediately syncs one address book (id > 0) or all address books
// that have at least one enabled collection (id == 0).
func (s *Service) SyncAddressBook(ctx context.Context, id int64) error {
	if id < 0 {
		return ErrAddressBookNotFound
	}
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()

	if id > 0 {
		err := s.syncOneAddressBook(ctx, id)
		if !errors.Is(err, ErrAddressBookNotFound) {
			s.notifyAddressBooksChanged(ctx)
		}
		return err
	}

	rows, err := s.db.Query(ctx, `SELECT ab.id FROM address_books ab
		WHERE `+abPollable+`
		ORDER BY `+abPriority)
	if err != nil {
		return err
	}
	var errs []error
	for _, r := range rows.Values {
		if err := s.syncOneAddressBook(ctx, store.Int(r[0])); err != nil {
			errs = append(errs, err)
		}
	}
	if len(rows.Values) > 0 {
		s.notifyAddressBooksChanged(ctx)
	}
	return errors.Join(errs...)
}

func (s *Service) cacheAddressBookToken(id int64, g auth.Grant) {
	keychainValues.Add(g.AccessToken)
	s.secrets.Add(g.AccessToken)
	if !g.Expiry.IsZero() {
		if ttl := time.Until(g.Expiry); ttl > 0 {
			g.Expiry = s.clk().Now().Add(ttl)
		}
	}
	s.abTokMu.Lock()
	if s.abTokens == nil {
		s.abTokens = map[int64]auth.Grant{}
	}
	s.abTokens[id] = g
	s.abTokMu.Unlock()
}

func (s *Service) addressBookRefreshLock(id int64) chan struct{} {
	s.abTokMu.Lock()
	defer s.abTokMu.Unlock()
	if s.abRefreshLocks == nil {
		s.abRefreshLocks = map[int64]chan struct{}{}
	}
	ch := s.abRefreshLocks[id]
	if ch == nil {
		ch = make(chan struct{}, 1)
		s.abRefreshLocks[id] = ch
	}
	return ch
}

func (s *Service) setAddressBookStatus(ctx context.Context, id int64, status, detail string) {
	_, _ = s.db.Execute(context.WithoutCancel(ctx),
		"UPDATE address_books SET status = ?, status_detail = ? WHERE id = ?",
		status, detail, id)
}

func (s *Service) readAddressBookSecret(ctx context.Context, id int64, user string) (string, error) {
	v, err := keychainGet(user)
	switch {
	case errors.Is(err, keyring.ErrNotFound):
		s.setAddressBookStatus(ctx, id, "reauth_required", "credentials_missing")
		return "", fmt.Errorf("%w: %w", backend.ErrAuthFailed, err)
	case err != nil:
		s.setAddressBookStatus(ctx, id, "error", "keychain_unavailable")
		return "", err
	}
	s.secrets.Add(v)
	return v, nil
}

func (s *Service) addressBookCredentials(ctx context.Context, rec addressBookRecord, rejectedToken string) (carddav.Credentials, error) {
	if rec.AuthType != "oauth" {
		user := s.addressBookKeyringUser(rec.ID)
		if rec.Account != 0 { // its own password once the account's was refused, else the account's
			if _, err := keychainGet(user); errors.Is(err, keyring.ErrNotFound) {
				user = s.keyringUser(rec.Account)
			}
		}
		pw, err := s.readAddressBookSecret(ctx, rec.ID, user)
		if err != nil {
			return carddav.Credentials{}, err
		}
		return carddav.Credentials{Username: rec.Username, Password: pw}, nil
	}

	s.abTokMu.Lock()
	cached := s.abTokens[rec.ID]
	s.abTokMu.Unlock()
	if cached.AccessToken != "" && cached.AccessToken != rejectedToken && s.clk().Now().Add(time.Minute).Before(cached.Expiry) {
		return carddav.Credentials{BearerToken: cached.AccessToken}, nil
	}

	sem := s.addressBookRefreshLock(rec.ID)
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-ctx.Done():
		return carddav.Credentials{}, ctx.Err()
	}

	s.abTokMu.Lock()
	cached = s.abTokens[rec.ID]
	s.abTokMu.Unlock()
	if cached.AccessToken != "" && cached.AccessToken != rejectedToken && s.clk().Now().Add(time.Minute).Before(cached.Expiry) {
		return carddav.Credentials{BearerToken: cached.AccessToken}, nil
	}

	rt, err := s.readAddressBookSecret(ctx, rec.ID, s.addressBookKeyringUser(rec.ID))
	if err != nil {
		return carddav.Credentials{}, err
	}

	cfg, err := oauthConfig(ProviderGoogleCardDAV)
	if err != nil {
		return carddav.Credentials{}, err
	}
	if rec.OAuthClientID != "" {
		secret, err := s.readAddressBookSecret(ctx, rec.ID, s.addressBookOAuthClientUser(rec.ID))
		if err != nil {
			return carddav.Credentials{}, err
		}
		cfg.ClientID, cfg.ClientSecret = rec.OAuthClientID, secret
	}

	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshTimeout)
	hc := &http.Client{Transport: conn.HTTPTransport(), Timeout: refreshTimeout}
	g, err := auth.Refresh(rctx, cfg, hc, rt)
	cancel()
	switch {
	case errors.Is(err, auth.ErrInvalidGrant):
		s.setAddressBookStatus(ctx, rec.ID, "reauth_required", "token_revoked")
		return carddav.Credentials{}, fmt.Errorf("%w: %w", backend.ErrAuthFailed, err)
	case errors.Is(err, auth.ErrInvalidClient):
		s.setAddressBookStatus(ctx, rec.ID, "reauth_required", "client_rejected")
		return carddav.Credentials{}, fmt.Errorf("%w: %w", backend.ErrAuthFailed, err)
	case err != nil:
		s.setAddressBookStatus(ctx, rec.ID, "error", "network_error")
		return carddav.Credentials{}, err
	}
	if g.RefreshToken != "" {
		s.secrets.Add(g.RefreshToken)
		if err := keychainSet(s.addressBookKeyringUser(rec.ID), g.RefreshToken); err != nil {
			s.setAddressBookStatus(ctx, rec.ID, "error", "keychain_unavailable")
			return carddav.Credentials{}, err
		}
	}
	s.cacheAddressBookToken(rec.ID, g)
	return carddav.Credentials{BearerToken: g.AccessToken}, nil
}

func (s *Service) syncOneAddressBook(ctx context.Context, id int64) error {
	s.abWriteMu.Lock()
	rec, err := s.addressBookRecord(ctx, id)
	s.abWriteMu.Unlock()
	if err != nil {
		return err
	}
	homeURL, err := carddav.NormalizeURL(rec.URL)
	if err != nil {
		s.setAddressBookStatus(ctx, id, "error", "invalid_url")
		return mapCardDAVErr(err)
	}

	creds, err := s.addressBookCredentials(ctx, rec, "")
	if err != nil {
		return err
	}

	err = s.syncAddressBookWithCreds(ctx, rec, homeURL, creds)
	if errors.Is(err, carddav.ErrAuthFailed) && rec.AuthType == "oauth" && creds.BearerToken != "" {
		creds, err = s.addressBookCredentials(ctx, rec, creds.BearerToken)
		if err != nil {
			return err
		}
		err = s.syncAddressBookWithCreds(ctx, rec, homeURL, creds)
	}
	if err != nil {
		if errors.Is(err, carddav.ErrAuthFailed) {
			s.setAddressBookStatus(ctx, id, "reauth_required", "auth_rejected")
			return backend.ErrAuthFailed
		}
		if ctx.Err() == nil {
			s.setAddressBookStatus(ctx, id, "error", "sync_failed")
		}
		return mapCardDAVErr(err)
	}

	now := s.clk().Now().Unix()
	_, _ = s.db.Execute(ctx, "UPDATE address_books SET status = 'ok', status_detail = '', last_synced_at = ? WHERE id = ?", now, id)
	return nil
}

type localCollectionState struct {
	id        int64
	href      string
	name      string
	enabled   bool
	syncToken string
}

func (s *Service) syncAddressBookWithCreds(ctx context.Context, rec addressBookRecord, homeURL *url.URL, creds carddav.Credentials) error {
	client := s.carddavClient()
	remoteCols, err := client.ListCollections(ctx, homeURL, creds, false)
	if err != nil {
		return err
	}

	s.abWriteMu.Lock()
	// Ensure the address book still exists before reconciling collection rows.
	chk, err := s.db.Query(ctx, "SELECT id FROM address_books WHERE id = ?", rec.ID)
	if err != nil || len(chk.Values) == 0 {
		s.abWriteMu.Unlock()
		return err
	}
	existingRows, err := s.db.Query(ctx, `SELECT id, href, name, enabled, sync_token FROM address_book_collections WHERE address_book_id = ?`, rec.ID)
	if err != nil {
		s.abWriteMu.Unlock()
		return err
	}
	byHref := map[string]localCollectionState{}
	for _, r := range existingRows.Values {
		st := localCollectionState{
			id:        store.Int(r[0]),
			href:      store.Str(r[1]),
			name:      store.Str(r[2]),
			enabled:   store.Int(r[3]) != 0,
			syncToken: store.Str(r[4]),
		}
		byHref[st.href] = st
	}

	remoteSeen := map[string]bool{}
	var reconcileStmts []sqlite.Statement
	for _, rc := range remoteCols {
		remoteSeen[rc.Href] = true
		if cur, ok := byHref[rc.Href]; ok {
			if rc.Name != "" && rc.Name != cur.name {
				reconcileStmts = append(reconcileStmts, sqlite.Statement{
					SQL:  "UPDATE address_book_collections SET name = ? WHERE id = ?",
					Args: []any{rc.Name, cur.id},
				})
			}
		} else {
			reconcileStmts = append(reconcileStmts, sqlite.Statement{
				SQL:  "INSERT INTO address_book_collections (address_book_id, href, name, enabled, sync_token, card_count) VALUES (?, ?, ?, 1, '', 0)",
				Args: []any{rec.ID, rc.Href, rc.Name},
			})
		}
	}
	for href, cur := range byHref {
		if !remoteSeen[href] {
			reconcileStmts = append(reconcileStmts, sqlite.Statement{
				SQL:  "DELETE FROM address_book_collections WHERE id = ?",
				Args: []any{cur.id},
			})
		}
	}
	if len(reconcileStmts) > 0 {
		if _, err := s.db.Transaction(ctx, reconcileStmts); err != nil {
			s.abWriteMu.Unlock()
			return err
		}
	}

	colRows, err := s.db.Query(ctx, `SELECT id, href, name, enabled, sync_token FROM address_book_collections WHERE address_book_id = ? AND enabled = 1 ORDER BY id ASC`, rec.ID)
	s.abWriteMu.Unlock()
	if err != nil {
		return err
	}

	for _, r := range colRows.Values {
		col := localCollectionState{
			id:        store.Int(r[0]),
			href:      store.Str(r[1]),
			name:      store.Str(r[2]),
			enabled:   store.Int(r[3]) != 0,
			syncToken: store.Str(r[4]),
		}
		if err := s.syncOneCollection(ctx, client, homeURL, col, creds); err != nil {
			return err
		}
	}
	return nil
}

type preparedCard struct {
	href            string
	etag            string
	fn              string
	org             string
	title           string
	photo           []byte
	photoFormat     string
	photoWidth      int
	photoHeight     int
	photoPendingURL string
	emails          []carddav.CardEmail
}

type retriedPhoto struct {
	href   string
	etag   string
	photo  []byte
	format string
	width  int
	height int
}

func (s *Service) syncOneCollection(ctx context.Context, client *carddav.Client, homeURL *url.URL, col localCollectionState, creds carddav.Credentials) error {
	for attempt := range 2 {
		retry, err := s.syncOneCollectionOnce(ctx, client, homeURL, col.id, col.href, creds)
		if err != nil || !retry {
			return err
		}
		if attempt == 1 && s.opts.AutoSync {
			s.wakeAddressBooks()
		}
	}
	return nil
}

func (s *Service) syncOneCollectionOnce(ctx context.Context, client *carddav.Client, homeURL *url.URL, colID int64, colHref string, creds carddav.Credentials) (retry bool, err error) {
	s.abWriteMu.Lock()
	snapRows, err := s.db.Query(ctx, "SELECT enabled, sync_token, sync_gen, name FROM address_book_collections WHERE id = ?", colID)
	if err != nil {
		s.abWriteMu.Unlock()
		return false, err
	}
	if len(snapRows.Values) == 0 || store.Int(snapRows.Values[0][0]) == 0 {
		s.abWriteMu.Unlock()
		return false, nil
	}
	snapSyncToken := store.Str(snapRows.Values[0][1])
	snapSyncGen := store.Int(snapRows.Values[0][2])
	snapName := store.Str(snapRows.Values[0][3])

	etagRows, err := s.db.Query(ctx, "SELECT href, etag, photo_pending_url FROM address_book_cards WHERE collection_id = ?", colID)
	s.abWriteMu.Unlock()
	if err != nil {
		return false, err
	}
	localETags := make(map[string]string, len(etagRows.Values))
	pendingPhotos := make(map[string]string)
	for _, r := range etagRows.Values {
		h := store.Str(r[0])
		localETags[h] = store.Str(r[1])
		if u := store.Str(r[2]); u != "" {
			pendingPhotos[h] = u
		}
	}

	var (
		toFetch       []string
		deleted       []string
		nextSyncToken string
		newName       string
		usedDelta     bool
	)
	if snapSyncToken != "" {
		delta, err := client.SyncCollection(ctx, homeURL, colHref, snapSyncToken, creds)
		switch {
		case err == nil:
			usedDelta = true
			nextSyncToken = delta.NextSyncToken
			deleted = delta.Deleted
			for href, etag := range delta.Upserted {
				local, exists := localETags[href]
				if !exists || etag == "" || local == "" || etag != local {
					toFetch = append(toFetch, href)
				}
			}
		case errors.Is(err, carddav.ErrSyncTokenInvalid):
			// Fall back to full PROPFIND reconciliation below.
		default:
			return false, err
		}
	}

	if !usedDelta {
		remoteETags, tok, displayName, err := client.ListCollectionETags(ctx, homeURL, colHref, creds)
		if err != nil {
			return false, err
		}
		nextSyncToken = tok
		if displayName != "" && displayName != snapName {
			newName = displayName
		}
		for href := range localETags {
			if _, ok := remoteETags[href]; !ok {
				deleted = append(deleted, href)
			}
		}
		for href, etag := range remoteETags {
			local, exists := localETags[href]
			if !exists || etag == "" || local == "" || etag != local {
				toFetch = append(toFetch, href)
			}
		}
	}
	slices.Sort(toFetch)
	slices.Sort(deleted)

	fetched, err := client.FetchCards(ctx, homeURL, colHref, toFetch, creds)
	if err != nil {
		return false, err
	}
	fetchedHrefs := make(map[string]bool, len(fetched))
	for _, fc := range fetched {
		fetchedHrefs[fc.Href] = true
	}
	for _, href := range toFetch {
		if !fetchedHrefs[href] {
			if _, existed := localETags[href]; existed {
				deleted = append(deleted, href)
			}
		}
	}

	photoHTTP := s.photoClient()
	prepared := make([]preparedCard, 0, len(fetched))
	for _, fc := range fetched {
		pc := carddav.ParseVCard(fc.RawVCard)
		item := preparedCard{
			href:   fc.Href,
			etag:   fc.ETag,
			fn:     pc.FN,
			org:    pc.Org,
			title:  pc.Title,
			emails: make([]carddav.CardEmail, 0, len(pc.Emails)),
		}
		for i, em := range pc.Emails {
			key := gomail.AddrKey(em.Address)
			if key == "" {
				continue
			}
			item.emails = append(item.emails, carddav.CardEmail{
				Pos:     i,
				AddrKey: key,
				Pref:    em.Pref,
			})
		}
		for _, ph := range pc.Photos {
			raw := ph.Data
			if len(raw) == 0 && ph.URL != "" {
				downloaded, derr := imagefetch.DownloadAvatar(ctx, photoHTTP, ph.URL)
				if derr != nil {
					if ctx.Err() != nil {
						return false, ctx.Err()
					}
					if !errors.Is(derr, imagefetch.ErrAvatarMiss) && !errors.Is(derr, imagefetch.ErrScheme) && item.photoPendingURL == "" {
						item.photoPendingURL = ph.URL
					}
					continue
				}
				raw = downloaded
			}
			if len(raw) == 0 {
				continue
			}
			checked, format, w, h, cerr := imagefetch.CheckAvatarImage(raw, "carddav")
			if cerr != nil {
				continue
			}
			item.photo = checked
			item.photoFormat = format
			item.photoWidth = w
			item.photoHeight = h
			item.photoPendingURL = ""
			break
		}
		prepared = append(prepared, item)
	}

	deletedSet := make(map[string]bool, len(deleted))
	for _, h := range deleted {
		deletedSet[h] = true
	}
	var retried []retriedPhoto
	if len(pendingPhotos) > 0 {
		pendingHrefs := make([]string, 0, len(pendingPhotos))
		for h := range pendingPhotos {
			if !fetchedHrefs[h] && !deletedSet[h] {
				pendingHrefs = append(pendingHrefs, h)
			}
		}
		slices.Sort(pendingHrefs)
		for _, h := range pendingHrefs {
			downloaded, derr := imagefetch.DownloadAvatar(ctx, photoHTTP, pendingPhotos[h])
			if derr != nil {
				if ctx.Err() != nil {
					return false, ctx.Err()
				}
				if errors.Is(derr, imagefetch.ErrAvatarMiss) || errors.Is(derr, imagefetch.ErrScheme) {
					retried = append(retried, retriedPhoto{href: h, etag: localETags[h]})
				}
				continue
			}
			checked, format, pw, ph, cerr := imagefetch.CheckAvatarImage(downloaded, "carddav")
			if cerr != nil {
				retried = append(retried, retriedPhoto{href: h, etag: localETags[h]})
				continue
			}
			retried = append(retried, retriedPhoto{
				href: h, etag: localETags[h],
				photo: checked, format: format, width: pw, height: ph,
			})
		}
	}

	s.abWriteMu.Lock()
	defer s.abWriteMu.Unlock()

	// Verify collection is still enabled and at the exact sync generation and token
	// snapshot we queried against (SetAddressBookCollectionEnabled(false) -> (true)
	// or a concurrent sync may have reset or advanced it while requests were in flight).
	still, err := s.db.Query(ctx, "SELECT enabled, sync_token, sync_gen FROM address_book_collections WHERE id = ?", colID)
	if err != nil || len(still.Values) == 0 || store.Int(still.Values[0][0]) == 0 {
		return false, err
	}
	curToken, curGen := store.Str(still.Values[0][1]), store.Int(still.Values[0][2])
	if curGen != snapSyncGen || curToken != snapSyncToken {
		// Retry only when the collection was reset (odd sync_gen); when a newer sync
		// already committed (even sync_gen), drop this stale snapshot without overwriting.
		return curGen%2 == 1, nil
	}

	var stmts []sqlite.Statement
	if newName != "" {
		stmts = append(stmts, sqlite.Statement{
			SQL:  "UPDATE address_book_collections SET name = ? WHERE id = ?",
			Args: []any{newName, colID},
		})
	}
	for _, href := range deleted {
		stmts = append(stmts, sqlite.Statement{
			SQL:  "DELETE FROM address_book_cards WHERE collection_id = ? AND href = ?",
			Args: []any{colID, href},
		})
	}
	for _, item := range prepared {
		var photoArg any
		if len(item.photo) > 0 {
			photoArg = item.photo
		}
		stmts = append(stmts,
			sqlite.Statement{
				SQL: `INSERT INTO address_book_cards
					(collection_id, href, etag, fn, org, title, photo, photo_format, photo_width, photo_height, photo_pending_url)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
					ON CONFLICT(collection_id, href) DO UPDATE SET
						etag = excluded.etag,
						fn = excluded.fn,
						org = excluded.org,
						title = excluded.title,
						photo = CASE WHEN excluded.photo IS NULL AND excluded.photo_pending_url <> '' THEN address_book_cards.photo ELSE excluded.photo END,
						photo_format = CASE WHEN excluded.photo IS NULL AND excluded.photo_pending_url <> '' THEN address_book_cards.photo_format ELSE excluded.photo_format END,
						photo_width = CASE WHEN excluded.photo IS NULL AND excluded.photo_pending_url <> '' THEN address_book_cards.photo_width ELSE excluded.photo_width END,
						photo_height = CASE WHEN excluded.photo IS NULL AND excluded.photo_pending_url <> '' THEN address_book_cards.photo_height ELSE excluded.photo_height END,
						photo_pending_url = excluded.photo_pending_url`,
				Args: []any{colID, item.href, item.etag, item.fn, item.org, item.title, photoArg, item.photoFormat, item.photoWidth, item.photoHeight, item.photoPendingURL},
			},
			sqlite.Statement{
				SQL:  `DELETE FROM address_book_emails WHERE card_id = (SELECT id FROM address_book_cards WHERE collection_id = ? AND href = ?)`,
				Args: []any{colID, item.href},
			},
		)
		for _, em := range item.emails {
			pref := 0
			if em.Pref {
				pref = 1
			}
			stmts = append(stmts, sqlite.Statement{
				SQL: `INSERT OR REPLACE INTO address_book_emails (card_id, pos, addr_key, pref)
					VALUES ((SELECT id FROM address_book_cards WHERE collection_id = ? AND href = ?), ?, ?, ?)`,
				Args: []any{colID, item.href, em.Pos, em.AddrKey, pref},
			})
		}
	}
	for _, rp := range retried {
		if len(rp.photo) > 0 {
			stmts = append(stmts, sqlite.Statement{
				SQL: `UPDATE address_book_cards
					SET photo = ?, photo_format = ?, photo_width = ?, photo_height = ?, photo_pending_url = ''
					WHERE collection_id = ? AND href = ? AND etag = ?`,
				Args: []any{rp.photo, rp.format, rp.width, rp.height, colID, rp.href, rp.etag},
			})
		} else {
			stmts = append(stmts, sqlite.Statement{
				SQL:  `UPDATE address_book_cards SET photo_pending_url = '' WHERE collection_id = ? AND href = ? AND etag = ?`,
				Args: []any{colID, rp.href, rp.etag},
			})
		}
	}
	stmts = append(stmts, sqlite.Statement{
		SQL: `UPDATE address_book_collections
			SET sync_token = ?,
				sync_gen = CASE WHEN sync_gen % 2 = 0 THEN sync_gen + 2 ELSE sync_gen + 1 END,
				card_count = (SELECT COUNT(*) FROM address_book_cards WHERE collection_id = ?)
			WHERE id = ? AND enabled = 1 AND sync_gen = ? AND sync_token = ?`,
		Args: []any{nextSyncToken, colID, colID, snapSyncGen, snapSyncToken},
	})
	_, err = s.db.Transaction(ctx, stmts)
	return false, err
}

func (s *Service) wakeAddressBooks() {
	select {
	case s.abWake <- struct{}{}:
	default:
	}
}

func (s *Service) armAddressBookTimer(d time.Duration) {
	s.abTimerMu.Lock()
	defer s.abTimerMu.Unlock()
	if s.abTimerStop != nil {
		s.abTimerStop()
	}
	s.abTimerActive = true
	s.abTimerStop = s.clk().AfterFunc(d, s.wakeAddressBooks)
}

func (s *Service) stopAddressBookTimer() {
	s.abTimerMu.Lock()
	defer s.abTimerMu.Unlock()
	s.abTimerActive = false
	if s.abTimerStop != nil {
		s.abTimerStop()
		s.abTimerStop = nil
	}
}

func (s *Service) rearmAddressBookTimer() {
	s.abTimerMu.Lock()
	active := s.abTimerActive
	if s.abTimerStop != nil {
		s.abTimerStop()
		s.abTimerStop = nil
	}
	if active {
		s.abTimerStop = s.clk().AfterFunc(AddressBookSyncInterval, s.wakeAddressBooks)
	}
	s.abTimerMu.Unlock()
}

func (s *Service) startAddressBookSync() {
	// Prime keychainValues best-effort so Redact covers stored address-book secrets before any sync.
	if rows, err := s.db.Query(context.Background(), "SELECT id FROM address_books"); err == nil {
		for _, r := range rows.Values {
			id := store.Int(r[0])
			_, _ = keychainGet(s.addressBookKeyringUser(id))
			_, _ = keychainGet(s.addressBookOAuthClientUser(id))
		}
	}
	if !s.opts.AutoSync {
		return
	}
	ctx, end, err := s.begin(context.Background(), 0)
	if err != nil {
		return
	}
	s.armAddressBookTimer(AddressBookSyncInterval)
	go func() {
		defer end()
		defer s.stopAddressBookTimer()
		for {
			s.syncDueAddressBooks(ctx)
			s.armAddressBookTimer(AddressBookSyncInterval)
			select {
			case <-ctx.Done():
				return
			case <-s.abWake:
			}
		}
	}()
}

// abPollable selects address books sync reaches: one with an enabled
// collection, or one with none left, whose server may add one later. Only
// every collection turned off by the user stops polling.
const abPollable = `(EXISTS (SELECT 1 FROM address_book_collections c WHERE c.address_book_id = ab.id AND c.enabled = 1)
	OR NOT EXISTS (SELECT 1 FROM address_book_collections c WHERE c.address_book_id = ab.id))`

func (s *Service) syncDueAddressBooks(ctx context.Context) {
	rows, err := s.db.Query(ctx, `SELECT ab.id FROM address_books ab
		WHERE `+abPollable+`
		AND ab.status != 'reauth_required'
		ORDER BY `+abPriority)
	if err != nil || len(rows.Values) == 0 {
		return
	}
	for _, r := range rows.Values {
		if ctx.Err() != nil {
			return
		}
		if err := s.syncOneAddressBook(ctx, store.Int(r[0])); err != nil && ctx.Err() == nil {
			slog.Warn("address book background sync failed", "id", store.Int(r[0]), "err", s.Redact(err.Error()))
		}
	}
	if ctx.Err() == nil {
		s.notifyAddressBooksChanged(ctx)
	}
}
