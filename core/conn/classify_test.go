package conn_test

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/conn"
	"github.com/godwit-email/godwit/core/internal/testmail"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassify(t *testing.T) {
	dial := func(ctx context.Context, e conn.Endpoint) error {
		c, err := conn.DialIMAP(ctx, e)
		if err == nil {
			c.Close()
		}
		require.Error(t, err)
		return err
	}
	ctx := context.Background()

	// dns: constructed, so no resolver is touched
	dnsErr := &net.DNSError{Err: "no such host", Name: "x.invalid", IsNotFound: true}
	assert.Equal(t, conn.FailureDNS, conn.Classify(fmt.Errorf("imap connect: %w", dnsErr)))

	// connect: a closed port on 127.0.0.1
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	ln.Close()
	assert.Equal(t, conn.FailureConnect, conn.Classify(dial(ctx, endpoint(t, addr))))

	// timeout: a listener that never answers
	tctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	assert.Equal(t, conn.FailureTimeout, conn.Classify(dial(tctx, fake(t, stall))))

	// tls: self-signed certificate
	srv := httptest.NewTLSServer(nil)
	defer srv.Close()
	e := endpoint(t, srv.Listener.Addr().String())
	e.Security = conn.TLS
	assert.Equal(t, conn.FailureTLS, conn.Classify(dial(ctx, e)))

	// tls: the server rejects the handshake with an alert (protocol_version)
	old := httptest.NewUnstartedServer(nil)
	old.TLS = &tls.Config{MaxVersion: tls.VersionTLS10}
	old.StartTLS()
	defer old.Close()
	e = endpoint(t, old.Listener.Addr().String())
	e.Security = conn.TLS
	err = dial(ctx, e)
	assert.Contains(t, err.Error(), "remote error: tls:")
	assert.Equal(t, conn.FailureTLS, conn.Classify(err))

	// auth
	im := testmail.StartIMAP(t)
	e = endpoint(t, im.Addr)
	e.Password = "wrong"
	assert.Equal(t, conn.FailureAuth, conn.Classify(dial(ctx, e)))

	// protocol: IMAP spoken to an SMTP port
	smtpAddr, _ := testmail.StartSMTP(t)
	assert.Equal(t, conn.FailureProtocol, conn.Classify(dial(ctx, endpoint(t, smtpAddr))))
}

func TestClassifyErrnos(t *testing.T) {
	op := func(errno syscall.Errno) error {
		return &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", errno)}
	}
	for _, errno := range []syscall.Errno{
		syscall.ECONNREFUSED, syscall.EHOSTUNREACH, syscall.ENETUNREACH,
		10061, 10065, 10051, // WSAECONNREFUSED, WSAEHOSTUNREACH, WSAENETUNREACH
	} {
		assert.Equal(t, conn.FailureConnect, conn.Classify(op(errno)), errno.Error())
	}
	assert.Equal(t, conn.FailureTimeout, conn.Classify(context.DeadlineExceeded))
	assert.Equal(t, conn.FailureProtocol, conn.Classify(errors.New("junk")))
	assert.Equal(t, conn.FailureKind(""), conn.Classify(nil))
}
