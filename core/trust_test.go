package core

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTrustImageSender(t *testing.T) {
	dir := t.TempDir()
	s := settingsService(t, dir, "")
	ctx := t.Context()
	require.NoError(t, s.TrustImageSender(ctx, `"Bob" <Bob@Example.COM>`))
	require.NoError(t, s.TrustImageSender(ctx, "bob@example.com"), "again: no duplicate")
	assert.Error(t, s.TrustImageSender(ctx, "not an address"))
	st, err := s.GetSettings(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"bob@example.com"}, st.TrustedImageSenders)

	// A settings page's full snapshot never drops or adds trusted senders.
	st.Theme, st.TrustedImageSenders = "dark", []string{"evil@x"}
	_, err = s.SetSettings(ctx, st)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	s = settingsService(t, dir, "")
	st, err = s.GetSettings(ctx)
	require.NoError(t, err)
	assert.Equal(t, "dark", st.Theme)
	assert.Equal(t, []string{"bob@example.com"}, st.TrustedImageSenders, "persists")

	require.NoError(t, s.UntrustImageSender(ctx, "BOB@example.com"))
	st, err = s.GetSettings(ctx)
	require.NoError(t, err)
	assert.Empty(t, st.TrustedImageSenders)
}

func TestTrustImageSenderCap(t *testing.T) {
	s := settingsService(t, t.TempDir(), "")
	ctx := t.Context()
	for i := range maxTrustedSenders {
		require.NoError(t, s.TrustImageSender(ctx, fmt.Sprintf("u%d@x.example", i)))
	}
	assert.Error(t, s.TrustImageSender(ctx, "one-more@x.example"))
	assert.NoError(t, s.TrustImageSender(ctx, "u0@x.example"), "one already there is fine")
}

func TestImagesAllowed(t *testing.T) {
	e := newSyncEnv(t)
	require.NoError(t, e.im.User.Create("Junk", nil))
	require.NoError(t, e.im.User.Create("Spam2", nil))
	e.put(t, "INBOX", eml("a@x", "Bob <bob@example.com>", "alice@example.com", "s", day, "b"))
	e.put(t, "INBOX", eml("b@x", "bob <carol@example.com>", "alice@example.com", "s", day, "b", "Reply-To: bob@example.com"))
	e.put(t, "INBOX", eml("d@x", `"bob@example.com" <mallory@evil.example>`, "alice@example.com", "s", day, "b"))
	e.put(t, "Junk", eml("c@x", "bob@example.com", "alice@example.com", "s", day, "b"))
	e.sync(t)
	trusted := func(mid string) bool {
		ok, err := e.s.ImagesAllowed(bg, e.idOf(t, mid))
		require.NoError(t, err)
		return ok
	}
	mode := func(m string) {
		st, err := e.s.GetSettings(bg)
		require.NoError(t, err)
		st.RemoteImages = m
		_, err = e.s.SetSettings(bg, st)
		require.NoError(t, err)
	}
	assert.True(t, trusted("a@x"), "automatic by default")
	assert.False(t, trusted("c@x"), "but never in the junk folder")
	mode("ask")
	require.NoError(t, e.s.TrustImageSender(bg, "bob@example.com"))
	assert.False(t, trusted("a@x"), "ask: even a trusted sender")
	mode("trusted")
	require.NoError(t, e.s.UntrustImageSender(bg, "bob@example.com"))
	assert.False(t, trusted("a@x"), "trusted only: not trusted yet")
	require.NoError(t, e.s.TrustImageSender(bg, "bob@example.com"))
	assert.True(t, trusted("a@x"))
	assert.False(t, trusted("b@x"), "the From address only, not the name or Reply-To")
	assert.False(t, trusted("d@x"), "a display name that holds the address is not it")
	assert.False(t, trusted("c@x"), "never in the junk folder")
	// Message.InJunk agrees, also once the role moved to another folder (the app offers no trust there).
	require.NoError(t, e.s.SetRoleFolder(bg, e.acc.ID, "junk", e.folderID(t, "Spam2")))
	assert.False(t, trusted("c@x"))
	for mid, junk := range map[string]bool{"a@x": false, "c@x": true} {
		m, err := e.s.GetMessage(bg, e.idOf(t, mid), false)
		require.NoError(t, err)
		assert.Equal(t, junk, m.InJunk, mid)
	}
	require.NoError(t, e.s.UntrustImageSender(bg, "bob@example.com"))
	assert.False(t, trusted("a@x"))
}
