package core

import (
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.sr.ht/~rockorager/go-jmap/mail/mailbox"
	"github.com/emersion/go-imap/v2"
	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/core/internal/testmail"
	"github.com/godwit-email/godwit/core/internal/testoauth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

func TestExportAccounts(t *testing.T) {
	e := newSyncEnv(t)
	ih, ip := splitAddr(t, e.im.Addr)
	sh, sp := e.acc.Sending.Host, e.acc.Sending.Port

	// Configure e.acc ("alice@example.com"): custom alias, sync months, label keywords, AI instructions, and an HTML signature with an image.
	alias, ai := "Work", "Sign off as Alice."
	kw := true
	sigHTML := SignatureHTMLPrefix + `<p><b>Alice</b></p><img src="` + dataURL("image/png", logo(t, 1)) + `">`
	sig, err := e.s.CreateSignature(bg, "Work Sig")
	require.NoError(t, err)
	sig, err = e.s.UpdateSignature(bg, sig.ID, SignaturePatch{HTML: &sigHTML})
	require.NoError(t, err)
	require.Contains(t, sig.HTML, "cid:sig-")

	_, err = e.s.UpdateAccount(bg, e.acc.ID, AccountPatch{
		Alias:          &alias,
		Sync:           &SyncRange{Months: 6},
		LabelKeywords:  &kw,
		AIInstructions: &ai,
		SignatureID:    &sig.ID,
	})
	require.NoError(t, err)

	// Add a JMAP API-token account with "all mail" sync range ({}) and a plain-text signature.
	st, err := e.s.GetSettings(bg)
	require.NoError(t, err)
	st.ExperimentalJMAP = true
	_, err = e.s.SetSettings(bg, st)
	require.NoError(t, err)

	jm := testmail.StartJMAP(t)
	jm.CreateMailbox("Drafts", mailbox.RoleDrafts)
	jm.CreateMailbox("Sent", mailbox.RoleSent)
	ju, err := url.Parse(jm.URL())
	require.NoError(t, err)
	jh, jp := splitAddr(t, ju.Host)
	jmapAcc, err := e.s.AddAccount(bg, AccountInput{
		Email:       "bob@example.org",
		Name:        "Bob",
		Username:    "bob-login",
		Password:    "secret",
		Provider:    "fastmail",
		Sync:        &SyncRange{},
		Signature:   "Cheers,\nBob",
		Receiving:   Server{Protocol: auth.JMAP, Host: jh, Port: jp, Security: SecurityNone, Path: "/.well-known/jmap", Auth: AuthAPIToken},
		ReceiveOnly: false,
	})
	require.NoError(t, err)

	// Put jmapAcc first so ExportAccounts must follow ReorderAccounts order.
	require.NoError(t, e.s.ReorderAccounts(bg, []int64{jmapAcc.ID, e.acc.ID}))

	raw, err := e.s.ExportAccounts(bg)
	require.NoError(t, err)
	text := string(raw)
	assert.True(t, strings.HasPrefix(text, "{\n  \"version\": 1,\n  \"accounts\": [\n"), "2-space indented versioned JSON:\n%s", text)
	assert.True(t, strings.HasSuffix(text, "\n}\n"), "ends with newline")

	var got AccountExport
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, AccountExportVersion, got.Version)
	require.Len(t, got.Accounts, 2)

	// First: bob@example.org (JMAP, all-mail sync, plain signature).
	bob := got.Accounts[0]
	assert.Equal(t, ExportedAccount{
		Email:     "bob@example.org",
		Name:      "Bob",
		Username:  "bob-login",
		Provider:  "fastmail",
		Receiving: ExportedServer{Protocol: auth.JMAP, Host: jh, Port: jp, Security: SecurityNone, Path: "/.well-known/jmap", Auth: AuthAPIToken},
		Sending:   ExportedServer{Protocol: auth.JMAP, Host: jh, Port: jp, Security: SecurityNone, Path: "/.well-known/jmap", Auth: AuthAPIToken},
		Sync:      ExportedSyncRange{},
		Signature: &ExportedSignature{Name: "Bob", HTML: "Cheers,\nBob"},
	}, bob)

	// Second: alice@example.com (IMAP+SMTP, default, HTML signature with data: image).
	alice := got.Accounts[1]
	require.NotNil(t, alice.Signature)
	assert.Equal(t, "Work Sig", alice.Signature.Name)
	assert.True(t, strings.HasPrefix(alice.Signature.HTML, SignatureHTMLPrefix))
	assert.Contains(t, alice.Signature.HTML, "data:image/png;base64,")
	assert.NotContains(t, alice.Signature.HTML, "cid:sig-")
	alice.Signature = nil
	assert.Equal(t, ExportedAccount{
		Email:          "alice@example.com",
		Name:           e.acc.Name,
		Alias:          "Work",
		Username:       "alice",
		Provider:       "other",
		IsDefault:      true,
		Receiving:      ExportedServer{Protocol: auth.IMAP, Host: ih, Port: ip, Security: SecurityNone, Auth: AuthPassword},
		Sending:        ExportedServer{Protocol: auth.SMTP, Host: sh, Port: sp, Security: SecurityNone, Auth: AuthPassword},
		Sync:           ExportedSyncRange{Months: 6},
		LabelKeywords:  true,
		AIInstructions: "Sign off as Alice.",
	}, alice)

	// Closed service refuses ExportAccounts with ErrClosed.
	require.NoError(t, e.s.Close())
	_, err = e.s.ExportAccounts(bg)
	assert.ErrorIs(t, err, ErrClosed)
}

func TestExportAccountsExcludesLocalArchive(t *testing.T) {
	keyring.MockInit()
	s, err := New(bg, Options{DataDir: t.TempDir(), ImageClient: loopbackImageClient()})
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })

	// Empty service exports an empty JSON array, not null.
	raw, err := s.ExportAccounts(bg)
	require.NoError(t, err)
	assert.Equal(t, "{\n  \"version\": 1,\n  \"accounts\": []\n}\n", string(raw))

	// Add a local archive and import an .eml into its inbox.
	arch, err := s.AddLocalArchive(bg, "Offline Archive")
	require.NoError(t, err)
	fs, err := s.ListFolders(bg, arch.ID)
	require.NoError(t, err)
	emlPath := filepath.Join(t.TempDir(), "archived.eml")
	require.NoError(t, os.WriteFile(emlPath, []byte(eml("a1", "bob@example.com", "alice@example.com", "Archived Secret", "Mon, 02 Jan 2026 15:04:05 +0000", "Archived body")), 0o600))
	_, err = s.ImportEML(bg, arch.ID, fs[0].ID, []string{emlPath})
	require.NoError(t, err)

	raw, err = s.ExportAccounts(bg)
	require.NoError(t, err)
	assert.Equal(t, "{\n  \"version\": 1,\n  \"accounts\": []\n}\n", string(raw))
	assert.NotContains(t, string(raw), "Offline Archive")
	assert.NotContains(t, string(raw), "local.invalid")
	assert.NotContains(t, string(raw), KindLocalArchive)

	// Add one real mail account alongside the local archive: only the real account is exported.
	smtpAddr, _ := testmail.StartSMTP(t)
	im := testmail.StartIMAP(t)
	mailAcc, err := s.AddAccount(bg, internalInput(t, im.Addr, smtpAddr, "real@example.com", true))
	require.NoError(t, err)

	raw, err = s.ExportAccounts(bg)
	require.NoError(t, err)
	var got AccountExport
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Len(t, got.Accounts, 1)
	assert.Equal(t, mailAcc.Email, got.Accounts[0].Email)
}

func TestExportAccountsNeverIncludesCredentialsOrMail(t *testing.T) {
	e := newOwnClientEnv(t)
	const clientSecret = "oauth-client-secret-7788"
	e.oa.Secret(clientSecret)

	// 1. OAuth account with custom client ID + secret, access token, and refresh token.
	oauthAcc, err := e.s.AddAccount(bg, e.input(t, "alice@example.com", e.signInWith(t, "alice@example.com", OAuthClient{
		ID:     testoauth.ClientID,
		Secret: clientSecret,
	})))
	require.NoError(t, err)
	refreshTok, err := keyring.Get(keyringService, e.s.keyringUser(oauthAcc.ID))
	require.NoError(t, err)
	require.NotEmpty(t, refreshTok)
	e.s.tokMu.Lock()
	accessTok := e.s.tokens[oauthAcc.ID].access
	e.s.tokMu.Unlock()
	require.NotEmpty(t, accessTok)

	// 2. Password IMAP+SMTP account with distinct receiving and sending passwords, plus mail and draft.
	const (
		imapPass = "imap-secret-pass-3344"
		smtpPass = "smtp-secret-pass-5566"
		jmapTok  = "jmap-bearer-tok-9900"
		mailSubj = "Confidential Merger 1122"
		mailBody = "Secret Wire Transfer 4455"
		draftTxt = "Draft Confidential 6677"
	)
	pwIM := testmail.StartIMAP(t)
	pwIM.SetPassword(imapPass)
	pwSMTPAddr, pwSMTP := testmail.StartSMTP(t)
	pwSMTP.SetPassword(smtpPass)
	ih, ip := splitAddr(t, pwIM.Addr)
	sh, sp := splitAddr(t, pwSMTPAddr)
	pwAcc, err := e.s.AddAccount(bg, AccountInput{
		Email:        "pw@example.com",
		Name:         "PW User",
		Username:     "alice",
		Password:     imapPass,
		SendPassword: smtpPass,
		Receiving:    Server{Protocol: auth.IMAP, Host: ih, Port: ip, Security: SecurityNone},
		Sending:      Server{Protocol: auth.SMTP, Host: sh, Port: sp, Security: SecurityNone},
	})
	require.NoError(t, err)

	// Seed and sync a message, and save a draft.
	_, err = pwIM.User.Append("INBOX", bytes.NewReader([]byte(eml("m1", "boss@example.com", "pw@example.com", mailSubj, "Mon, 02 Jan 2026 15:04:05 +0000", mailBody))), &imap.AppendOptions{})
	require.NoError(t, err)
	require.NoError(t, e.s.SyncNow(bg, pwAcc.ID))
	d, err := e.s.NewDraft(bg, pwAcc.ID, "new", 0)
	require.NoError(t, err)
	d.Subject, d.HTML = mailSubj, "<p>"+draftTxt+"</p>"
	_, err = e.s.SaveDraft(bg, d)
	require.NoError(t, err)

	// 3. Receive-only account (dead SMTP port).
	roAcc, err := e.s.AddAccount(bg, AccountInput{
		Email:       "ro@example.com",
		Username:    "alice",
		Password:    imapPass,
		ReceiveOnly: true,
		Receiving:   Server{Protocol: auth.IMAP, Host: ih, Port: ip, Security: SecurityNone},
		Sending:     Server{Protocol: auth.SMTP, Host: "127.0.0.1", Port: 1, Security: SecurityNone},
	})
	require.NoError(t, err)
	require.True(t, roAcc.ReceiveOnly)

	// 4. JMAP API-token account.
	st, err := e.s.GetSettings(bg)
	require.NoError(t, err)
	st.ExperimentalJMAP = true
	_, err = e.s.SetSettings(bg, st)
	require.NoError(t, err)
	jm := testmail.StartJMAP(t)
	jm.SetPassword(jmapTok)
	ju, err := url.Parse(jm.URL())
	require.NoError(t, err)
	jh, jp := splitAddr(t, ju.Host)
	_, err = e.s.AddAccount(bg, AccountInput{
		Email:     "jmap@example.com",
		Username:  "alice",
		Password:  jmapTok,
		Receiving: Server{Protocol: auth.JMAP, Host: jh, Port: jp, Security: SecurityNone, Auth: AuthAPIToken},
	})
	require.NoError(t, err)

	// Record network counters before ExportAccounts to verify zero egress / connections.
	imapLoginsBefore := e.im.Logins() + pwIM.Logins()
	smtpAuthsBefore := e.smtp.AuthAttempts() + pwSMTP.AuthAttempts()
	oauthReqsBefore := e.oa.TokenRequests()

	raw, err := e.s.ExportAccounts(bg)
	require.NoError(t, err)
	text := string(raw)

	assert.Equal(t, imapLoginsBefore, e.im.Logins()+pwIM.Logins(), "ExportAccounts must not dial IMAP")
	assert.Equal(t, smtpAuthsBefore, e.smtp.AuthAttempts()+pwSMTP.AuthAttempts(), "ExportAccounts must not dial SMTP")
	assert.Equal(t, oauthReqsBefore, e.oa.TokenRequests(), "ExportAccounts must not contact OAuth endpoints")

	for _, forbidden := range []string{
		clientSecret,
		refreshTok,
		accessTok,
		imapPass,
		smtpPass,
		jmapTok,
		mailSubj,
		mailBody,
		draftTxt,
	} {
		assert.NotContains(t, text, forbidden, "exported JSON must never contain %q", forbidden)
	}

	var got AccountExport
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Len(t, got.Accounts, 4)
	assert.Equal(t, testoauth.ClientID, got.Accounts[0].OAuthClientID, "public/own OAuth client ID is kept for re-auth")
	assert.Equal(t, AuthOAuth, got.Accounts[0].Receiving.Auth)
	assert.True(t, got.Accounts[2].ReceiveOnly)
	assert.Equal(t, AuthAPIToken, got.Accounts[3].Receiving.Auth)
}
