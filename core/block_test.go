package core

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBlockSender(t *testing.T) {
	dir := t.TempDir()
	s := settingsService(t, dir, "")
	ctx := t.Context()
	require.NoError(t, s.BlockSender(ctx, `"Spammer" <Spam@Example.COM>`))
	require.NoError(t, s.BlockSender(ctx, "spam@example.com"), "again: no duplicate")
	assert.Error(t, s.BlockSender(ctx, "not-an-address"))
	st, err := s.GetSettings(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"spam@example.com"}, st.BlockedSenders)

	// A settings page's full snapshot never drops or adds blocked senders.
	st.Theme, st.BlockedSenders = "dark", []string{"other@x"}
	_, err = s.SetSettings(ctx, st)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	s = settingsService(t, dir, "")
	st, err = s.GetSettings(ctx)
	require.NoError(t, err)
	assert.Equal(t, "dark", st.Theme)
	assert.Equal(t, []string{"spam@example.com"}, st.BlockedSenders, "persists")

	require.NoError(t, s.UnblockSender(ctx, "SPAM@example.com"))
	st, err = s.GetSettings(ctx)
	require.NoError(t, err)
	assert.Empty(t, st.BlockedSenders)
}

func TestBlockSenderCap(t *testing.T) {
	s := settingsService(t, t.TempDir(), "")
	ctx := t.Context()
	for i := range maxBlockedSenders {
		require.NoError(t, s.BlockSender(ctx, fmt.Sprintf("u%d@x.example", i)))
	}
	assert.Error(t, s.BlockSender(ctx, "one-more@x.example"))
	assert.NoError(t, s.BlockSender(ctx, "u0@x.example"), "one already there is fine")
}
