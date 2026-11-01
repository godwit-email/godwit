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
		WordDecoder: &mime.WordDecoder{CharsetReader: charset.Reader}, // GBK/Big5 words in BODYSTRUCTURE

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

// imapConn works around non-conforming servers below go-imap:
//   - it inserts the space some servers (Strato / Open-Xchange) omit after an
//     empty literal: "{0}\r\nUID 42" instead of "{0}\r\n UID 42";
//   - it repairs Tencent Exmail's charset parameter (charsetFix) and unquotes
//     the HEADER.FIELDS names Tencent Exmail cannot match (headerFieldsFix).
//
// It tracks every "{N}\r\n" so literal contents (message bodies) pass through
// untouched. Only the TLS and plain paths use it: STARTTLS upgrades the raw
// conn inside imapclient, below which this would see ciphertext.
type imapConn struct {
	net.Conn
	state int   // 0 scanning, 1 in "{N", 2 after "}", 3 after "}\r"
	n     int64 // literal size being parsed
	skip  int64 // literal bytes left to pass through
	empty bool  // the previous byte ended an empty literal
	fix   charsetFix
	buf   []byte // rewritten bytes not yet returned
	extra []byte // the unread part of buf
	err   error  // read error to return once extra is drained

	wfix headerFieldsFix
	wbuf []byte
}

// step consumes one byte and reports whether a space must go before it.
func (c *imapConn) step(b byte) bool {
	if c.skip > 0 {
		c.skip--
		return false
	}
	space := c.empty && b >= 'A' && b <= 'Z'
	c.empty = false
	switch {
	case c.state == 1 && b >= '0' && b <= '9' && c.n < 1<<40:
		c.n = c.n*10 + int64(b-'0')
	case c.state == 1 && b == '}':
		c.state = 2
	case c.state == 2 && b == '\r':
		c.state = 3
	case c.state == 3 && b == '\n':
		c.state, c.skip, c.empty = 0, c.n, c.n == 0
	case b == '{':
		c.state, c.n = 1, 0
	default:
		c.state = 0
	}
	return space
}

func (c *imapConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	// A candidate charset group may be withheld whole, so read until there is
	// something to return.
	for len(c.extra) == 0 && c.err == nil {
		n, err := c.Conn.Read(p)
		if len(c.fix.hold) == 0 && int64(n) <= c.skip {
			c.skip -= int64(n)
			return n, err
		}
		out := c.buf[:0]
		for _, b := range p[:n] {
			if c.skip > 0 { // literal data; nothing is held when one starts
				c.skip--
				out = append(out, b)
				continue
			}
			if c.step(b) {
				out = c.fix.feed(out, ' ')
			}
			out = c.fix.feed(out, b)
		}
		if err != nil {
			out = c.fix.flush(out)
			c.err = err
		}
		c.buf, c.extra = out, out
	}
	n := copy(p, c.extra)
	c.extra = c.extra[n:]
	if len(c.extra) > 0 {
		return n, nil
	}
	err := c.err
	c.err = nil
	return n, err
}

// Write sends p with headerFieldsFix applied. A HEADER.FIELDS list split
// across writes is sent with the write that completes it.
func (c *imapConn) Write(p []byte) (int, error) {
	out := c.wfix.feed(c.wbuf[:0], p)
	c.wbuf = out
	if len(out) > 0 {
		if _, err := c.Conn.Write(out); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}
