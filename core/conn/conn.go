// Package conn dials and authenticates IMAP and SMTP connections.
package conn

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"strconv"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/charset"
	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
)

// Security selects the transport protection of a connection.
type Security string

const (
	TLS      Security = "tls"
	StartTLS Security = "starttls"
	None     Security = "none"
)

const timeout = 30 * time.Second

// ErrAuthFailed means the server rejected the credentials. Server text is never
// included, because servers may echo the submitted password.
var ErrAuthFailed = errors.New("authentication failed")

// Endpoint describes one server and the credentials to use on it.
type Endpoint struct {
	Host               string
	Port               int
	Security           Security
	Username, Password string
}

func (e Endpoint) addr() string { return net.JoinHostPort(e.Host, strconv.Itoa(e.Port)) }

// open dials e and returns the raw connection plus a bound context. Everything
// until the returned release func runs (dial, TLS, greeting, auth) is limited to
// 30 seconds or ctx, whichever ends first; cancellation closes the connection.
func (e Endpoint) open(ctx context.Context, implicitTLS bool) (net.Conn, context.Context, func(), error) {
	ctx2, cancel := context.WithTimeout(ctx, timeout)
	raw, err := (&net.Dialer{}).DialContext(ctx2, "tcp", e.addr())
	if err != nil {
		cancel()
		return nil, nil, nil, err
	}
	dl, _ := ctx2.Deadline()
	raw.SetDeadline(dl)
	// The callback closes only the immutable raw conn; closing it also aborts a TLS
	// handshake or any later I/O on a wrapper around it.
	stop := context.AfterFunc(ctx2, func() { raw.Close() })
	var nc net.Conn = raw
	if implicitTLS {
		tc := tls.Client(raw, &tls.Config{ServerName: e.Host})
		if err := tc.HandshakeContext(ctx2); err != nil {
			err = ctxErr(ctx2, err) // before cancel(), which would make every failure look cancelled
			stop()
			raw.Close()
			cancel()
			return nil, nil, nil, err
		}
		nc = tc
	}
	return nc, ctx2, func() {
		stop()
		raw.SetDeadline(time.Time{})
		cancel()
	}, nil
}

// loginErr maps a failure of LOGIN/AUTH to an error without server text: context
// and network errors are kept, everything else (status replies, malformed or
// unexpected protocol data, which may echo the password) becomes ErrAuthFailed.
func loginErr(ctx context.Context, proto string, err error) error {
	var ne net.Error
	switch {
	case ctx.Err() != nil:
		return fmt.Errorf("%s login: %w", proto, ctx.Err())
	case errors.As(err, &ne), errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, net.ErrClosed):
		return fmt.Errorf("%s login: %w", proto, err)
	}
	return fmt.Errorf("%s: %w", proto, ErrAuthFailed)
}

// ctxErr prefers the context's error over the "use of closed connection" it caused.
func ctxErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// DialIMAP connects to e and logs in.
func DialIMAP(ctx context.Context, e Endpoint) (*imapclient.Client, error) {
	nc, cctx, release, err := e.open(ctx, e.Security == TLS)
	if err != nil {
		return nil, fmt.Errorf("imap connect %s: %w", e.addr(), err)
	}
	opts := &imapclient.Options{
		TLSConfig:   &tls.Config{ServerName: e.Host},
		WordDecoder: &mime.WordDecoder{CharsetReader: charset.Reader}, // GBK/Big5 subjects in ENVELOPE
	}
	var c *imapclient.Client
	switch e.Security {
	case TLS, None:
		c = imapclient.New(nc, opts)
		err = c.WaitGreeting()
	case StartTLS:
		c, err = imapclient.NewStartTLS(nc, opts)
	default:
		err = fmt.Errorf("conn: unknown security %q", e.Security)
	}
	if err == nil {
		err = c.Login(e.Username, e.Password).Wait()
		if err != nil {
			err = loginErr(cctx, "imap", err)
		}
	} else {
		err = fmt.Errorf("imap connect %s: %w", e.addr(), ctxErr(cctx, err))
	}
	if err != nil {
		nc.Close()
		release()
		return nil, err
	}
	release()
	return c, nil
}

// DialSMTP connects to e and authenticates with PLAIN.
func DialSMTP(ctx context.Context, e Endpoint) (*smtp.Client, error) {
	nc, cctx, release, err := e.open(ctx, e.Security == TLS)
	if err != nil {
		return nil, fmt.Errorf("smtp connect %s: %w", e.addr(), err)
	}
	var c *smtp.Client
	switch e.Security {
	case TLS, None:
		c = smtp.NewClient(nc)
	case StartTLS:
		c, err = smtp.NewClientStartTLS(nc, &tls.Config{ServerName: e.Host})
	default:
		err = fmt.Errorf("conn: unknown security %q", e.Security)
	}
	if err == nil {
		if err = c.Auth(sasl.NewPlainClient("", e.Username, e.Password)); err != nil {
			err = loginErr(cctx, "smtp", err)
		}
	} else {
		err = fmt.Errorf("smtp connect %s: %w", e.addr(), ctxErr(cctx, err))
	}
	if err != nil {
		nc.Close()
		release()
		return nil, err
	}
	release()
	return c, nil
}
