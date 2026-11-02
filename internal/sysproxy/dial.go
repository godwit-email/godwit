package sysproxy

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	xproxy "golang.org/x/net/proxy"
)

// Errors from Dial wrap one of these when the proxy step itself failed.
var (
	ErrUnreachable = errors.New("proxy unreachable")
	ErrRefused     = errors.New("proxy refused the tunnel")
	ErrAuth        = errors.New("proxy authentication failed")
)

// Dial opens a tunnel to target (host:port) through proxy: an http:// one
// by CONNECT, a socks5:// one by SOCKS5, with the proxy's credentials when
// it has some. ctx bounds the whole of it.
func Dial(ctx context.Context, proxy *url.URL, target string) (net.Conn, error) {
	d := proxyDialer{&net.Dialer{}}
	addr := proxy.Host
	if proxy.Port() == "" { // the scheme's default port
		addr = net.JoinHostPort(proxy.Hostname(), map[string]string{"http": "80", "socks5": "1080"}[proxy.Scheme])
	}
	switch proxy.Scheme {
	case "socks5":
		var auth *xproxy.Auth
		if u := proxy.User; u != nil {
			pass, _ := u.Password()
			auth = &xproxy.Auth{User: u.Username(), Password: pass}
		}
		s, err := xproxy.SOCKS5("tcp", addr, auth, d)
		if err != nil {
			return nil, err
		}
		c, err := s.(xproxy.ContextDialer).DialContext(ctx, "tcp", target)
		// x/net's SOCKS errors are plain strings; a failed reply is "unknown error <reply>"
		switch {
		case err == nil, errors.Is(err, ErrUnreachable):
		case strings.Contains(err.Error(), "authentication"):
			err = fmt.Errorf("%w: %w", ErrAuth, err)
		case strings.Contains(err.Error(), "unknown error "):
			err = fmt.Errorf("%w: %w", ErrRefused, err)
		}
		return c, err
	case "http":
		c, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, err
		}
		if dl, ok := ctx.Deadline(); ok {
			c.SetDeadline(dl)
		}
		stop := context.AfterFunc(ctx, func() { c.SetDeadline(time.Unix(1, 0)) }) // cancel unblocks the CONNECT
		br := bufio.NewReader(c)
		var resp *http.Response
		authz := ""
		if u := proxy.User; u != nil {
			pass, _ := u.Password()
			authz = "Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(u.Username()+":"+pass)) + "\r\n"
		}
		if _, err = fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n%s\r\n", target, target, authz); err == nil {
			resp, err = http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
		}
		switch {
		case !stop():
			err = ctx.Err()
		case err != nil:
		case resp.StatusCode == http.StatusProxyAuthRequired:
			err = fmt.Errorf("%w: %s", ErrAuth, resp.Status)
		case resp.StatusCode != http.StatusOK:
			err = fmt.Errorf("%w: %s", ErrRefused, resp.Status)
		}
		if err != nil {
			c.Close()
			return nil, err
		}
		c.SetDeadline(time.Time{})
		if br.Buffered() > 0 { // the target spoke first (a mail greeting) in the same read as the proxy's 200
			return bufConn{c, br}, nil
		}
		return c, nil
	}
	return nil, fmt.Errorf("unsupported proxy %q", proxy.Scheme)
}

// proxyDialer connects to the proxy itself; a failure wraps ErrUnreachable.
type proxyDialer struct{ d *net.Dialer }

func (p proxyDialer) Dial(network, addr string) (net.Conn, error) {
	return p.DialContext(context.Background(), network, addr)
}

func (p proxyDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	c, err := p.d.DialContext(ctx, network, addr)
	if err != nil {
		err = fmt.Errorf("%w: %w", ErrUnreachable, err)
	}
	return c, err
}

// bufConn reads what was buffered past the CONNECT reply before the socket.
type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (c bufConn) Read(p []byte) (int, error) { return c.r.Read(p) }
