package core

import (
	"strconv"
	"strings"
	"testing"

	"github.com/godwit-email/godwit/core/mail"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSignaturesShared(t *testing.T) {
	e := newSendEnv(t, false)
	bobIn := internalInput(t, e.im.Addr, e.acc.SMTPHost+":"+strconv.Itoa(e.acc.SMTPPort), "bob@example.com", false)
	bob, err := e.s.AddAccount(bg, bobIn)
	require.NoError(t, err)

	_, err = e.s.CreateSignature(bg, "  ")
	assert.ErrorIs(t, err, ErrSignatureName)
	_, err = e.s.CreateSignature(bg, strings.Repeat("x", MaxSignatureName+1))
	assert.ErrorIs(t, err, ErrSignatureName)
	work, err := e.s.CreateSignature(bg, " Work ")
	require.NoError(t, err)
	assert.Equal(t, Signature{ID: work.ID, Name: "Work", Accounts: []int64{}}, work)

	html := SignatureHTMLPrefix + `<p>Alice</p><img src="` + dataURL("image/png", logo(t, 1)) + `"><script>x()</script>`
	work, err = e.s.UpdateSignature(bg, work.ID, SignaturePatch{HTML: &html})
	require.NoError(t, err)
	assert.NotContains(t, work.HTML, "script", "sanitized as saved")
	assert.NotContains(t, work.HTML, "data:")

	// Both accounts use it: new mail of each ends with it.
	for _, id := range []int64{e.acc.ID, bob.ID} {
		_, err = e.s.UpdateAccount(bg, id, AccountPatch{SignatureID: &work.ID})
		require.NoError(t, err)
	}
	sigs, err := e.s.ListSignatures(bg)
	require.NoError(t, err)
	require.Len(t, sigs, 2, "Alice's own, and Work")
	assert.Empty(t, sigs[0].Accounts, "Alice's own is used by no one now")
	assert.Equal(t, []int64{e.acc.ID, bob.ID}, sigs[1].Accounts)
	for _, mode := range []string{"new"} {
		d, err := e.s.NewDraft(bg, bob.ID, mode, 0)
		require.NoError(t, err)
		assert.Contains(t, d.HTML, "Alice")
		assert.Len(t, mail.CIDs(d.HTML), 1)
	}

	// Writing an account's signature (the CLI, the Local API) rewrites its default one, for both.
	_, err = e.s.UpdateAccount(bg, bob.ID, AccountPatch{Signature: new("Bob plain")})
	require.NoError(t, err)
	accs, err := e.s.ListAccounts(bg)
	require.NoError(t, err)
	assert.Equal(t, "Bob plain", accs[0].Signature)
	assert.Equal(t, "Bob plain", accs[1].Signature)
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM signature_images"), "Bob's draft still shows the image")

	// Picking another replaces it; a missing one is refused.
	other, err := e.s.CreateSignature(bg, "Other")
	require.NoError(t, err)
	acc, err := e.s.UpdateAccount(bg, bob.ID, AccountPatch{SignatureID: &other.ID})
	require.NoError(t, err)
	assert.Equal(t, other.ID, acc.SignatureID)
	assert.Equal(t, "", acc.Signature)
	_, err = e.s.UpdateAccount(bg, bob.ID, AccountPatch{SignatureID: new(int64(999))})
	assert.ErrorIs(t, err, ErrSignatureNotFound)

	// Deleting one leaves its accounts without a signature.
	require.NoError(t, e.s.DeleteSignature(bg, work.ID))
	accs, err = e.s.ListAccounts(bg)
	require.NoError(t, err)
	assert.Zero(t, accs[0].SignatureID)
	assert.Equal(t, other.ID, accs[1].SignatureID)
	d, err := e.s.NewDraft(bg, e.acc.ID, "new", 0)
	require.NoError(t, err)
	assert.NotContains(t, d.HTML, "signature")
	_, err = e.s.UpdateSignature(bg, work.ID, SignaturePatch{Name: new("x")})
	assert.ErrorIs(t, err, ErrSignatureNotFound)
}

// An account without a signature that is given one (the CLI, AddAccount)
// gets a new one named as the account.
func TestAccountSignatureMadeForIt(t *testing.T) {
	e := newSendEnv(t, false)
	sigs, err := e.s.ListSignatures(bg)
	require.NoError(t, err)
	require.Len(t, sigs, 1, "the one AddAccount made")
	assert.Equal(t, "Alice", sigs[0].Name)
	require.NoError(t, e.s.DeleteSignature(bg, sigs[0].ID))
	_, err = e.s.UpdateAccount(bg, e.acc.ID, AccountPatch{Signature: new("")})
	require.NoError(t, err)
	sigs, err = e.s.ListSignatures(bg)
	require.NoError(t, err)
	assert.Empty(t, sigs, "clearing none makes none")

	alias := "Personal"
	_, err = e.s.UpdateAccount(bg, e.acc.ID, AccountPatch{Alias: &alias, Signature: new(SignatureHTMLPrefix + `<p>A</p><img src="` + dataURL("image/png", logo(t, 1)) + `">`)})
	require.NoError(t, err)
	sigs, err = e.s.ListSignatures(bg)
	require.NoError(t, err)
	require.Len(t, sigs, 1)
	assert.Equal(t, "Personal", sigs[0].Name)
	assert.Equal(t, []int64{e.acc.ID}, sigs[0].Accounts)
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM signature_images WHERE signature_id = ?", sigs[0].ID))

	// A deleted id is never handed out again, so no stale image is adopted.
	require.NoError(t, e.s.DeleteSignature(bg, sigs[0].ID))
	_, err = e.s.UpdateAccount(bg, e.acc.ID, AccountPatch{Signature: new("again")})
	require.NoError(t, err)
	sigs, err = e.s.ListSignatures(bg)
	require.NoError(t, err)
	require.Len(t, sigs, 1)
	assert.Greater(t, sigs[0].ID, int64(2))
}

func TestSignaturePreview(t *testing.T) {
	assert.Equal(t, []string{"Bob", "CEO"}, SignaturePreview(SignatureHTMLPrefix+"<p><b>Bob</b></p><p></p><p>CEO</p><p>Acme</p>", 2))
	assert.Equal(t, []string{"Bob"}, SignaturePreview("Bob\n\n", 2))
	assert.Empty(t, SignaturePreview("", 2))
}
