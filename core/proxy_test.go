package core_test

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

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

// tlsFront serves TLS as localhost in front of the plaintext server at addr,
// and conn trusts it meanwhile (after connectProxy, which sets TLSRoots).
// Asked for as "localhost." it is reached by name, out of the loopback rule,
// so the proxy setting decides the route; its port is returned.
func tlsFront(t *testing.T, addr string) int {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"localhost"},
		NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}},
		&x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"localhost"}, NotAfter: time.Now().Add(time.Hour)}, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	conn.TLSRoots.AddCert(cert)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}})
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				up, err := net.Dial("tcp", addr)
				if err != nil {
					return
				}
				go func() { io.Copy(up, c); up.Close() }()
				io.Copy(c, up)
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// 不使用代理 connects direct although the system has a proxy; switching back
// to 跟随系统 applies to the next connection.
func TestProxyModeOff(t *testing.T) {
	proxy, asked := connectProxy(t)
	s := proxiedService(t, proxy)
	im, smtpAddr := servers(t)
	st, err := s.GetSettings(ctx)
	require.NoError(t, err)
	st.ProxyMode = core.ProxyOff
	_, err = s.SetSettings(ctx, st)
	require.NoError(t, err)
	direct := input(t, im.Addr, smtpAddr, "secret")
	direct.IMAPHost, direct.SMTPHost = "localhost.", "localhost."
	direct.IMAPPort, direct.SMTPPort = tlsFront(t, im.Addr), tlsFront(t, smtpAddr)
	direct.IMAPSecurity, direct.SMTPSecurity = core.SecurityTLS, core.SecurityTLS
	require.NoError(t, s.TestAccount(ctx, direct))
	assert.Empty(t, asked(), "off connects direct")

	st.ProxyMode = core.ProxySystem
	_, err = s.SetSettings(ctx, st)
	require.NoError(t, err)
	via := input(t, im.Addr, smtpAddr, "secret")
	via.IMAPHost, via.SMTPHost = "example.com", "example.com"
	via.IMAPSecurity, via.SMTPSecurity = core.SecurityTLS, core.SecurityTLS
	require.NoError(t, s.TestAccount(ctx, via))
	assert.NotEmpty(t, asked(), "system goes through the system proxy")
}
