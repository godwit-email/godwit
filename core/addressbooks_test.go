package core

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/conn"
	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/core/internal/testmail"
	"github.com/godwit-email/godwit/core/internal/testoauth"
	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

func makePNGBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 20, G: 120, B: 220, A: 255})
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func newCardDAVService(t *testing.T, dir string, autoSync bool) *Service {
	t.Helper()
	SerializeKeyringForTest()
	roots := testmail.CardDAVRoots()
	prevRoots := conn.TLSRoots
	conn.TLSRoots = roots
	t.Cleanup(func() { conn.TLSRoots = prevRoots })
	s, err := New(bg, Options{DataDir: dir, AutoSync: autoSync, ImageClient: loopbackImageClient()})
	require.NoError(t, err)
	return s
}

func TestAddressBookVCardFieldsPhotosAndCollectionToggle(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()
	s := newCardDAVService(t, dir, false)
	defer s.Close()

	var hookCalls atomic.Int64
	setFor(t, &onAddressBooksChanged, func(*Service, context.Context) {
		hookCalls.Add(1)
	})

	srv := testmail.StartCardDAV(t)
	srv.PutCollection("work", "Work")
	srv.PutCollection("archive", "Archive")

	smallPNG := makePNGBytes(t, 16, 16) // < 32px: rejected for favicons, accepted for carddav
	extPNG := makePNGBytes(t, 24, 24)
	photoURL := srv.ServePhoto("alice.png", "image/png", extPNG)
	missingPhotoURL := srv.URL() + "/photos/missing.png"

	// Card 1: folded UTF-8 FN, ORG + TITLE, vCard 4.0 PREF=2 & PREF=1, external HTTPS PHOTO, ignored TEL/ADR/NOTE/AGENT.
	srv.PutCard("work", "alice", strings.Join([]string{
		"BEGIN:VCARD",
		"VERSION:4.0",
		"FN:Alice ",
		" Zhang",
		"ORG:Godwit Inc.;Platform",
		"TITLE:Staff Engineer",
		"EMAIL;PREF=2:alice.secondary@example.com",
		"EMAIL;PREF=1:MAILTO:Alice.Primary@Example.com",
		"EMAIL:alice.Primary@example.com",
		"TEL:+1-555-0100",
		"ADR:;;123 Main St;;;;",
		"NOTE:Private note must not be stored",
		"PHOTO:" + photoURL,
		"BEGIN:AGENT",
		"FN:Assistant",
		"EMAIL:assistant@example.com",
		"END:AGENT",
		"END:VCARD",
	}, "\r\n"))

	// Card 2: blank FN falls back to ORG; inline 16x16 PNG via ENCODING=b; vCard 3.0 TYPE=PREF.
	b64PNG := base64.StdEncoding.EncodeToString(smallPNG)
	srv.PutCard("work", "acme", strings.Join([]string{
		"BEGIN:VCARD",
		"VERSION:3.0",
		"FN:   ",
		"ORG:Acme Billing;",
		"EMAIL;TYPE=WORK,PREF:billing@acme.example",
		"EMAIL;TYPE=WORK:support@acme.example",
		"PHOTO;ENCODING=b;TYPE=PNG:" + b64PNG[:20],
		" " + b64PNG[20:],
		"END:VCARD",
	}, "\r\n"))

	// Card 3: QUOTED-PRINTABLE UTF-8 FN + broken external PHOTO (404) does not abort sync.
	srv.PutCard("work", "qp", strings.Join([]string{
		"BEGIN:VCARD",
		"VERSION:3.0",
		"FN;CHARSET=UTF-8;ENCODING=QUOTED-PRINTABLE:=E5=BC=A0=E4=BC=9F",
		"EMAIL;PREF:zhangwei@example.com",
		"PHOTO;VALUE=URI:" + missingPhotoURL,
		"END:VCARD",
	}, "\r\n"))

	// Card 4: data: URI SVG photo in second collection.
	safeSVG := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><circle cx="12" cy="12" r="10" fill="#336699"/></svg>`
	srv.PutCard("archive", "svgcard", strings.Join([]string{
		"BEGIN:VCARD",
		"VERSION:4.0",
		"FN:Vector User",
		"EMAIL:vector@example.com",
		"PHOTO:data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(safeSVG)),
		"END:VCARD",
	}, "\r\n"))

	ab, err := s.AddAddressBook(bg, AddressBookInput{
		Preset: PresetCustom, Name: "Corp", URL: srv.URL(), Username: "alice", Password: "secret",
	})
	require.NoError(t, err)
	require.Len(t, ab.Collections, 2)
	assert.Equal(t, 3, ab.Collections[0].CardCount)
	assert.Equal(t, 1, ab.Collections[1].CardCount)
	assert.GreaterOrEqual(t, hookCalls.Load(), int64(1))
	assert.Equal(t, 1, srv.PhotoHits("alice.png"))

	// Syncing again when ETag has not changed does NOT re-download external PHOTO.
	require.NoError(t, s.SyncAddressBook(bg, ab.ID))
	assert.Equal(t, 1, srv.PhotoHits("alice.png"), "unchanged ETag must reuse cached PHOTO")

	// Verify exact SQLite schema contract for downstream consumers (#708, #709).
	rows, err := s.db.Query(bg, `SELECT fn, org, title, photo, photo_format, photo_width, photo_height
		FROM address_book_cards WHERE collection_id = ? ORDER BY href ASC`, ab.Collections[0].ID)
	require.NoError(t, err)
	require.Len(t, rows.Values, 3)

	// acme.vcf
	assert.Equal(t, "Acme Billing", store.Str(rows.Values[0][0]), "blank FN falls back to ORG")
	assert.Equal(t, "Acme Billing", store.Str(rows.Values[0][1]))
	assert.Equal(t, smallPNG, rows.Values[0][3].([]byte))
	assert.Equal(t, "png", store.Str(rows.Values[0][4]))
	assert.Equal(t, int64(16), store.Int(rows.Values[0][5]))

	// alice.vcf
	assert.Equal(t, "Alice Zhang", store.Str(rows.Values[1][0]))
	assert.Equal(t, "Godwit Inc. Platform", store.Str(rows.Values[1][1]))
	assert.Equal(t, "Staff Engineer", store.Str(rows.Values[1][2]))
	assert.Equal(t, extPNG, rows.Values[1][3].([]byte))
	assert.Equal(t, int64(24), store.Int(rows.Values[1][5]))

	// qp.vcf (broken photo discarded, card kept)
	assert.Equal(t, "张伟", store.Str(rows.Values[2][0]))
	assert.Nil(t, rows.Values[2][3])
	assert.Equal(t, "", store.Str(rows.Values[2][4]))

	// Check Alice's emails: assistant@example.com from nested AGENT ignored; PREF=1 wins over PREF=2; duplicate merged.
	emRows, err := s.db.Query(bg, `SELECT e.pos, e.addr_key, e.pref
		FROM address_book_emails e
		JOIN address_book_cards c ON c.id = e.card_id
		WHERE c.collection_id = ? AND c.href LIKE '%/alice.vcf'
		ORDER BY e.pos ASC`, ab.Collections[0].ID)
	require.NoError(t, err)
	require.Len(t, emRows.Values, 2)
	assert.Equal(t, "alice.secondary@example.com", store.Str(emRows.Values[0][1]))
	assert.Equal(t, int64(0), store.Int(emRows.Values[0][2]), "PREF=2 is lower priority than PREF=1")
	assert.Equal(t, "alice.primary@example.com", store.Str(emRows.Values[1][1]))
	assert.Equal(t, int64(1), store.Int(emRows.Values[1][2]), "PREF=1 is marked pref=1")

	// Transient failure preserves cached cards and marks status = "error".
	srv.FailRequests(http.StatusServiceUnavailable)
	require.Error(t, s.SyncAddressBook(bg, ab.ID))
	books, err := s.ListAddressBooks(bg)
	require.NoError(t, err)
	assert.Equal(t, "error", books[0].Status)
	cntRows, err := s.db.Query(bg, "SELECT COUNT(*) FROM address_book_cards")
	require.NoError(t, err)
	assert.Equal(t, int64(4), store.Int(cntRows.Values[0][0]), "cached cards stay available offline")
	srv.FailRequests(0)

	// Disabling "Work" immediately wipes its cards, emails, photo BLOBs, and sync_token in the same tx.
	workColID := ab.Collections[0].ID
	require.NoError(t, s.SetAddressBookCollectionEnabled(bg, workColID, false))
	books, err = s.ListAddressBooks(bg)
	require.NoError(t, err)
	assert.False(t, books[0].Collections[0].Enabled)
	assert.Equal(t, 0, books[0].Collections[0].CardCount)
	colMeta, err := s.db.Query(bg, "SELECT sync_token, (SELECT COUNT(*) FROM address_book_cards WHERE collection_id = ?) FROM address_book_collections WHERE id = ?", workColID, workColID)
	require.NoError(t, err)
	assert.Equal(t, "", store.Str(colMeta.Values[0][0]))
	assert.Equal(t, int64(0), store.Int(colMeta.Values[0][1]))
	// Archive collection remains untouched.
	assert.Equal(t, 1, books[0].Collections[1].CardCount)

	// Re-enabling and syncing restores Work's cards.
	require.NoError(t, s.SetAddressBookCollectionEnabled(bg, workColID, true))
	require.NoError(t, s.SyncAddressBook(bg, 0))
	books, err = s.ListAddressBooks(bg)
	require.NoError(t, err)
	assert.Equal(t, 3, books[0].Collections[0].CardCount)

	// RemoveAddressBook cleans keychain and cascades all tables.
	keyUser := s.addressBookKeyringUser(ab.ID)
	_, err = keyring.Get(keyringService, keyUser)
	require.NoError(t, err)
	require.NoError(t, s.RemoveAddressBook(bg, ab.ID))
	_, err = keyring.Get(keyringService, keyUser)
	assert.ErrorIs(t, err, keyring.ErrNotFound)
	for _, tbl := range []string{"address_books", "address_book_collections", "address_book_cards", "address_book_emails"} {
		r, err := s.db.Query(bg, "SELECT COUNT(*) FROM "+tbl)
		require.NoError(t, err)
		assert.Equal(t, int64(0), store.Int(r.Values[0][0]), tbl)
	}
}

func TestAddressBookGoogleOAuthRefreshAndReauth(t *testing.T) {
	keyring.MockInit()
	oa := testoauth.Start(t)
	oa.Secret("custom-client-secret-9999")
	setFor(t, &oauthConfig, func(providerID string) (auth.OAuthConfig, error) {
		if providerID != ProviderGoogleCardDAV {
			t.Fatalf("unexpected providerID %q", providerID)
		}
		cfg := oa.Config("openid", "email", "https://www.googleapis.com/auth/carddav")
		cfg.ClientID = ""
		return cfg, nil
	})

	srv := testmail.StartCardDAV(t)
	srv.SetOAuthValidator(oa.ValidAccess)
	srv.PutCard("contacts", "g1", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Grace Hopper\r\nEMAIL:grace@example.com\r\nEND:VCARD\r\n")
	SetAddressBookPresetURLForTest(t, PresetGoogle, srv.URL()+"/.well-known/carddav")

	dir := t.TempDir()
	s := newCardDAVService(t, dir, false)
	defer s.Close()
	clock := useTestClock(t)
	s.SetClockForTest(clock)

	customClient := OAuthClient{ID: testoauth.ClientID, Secret: "custom-client-secret-9999"}
	signIn := func(c OAuthClient) string {
		flowID, authURL, err := s.BeginOAuth(bg, ProviderGoogleCardDAV, "alice@gmail.com", c)
		require.NoError(t, err)
		ch := await(s, flowID)
		oa.Browse(t, authURL, "alice@gmail.com")
		require.NoError(t, recvAwait(t, ch).err)
		return flowID
	}

	flowID := signIn(customClient)
	// CheckAddressBook peeks grant without consuming it, so AddAddressBook can still use flowID.
	cols, _, err := s.CheckAddressBook(bg, AddressBookInput{Preset: PresetGoogle, OAuthFlowID: flowID})
	require.NoError(t, err)
	require.Len(t, cols, 1)
	assert.Equal(t, 1, cols[0].CardCount)

	ab, err := s.AddAddressBook(bg, AddressBookInput{Preset: PresetGoogle, OAuthFlowID: flowID})
	require.NoError(t, err)
	assert.Equal(t, "Google", ab.Name)
	assert.Equal(t, "oauth", ab.AuthType)
	assert.Equal(t, "alice@gmail.com", ab.Username)
	assert.Contains(t, s.Redact("secret=custom-client-secret-9999"), "[redacted]")

	// Advance past access token expiry -> SyncAddressBook refreshes token via OAuth endpoint.
	beforeReqs := oa.TokenRequests()
	clock.Advance(2 * time.Hour)
	require.NoError(t, s.SyncAddressBook(bg, ab.ID))
	assert.Equal(t, beforeReqs+1, oa.TokenRequests())

	// Revoke refresh tokens -> SyncAddressBook marks reauth_required (token_revoked).
	oa.RevokeAll()
	clock.Advance(2 * time.Hour)
	err = s.SyncAddressBook(bg, ab.ID)
	require.ErrorIs(t, err, backend.ErrAuthFailed)
	books, err := s.ListAddressBooks(bg)
	require.NoError(t, err)
	assert.Equal(t, "reauth_required", books[0].Status)
	assert.Equal(t, "token_revoked", books[0].StatusDetail)

	// Reauthorize reusing stored OAuthClient via OAuthClient{AddressBook: ab.ID}.
	newFlow := signIn(OAuthClient{AddressBook: ab.ID})
	updated, err := s.UpdateAddressBookAuth(bg, ab.ID, "", newFlow, OAuthClient{})
	require.NoError(t, err)
	assert.Equal(t, "ok", updated.Status)
	assert.Empty(t, updated.StatusDetail)
}

func tcpConnectProxy(t *testing.T, destPort string) (*url.URL, func() []string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	var asked []string
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				br := bufio.NewReader(c)
				req, err := http.ReadRequest(br)
				if err != nil || req.Method != http.MethodConnect {
					return
				}
				mu.Lock()
				asked = append(asked, req.Host)
				mu.Unlock()
				up, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", destPort))
				if err != nil {
					return
				}
				defer up.Close()
				_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\n\r\n")
				go func() { _, _ = io.Copy(up, br); up.Close() }()
				_, _ = io.Copy(c, up)
			}()
		}
	}()
	return &url.URL{Scheme: "http", Host: ln.Addr().String()}, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), asked...)
	}
}

func TestAddressBookAndExternalPhotoUseSystemProxy(t *testing.T) {
	keyring.MockInit()
	SerializeKeyringForTest()
	roots := testmail.CardDAVRoots()
	prevRoots := conn.TLSRoots
	conn.TLSRoots = roots
	t.Cleanup(func() { conn.TLSRoots = prevRoots })

	srv := testmail.StartCardDAV(t)
	u, err := url.Parse(srv.URL())
	require.NoError(t, err)
	_, port, err := net.SplitHostPort(u.Host)
	require.NoError(t, err)
	proxyURL, asked := tcpConnectProxy(t, port)

	// Serve external photo on public IP literal 93.184.216.34:<port>, which imagefetch.NewClient(false)
	// validates as public and tunnels through s.Proxy without real internet traffic.
	photoBytes := makePNGBytes(t, 20, 20)
	srv.ServePhoto("proxied.png", "image/png", photoBytes)
	externalPhotoURL := "https://93.184.216.34:" + port + "/photos/proxied.png"
	srv.PutCard("contacts", "proxied", "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Proxied Person\r\nEMAIL:proxied@example.com\r\nPHOTO:"+externalPhotoURL+"\r\nEND:VCARD\r\n")

	SetAddressBookPresetURLForTest(t, PresetICloud, "https://contacts.icloud.com:"+port)

	s, err := New(bg, Options{
		DataDir:     t.TempDir(),
		AutoSync:    false,
		SystemProxy: func(*url.URL) *url.URL { return proxyURL },
	})
	require.NoError(t, err)
	defer s.Close()

	ab, err := s.AddAddressBook(bg, AddressBookInput{
		Preset: PresetICloud, Username: "alice", Password: "secret",
	})
	require.NoError(t, err)
	assert.Equal(t, "iCloud", ab.Name)
	assert.Equal(t, 1, ab.Collections[0].CardCount)
	assert.Equal(t, 1, srv.PhotoHits("proxied.png"))

	targets := asked()
	assert.Contains(t, targets, net.JoinHostPort("contacts.icloud.com", port), "CardDAV discovery & sync went through proxy")
	assert.Contains(t, targets, net.JoinHostPort("93.184.216.34", port), "external HTTPS PHOTO went through proxy")

	rows, err := s.db.Query(bg, "SELECT photo FROM address_book_cards")
	require.NoError(t, err)
	require.Len(t, rows.Values, 1)
	assert.Equal(t, photoBytes, rows.Values[0][0].([]byte))
	_ = strconv.Itoa(0)
}

func hostRouteConnectProxy(t *testing.T, routes map[string]string) *url.URL {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				br := bufio.NewReader(c)
				req, err := http.ReadRequest(br)
				if err != nil || req.Method != http.MethodConnect {
					return
				}
				h, _, err := net.SplitHostPort(req.Host)
				if err != nil {
					h = req.Host
				}
				destPort, ok := routes[strings.ToLower(h)]
				if !ok {
					_, _ = io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
					return
				}
				up, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", destPort))
				if err != nil {
					return
				}
				defer up.Close()
				_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\n\r\n")
				go func() { _, _ = io.Copy(up, br); up.Close() }()
				_, _ = io.Copy(c, up)
			}()
		}
	}()
	return &url.URL{Scheme: "http", Host: ln.Addr().String()}
}

func TestAddressBookDiscoveryRejectsCrossDomainAndAllowsSameDomain(t *testing.T) {
	keyring.MockInit()
	SerializeKeyringForTest()
	prevRoots := conn.TLSRoots
	conn.TLSRoots = testmail.CardDAVRoots()
	t.Cleanup(func() { conn.TLSRoots = prevRoots })

	good := testmail.StartCardDAV(t)
	good.PutCard("contacts", "one", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:One\r\nEMAIL:one@example.com\r\nEND:VCARD\r\n")
	evil := testmail.StartCardDAV(t)
	evil.PutCard("contacts", "trap", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Trap\r\nEMAIL:trap@evil.example\r\nEND:VCARD\r\n")

	portOf := func(raw string) string {
		u, err := url.Parse(raw)
		require.NoError(t, err)
		_, p, err := net.SplitHostPort(u.Host)
		require.NoError(t, err)
		return p
	}
	goodPort, evilPort := portOf(good.URL()), portOf(evil.URL())
	proxyURL := hostRouteConnectProxy(t, map[string]string{
		"example.com":                   goodPort,
		"dav.example.com":               goodPort,
		"contacts.icloud.com":           goodPort,
		"p01-contacts.icloud.com":       goodPort,
		"www.googleapis.com":            goodPort,
		"apidata.googleusercontent.com": goodPort,
		"evil.example":                  evilPort,
	})

	s, err := New(bg, Options{
		DataDir:     t.TempDir(),
		AutoSync:    false,
		SystemProxy: func(*url.URL) *url.URL { return proxyURL },
	})
	require.NoError(t, err)
	defer s.Close()

	resetGood := func() {
		good.SetWellKnownRedirect("")
		good.SetPrincipalOnlyURL("")
		good.SetHomeSetURL("")
		evil.ResetRequests()
	}

	for name, configure := range map[string]func(){
		"cross-domain redirect": func() {
			good.SetWellKnownRedirect("https://evil.example/dav/principals/alice/")
		},
		"cross-domain home-set href": func() {
			good.SetWellKnownRedirect("/dav/")
			good.SetHomeSetURL("https://evil.example/dav/addressbooks/alice/")
		},
		"cross-domain principal href": func() {
			good.SetWellKnownRedirect("/dav/")
			good.SetPrincipalOnlyURL("https://evil.example/dav/principals/alice/")
		},
	} {
		t.Run(name, func(t *testing.T) {
			resetGood()
			configure()
			in := AddressBookInput{
				Preset: PresetCustom, URL: "https://example.com", Username: "alice", Password: "secret",
			}
			_, _, err := s.CheckAddressBook(bg, in)
			require.Error(t, err)
			_, err = s.AddAddressBook(bg, in)
			require.Error(t, err)
			for _, req := range evil.Requests() {
				assert.False(t, req.HasAuth, "evil.example must never receive Authorization: %+v", req)
			}
			assert.Empty(t, evil.Requests(), "evil.example must not even be contacted")
			books, err := s.ListAddressBooks(bg)
			require.NoError(t, err)
			assert.Empty(t, books)
		})
	}

	// Same registrable domain redirect (contacts.icloud.com -> p01-contacts.icloud.com) succeeds.
	resetGood()
	good.SetWellKnownRedirect("https://p01-contacts.icloud.com/dav/principals/alice/")
	cols, homeURL, err := s.CheckAddressBook(bg, AddressBookInput{
		Preset: PresetCustom, URL: "https://contacts.icloud.com", Username: "alice", Password: "secret",
	})
	require.NoError(t, err)
	assert.Equal(t, "https://p01-contacts.icloud.com/dav/addressbooks/alice/", homeURL)
	require.Len(t, cols, 1)

	// Same registrable domain XML home-set href (example.com -> dav.example.com) succeeds and persists.
	resetGood()
	good.SetHomeSetURL("https://dav.example.com/dav/addressbooks/alice/")
	ab, err := s.AddAddressBook(bg, AddressBookInput{
		Preset: PresetCustom, URL: "https://example.com", Username: "alice", Password: "secret",
	})
	require.NoError(t, err)
	assert.Equal(t, "https://dav.example.com/dav/addressbooks/alice/", ab.URL)

	// Google CardDAV well-known redirect (www.googleapis.com -> apidata.googleusercontent.com) succeeds.
	resetGood()
	good.SetWellKnownRedirect("https://apidata.googleusercontent.com/dav/principals/alice/")
	_, homeURL, err = s.CheckAddressBook(bg, AddressBookInput{
		Preset: PresetCustom, URL: "https://www.googleapis.com/.well-known/carddav", Username: "alice", Password: "secret",
	})
	require.NoError(t, err)
	assert.Equal(t, "https://apidata.googleusercontent.com/dav/addressbooks/alice/", homeURL)
}

func TestAddAddressBookKeychainRaceAndRollback(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()
	s := newCardDAVService(t, dir, false)
	defer s.Close()

	srv := testmail.StartCardDAV(t)
	srv.PutCard("contacts", "one", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:One\r\nEMAIL:one@example.com\r\nEND:VCARD\r\n")

	origSet := keyringSet
	syncDone := make(chan error, 1)
	setFor(t, &keyringSet, func(service, user, pass string) error {
		if !strings.Contains(user, "/addressbook:") {
			return origSet(service, user, pass)
		}
		// Simulate transient server error after Discover, plus a concurrent sync poll
		// arriving while AddAddressBook is still persisting credentials to the keychain.
		srv.FailRequests(http.StatusServiceUnavailable)
		go func() {
			syncDone <- s.SyncAddressBook(bg, 0)
		}()
		time.Sleep(50 * time.Millisecond)
		return origSet(service, user, pass)
	})

	ab, err := s.AddAddressBook(bg, AddressBookInput{
		Preset: PresetCustom, Name: "RaceBook", URL: srv.URL(), Username: "alice", Password: "secret",
	})
	require.NoError(t, err)
	syncErr := <-syncDone
	assert.NotErrorIs(t, syncErr, keyring.ErrNotFound, "concurrent SyncAddressBook must never observe DB row before keychain credentials exist")
	assert.NotErrorIs(t, syncErr, backend.ErrAuthFailed)

	books, err := s.ListAddressBooks(bg)
	require.NoError(t, err)
	require.Len(t, books, 1)
	assert.Equal(t, ab.ID, books[0].ID)
	assert.NotEqual(t, "reauth_required", books[0].Status, "concurrent sync before keychainSet finished must not strand address book in reauth_required (detail=%s)", books[0].StatusDetail)
	assert.NotEqual(t, "credentials_missing", books[0].StatusDetail)

	// Restore server and verify subsequent sync recovers cleanly to status='ok'.
	srv.FailRequests(0)
	setFor(t, &keyringSet, origSet)
	require.NoError(t, s.SyncAddressBook(bg, ab.ID))
	books, err = s.ListAddressBooks(bg)
	require.NoError(t, err)
	assert.Equal(t, "ok", books[0].Status)

	// Keychain write failure deletes the DB row cleanly while holding abWriteMu.
	setFor(t, &keyringSet, func(service, user, pass string) error {
		if strings.Contains(user, "/addressbook:") {
			return assert.AnError
		}
		return origSet(service, user, pass)
	})
	_, err = s.AddAddressBook(bg, AddressBookInput{
		Preset: PresetCustom, Name: "FailBook", URL: srv.URL(), Username: "alice", Password: "secret",
	})
	require.ErrorIs(t, err, ErrKeychainUnavailable)
	books, err = s.ListAddressBooks(bg)
	require.NoError(t, err)
	require.Len(t, books, 1, "failed AddAddressBook must roll back inserted address_books row")
}

func TestCollectionResetDuringInFlightSyncRejectsStaleDelta(t *testing.T) {
	s := newCardDAVService(t, t.TempDir(), false)

	srv := testmail.StartCardDAV(t)
	srv.PutCard("contacts", "unchanged", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Unchanged\r\nEMAIL:keep@example.com\r\nEND:VCARD\r\n")

	ab, err := s.AddAddressBook(bg, AddressBookInput{
		Preset: PresetCustom, Name: "ResetBook", URL: srv.URL(), Username: "alice", Password: "secret",
	})
	require.NoError(t, err)
	require.Len(t, ab.Collections, 1)
	colID := ab.Collections[0].ID

	// Add a second card so the next sync uses incremental sync-collection for "delta" only.
	srv.PutCard("contacts", "delta", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Delta\r\nEMAIL:delta@example.com\r\nEND:VCARD\r\n")

	var toggled sync.Once
	srv.SetMultigetHook(func(href string) int {
		if strings.HasSuffix(href, "/delta.vcf") {
			toggled.Do(func() {
				require.NoError(t, s.SetAddressBookCollectionEnabled(bg, colID, false))
				require.NoError(t, s.SetAddressBookCollectionEnabled(bg, colID, true))
			})
		}
		return http.StatusOK
	})

	require.NoError(t, s.SyncAddressBook(bg, ab.ID))
	srv.SetMultigetHook(nil)

	contacts, err := s.Contacts(bg)
	require.NoError(t, err)
	addrs := make([]string, len(contacts))
	for i, c := range contacts {
		addrs[i] = c.Address
	}
	assert.ElementsMatch(t, []string{"keep@example.com", "delta@example.com"}, addrs, "stale incremental delta must not drop unchanged cards after disable+re-enable")
}

func TestMultigetPerCardFailureRetainsSyncToken(t *testing.T) {
	s := newCardDAVService(t, t.TempDir(), false)

	srv := testmail.StartCardDAV(t)
	srv.PutCard("contacts", "one", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:One\r\nEMAIL:one@example.com\r\nEND:VCARD\r\n")

	ab, err := s.AddAddressBook(bg, AddressBookInput{
		Preset: PresetCustom, Name: "MultigetFailBook", URL: srv.URL(), Username: "alice", Password: "secret",
	})
	require.NoError(t, err)

	// Add a second card, but fail its individual multiget propstat with 500.
	srv.PutCard("contacts", "two", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Two\r\nEMAIL:two@example.com\r\nEND:VCARD\r\n")
	srv.SetMultigetHook(func(href string) int {
		if strings.HasSuffix(href, "/two.vcf") {
			return http.StatusInternalServerError
		}
		return http.StatusOK
	})

	err = s.SyncAddressBook(bg, ab.ID)
	require.Error(t, err, "per-card 500 in multiget must fail the sync so sync_token does not advance")

	// Server recovers without modifying "two": next incremental sync still fetches "two".
	srv.SetMultigetHook(nil)
	require.NoError(t, s.SyncAddressBook(bg, ab.ID))

	contacts, err := s.Contacts(bg)
	require.NoError(t, err)
	addrs := make([]string, len(contacts))
	for i, c := range contacts {
		addrs[i] = c.Address
	}
	assert.ElementsMatch(t, []string{"one@example.com", "two@example.com"}, addrs)
}

func TestHomeSetPropfindPartialFailurePreservesCollections(t *testing.T) {
	s := newCardDAVService(t, t.TempDir(), false)

	srv := testmail.StartCardDAV(t)
	srv.PutCollection("work", "Work")
	srv.PutCollection("archive", "Archive")
	srv.PutCard("work", "bob", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Bob\r\nEMAIL:bob@example.com\r\nEND:VCARD\r\n")
	srv.PutCard("archive", "old", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Old\r\nEMAIL:old@example.com\r\nEND:VCARD\r\n")

	ab, err := s.AddAddressBook(bg, AddressBookInput{
		Preset: PresetCustom, Name: "PartialPropfind", URL: srv.URL(), Username: "alice", Password: "secret",
	})
	require.NoError(t, err)
	var archiveColID int64
	for _, col := range ab.Collections {
		if col.Name == "Archive" {
			archiveColID = col.ID
		}
	}
	require.NotZero(t, archiveColID)
	require.NoError(t, s.SetAddressBookCollectionEnabled(bg, archiveColID, false))

	// Case 1: transient 500 response on "work" during home-set PROPFIND must fail
	// before deletion reconciliation so cached "bob@example.com" is not cascaded away.
	srv.SetCollectionPropfindHook(func(slug string) string {
		if slug == "work" {
			return `<D:response><D:href>/dav/addressbooks/alice/work/</D:href><D:status>HTTP/1.1 500 Internal Server Error</D:status></D:response>`
		}
		return ""
	})
	require.Error(t, s.SyncAddressBook(bg, ab.ID))
	contacts, err := s.Contacts(bg)
	require.NoError(t, err)
	require.Len(t, contacts, 1)
	assert.Equal(t, "bob@example.com", contacts[0].Address)

	// Case 2: failed resourcetype propstat on disabled "archive" must fail rather
	// than dropping "archive" and losing its disabled preference on recovery.
	srv.SetCollectionPropfindHook(func(slug string) string {
		if slug == "archive" {
			return `<D:response><D:href>/dav/addressbooks/alice/archive/</D:href>` +
				`<D:propstat><D:prop><D:displayname>Archive</D:displayname></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>` +
				`<D:propstat><D:prop><D:resourcetype/></D:prop><D:status>HTTP/1.1 404 Not Found</D:status></D:propstat>` +
				`</D:response>`
		}
		return ""
	})
	require.Error(t, s.SyncAddressBook(bg, ab.ID))

	// Recovery preserves both "work" contacts and "archive" disabled preference.
	srv.SetCollectionPropfindHook(nil)
	require.NoError(t, s.SyncAddressBook(bg, ab.ID))
	books, err := s.ListAddressBooks(bg)
	require.NoError(t, err)
	require.Len(t, books, 1)
	var archiveEnabled *bool
	for _, col := range books[0].Collections {
		if col.Name == "Archive" {
			on := col.Enabled
			archiveEnabled = &on
		}
	}
	require.NotNil(t, archiveEnabled)
	assert.False(t, *archiveEnabled, "disabled collection preference must survive transient home-set PROPFIND failure")
	contacts, err = s.Contacts(bg)
	require.NoError(t, err)
	require.Len(t, contacts, 1)
	assert.Equal(t, "bob@example.com", contacts[0].Address)
}
