package plugins

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeDNS answers from a map and records every lookup; it never touches real DNS.
type fakeDNS struct {
	mu      sync.Mutex
	ips     map[string][]string
	lookups []string
}

func (d *fakeDNS) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lookups = append(d.lookups, host)
	var out []net.IPAddr
	for _, s := range d.ips[host] {
		out = append(out, net.IPAddr{IP: net.ParseIP(s)})
	}
	if len(out) == 0 {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	return out, nil
}

type netEnv struct {
	srv   *httptest.Server
	port  string
	dns   *fakeDNS
	mu    sync.Mutex
	dials []string
	sni   []string
}

// newNetEnv starts a TLS server for *.example.com on 127.0.0.1. Every name
// below resolves to it unless a test changes dns.
func newNetEnv(t *testing.T) *netEnv {
	e := &netEnv{dns: &fakeDNS{ips: map[string][]string{}}}
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test", "yes")
		fmt.Fprint(w, "hello")
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		h := map[string]string{"host": r.Host}
		for k := range r.Header {
			h[strings.ToLower(k)] = r.Header.Get(k)
		}
		h["method"] = r.Method
		_ = json.NewEncoder(w).Encode(h)
	})
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Query().Get("to"), http.StatusFound)
	})
	mux.HandleFunc("/chain/{n}", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.PathValue("n"))
		if n == 0 {
			fmt.Fprint(w, "done")
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/chain/%d", n-1), http.StatusFound)
	})
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, 6<<20))
	})
	mux.HandleFunc("/exact", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, 5<<20))
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	e.srv = httptest.NewUnstartedServer(mux)
	e.srv.TLS = &tls.Config{GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
		e.mu.Lock()
		e.sni = append(e.sni, h.ServerName)
		e.mu.Unlock()
		return nil, nil
	}}
	e.srv.StartTLS()
	t.Cleanup(e.srv.Close)
	_, e.port, _ = net.SplitHostPort(e.srv.Listener.Addr().String())
	for _, h := range []string{"example.com", "api.example.com", "other.example.com", "evil.example.com"} {
		e.dns.ips[h] = []string{"127.0.0.1"}
	}
	return e
}

func (e *netEnv) client(loopback bool) *Client {
	pool := x509.NewCertPool()
	pool.AddCert(e.srv.Certificate())
	d := &net.Dialer{}
	return NewClient(NetOptions{Resolver: e.dns, AllowLoopback: loopback, UserAgent: "Godwit-Plugin/1.2.3", rootCAs: pool,
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			e.mu.Lock()
			e.dials = append(e.dials, addr)
			e.mu.Unlock()
			if !strings.HasPrefix(addr, "127.0.0.1:") {
				return nil, errors.New("test dialer: only the local server") // never the real network
			}
			return d.DialContext(ctx, network, addr)
		}})
}

func (e *netEnv) url(host, path string) string { return "https://" + host + ":" + e.port + path }
func (e *netEnv) hp(host string) string        { return host + ":" + e.port }

func TestFetchAllowed(t *testing.T) {
	e := newNetEnv(t)
	c := e.client(true)
	allowed := []string{"API.example.com:" + e.port}
	r, dest, err := c.Do(t.Context(), allowed, FetchRequest{URL: e.url("api.example.com", "/ok?q=secret")})
	require.NoError(t, err)
	assert.Equal(t, e.hp("api.example.com"), dest)
	assert.Equal(t, 200, r.Status)
	assert.Equal(t, "hello", r.Body)
	assert.Equal(t, "yes", r.Headers["x-test"])
	assert.Equal(t, []string{"127.0.0.1:" + e.port}, e.dials, "dialed the checked IP")
	assert.Equal(t, []string{"api.example.com"}, e.sni, "TLS still names the host")

	r, _, err = c.Do(t.Context(), allowed, FetchRequest{URL: e.url("api.example.com", "/chain/5")})
	require.NoError(t, err, "5 redirects are fine")
	assert.Equal(t, "done", r.Body)
	r, _, err = c.Do(t.Context(), allowed, FetchRequest{URL: e.url("api.example.com", "/exact")})
	require.NoError(t, err, "exactly 5 MB is fine")
	assert.Len(t, r.Body, 5<<20)
	r, _, err = c.Do(t.Context(), allowed, FetchRequest{URL: e.url("api.example.com", "/echo"), Method: "POST", Body: strings.Repeat("x", 1<<20)})
	require.NoError(t, err, "a 1 MB body is fine")
	assert.Contains(t, r.Body, `"method":"POST"`)
}

func TestFetchRejects(t *testing.T) {
	e := newNetEnv(t)
	e.dns.ips["loop.example.com"] = []string{"127.0.0.1"}
	e.dns.ips["private.example.com"] = []string{"10.0.0.1"}
	e.dns.ips["mapped.example.com"] = []string{"::ffff:127.0.0.1"}
	e.dns.ips["mixed.example.com"] = []string{"93.184.216.34", "192.168.1.1"}
	e.dns.ips["cgnat.example.com"] = []string{"100.64.0.1"}
	e.dns.ips["meta.example.com"] = []string{"169.254.169.254"}
	e.dns.ips["v6.example.com"] = []string{"fd00::1"}
	e.dns.ips["zero.example.com"] = []string{"0.0.0.0"}
	allowed := []string{e.hp("api.example.com"), "loop.example.com", "private.example.com", "mapped.example.com",
		"mixed.example.com", "cgnat.example.com", "meta.example.com", "v6.example.com", "zero.example.com", "127.0.0.1"}
	other := func(path string) string { return e.url("api.example.com", path) }
	for _, tc := range []struct {
		name, url, body string
		strict          bool // a client without the test loopback allowance
		reason          string
	}{
		{"undeclared host", e.url("other.example.com", "/ok"), "", false, "host_not_allowed"},
		{"port differs", "https://api.example.com:1/ok", "", false, "host_not_allowed"},
		{"default port not declared", "https://api.example.com/ok", "", false, "host_not_allowed"},
		{"http", "http://api.example.com:" + e.port + "/ok", "", false, "scheme"},
		{"ftp", "ftp://api.example.com:" + e.port + "/ok", "", false, "scheme"},
		{"ip literal", "https://127.0.0.1/ok", "", false, "ip_literal"},
		{"ip6 literal", "https://[::ffff:127.0.0.1]/ok", "", false, "ip_literal"},
		{"userinfo", "https://user:pass@" + e.hp("api.example.com") + "/ok", "", false, "userinfo"},
		{"redirect to undeclared", other("/redirect?to=" + e.url("evil.example.com", "/ok")), "", false, "host_not_allowed"},
		{"redirect to http", other("/redirect?to=http://" + e.hp("api.example.com") + "/ok"), "", false, "scheme"},
		{"redirect to userinfo", other("/redirect?to=https://u:p@" + e.hp("api.example.com") + "/ok"), "", false, "userinfo"},
		{"redirect to ip", other("/redirect?to=https://127.0.0.1:" + e.port + "/ok"), "", false, "ip_literal"},
		{"6 redirects", other("/chain/6"), "", false, "too_many_redirects"},
		{"resolves to loopback", "https://loop.example.com/ok", "", true, "private_address"},
		{"resolves to 10/8", "https://private.example.com/ok", "", false, "private_address"},
		{"resolves to mapped loopback", "https://mapped.example.com/ok", "", true, "private_address"},
		{"one private address", "https://mixed.example.com/ok", "", false, "private_address"},
		{"resolves to CGNAT", "https://cgnat.example.com/ok", "", false, "private_address"},
		{"resolves to metadata", "https://meta.example.com/ok", "", false, "private_address"},
		{"resolves to ULA", "https://v6.example.com/ok", "", false, "private_address"},
		{"resolves to 0.0.0.0", "https://zero.example.com/ok", "", false, "private_address"},
		{"6 MB response", other("/big"), "", false, "too_large"},
		{"2 MB request", other("/echo"), strings.Repeat("x", 2<<20), false, "too_large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e.mu.Lock()
			e.dials = nil
			e.mu.Unlock()
			r, _, err := e.client(!tc.strict).Do(t.Context(), allowed, FetchRequest{URL: tc.url, Method: "POST", Body: tc.body})
			var ne *NetError
			require.True(t, errors.As(err, &ne), "got %v", err)
			assert.Equal(t, tc.reason, ne.Reason)
			assert.Zero(t, r, "no partial response")
			assert.NotContains(t, err.Error(), "/ok")
			if tc.reason == "private_address" {
				assert.Empty(t, e.dials, "a blocked address is never dialed")
			}
		})
	}

	c := e.client(true)
	c.opts.Timeout = 50 * time.Millisecond
	c = NewClient(c.opts)
	_, _, err := c.Do(t.Context(), allowed, FetchRequest{URL: other("/slow")})
	var ne *NetError
	require.True(t, errors.As(err, &ne), "got %v", err)
	assert.Equal(t, "timeout", ne.Reason)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err = e.client(true).Do(ctx, allowed, FetchRequest{URL: other("/ok")})
	assert.ErrorIs(t, err, context.Canceled, "a cancelled call is not a denial")
}

func TestFetchIgnoresProxyEnv(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://proxy.example.com:3128")
	t.Setenv("HTTP_PROXY", "http://proxy.example.com:3128")
	t.Setenv("ALL_PROXY", "http://proxy.example.com:3128")
	e := newNetEnv(t)
	e.dns.ips["proxy.example.com"] = []string{"127.0.0.1"}
	r, _, err := e.client(true).Do(t.Context(), []string{e.hp("api.example.com")}, FetchRequest{URL: e.url("api.example.com", "/echo")})
	require.NoError(t, err)
	assert.Contains(t, r.Body, `"host":"`+e.hp("api.example.com")+`"`)
	assert.Equal(t, []string{"api.example.com"}, e.dns.lookups, "the proxy was never looked up")
	assert.Equal(t, []string{"127.0.0.1:" + e.port}, e.dials)
	// The environment's proxy is read once per process, so check the transport too.
	assert.Nil(t, e.client(true).tr.Proxy)
}

func TestFetchStripsHeaders(t *testing.T) {
	e := newNetEnv(t)
	c := e.client(true)
	allowed := []string{e.hp("example.com"), e.hp("api.example.com"), e.hp("other.example.com")}
	hdr := map[string]string{"Cookie": "sid=1", "Host": "evil.example.com", "Proxy-Authorization": "Basic x",
		"proxy-connection": "keep-alive", "Authorization": "Bearer tok", "User-Agent": "spoof", "X-Mine": "1"}
	seen := func(url string) map[string]string {
		r, _, err := c.Do(t.Context(), allowed, FetchRequest{URL: url, Headers: hdr})
		require.NoError(t, err)
		var h map[string]string
		require.NoError(t, json.Unmarshal([]byte(r.Body), &h))
		return h
	}
	h := seen(e.url("api.example.com", "/echo"))
	assert.Equal(t, e.hp("api.example.com"), h["host"])
	assert.Equal(t, "Bearer tok", h["authorization"], "the plugin's own header to its declared host")
	assert.Equal(t, "Godwit-Plugin/1.2.3", h["user-agent"])
	assert.Equal(t, "1", h["x-mine"])
	for _, k := range []string{"cookie", "proxy-authorization", "proxy-connection"} {
		assert.NotContains(t, h, k)
	}

	h = seen(e.url("api.example.com", "/redirect?to="+e.url("other.example.com", "/echo")))
	assert.Equal(t, e.hp("other.example.com"), h["host"])
	assert.NotContains(t, h, "authorization", "dropped on a cross-host redirect")
	assert.NotContains(t, h, "cookie")

	// net/http keeps it for a subdomain; another host is another host.
	h = seen(e.url("example.com", "/redirect?to="+e.url("api.example.com", "/echo")))
	assert.Equal(t, e.hp("api.example.com"), h["host"])
	assert.NotContains(t, h, "authorization", "dropped on a redirect to a subdomain")

	back := e.url("other.example.com", "/redirect?to="+e.url("api.example.com", "/echo"))
	h = seen(e.url("api.example.com", "/redirect?to="+strings.ReplaceAll(back, "?", "%3F")))
	assert.Equal(t, e.hp("api.example.com"), h["host"])
	assert.NotContains(t, h, "authorization", "not restored once the chain left the host")
}
