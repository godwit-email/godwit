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

	"github.com/emersion/go-imap/v2"
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

// ClientVersion is sent in the IMAP ID command; release builds set it.
var ClientVersion = "dev"

// Endpoint describes one server and the credentials to use on it. A non-empty
// AccessToken selects XOAUTH2 and Password is ignored.
type Endpoint struct {
	Host               string
	Port               int
	Security           Security
	Username, Password string
	AccessToken        string
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

// connectErr is ctxErr for a failure before login. The reader may already have
// met the reply to LOGIN, so a protocol error here can echo the password; like
// loginErr it keeps only errors that carry no server text.
func connectErr(ctx context.Context, err error) error {
	err = ctxErr(ctx, err)
	var ne net.Error
	if errors.As(err, &ne) || isTLS(err) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
		return err
	}
	return errors.New("unexpected server response")
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
	c, _, err := DialIMAPAbortable(ctx, e, nil)
	return c, err
}

// DialIMAPAbortable is DialIMAP that also returns abort, which closes the network
// connection at once. Unlike Client.Close it does not wait for a response literal
// being read, so it can cut off a download. h, if not nil, receives unsolicited
// server updates (IDLE).
func DialIMAPAbortable(ctx context.Context, e Endpoint, h *imapclient.UnilateralDataHandler) (*imapclient.Client, func(), error) {
	nc, cctx, release, err := e.open(ctx, e.Security == TLS)
	if err != nil {
		return nil, nil, fmt.Errorf("imap connect %s: %w", e.addr(), err)
	}
	opts := &imapclient.Options{
		TLSConfig:   &tls.Config{ServerName: e.Host},
		WordDecoder: &mime.WordDecoder{CharsetReader: charset.Reader}, // GBK/Big5 subjects in ENVELOPE

		UnilateralDataHandler: h,
	}
	var c *imapclient.Client
	switch e.Security {
	case TLS, None:
		c = imapclient.New(&imapConn{Conn: nc}, opts)
		err = c.WaitGreeting()
	case StartTLS:
		c, err = imapclient.NewStartTLS(nc, opts)
	default:
		err = fmt.Errorf("conn: unknown security %q", e.Security)
	}
	if err == nil {
		if err = imapLogin(c, e); err != nil {
			err = loginErr(cctx, "imap", err)
		} else if c.Caps().Has(imap.CapID) {
			// Some servers (NetEase) refuse SELECT until the client identifies
			// itself. A failed ID is not a failed login.
			c.ID(&imap.IDData{Name: "Godwit", Version: ClientVersion}).Wait()
			if cctx.Err() != nil {
				err = fmt.Errorf("imap id: %w", cctx.Err())
			}
		}
	} else {
		err = fmt.Errorf("imap connect %s: %w", e.addr(), connectErr(cctx, err))
	}
	if err != nil {
		nc.Close()
		release()
		return nil, nil, err
	}
	release()
	return c, func() { nc.Close() }, nil
}

func imapLogin(c *imapclient.Client, e Endpoint) error {
	if e.AccessToken == "" {
		return c.Login(e.Username, e.Password).Wait()
	}
	caps, err := c.Capability().Wait()
	if err != nil {
		return err
	}
	if !caps.Has("AUTH=XOAUTH2") {
		return ErrAuthFailed
	}
	return c.Authenticate(NewXOAuth2Client(e.Username, e.AccessToken))
}

func smtpAuth(c *smtp.Client, e Endpoint) error {
	if e.AccessToken == "" {
		return c.Auth(sasl.NewPlainClient("", e.Username, e.Password))
	}
	if !c.SupportsAuth("XOAUTH2") {
		// SupportsAuth is also false when EHLO failed; Noop returns that error.
		if err := c.Noop(); err != nil {
			return err
		}
		return ErrAuthFailed
	}
	return c.Auth(NewXOAuth2Client(e.Username, e.AccessToken))
}

// DialSMTP connects to e and authenticates with PLAIN, or XOAUTH2 when e has an
// AccessToken.
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
		if err = smtpAuth(c, e); err != nil {
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

const emptyLiteral = "{0}\r\n"

// imapConn inserts a missing space after an empty literal ("{0}\r\n") when a
// server (such as Strato) immediately follows it with the next FETCH attribute
// name (e.g. "{0}\r\nUID" instead of "{0}\r\n UID").
type imapConn struct {
	net.Conn
	matched int    // 0..5: bytes of emptyLiteral matched at the stream tail
	extra   []byte // leftover bytes when inserting ' ' overflowed p
	err     error
}

func (c *imapConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(c.extra) > 0 {
		n := copy(p, c.extra)
		c.extra = c.extra[n:]
		var err error
		if len(c.extra) == 0 {
			err, c.err = c.err, nil
		}
		return n, err
	}
	n, err := c.Conn.Read(p)
	if n == 0 {
		return 0, err
	}
	var out []byte
	for i, b := range p[:n] {
		if c.matched == len(emptyLiteral) && b >= 'A' && b <= 'Z' {
			if out == nil {
				out = make([]byte, 0, n+4)
				out = append(out, p[:i]...)
			}
			out = append(out, ' ')
		}
		if out != nil {
			out = append(out, b)
		}
		if c.matched < len(emptyLiteral) && b == emptyLiteral[c.matched] {
			c.matched++
		} else if b == emptyLiteral[0] {
			c.matched = 1
		} else {
			c.matched = 0
		}
	}
	if out == nil {
		return n, err
	}
	copied := copy(p, out)
	if copied < len(out) {
		c.extra = append(c.extra[:0], out[copied:]...)
		c.err = err
		return copied, nil
	}
	return copied, err
}

