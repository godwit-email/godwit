package core_test

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/conn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

// connectProxy starts an HTTP CONNECT proxy that sends every tunnel to the
// asked port on 127.0.0.1, and records the targets it was asked for. It
// speaks TLS for the server (as example.com), which conn trusts meanwhile,
// so a mail server by host name can be checked without plaintext.
func connectProxy(t *testing.T) (*url.URL, func() []string) {
	t.Helper()
	srv := httptest.NewTLSServer(nil)
	srv.Close() // only its certificate is wanted
	conn.TLSRoots = x509.NewCertPool()
	conn.TLSRoots.AddCert(srv.Certificate())
	t.Cleanup(func() { conn.TLSRoots = nil })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	var asked []string
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				br := bufio.NewReader(c)
				req, err := http.ReadRequest(br)
				if err != nil || req.Method != http.MethodConnect {
					return
				}
				mu.Lock()
				asked = append(asked, req.Host)
				mu.Unlock()
				_, port, _ := net.SplitHostPort(req.Host)
				up, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", port))
				if err != nil {
					return
				}
				io.WriteString(c, "HTTP/1.1 200 OK\r\n\r\n")
				if br.Buffered() > 0 {
					return // the client spoke before the tunnel was up
				}
				tc := tls.Server(c, srv.TLS)
				defer tc.Close()
				go func() { io.Copy(up, tc); up.Close() }()
				io.Copy(tc, up)
			}()
		}
	}()
	return &url.URL{Scheme: "http", Host: ln.Addr().String()}, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), asked...)
	}
}

// proxiedService is a Service whose system proxy is proxy.
func proxiedService(t *testing.T, proxy *url.URL) *core.Service {
	t.Helper()
	keyring.MockInit()
	s, err := core.New(ctx, core.Options{DataDir: t.TempDir(), SystemProxy: func(*url.URL) *url.URL { return proxy }})
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}

func TestAccountThroughSystemProxy(t *testing.T) {
	proxy, asked := connectProxy(t)
	s := proxiedService(t, proxy)
	im, smtpAddr := servers(t)
	in := input(t, im.Addr, smtpAddr, "secret")
	in.IMAPHost, in.SMTPHost = "example.com", "example.com" // the proxy, not DNS, finds it
	in.IMAPSecurity, in.SMTPSecurity = core.SecurityTLS, core.SecurityTLS
	require.NoError(t, s.TestAccount(ctx, in))
	_, ip := hostPort(t, im.Addr)
	_, sp := hostPort(t, smtpAddr)
	assert.Contains(t, asked(), net.JoinHostPort("example.com", strconv.Itoa(ip)), "IMAP asked the proxy for the host name")
	assert.Contains(t, asked(), net.JoinHostPort("example.com", strconv.Itoa(sp)), "SMTP asked the proxy for the host name")
}

func TestLoopbackAccountSkipsSystemProxy(t *testing.T) {
	proxy, asked := connectProxy(t)
	s := proxiedService(t, proxy)
	im, smtpAddr := servers(t)
	require.NoError(t, s.TestAccount(ctx, input(t, im.Addr, smtpAddr, "secret"))) // plaintext: a proxy's TLS would fail it
	assert.Empty(t, asked(), "a server on this computer connects direct")
}

func TestProxyDecision(t *testing.T) {
	proxy := &url.URL{Scheme: "socks5", Host: "127.0.0.1:1080"}
	s := proxiedService(t, proxy)
	assert.Equal(t, proxy, s.Proxy(&url.URL{Scheme: "https", Host: "example.com"}))
	for _, h := range []string{"localhost:8080", "127.0.0.1", "[::1]:443"} {
		assert.Nil(t, s.Proxy(&url.URL{Scheme: "https", Host: h}), h)
	}
}
