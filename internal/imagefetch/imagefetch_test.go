package imagefetch

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestPublicIP(t *testing.T) {
	for addr, want := range map[string]bool{
		"8.8.8.8": true, "2606:4700::1111": true,
		"127.0.0.1": false, "10.0.0.1": false, "192.168.1.1": false, "169.254.1.1": false,
		"0.0.0.0": false, "::1": false, "fe80::1": false, "224.0.0.1": false,
		"100.64.0.1": false, "100.127.255.254": false, "100.128.0.1": true, "198.18.0.1": false, "240.0.0.1": false, "0.1.2.3": false, "fd00::1": false, "2001:db8::1": false, "fec0::1": false, "2001:2::1": false, "2002:a00:1::1": false, "5f00::1": false, "2001:10::1": false, "3fff::1": false, "4000::1": false, "64:ff9b:1::a00:1": false, "64:ff9b::a00:1": false, "64:ff9b::808:808": true,
	} {
		if got := PublicIP(netip.MustParseAddr(addr)); got != want {
			t.Errorf("PublicIP(%s) = %v", addr, got)
		}
	}
}

func TestConfig(t *testing.T) {
	format, px, err := Config(pngOf(t, 3, 2))
	if err != nil || format != "png" || px != 6 {
		t.Fatalf("got %q %d %v", format, px, err)
	}
	if _, _, err := Config([]byte("<svg/>")); !errors.Is(err, ErrFormat) {
		t.Errorf("svg: %v", err)
	}
}

func TestBudget(t *testing.T) {
	b := NewBudget()
	for range MaxImages {
		if err := b.Take(); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Take(); !errors.Is(err, ErrTooMany) {
		t.Errorf("the 51st: %v", err)
	}
	if err := b.TakePixels(MaxTotalPixels); err != nil {
		t.Fatal(err)
	}
	if err := b.TakePixels(1); !errors.Is(err, ErrTotalPixels) {
		t.Errorf("over the total: %v", err)
	}

	b = NewBudget()
	if err := b.TakeInline(MaxTotalBytes-10, 5); err != nil {
		t.Fatal(err)
	}
	if err := b.TakeInline(11, 5); !errors.Is(err, ErrTotalTooLarge) {
		t.Errorf("over the bytes: %v", err)
	}
	if err := b.TakeInline(10, MaxTotalPixels); !errors.Is(err, ErrTotalPixels) || b.bytes.Load() != 10 {
		t.Errorf("over the pixels: %v, %d bytes left (want them back)", err, b.bytes.Load())
	}
}

func TestDataURL(t *testing.T) {
	pic := pngOf(t, 1, 1)
	got, err := DataURL(Encode("png", pic))
	if err != nil || !bytes.Equal(got, pic) {
		t.Fatalf("round trip: %v", err)
	}
	if !strings.HasPrefix(Encode("webp", pic), "data:image/webp;base64,") {
		t.Error("Encode's type")
	}
	big := "data:image/png;base64," + base64.StdEncoding.EncodeToString(make([]byte, MaxImageBytes+1))
	if _, err := DataURL(big); !errors.Is(err, ErrTooLarge) {
		t.Errorf("big: %v", err)
	}
	if _, err := DataURL("https://x.example/a.png"); !errors.Is(err, ErrScheme) {
		t.Errorf("not data: %v", err)
	}
}

func TestFetch(t *testing.T) {
	pic := pngOf(t, 2, 3)
	var hits atomic.Int64
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		hdr = r.Header.Clone()
		w.Header().Set("Set-Cookie", "track=1")
		if r.URL.Path == "/page" {
			w.Write([]byte("<html>"))
			return
		}
		w.Write(pic)
	}))
	defer srv.Close()

	if _, _, _, _, err := Fetch(t.Context(), NewClient(false), srv.URL+"/a.png"); !errors.Is(err, ErrHost) || hits.Load() != 0 {
		t.Fatalf("loopback: %v, %d requests", err, hits.Load())
	}
	c := NewClient(true)
	data, format, w, h, err := Fetch(t.Context(), c, srv.URL+"/a.png")
	if err != nil || !bytes.Equal(data, pic) || format != "png" || w != 2 || h != 3 {
		t.Fatalf("fetch: %q %dx%d %v", format, w, h, err)
	}
	if hdr.Get("Cookie") != "" || hdr.Get("Referer") != "" || hdr.Get("User-Agent") != UserAgent || strings.Contains(hdr.Get("Accept"), "avif") {
		t.Errorf("headers %v", hdr)
	}
	if _, _, _, _, err := Fetch(t.Context(), c, srv.URL+"/page"); !errors.Is(err, ErrFormat) {
		t.Errorf("html: %v", err)
	}
	if _, err := Download(t.Context(), c, "ftp://x.example/a.png"); !errors.Is(err, ErrScheme) {
		t.Errorf("ftp: %v", err)
	}
}

// fakeDNS answers lookups from answers until the test ends.
func fakeDNS(t *testing.T, answers map[string][]string) {
	t.Helper()
	old := lookup
	t.Cleanup(func() { lookup = old })
	lookup = func(_ context.Context, _, host string) ([]netip.Addr, error) {
		var out []netip.Addr
		for _, a := range answers[host] {
			out = append(out, netip.MustParseAddr(a))
		}
		if len(out) == 0 {
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
		return out, nil
	}
}

func TestResolve(t *testing.T) {
	fakeDNS(t, map[string][]string{
		"public.example": {"2606:4700::1111", "93.184.216.34"},
		"fake.example":   {"198.18.0.7"},
		"mixed.example":  {"198.18.0.7", "93.184.216.34"},
		"lan.example":    {"93.184.216.34", "192.168.1.1"},
	})
	for host, want := range map[string]struct {
		first string
		fake  bool
		err   error
	}{
		"public.example": {first: "93.184.216.34"}, // IPv4 first
		"fake.example":   {first: "198.18.0.7", fake: true},
		"mixed.example":  {err: ErrHost}, // a fake answer beside a real one is judged as any
		"lan.example":    {err: ErrHost}, // every answer must be public
		"8.8.8.8":        {first: "8.8.8.8"},
		"198.18.0.7":     {err: ErrHost}, // written as is: no name stands behind it
		"10.0.0.1":       {err: ErrHost},
	} {
		addrs, fake, err := resolve(t.Context(), host)
		if !errors.Is(err, want.err) || err == nil && (addrs[0].String() != want.first || fake != want.fake) {
			t.Errorf("%s: %v %v %v, want %+v", host, addrs, fake, err, want)
		}
	}
}

// connectProxy is an HTTP proxy that tunnels every CONNECT to backend,
// noting the target it was asked for.
func connectProxy(t *testing.T, backend string) (*httptest.Server, *atomic.Value) {
	t.Helper()
	var asked atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT only", http.StatusMethodNotAllowed)
			return
		}
		asked.Store(r.Host)
		up, err := net.Dial("tcp", backend)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
		c, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			up.Close()
			return
		}
		go func() { io.Copy(up, c); up.Close() }()
		go func() { io.Copy(c, up); c.Close() }()
	}))
	t.Cleanup(srv.Close)
	return srv, &asked
}

func TestSystemProxyPinsAddress(t *testing.T) {
	pic := pngOf(t, 1, 2)
	img := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(pic) }))
	defer img.Close()
	proxy, asked := connectProxy(t, img.Listener.Addr().String())
	fakeDNS(t, map[string][]string{
		"images.example": {"93.184.216.34"},
		"fake.example":   {"198.18.0.7"},
		"lan.example":    {"192.168.1.1"},
	})
	proxyURL, _ := url.Parse(proxy.URL)
	SystemProxy = func(*url.URL) *url.URL { return proxyURL }
	defer func() { SystemProxy = nil }()

	c := NewClient(false)
	data, _, _, _, err := Fetch(t.Context(), c, "http://images.example/a.png")
	if err != nil || !bytes.Equal(data, pic) || asked.Load() != "93.184.216.34:80" {
		t.Fatalf("through the proxy: %v, asked %v", err, asked.Load())
	}
	if _, _, _, _, err := Fetch(t.Context(), c, "http://fake.example/a.png"); err != nil || asked.Load() != "fake.example:80" {
		t.Errorf("a fake-ip name goes to a proxy on this computer by name: %v, asked %v", err, asked.Load())
	}
	asked.Store("")
	for _, u := range []string{"http://lan.example/a.png", "http://192.168.1.1/a.png", "http://127.0.0.1/a.png", "http://localhost/a.png", "http://x.localhost/a.png"} {
		if _, _, _, _, err := Fetch(t.Context(), c, u); !errors.Is(err, ErrHost) || asked.Load() != "" {
			t.Errorf("%s: %v, asked %v", u, err, asked.Load())
		}
	}

	SystemProxy = func(*url.URL) *url.URL { return &url.URL{Scheme: "http", Host: "203.0.113.9:8080"} }
	if _, _, _, _, err := Fetch(t.Context(), NewClient(false), "http://fake.example/a.png"); !errors.Is(err, ErrHost) {
		t.Errorf("a fake-ip name through a proxy elsewhere: %v", err)
	}
}

func TestSystemProxyRefusesTunnel(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusForbidden)
	}))
	defer proxy.Close()
	fakeDNS(t, map[string][]string{"images.example": {"93.184.216.34"}})
	proxyURL, _ := url.Parse(proxy.URL)
	SystemProxy = func(*url.URL) *url.URL { return proxyURL }
	defer func() { SystemProxy = nil }()
	if _, _, _, _, err := Fetch(t.Context(), NewClient(false), "http://images.example/a.png"); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("refused tunnel: %v", err)
	}
}
