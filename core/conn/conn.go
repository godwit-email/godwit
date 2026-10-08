// Package conn dials and authenticates IMAP and SMTP connections.
package conn

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"
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

// Endpoint describes one server and the credentials to use on it.
type Endpoint struct {
	Host               string
	Port               int
	Security           Security
	Username, Password string
}

func (e Endpoint) addr() string { return net.JoinHostPort(e.Host, strconv.Itoa(e.Port)) }

func (e Endpoint) dialer(ctx context.Context) (*net.Dialer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d := &net.Dialer{Timeout: timeout}
	// ponytail: ctx only bounds the dial, not the login exchange; the 30s read/write deadlines do.
	if dl, ok := ctx.Deadline(); ok {
		d.Deadline = dl
	}
	return d, nil
}

// DialIMAP connects to e and logs in.
func DialIMAP(ctx context.Context, e Endpoint) (*imapclient.Client, error) {
	d, err := e.dialer(ctx)
	if err != nil {
		return nil, err
	}
	opts := &imapclient.Options{Dialer: d, TLSConfig: &tls.Config{ServerName: e.Host}}
	var c *imapclient.Client
	switch e.Security {
	case TLS:
		c, err = imapclient.DialTLS(e.addr(), opts)
	case StartTLS:
		c, err = imapclient.DialStartTLS(e.addr(), opts)
	case None:
		c, err = imapclient.DialInsecure(e.addr(), opts)
	default:
		return nil, fmt.Errorf("conn: unknown security %q", e.Security)
	}
	if err != nil {
		return nil, fmt.Errorf("imap connect %s: %w", e.addr(), err)
	}
	if err := c.Login(e.Username, e.Password).Wait(); err != nil {
		c.Close()
		return nil, fmt.Errorf("imap login: %w", err)
	}
	return c, nil
}

// DialSMTP connects to e and authenticates with PLAIN.
func DialSMTP(ctx context.Context, e Endpoint) (*smtp.Client, error) {
	d, err := e.dialer(ctx)
	if err != nil {
		return nil, err
	}
	cfg := &tls.Config{ServerName: e.Host}
	var c *smtp.Client
	switch e.Security {
	case TLS:
		var nc net.Conn
		if nc, err = (&tls.Dialer{NetDialer: d, Config: cfg}).DialContext(ctx, "tcp", e.addr()); err == nil {
			c = smtp.NewClient(nc)
		}
	case StartTLS, None:
		var nc net.Conn
		if nc, err = d.DialContext(ctx, "tcp", e.addr()); err == nil {
			if e.Security == StartTLS {
				c, err = smtp.NewClientStartTLS(nc, cfg)
			} else {
				c = smtp.NewClient(nc)
			}
		}
	default:
		return nil, fmt.Errorf("conn: unknown security %q", e.Security)
	}
	if err != nil {
		return nil, fmt.Errorf("smtp connect %s: %w", e.addr(), err)
	}
	if err := c.Auth(sasl.NewPlainClient("", e.Username, e.Password)); err != nil {
		c.Close()
		return nil, fmt.Errorf("smtp auth: %w", err)
	}
	return c, nil
}
