package core

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~rockorager/go-jmap/mail/mailbox"
	"github.com/emersion/go-imap/v2"
	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/internal/testmail"
	"github.com/godwit-email/godwit/core/internal/testoauth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

func TestPreviewAndImportAccountsRoundTrip(t *testing.T) {
	src := newOwnClientEnv(t)
	ih, ip := splitAddr(t, src.im.Addr)

	// 1. OAuth account with custom OAuthClientID, alias, months sync range, label keywords, AI instructions, and HTML signature with inline image.
	oauthAcc, err := src.s.AddAccount(bg, src.input(t, "alice@example.com", src.signInWith(t, "alice@example.com", ownClient)))
	require.NoError(t, err)
	sh, sp := oauthAcc.Sending.Host, oauthAcc.Sending.Port
	alias, ai := "Work", "Keep replies concise."
	kw := true
	sigHTML := SignatureHTMLPrefix + `<p><b>Alice</b></p><img src="` + dataURL("image/png", logo(t, 1)) + `">`
	sig, err := src.s.CreateSignature(bg, "Work Sig")
	require.NoError(t, err)
	sig, err = src.s.UpdateSignature(bg, sig.ID, SignaturePatch{HTML: &sigHTML})
	require.NoError(t, err)
	_, err = src.s.UpdateAccount(bg, oauthAcc.ID, AccountPatch{
		Alias:          &alias,
		Sync:           &SyncRange{Months: 6},
		LabelKeywords:  &kw,
		AIInstructions: &ai,
		SignatureID:    &sig.ID,
	})
	require.NoError(t, err)

	// 2. JMAP API-token account with all-mail sync range and plain signature.
	st, err := src.s.GetSettings(bg)
	require.NoError(t, err)
	st.ExperimentalJMAP = true
	_, err = src.s.SetSettings(bg, st)
	require.NoError(t, err)
	jm := testmail.StartJMAP(t)
	jm.CreateMailbox("Drafts", mailbox.RoleDrafts)
	jm.CreateMailbox("Sent", mailbox.RoleSent)
	ju, err := url.Parse(jm.URL())
	require.NoError(t, err)
	jh, jp := splitAddr(t, ju.Host)
	jmapAcc, err := src.s.AddAccount(bg, AccountInput{
		Email:     "bob@example.org",
		Name:      "Bob",
		Username:  "bob-login",
		Password:  "secret",
		Provider:  "fastmail",
		Sync:      &SyncRange{},
		Signature: "Cheers,\nBob",
		Receiving: Server{Protocol: auth.JMAP, Host: jh, Port: jp, Security: SecurityNone, Path: "/.well-known/jmap", Auth: AuthAPIToken},
	})
	require.NoError(t, err)

	// 3. Receive-only password IMAP+SMTP account.
	roAcc, err := src.s.AddAccount(bg, AccountInput{
		Email:       "ro@example.com",
		Name:        "RO",
		Username:    "alice",
		Password:    "secret",
		ReceiveOnly: true,
		Receiving:   Server{Protocol: auth.IMAP, Host: ih, Port: ip, Security: SecurityNone},
		Sending:     Server{Protocol: auth.SMTP, Host: "127.0.0.1", Port: 1, Security: SecurityNone},
	})
	require.NoError(t, err)

	// Reorder so jmapAcc is first, oauthAcc (default) second, roAcc third.
	require.NoError(t, src.s.ReorderAccounts(bg, []int64{jmapAcc.ID, oauthAcc.ID, roAcc.ID}))

	exported, err := src.s.ExportAccounts(bg)
	require.NoError(t, err)

	// Fresh destination service (ExperimentalJMAP is off by default — import must still accept JMAP!).
	dst, err := New(bg, Options{DataDir: t.TempDir(), ImageClient: loopbackImageClient()})
	require.NoError(t, err)
	t.Cleanup(func() { dst.Close() })

	preview, err := dst.PreviewAccountImport(bg, exported)
	require.NoError(t, err)
	assert.Equal(t, []AccountImportEntry{
		{
			Email:             "bob@example.org",
			Name:              "Bob",
			ReceivingProtocol: auth.JMAP,
			ReceivingHost:     jh,
			ReceivingPort:     jp,
			SendingProtocol:   auth.JMAP,
			SendingHost:       jh,
			SendingPort:       jp,
			Auth:              AuthAPIToken,
			Existing:          false,
		},
		{
			Email:             "alice@example.com",
			Name:              oauthAcc.Name,
			ReceivingProtocol: auth.IMAP,
			ReceivingHost:     ih,
			ReceivingPort:     ip,
			SendingProtocol:   auth.SMTP,
			SendingHost:       sh,
			SendingPort:       sp,
			Auth:              AuthOAuth,
			Existing:          false,
		},
		{
			Email:             "ro@example.com",
			Name:              "RO",
			ReceivingProtocol: auth.IMAP,
			ReceivingHost:     ih,
			ReceivingPort:     ip,
			SendingProtocol:   auth.SMTP,
			SendingHost:       "127.0.0.1",
			SendingPort:       1,
			ReceiveOnly:       true,
			Auth:              AuthPassword,
			Existing:          false,
		},
	}, preview.Accounts)

	res, err := dst.ImportAccounts(bg, exported)
	require.NoError(t, err)
	require.Len(t, res.Imported, 3)
	assert.Empty(t, res.Skipped)

	got, err := dst.ListAccounts(bg)
	require.NoError(t, err)
	require.Len(t, got, 3)

	// Order, default flag, servers, sync range, label keywords, AI instructions, signature, and reauth_required status are all restored.
	assert.Equal(t, "bob@example.org", got[0].Email)
	assert.False(t, got[0].IsDefault)
	assert.Equal(t, SyncRange{}, got[0].Sync)
	assert.Equal(t, "Cheers,\nBob", got[0].Signature)
	assert.Equal(t, "reauth_required", got[0].Status)
	assert.Equal(t, "credentials_missing", got[0].StatusDetail)

	assert.Equal(t, "alice@example.com", got[1].Email)
	assert.True(t, got[1].IsDefault, "exported default account is restored when destination had no default")
	assert.Equal(t, "Work", got[1].Alias)
	assert.Equal(t, SyncRange{Months: 6}, got[1].Sync)
	assert.True(t, got[1].LabelKeywords)
	assert.Equal(t, "Keep replies concise.", got[1].AIInstructions)
	assert.Equal(t, testoauth.ClientID, got[1].OAuthClientID)
	assert.Equal(t, AuthOAuth, got[1].AuthType)
	assert.Equal(t, "reauth_required", got[1].Status)
	assert.Equal(t, "credentials_missing", got[1].StatusDetail)
	assert.True(t, strings.HasPrefix(got[1].Signature, SignatureHTMLPrefix))
	assert.Contains(t, got[1].Signature, "cid:sig-")

	sigs, err := dst.ListSignatures(bg)
	require.NoError(t, err)
	require.Len(t, sigs, 2)
	assert.Equal(t, "Work Sig", sigs[1].Name)

	assert.Equal(t, "ro@example.com", got[2].Email)
	assert.True(t, got[2].ReceiveOnly)
	assert.Equal(t, "reauth_required", got[2].Status)
	assert.Equal(t, "credentials_missing", got[2].StatusDetail)

	// Exporting from dst produces byte-for-byte identical JSON to src!
	reexported, err := dst.ExportAccounts(bg)
	require.NoError(t, err)
	assert.Equal(t, string(exported), string(reexported))

	// Second preview and import detect case-insensitive duplicates and skip them.
	upperCaseExport := strings.ReplaceAll(string(exported), `"alice@example.com"`, `"ALICE@Example.com"`)
	preview2, err := dst.PreviewAccountImport(bg, []byte(upperCaseExport))
	require.NoError(t, err)
	for _, entry := range preview2.Accounts {
		assert.True(t, entry.Existing, "expected %s to be marked Existing", entry.Email)
	}

	res2, err := dst.ImportAccounts(bg, []byte(upperCaseExport))
	require.NoError(t, err)
	assert.Empty(t, res2.Imported)
	assert.Equal(t, []AccountImportSkip{{Email: "bob@example.org"}, {Email: "ALICE@Example.com"}, {Email: "ro@example.com"}}, res2.Skipped)
}

func TestAccountImportPreservesExistingDefaultAndSharesSignature(t *testing.T) {
	keyring.MockInit()
	src, err := New(bg, Options{DataDir: t.TempDir(), ImageClient: loopbackImageClient()})
	require.NoError(t, err)
	t.Cleanup(func() { src.Close() })

	im := testmail.StartIMAP(t)
	smtpAddr, _ := testmail.StartSMTP(t)
	a1, err := src.AddAccount(bg, internalInput(t, im.Addr, smtpAddr, "first@example.com", true))
	require.NoError(t, err)
	a2, err := src.AddAccount(bg, internalInput(t, im.Addr, smtpAddr, "second@example.com", false))
	require.NoError(t, err)

	// Share a single signature across both accounts.
	sig, err := src.CreateSignature(bg, "Shared Team")
	require.NoError(t, err)
	body := "Team Signature"
	_, err = src.UpdateSignature(bg, sig.ID, SignaturePatch{HTML: &body})
	require.NoError(t, err)
	_, err = src.UpdateAccount(bg, a1.ID, AccountPatch{SignatureID: &sig.ID})
	require.NoError(t, err)
	_, err = src.UpdateAccount(bg, a2.ID, AccountPatch{SignatureID: &sig.ID})
	require.NoError(t, err)

	exported, err := src.ExportAccounts(bg)
	require.NoError(t, err)

	// Destination already has its own default account ("existing@example.com").
	dst, err := New(bg, Options{DataDir: t.TempDir(), ImageClient: loopbackImageClient()})
	require.NoError(t, err)
	t.Cleanup(func() { dst.Close() })
	existing, err := dst.AddAccount(bg, internalInput(t, im.Addr, smtpAddr, "existing@example.com", true))
	require.NoError(t, err)

	res, err := dst.ImportAccounts(bg, exported)
	require.NoError(t, err)
	require.Len(t, res.Imported, 2)
	assert.False(t, res.Imported[0].IsDefault, "imported account must not steal default when one already exists")
	assert.False(t, res.Imported[1].IsDefault)
	assert.Equal(t, res.Imported[0].SignatureID, res.Imported[1].SignatureID, "identical exported signature is shared")

	accs, err := dst.ListAccounts(bg)
	require.NoError(t, err)
	require.Len(t, accs, 3)
	assert.Equal(t, existing.ID, accs[0].ID)
	assert.True(t, accs[0].IsDefault)
}

func TestAccountImportValidationAndAtomicity(t *testing.T) {
	keyring.MockInit()
	s, err := New(bg, Options{DataDir: t.TempDir(), ImageClient: loopbackImageClient()})
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })

	validEntry := ExportedAccount{
		Email:     "good@example.com",
		Name:      "Good",
		Username:  "good",
		Provider:  "other",
		IsDefault: true,
		Receiving: ExportedServer{Protocol: auth.IMAP, Host: "127.0.0.1", Port: 993, Security: SecurityNone, Auth: AuthPassword},
		Sending:   ExportedServer{Protocol: auth.SMTP, Host: "127.0.0.1", Port: 587, Security: SecurityNone, Auth: AuthPassword},
		Sync:      ExportedSyncRange{Limit: 2000},
		Signature: &ExportedSignature{Name: "Good Sig", HTML: "Best"},
	}
	mustJSON := func(v any) []byte {
		b, err := json.Marshal(v)
		require.NoError(t, err)
		return b
	}

	badVersionCases := []struct {
		name string
		raw  []byte
	}{
		{"version 0", []byte(`{"version":0,"accounts":[]}`)},
		{"version 2 with future fields", []byte(`{"version":2,"future":true,"accounts":[{"email":"x@example.com","future_field":1}]}`)},
		{"negative version", []byte(`{"version":-1,"accounts":[]}`)},
	}
	for _, tc := range badVersionCases {
		_, err := s.PreviewAccountImport(bg, tc.raw)
		assert.ErrorIs(t, err, ErrUnsupportedExportVersion, tc.name)
		_, err = s.ImportAccounts(bg, tc.raw)
		assert.ErrorIs(t, err, ErrUnsupportedExportVersion, tc.name)
	}

	badExportCases := []struct {
		name string
		raw  []byte
	}{
		{"corrupted json", []byte(`{"version": 1, "accounts": [`)},
		{"trailing garbage", []byte(`{"version":1,"accounts":[]} extra`)},
		{"missing version", []byte(`{"accounts":[]}`)},
		{"missing accounts", []byte(`{"version":1}`)},
		{"null accounts", []byte(`{"version":1,"accounts":null}`)},
		{"unknown field", []byte(`{"version":1,"unknown":1,"accounts":[]}`)},
		{"second entry missing host (atomic)", mustJSON(AccountExport{
			Version: AccountExportVersion,
			Accounts: []ExportedAccount{
				validEntry,
				{
					Email:     "bad@example.com",
					Receiving: ExportedServer{Protocol: auth.IMAP, Host: "", Port: 993, Security: SecurityTLS, Auth: AuthPassword},
					Sending:   ExportedServer{Protocol: auth.SMTP, Host: "smtp.example.com", Port: 587, Security: SecurityStartTLS, Auth: AuthPassword},
				},
			},
		})},
		{"plaintext to remote host", mustJSON(AccountExport{
			Version: AccountExportVersion,
			Accounts: []ExportedAccount{
				validEntry,
				{
					Email:     "plain@example.com",
					Receiving: ExportedServer{Protocol: auth.IMAP, Host: "imap.example.com", Port: 143, Security: SecurityNone, Auth: AuthPassword},
					Sending:   ExportedServer{Protocol: auth.SMTP, Host: "smtp.example.com", Port: 587, Security: SecurityStartTLS, Auth: AuthPassword},
				},
			},
		})},
		{"invalid email with display name", mustJSON(AccountExport{
			Version: AccountExportVersion,
			Accounts: []ExportedAccount{
				validEntry,
				{
					Email:     "Bad <bad@example.com>",
					Receiving: ExportedServer{Protocol: auth.IMAP, Host: "127.0.0.1", Port: 993, Security: SecurityNone, Auth: AuthPassword},
					Sending:   ExportedServer{Protocol: auth.SMTP, Host: "127.0.0.1", Port: 587, Security: SecurityNone, Auth: AuthPassword},
				},
			},
		})},
		{"local.invalid address", mustJSON(AccountExport{
			Version: AccountExportVersion,
			Accounts: []ExportedAccount{
				{
					Email:     "archive-123@local.invalid",
					Receiving: ExportedServer{Protocol: auth.IMAP, Host: "127.0.0.1", Port: 993, Security: SecurityNone, Auth: AuthPassword},
					Sending:   ExportedServer{Protocol: auth.SMTP, Host: "127.0.0.1", Port: 587, Security: SecurityNone, Auth: AuthPassword},
				},
			},
		})},
		{"bad sync range", mustJSON(AccountExport{
			Version: AccountExportVersion,
			Accounts: []ExportedAccount{
				validEntry,
				{
					Email:     "sync@example.com",
					Receiving: ExportedServer{Protocol: auth.IMAP, Host: "127.0.0.1", Port: 993, Security: SecurityNone, Auth: AuthPassword},
					Sending:   ExportedServer{Protocol: auth.SMTP, Host: "127.0.0.1", Port: 587, Security: SecurityNone, Auth: AuthPassword},
					Sync:      ExportedSyncRange{Limit: 100, Months: 3},
				},
			},
		})},
		{"corrupted signature image", mustJSON(AccountExport{
			Version: AccountExportVersion,
			Accounts: []ExportedAccount{
				validEntry,
				{
					Email:     "sig@example.com",
					Receiving: ExportedServer{Protocol: auth.IMAP, Host: "127.0.0.1", Port: 993, Security: SecurityNone, Auth: AuthPassword},
					Sending:   ExportedServer{Protocol: auth.SMTP, Host: "127.0.0.1", Port: 587, Security: SecurityNone, Auth: AuthPassword},
					Signature: &ExportedSignature{Name: "Bad", HTML: SignatureHTMLPrefix + `<img src="data:image/png;base64,not-valid-png">`},
				},
			},
		})},
		{"conflicting protocols for same email in file", mustJSON(AccountExport{
			Version: AccountExportVersion,
			Accounts: []ExportedAccount{
				validEntry,
				{
					Email:     "GOOD@example.com",
					Receiving: ExportedServer{Protocol: auth.JMAP, Host: "127.0.0.1", Port: 8080, Security: SecurityNone, Auth: AuthAPIToken},
					Sending:   ExportedServer{Protocol: auth.JMAP, Host: "127.0.0.1", Port: 8080, Security: SecurityNone, Auth: AuthAPIToken},
				},
			},
		})},
	}
	for _, tc := range badExportCases {
		_, err := s.PreviewAccountImport(bg, tc.raw)
		assert.ErrorIs(t, err, ErrBadAccountExport, tc.name)
		_, err = s.ImportAccounts(bg, tc.raw)
		assert.ErrorIs(t, err, ErrBadAccountExport, tc.name)
	}

	// Verify ZERO accounts and ZERO signatures were written across all failed imports.
	accs, err := s.ListAccounts(bg)
	require.NoError(t, err)
	assert.Empty(t, accs)
	sigs, err := s.ListSignatures(bg)
	require.NoError(t, err)
	assert.Empty(t, sigs)

	require.NoError(t, s.Close())
	_, err = s.PreviewAccountImport(bg, []byte(`{"version":1,"accounts":[]}`))
	assert.ErrorIs(t, err, ErrClosed)
	_, err = s.ImportAccounts(bg, []byte(`{"version":1,"accounts":[]}`))
	assert.ErrorIs(t, err, ErrClosed)
}

func TestAccountImportSkipsAddressOnAnotherProtocol(t *testing.T) {
	keyring.MockInit()
	s, err := New(bg, Options{DataDir: t.TempDir(), ImageClient: loopbackImageClient()})
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })

	im := testmail.StartIMAP(t)
	smtpAddr, _ := testmail.StartSMTP(t)
	_, err = s.AddAccount(bg, internalInput(t, im.Addr, smtpAddr, "same@example.com", true))
	require.NoError(t, err)
	_, err = s.AddAccount(bg, internalInput(t, im.Addr, smtpAddr, "clash@example.com", false))
	require.NoError(t, err)
	before, err := s.ListAccounts(bg)
	require.NoError(t, err)

	imapSlot := ExportedServer{Protocol: auth.IMAP, Host: "127.0.0.1", Port: 993, Security: SecurityNone, Auth: AuthPassword}
	smtpSlot := ExportedServer{Protocol: auth.SMTP, Host: "127.0.0.1", Port: 587, Security: SecurityNone, Auth: AuthPassword}
	jmapSlot := ExportedServer{Protocol: auth.JMAP, Host: "127.0.0.1", Port: 8080, Security: SecurityNone, Auth: AuthAPIToken}
	export := mustJSON(t, AccountExport{
		Version: AccountExportVersion,
		Accounts: []ExportedAccount{
			{Email: "new@example.com", Receiving: imapSlot, Sending: smtpSlot},
			{Email: "SAME@example.com", Receiving: imapSlot, Sending: smtpSlot},
			{Email: "CLASH@example.com", Receiving: jmapSlot, Sending: jmapSlot},
		},
	})

	preview, err := s.PreviewAccountImport(bg, export)
	require.NoError(t, err)
	require.Len(t, preview.Accounts, 3)
	assert.False(t, preview.Accounts[0].Existing)
	assert.True(t, preview.Accounts[1].Existing)
	assert.Empty(t, preview.Accounts[1].ExistingProtocol)
	assert.True(t, preview.Accounts[2].Existing)
	assert.Equal(t, "imap", preview.Accounts[2].ExistingProtocol)

	res, err := s.ImportAccounts(bg, export)
	require.NoError(t, err)
	require.Len(t, res.Imported, 1)
	assert.Equal(t, "new@example.com", res.Imported[0].Email)
	assert.Equal(t, []AccountImportSkip{
		{Email: "SAME@example.com"},
		{Email: "CLASH@example.com", ExistingProtocol: "imap"},
	}, res.Skipped)

	after, err := s.ListAccounts(bg)
	require.NoError(t, err)
	require.Len(t, after, 3)
	assert.Equal(t, before, after[:2], "the local accounts are untouched")
}

func TestImportedAccountsParkWithZeroLoginsUntilReauthorized(t *testing.T) {
	SerializeKeyringForTest()

	// Set up OAuth server + IMAP/SMTP servers for 3 accounts:
	// 1. Password IMAP+SMTP
	// 2. Default-client OAuth IMAP+SMTP
	// 3. Own-client OAuth IMAP+SMTP (client secret NOT exported)
	const (
		pwSecret     = "pw-secret-8899"
		clientSecret = "own-client-secret-7766"
	)
	pwIM := testmail.StartIMAP(t)
	pwIM.SetPassword(pwSecret)
	pwSMTPAddr, pwSMTP := testmail.StartSMTP(t)
	pwSMTP.SetPassword(pwSecret)
	_, err := pwIM.User.Append("INBOX", bytes.NewReader([]byte(eml("pw-1", "sender@example.com", "pw@example.com", "Hello PW", "Mon, 02 Jan 2026 15:04:05 +0000", "PW body"))), &imap.AppendOptions{})
	require.NoError(t, err)

	oa := testoauth.Start(t)
	setFor(t, &oauthConfig, func(prov string) (auth.OAuthConfig, error) {
		cfg := oa.Config("openid", "email", "offline_access")
		if prov == "gmail" {
			cfg.ClientID = "" // own-client provider
		}
		return cfg, nil
	})
	oauthIM := testmail.StartIMAP(t, testmail.WithXOAuth2Func(oa.ValidAccess))
	oauthSMTPAddr, _ := testmail.StartSMTP(t, testmail.WithXOAuth2Func(oa.ValidAccess))
	_, err = oauthIM.User.Append("INBOX", bytes.NewReader([]byte(eml("oa-1", "sender@example.com", "alice@example.com", "Hello OAuth", "Mon, 02 Jan 2026 15:04:05 +0000", "OAuth body"))), &imap.AppendOptions{})
	require.NoError(t, err)

	pih, pip := splitAddr(t, pwIM.Addr)
	psh, psp := splitAddr(t, pwSMTPAddr)
	oih, oip := splitAddr(t, oauthIM.Addr)
	osh, osp := splitAddr(t, oauthSMTPAddr)

	exportBytes, err := json.Marshal(AccountExport{
		Version: AccountExportVersion,
		Accounts: []ExportedAccount{
			{
				Email:     "pw@example.com",
				Name:      "PW",
				Username:  "alice",
				Provider:  "other",
				IsDefault: true,
				Receiving: ExportedServer{Protocol: auth.IMAP, Host: pih, Port: pip, Security: SecurityNone, Auth: AuthPassword},
				Sending:   ExportedServer{Protocol: auth.SMTP, Host: psh, Port: psp, Security: SecurityNone, Auth: AuthPassword},
				Sync:      ExportedSyncRange{Limit: 2000},
			},
			{
				Email:     "alice@outlook.example",
				Name:      "Outlook User",
				Username:  "alice@outlook.example",
				Provider:  "outlook",
				Receiving: ExportedServer{Protocol: auth.IMAP, Host: oih, Port: oip, Security: SecurityNone, Auth: AuthOAuth},
				Sending:   ExportedServer{Protocol: auth.SMTP, Host: osh, Port: osp, Security: SecurityNone, Auth: AuthOAuth},
				Sync:      ExportedSyncRange{Limit: 2000},
			},
			{
				Email:         "alice@gmail.example",
				Name:          "Own Client User",
				Username:      "alice@gmail.example",
				Provider:      "gmail",
				OAuthClientID: testoauth.ClientID,
				Receiving:     ExportedServer{Protocol: auth.IMAP, Host: oih, Port: oip, Security: SecurityNone, Auth: AuthOAuth},
				Sending:       ExportedServer{Protocol: auth.SMTP, Host: osh, Port: osp, Security: SecurityNone, Auth: AuthOAuth},
				Sync:          ExportedSyncRange{Limit: 2000},
			},
		},
	})
	require.NoError(t, err)

	parked := make(chan int64, 16)
	setFor(t, &onParked, func(id int64) {
		select {
		case parked <- id:
		default:
		}
	})
	setFor(t, &backoffMin, 20*time.Millisecond)
	setFor(t, &backoffMax, 200*time.Millisecond)
	keyring.MockInit()

	dst, err := New(bg, Options{DataDir: t.TempDir(), AutoSync: true, ImageClient: loopbackImageClient()})
	require.NoError(t, err)
	t.Cleanup(func() { dst.Close() })
	evs, unsub := dst.Subscribe()
	t.Cleanup(unsub)

	res, err := dst.ImportAccounts(bg, exportBytes)
	require.NoError(t, err)
	require.Len(t, res.Imported, 3)
	pwAcc, defOAuthAcc, ownOAuthAcc := res.Imported[0], res.Imported[1], res.Imported[2]

	// Wait for all 3 background workers to park in awaitCredentials.
	seenParked := map[int64]bool{}
	deadline := time.After(3 * time.Second)
	for len(seenParked) < 3 {
		select {
		case id := <-parked:
			seenParked[id] = true
		case <-deadline:
			t.Fatalf("timed out waiting for 3 imported workers to park, got %v", seenParked)
		}
	}

	// Waking the worker directly while credentials are still missing re-parks it with 0 login attempts!
	dst.wakeWorker(pwAcc.ID)
	select {
	case <-parked:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not re-park after wake")
	}

	assert.Equal(t, int64(0), pwIM.Logins(), "imported password account must make 0 IMAP login attempts before reauthorization")
	assert.Equal(t, 0, pwSMTP.AuthAttempts(), "imported password account must make 0 SMTP login attempts before reauthorization")
	assert.Equal(t, int64(0), oauthIM.Logins(), "imported OAuth accounts must make 0 IMAP login attempts before reauthorization")
	assert.Equal(t, 0, oa.TokenRequests(), "imported OAuth accounts must make 0 token requests before reauthorization")

	waitSync := func(wantID int64) {
		t.Helper()
		timeout := time.After(5 * time.Second)
		for {
			select {
			case ev := <-evs:
				if ev.Type == events.SyncFinished && ev.AccountID == wantID {
					return
				}
			case <-timeout:
				t.Fatalf("timed out waiting for SyncFinished on account %d", wantID)
			}
		}
	}

	inboxThreads := func(accID int64) []ThreadSummary {
		t.Helper()
		fs, err := dst.ListFolders(bg, accID)
		require.NoError(t, err)
		for _, f := range fs {
			if f.Role == "inbox" {
				tp, err := dst.ListThreads(bg, View{FolderID: f.ID}, "", 10)
				require.NoError(t, err)
				return tp.Items
			}
		}
		t.Fatalf("no inbox folder for account %d", accID)
		return nil
	}

	// 1. Reauthorize password account: UpdatePassword clears reauth_required, records features, wakes worker, and syncs mail!
	require.NoError(t, dst.UpdatePassword(bg, pwAcc.ID, pwSecret))
	updatedPW, err := dst.account(bg, pwAcc.ID)
	require.NoError(t, err)
	assert.Equal(t, "ok", updatedPW.Status)
	assert.Empty(t, updatedPW.StatusDetail)
	assert.NotNil(t, updatedPW.Features, "UpdatePassword populates server features")
	waitSync(pwAcc.ID)
	items := inboxThreads(pwAcc.ID)
	require.Len(t, items, 1)
	assert.Equal(t, "Hello PW", items[0].Subject)

	// 2. Reauthorize default-client OAuth account: BeginOAuth(Account: id) -> AwaitOAuth -> Reauthorize -> syncs!
	flowID, authURL, err := dst.BeginOAuth(bg, defOAuthAcc.Provider, defOAuthAcc.Email, OAuthClient{Account: defOAuthAcc.ID})
	require.NoError(t, err)
	ch := await(dst, flowID)
	oa.Browse(t, authURL, defOAuthAcc.Email)
	require.NoError(t, recvAwait(t, ch).err)
	require.NoError(t, dst.Reauthorize(bg, defOAuthAcc.ID, flowID))
	waitSync(defOAuthAcc.ID)
	items = inboxThreads(defOAuthAcc.ID)
	require.Len(t, items, 1)
	assert.Equal(t, "Hello OAuth", items[0].Subject)

	// 3. Reauthorize own-client OAuth account: secret was NOT exported, so Account-only BeginOAuth fails with ErrSecretNotFound,
	//    and supplying client ID + secret succeeds, saves secret for future refreshes, and syncs!
	oa.Secret(clientSecret)
	_, _, err = dst.BeginOAuth(bg, ownOAuthAcc.Provider, ownOAuthAcc.Email, OAuthClient{Account: ownOAuthAcc.ID})
	assert.ErrorIs(t, err, ErrSecretNotFound)

	flowID, authURL, err = dst.BeginOAuth(bg, ownOAuthAcc.Provider, ownOAuthAcc.Email, OAuthClient{ID: testoauth.ClientID, Secret: clientSecret})
	require.NoError(t, err)
	ch = await(dst, flowID)
	oa.Browse(t, authURL, ownOAuthAcc.Email)
	require.NoError(t, recvAwait(t, ch).err)
	require.NoError(t, dst.Reauthorize(bg, ownOAuthAcc.ID, flowID))
	waitSync(ownOAuthAcc.ID)

	items = inboxThreads(ownOAuthAcc.ID)
	require.Len(t, items, 1)
	assert.Equal(t, "Hello OAuth", items[0].Subject)

	// Force token refresh on ownOAuthAcc to prove client secret was persisted in keychain.
	dst.tokMu.Lock()
	delete(dst.tokens, ownOAuthAcc.ID)
	dst.tokMu.Unlock()
	reqsBefore := oa.TokenRequests()
	require.NoError(t, dst.SyncNow(bg, ownOAuthAcc.ID))
	assert.Equal(t, reqsBefore+1, oa.TokenRequests())
}
