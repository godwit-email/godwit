package core_test

import (
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/auth"
	"github.com/stretchr/testify/require"
)

func TestOutlookNotConfigured(t *testing.T) {
	old := auth.MicrosoftClientID
	auth.MicrosoftClientID = "" // builds without a client ID cannot sign in to Outlook
	defer func() { auth.MicrosoftClientID = old }()
	s := newTestService(t)
	_, _, err := s.BeginOAuth(ctx, "outlook", "alice@outlook.com", core.OAuthClient{})
	require.ErrorIs(t, err, auth.ErrOAuthNotConfigured)
}

func TestBeginOAuthNeedsOAuthProvider(t *testing.T) {
	s := newTestService(t)
	for _, id := range []string{"gmail", "nope"} {
		_, _, err := s.BeginOAuth(ctx, id, "", core.OAuthClient{})
		require.Error(t, err, id)
	}
}
