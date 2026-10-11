package imagefetch

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/netip"
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
	b.GivePixels(1)
	if err := b.TakePixels(1); err != nil {
		t.Errorf("given back: %v", err)
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

func TestDownload(t *testing.T) {
	pic := pngOf(t, 2, 2)
	var hits atomic.Int64
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		hdr = r.Header.Clone()
		w.Header().Set("Set-Cookie", "track=1")
		w.Write(pic)
	}))
	defer srv.Close()

	b := NewBudget()
	if _, err := b.Download(t.Context(), NewClient(false), srv.URL+"/a.png"); !errors.Is(err, ErrHost) || hits.Load() != 0 {
		t.Fatalf("loopback: %v, %d requests", err, hits.Load())
	}
	c := NewClient(true)
	data, err := b.Download(t.Context(), c, srv.URL+"/a.png")
	if err != nil || !bytes.Equal(data, pic) {
		t.Fatalf("download: %v", err)
	}
	if hdr.Get("Cookie") != "" || hdr.Get("Referer") != "" {
		t.Errorf("headers %v", hdr)
	}
	if left := b.BytesLeft(); left != MaxTotalBytes-int64(len(pic)) {
		t.Errorf("%d bytes left", left)
	}
	if _, err := b.Download(t.Context(), c, "ftp://x.example/a.png"); !errors.Is(err, ErrScheme) {
		t.Errorf("ftp: %v", err)
	}
}
