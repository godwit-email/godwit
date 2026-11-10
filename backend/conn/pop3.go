package conn

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/knadh/go-pop3"
)

// DialPOP3 connects to e and signs in with USER/PASS. close cuts the network
// connection at once, so a ctx that ends can unblock a read: go-pop3 takes
// no context. The library has no STLS, so it is done here before the
// connection is handed over.
func DialPOP3(ctx context.Context, e Endpoint) (c *pop3.Conn, close func(), err error) {
	c, close, err = dialPOP3(ctx, e)
	if plaintextOnTLS(e, err) {
		e.Security = StartTLS
		c, close, err = dialPOP3(ctx, e)
	}
	return c, close, err
}

func dialPOP3(ctx context.Context, e Endpoint) (*pop3.Conn, func(), error) {
	nc, cctx, release, err := e.open(ctx, e.Security == TLS)
	if err != nil {
		return nil, nil, fmt.Errorf("pop3 connect %s: %w", e.addr(), err)
	}
	fail := func(err error) (*pop3.Conn, func(), error) {
		nc.Close()
		release()
		return nil, nil, err
	}
	switch e.Security {
	case TLS, None:
	case StartTLS:
		if nc, err = stls(cctx, nc, e); err != nil {
			return fail(fmt.Errorf("pop3 connect %s: %w", e.addr(), connectErr(cctx, err)))
		}
	default:
		return fail(fmt.Errorf("conn: unknown security %q", e.Security))
	}
	c, err := pop3.New(pop3.Opt{Host: e.Host, Port: e.Port, Dialer: given{nc}}).NewConn()
	if err != nil {
		return fail(fmt.Errorf("pop3 connect %s: %w", e.addr(), connectErr(cctx, err)))
	}
	if err = c.User(e.Username); err == nil {
		err = c.Pass(e.Password)
	}
	if err != nil {
		return fail(loginErr(cctx, "pop3", e, err))
	}
	release()
	return c, func() { nc.Close() }, nil
}

// stls reads the greeting, upgrades nc with STLS and returns the TLS
// connection, which replays a greeting for go-pop3 to read.
func stls(ctx context.Context, nc net.Conn, e Endpoint) (net.Conn, error) {
	r := bufio.NewReader(nc)
	if _, err := okLine(r); err != nil {
		return nil, err
	}
	if _, err := io.WriteString(nc, "STLS\r\n"); err != nil {
		return nil, err
	}
	if _, err := okLine(r); err != nil {
		return nil, err
	}
	if r.Buffered() > 0 { // anything sent before the handshake could be injected
		return nil, errors.New("pop3: data after STLS")
	}
	tc := tls.Client(nc, &tls.Config{ServerName: e.Host, RootCAs: TLSRoots})
	if err := tc.HandshakeContext(ctx); err != nil {
		return nil, err
	}
	return greeted{tc, io.MultiReader(strings.NewReader("+OK\r\n"), tc)}, nil
}

func okLine(r *bufio.Reader) (string, error) {
	l, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(l, "+OK") {
		return "", errors.New("pop3: the server refused STLS")
	}
	return l, nil
}

// greeted is a connection whose reads start with r.
type greeted struct {
	net.Conn
	r io.Reader
}

func (g greeted) Read(p []byte) (int, error) { return g.r.Read(p) }

// given is a pop3.Dialer that hands over a connection already made.
type given struct{ net.Conn }

func (g given) Dial(string, string) (net.Conn, error) { return g.Conn, nil }
