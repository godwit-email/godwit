package core_test

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
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
	return standInProxy(t, "http")
}

// standInProxy is connectProxy speaking scheme: http (CONNECT) or socks5.
func standInProxy(t *testing.T, scheme string) (*url.URL, func() []string) {
	t.Helper()
	return authProxy(t, scheme, "", "")
}

// authProxy is standInProxy refusing a tunnel without user and pass, when user is set.
func authProxy(t *testing.T, scheme, user, pass string) (*url.URL, func() []string) {
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
				var target string
				if scheme == "http" {
					req, err := http.ReadRequest(br)
					if err != nil || req.Method != http.MethodConnect {
						return
					}
					if user != "" && req.Header.Get("Proxy-Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+pass)) {
						io.WriteString(c, "HTTP/1.1 407 Proxy Authentication Required\r\n\r\n")
						return
					}
					target = req.Host
				} else {
					// greeting: ver, nmethods, methods; request: ver, cmd, rsv, atyp=3, len, name, port
					hdr := make([]byte, 2)
					io.ReadFull(br, hdr)
					methods := make([]byte, hdr[1])
					io.ReadFull(br, methods)
					if user == "" {
						c.Write([]byte{5, 0})
					} else {
						if !slices.Contains(methods, 2) {
							c.Write([]byte{5, 0xff})
							return
						}
						// RFC 1929: ver, ulen, user, plen, pass
						c.Write([]byte{5, 2})
						b := make([]byte, 2)
						io.ReadFull(br, b)
						u := make([]byte, b[1])
						io.ReadFull(br, u)
						io.ReadFull(br, b[:1])
						pw := make([]byte, b[0])
						io.ReadFull(br, pw)
						if string(u) != user || string(pw) != pass {
							c.Write([]byte{1, 1})
							return
						}
						c.Write([]byte{1, 0})
					}
					req := make([]byte, 5)
					io.ReadFull(br, req)
					name := make([]byte, int(req[4])+2)
					io.ReadFull(br, name)
					target = net.JoinHostPort(string(name[:req[4]]), strconv.Itoa(int(name[req[4]])<<8|int(name[req[4]+1])))
				}
				mu.Lock()
				asked = append(asked, target)
				mu.Unlock()
				_, port, _ := net.SplitHostPort(target)
				up, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", port))
				if err != nil {
					return
				}
				if scheme == "http" {
					io.WriteString(c, "HTTP/1.1 200 OK\r\n\r\n")
				} else {
					c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
				}
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
	return &url.URL{Scheme: scheme, Host: ln.Addr().String()}, func() []string {
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

// customProxy saves 自定义 with proxy as the custom proxy.
func customProxy(t *testing.T, s *core.Service, proxy *url.URL) {
	t.Helper()
	st, err := s.GetSettings(ctx)
	require.NoError(t, err)
	st.ProxyMode, st.ProxyURL = core.ProxyCustom, proxy.String()
	_, err = s.SetSettings(ctx, st)
	require.NoError(t, err)
}

// 自定义 sends mail through the user's HTTP or SOCKS5 proxy, not the system's,
// and asks it for the server's host name.
func TestAccountThroughCustomProxy(t *testing.T) {
	for _, scheme := range []string{"http", "socks5"} {
		t.Run(scheme, func(t *testing.T) {
			system, systemAsked := connectProxy(t)
			s := proxiedService(t, system)
			proxy, asked := standInProxy(t, scheme)
			customProxy(t, s, proxy)
			im, smtpAddr := servers(t)
			in := input(t, im.Addr, smtpAddr, "secret")
			in.IMAPHost, in.SMTPHost = "example.com", "example.com"
			in.IMAPSecurity, in.SMTPSecurity = core.SecurityTLS, core.SecurityTLS
			require.NoError(t, s.TestAccount(ctx, in))
			_, ip := hostPort(t, im.Addr)
			_, sp := hostPort(t, smtpAddr)
			assert.Contains(t, asked(), net.JoinHostPort("example.com", strconv.Itoa(ip)), "IMAP asked the custom proxy for the host name")
			assert.Contains(t, asked(), net.JoinHostPort("example.com", strconv.Itoa(sp)), "SMTP asked the custom proxy for the host name")
			assert.Empty(t, systemAsked(), "the system proxy is not used")
		})
	}
}

func TestLoopbackAccountSkipsCustomProxy(t *testing.T) {
	s := proxiedService(t, nil)
	proxy, asked := standInProxy(t, "socks5")
	customProxy(t, s, proxy)
	im, smtpAddr := servers(t)
	require.NoError(t, s.TestAccount(ctx, input(t, im.Addr, smtpAddr, "secret")))
	assert.Empty(t, asked(), "a server on this computer connects direct")
}

// 自定义 with a username and password: the HTTP and SOCKS5 proxies receive
// them; without them the proxy refuses.
func TestAccountThroughCustomProxyWithCredentials(t *testing.T) {
	for _, scheme := range []string{"http", "socks5"} {
		t.Run(scheme, func(t *testing.T) {
			s := proxiedService(t, nil)
			proxy, asked := authProxy(t, scheme, "alice", "pr0xy-pass")
			im, smtpAddr := servers(t)
			in := input(t, im.Addr, smtpAddr, "secret")
			in.IMAPHost, in.SMTPHost = "example.com", "example.com"
			in.IMAPSecurity, in.SMTPSecurity = core.SecurityTLS, core.SecurityTLS

			customProxy(t, s, proxy)
			require.Error(t, s.TestAccount(ctx, in), "no credentials, refused")
			assert.Empty(t, asked())

			st, err := s.GetSettings(ctx)
			require.NoError(t, err)
			st.ProxyUser, st.ProxyPassword = "alice", "pr0xy-pass"
			saved, err := s.SetSettings(ctx, st)
			require.NoError(t, err)
			assert.Empty(t, saved.ProxyPassword, "the password is not handed back")
			assert.True(t, saved.ProxyPasswordSet)
			require.NoError(t, s.TestAccount(ctx, in))
			assert.NotEmpty(t, asked())
		})
	}
}
