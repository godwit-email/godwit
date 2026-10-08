package conn_test

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/godwitmail/godwit/core/conn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fake starts a raw TCP server; handle runs per connection.
func fake(t *testing.T, handle func(net.Conn)) conn.Endpoint {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); handle(c) }()
		}
	}()
	a := ln.Addr().(*net.TCPAddr)
	return conn.Endpoint{Host: "127.0.0.1", Port: a.Port, Security: conn.None, Username: "alice", Password: "hunter2"}
}

func stall(c net.Conn) {
	for {
		if _, err := c.Read(make([]byte, 512)); err != nil {
			return
		}
	}
} // never answers; returns when the client closes

func returnsPromptly(t *testing.T, dial func(ctx context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	done := make(chan error, 1)
	go func() { done <- dial(ctx) }()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(3 * time.Second):
		t.Fatal("dial did not return after ctx cancellation")
	}
}

func TestDialCancelWhileStalled(t *testing.T) {
	silent := fake(t, stall)
	returnsPromptly(t, func(ctx context.Context) error { _, err := conn.DialIMAP(ctx, silent); return err })
	returnsPromptly(t, func(ctx context.Context) error { _, err := conn.DialSMTP(ctx, silent); return err })

	// greeting arrives, but LOGIN / AUTH are never answered
	imapHalf := fake(t, func(c net.Conn) { c.Write([]byte("* OK hi\r\n")); stall(c) })
	returnsPromptly(t, func(ctx context.Context) error { _, err := conn.DialIMAP(ctx, imapHalf); return err })
	smtpHalf := fake(t, func(c net.Conn) {
		c.Write([]byte("220 hi\r\n"))
		r := bufio.NewReader(c)
		r.ReadString('\n') // EHLO
		c.Write([]byte("250-hi\r\n250 AUTH PLAIN\r\n"))
		stall(c)
	})
	returnsPromptly(t, func(ctx context.Context) error { _, err := conn.DialSMTP(ctx, smtpHalf); return err })
}

func TestAuthErrorsDoNotLeakServerText(t *testing.T) {
	imapEcho := fake(t, func(c net.Conn) {
		c.Write([]byte("* OK hi\r\n"))
		line, _ := bufio.NewReader(c).ReadString('\n')
		tag := strings.Fields(line)[0]
		c.Write([]byte(tag + " NO login failed for password hunter2\r\n"))
	})
	_, err := conn.DialIMAP(context.Background(), imapEcho)
	require.ErrorIs(t, err, conn.ErrAuthFailed)
	assert.NotContains(t, err.Error(), "hunter2")

	smtpEcho := fake(t, func(c net.Conn) {
		c.Write([]byte("220 hi\r\n"))
		r := bufio.NewReader(c)
		r.ReadString('\n')
		c.Write([]byte("250-hi\r\n250 AUTH PLAIN\r\n"))
		r.ReadString('\n')
		c.Write([]byte("535 5.7.8 password hunter2 rejected\r\n"))
	})
	_, err = conn.DialSMTP(context.Background(), smtpEcho)
	require.ErrorIs(t, err, conn.ErrAuthFailed)
	assert.NotContains(t, err.Error(), "hunter2")
}

func TestMalformedAuthResponsesDoNotLeakPassword(t *testing.T) {
	imapBad := fake(t, func(c net.Conn) {
		c.Write([]byte("* OK hi\r\n"))
		bufio.NewReader(c).ReadString('\n')
		c.Write([]byte("hunter2 NO rejected\r\n")) // unknown tag line echoing the password
	})
	_, err := conn.DialIMAP(context.Background(), imapBad)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "hunter2")

	smtpBad := fake(t, func(c net.Conn) {
		c.Write([]byte("220 hi\r\n"))
		r := bufio.NewReader(c)
		r.ReadString('\n')
		c.Write([]byte("250-hi\r\n250 AUTH PLAIN\r\n"))
		r.ReadString('\n')
		c.Write([]byte("hunter2 is not a reply code\r\n"))
	})
	_, err = conn.DialSMTP(context.Background(), smtpBad)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "hunter2")
}

func TestDialCancelDuringStalledTLSHandshake(t *testing.T) {
	silent := fake(t, stall) // accepts, never answers the ClientHello
	silent.Security = conn.TLS
	returnsPromptly(t, func(ctx context.Context) error { _, err := conn.DialIMAP(ctx, silent); return err })
	returnsPromptly(t, func(ctx context.Context) error { _, err := conn.DialSMTP(ctx, silent); return err })
}

func TestTLSHandshakeFailureIsNotReportedAsCancel(t *testing.T) {
	plain := fake(t, func(c net.Conn) {
		c.Write([]byte("this is not a TLS server\r\n"))
		stall(c)
	})
	plain.Security = conn.TLS
	for name, dial := range map[string]func() error{
		"imap": func() error { _, err := conn.DialIMAP(context.Background(), plain); return err },
		"smtp": func() error { _, err := conn.DialSMTP(context.Background(), plain); return err },
	} {
		err := dial()
		require.Error(t, err, name)
		assert.NotErrorIs(t, err, context.Canceled, name)
		assert.NotErrorIs(t, err, context.DeadlineExceeded, name)
	}
}
