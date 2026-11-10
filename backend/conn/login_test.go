package conn

import (
	"context"
	"errors"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/godwit-email/godwit/backend"
	"github.com/stretchr/testify/assert"
)

// A refused login keeps the server's reply, credentials cut out, for
// RefusalReply only: the error's message carries none of it.
func TestLoginErrKeepsRefusal(t *testing.T) {
	e := Endpoint{Host: "imap.example.com", Username: "alice@example.com", AccessToken: "tok-123456"}
	refused := &imap.Error{Type: imap.StatusResponseTypeNo, Text: "AUTHENTICATE failed for alice@example.com with tok-123456"}
	err := loginErr(context.Background(), "imap", e, refused)

	assert.ErrorIs(t, err, backend.ErrAuthFailed)
	assert.Equal(t, "imap: authentication failed", err.Error())
	assert.Equal(t, "imap: NO AUTHENTICATE failed for *** with ***", RefusalReply(err))

	err = loginErr(context.Background(), "imap", e, errors.New("unexpected data that may echo tok-123456"))
	assert.ErrorIs(t, err, backend.ErrAuthFailed)
	assert.Empty(t, RefusalReply(err))
}
