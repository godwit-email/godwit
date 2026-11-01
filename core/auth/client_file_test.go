package auth_test

import (
	"testing"

	"github.com/godwit-email/godwit/core/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The client_secret_….json Google Cloud offers when a client is created.
func TestParseClientFile(t *testing.T) {
	id, secret, err := auth.ParseClientFile([]byte(`{"installed":{"client_id":"123-abc.apps.googleusercontent.com","project_id":"godwit-mail",
		"auth_uri":"https://accounts.google.com/o/oauth2/auth","token_uri":"https://oauth2.googleapis.com/token",
		"client_secret":"GOCSPX-s3cret","redirect_uris":["http://localhost"]}}`))
	require.NoError(t, err)
	assert.Equal(t, "123-abc.apps.googleusercontent.com", id)
	assert.Equal(t, "GOCSPX-s3cret", secret)

	_, _, err = auth.ParseClientFile([]byte(`{"web":{"client_id":"123-abc.apps.googleusercontent.com","client_secret":"GOCSPX-s3cret"}}`))
	assert.ErrorIs(t, err, auth.ErrWebClient, "a web client cannot take the sign-in on a loopback address")

	for _, bad := range []string{``, `not json`, `{}`, `{"installed":{}}`, `{"installed":{"client_id":"123"}}`, `[1]`} {
		_, _, err = auth.ParseClientFile([]byte(bad))
		assert.ErrorIs(t, err, auth.ErrNotClientFile, bad)
	}
}
