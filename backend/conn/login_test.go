package conn

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/godwit-email/godwit/backend"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// A server that accepts and then says nothing, or greets and never answers
// LOGIN, costs the dial its deadline. The error names the step and the
// server, so a timeout is never the bare context error (#755).
func TestSilentServerNamesStepAndHost(t *testing.T) {
	for step, greets := range map[string]bool{"connect": false, "login": true} {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		t.Cleanup(func() { ln.Close() })
		done := make(chan struct{})
		t.Cleanup(func() { close(done) })
		go func() {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
			if greets {
				c.Write([]byte("* OK ready\r\n"))
			}
			<-done // silent until the client gives up
		}()
		port := ln.Addr().(*net.TCPAddr).Port
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		_, err = DialIMAP(ctx, Endpoint{Host: "127.0.0.1", Port: port, Security: None, Username: "alice", Password: "hunter2"})
		cancel()
		require.Error(t, err, step)
		// the read's own timeout may win the race with the context's: both are a timeout
		assert.Contains(t, err.Error(), fmt.Sprintf("imap %s 127.0.0.1:%d: ", step, port))
		assert.Equal(t, FailureTimeout, Classify(err), "%s: %v", step, err)
	}
}
