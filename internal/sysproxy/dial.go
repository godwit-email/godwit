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
	"time"

	xproxy "golang.org/x/net/proxy"
)

// Dial opens a tunnel to target (host:port) through proxy: an http:// one
// by CONNECT, a socks5:// one by SOCKS5, with the proxy's credentials when
// it has some. ctx bounds the whole of it.
func Dial(ctx context.Context, proxy *url.URL, target string) (net.Conn, error) {
	d := &net.Dialer{}
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
		return s.(xproxy.ContextDialer).DialContext(ctx, "tcp", target)
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
		case resp.StatusCode != http.StatusOK:
			err = fmt.Errorf("proxy refused the tunnel: %s", resp.Status)
		case br.Buffered() > 0:
			err = errors.New("proxy sent data before the tunnel opened")
		}
		if err != nil {
			c.Close()
			return nil, err
		}
		c.SetDeadline(time.Time{})
		return c, nil
	}
	return nil, fmt.Errorf("unsupported proxy %q", proxy.Scheme)
}
