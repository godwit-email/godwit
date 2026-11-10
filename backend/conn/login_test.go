package conn

import (
	"bytes"
	"context"
	"errors"
	"log"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/godwit-email/godwit/backend"
	"github.com/stretchr/testify/assert"
)

// A refused login logs the server's reply without the credentials; the error
// returned carries none of it.
func TestLoginErrLogsRefusal(t *testing.T) {
	var buf bytes.Buffer
	old, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() { log.SetOutput(old); log.SetFlags(flags) }()

	e := Endpoint{Host: "imap.example.com", Username: "alice@example.com", AccessToken: "tok-123456"}
	refused := &imap.Error{Type: imap.StatusResponseTypeNo, Text: "AUTHENTICATE failed for alice@example.com with tok-123456"}
	err := loginErr(context.Background(), "imap", e, refused)

	assert.ErrorIs(t, err, backend.ErrAuthFailed)
	assert.NotContains(t, err.Error(), "AUTHENTICATE")
	assert.Equal(t, "imap: imap.example.com refused the login: imap: NO AUTHENTICATE failed for *** with ***\n", buf.String())

	buf.Reset()
	loginErr(context.Background(), "imap", e, errors.New("unexpected data that may echo tok-123456"))
	assert.Empty(t, buf.String())
}
