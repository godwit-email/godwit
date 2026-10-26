package imagefetch

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

const safeSVG = `<svg xmlns="http://www.w3.org/2000/svg" version="1.2" baseProfile="tiny-ps" viewBox="0 0 32 32"><title>Logo</title><path fill="#123456" d="M0 0L32 0L32 32Z"/></svg>`

func icoOf(t *testing.T, w, h int, dib bool) []byte {
	t.Helper()
	data := pngOf(t, w, h)
	if dib {
		stride := ((w + 31) / 32) * 4
		data = make([]byte, 40+w*h*4+stride*h)
		binary.LittleEndian.PutUint32(data, 40)
		binary.LittleEndian.PutUint32(data[4:], uint32(w))
		binary.LittleEndian.PutUint32(data[8:], uint32(h*2))
		binary.LittleEndian.PutUint16(data[12:], 1)
		binary.LittleEndian.PutUint16(data[14:], 32)
		for i := 40; i < 40+w*h*4; i += 4 {
			data[i+3] = 255
		}
	}
	out := make([]byte, 22)
	binary.LittleEndian.PutUint16(out[2:], 1)
	binary.LittleEndian.PutUint16(out[4:], 1)
	out[6], out[7] = byte(w), byte(h)
	binary.LittleEndian.PutUint16(out[10:], 1)
	binary.LittleEndian.PutUint16(out[12:], 32)
	binary.LittleEndian.PutUint32(out[14:], uint32(len(data)))
	binary.LittleEndian.PutUint32(out[18:], 22)
	return append(out, data...)
}

func TestFaviconImageMinSize(t *testing.T) {
	for _, dim := range [][2]int{{16, 16}, {31, 32}, {32, 31}, {32, 32}} {
		for _, kind := range []string{"png", "ico-png", "ico-dib"} {
			t.Run(fmt.Sprintf("%s-%dx%d", kind, dim[0], dim[1]), func(t *testing.T) {
				data := pngOf(t, dim[0], dim[1])
				if kind != "png" {
					data = icoOf(t, dim[0], dim[1], kind == "ico-dib")
				}
				_, format, w, h, err := CheckAvatarImage(data, "favicon")
				if dim[0] < 32 || dim[1] < 32 {
					if err == nil {
						t.Fatal("undersized favicon accepted")
					}
					return
				}
				if err != nil || format != "png" || w != 32 || h != 32 {
					t.Fatalf("%s %dx%d: %v", format, w, h, err)
				}
			})
		}
	}
	if _, _, _, _, err := CheckAvatarImage(pngOf(t, 16, 16), "gravatar"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := CheckAvatarImage([]byte(strings.ReplaceAll(safeSVG, "32", "16")), "favicon"); err != nil {
		t.Fatal(err)
	}
}

func TestBIMIImageAndAvatarFormats(t *testing.T) {
	for _, source := range []string{"bimi", "favicon"} {
		out, format, w, h, err := CheckAvatarImage([]byte(safeSVG), source)
		if err != nil || format != "svg" || w != 32 || h != 32 || !bytes.Equal(out, []byte(safeSVG)) {
			t.Fatalf("%s: %s %dx%d %v", source, format, w, h, err)
		}
	}
	static := strings.Replace(safeSVG, ` baseProfile="tiny-ps"`, "", 1)
	if _, _, _, _, err := CheckAvatarImage([]byte(static), "favicon"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		source string
		data   []byte
	}{
		{"bimi", []byte(static)}, {"bimi", pngOf(t, 32, 32)}, {"gravatar", []byte(safeSVG)},
		{"body", pngOf(t, 32, 32)}, {"favicon", []byte("<html>not an image</html>")},
		{"favicon", pngOf(t, 1025, 32)}, {"favicon", make([]byte, 256*1024+1)},
	} {
		if _, _, _, _, err := CheckAvatarImage(tc.data, tc.source); err == nil {
			t.Errorf("accepted %s unsafe format", tc.source)
		}
	}
	for _, data := range [][]byte{[]byte(safeSVG), icoOf(t, 32, 32, false), icoOf(t, 32, 32, true)} {
		if _, _, _, err := Dims(data); !errors.Is(err, ErrFormat) {
			t.Fatalf("ordinary Dims: %v", err)
		}
	}
}

func TestAvatarSVGRejectsUnsafeAndUnbounded(t *testing.T) {
	for name, svg := range map[string]string{
		"script":    strings.Replace(safeSVG, "</svg>", "<script>alert(1)</script></svg>", 1),
		"animation": strings.Replace(safeSVG, "</svg>", "<animate/></svg>", 1),
		"image":     strings.Replace(safeSVG, "</svg>", `<image href="data:image/png;base64,AA=="/></svg>`, 1),
		"external":  strings.Replace(safeSVG, `fill="#123456"`, `fill="url(https://evil.example/a)"`, 1),
		"css":       strings.Replace(safeSVG, `fill="#123456"`, `style="fill:url(https://evil.example/a)"`, 1),
		"event":     strings.Replace(safeSVG, "<path ", `<path onload="alert(1)" `, 1),
		"use cycle": strings.Replace(safeSVG, "</svg>", `<use id="a" href="#a"/></svg>`, 1),
		"doctype":   `<!DOCTYPE svg [<!ENTITY x SYSTEM "file:///etc/passwd">]>` + safeSVG,
		"pi":        `<?xml-stylesheet href="https://evil.example/a"?>` + safeSVG,
		"xml":       safeSVG + `<svg/>`,
		"namespace": strings.Replace(safeSVG, "http://www.w3.org/2000/svg", "https://evil.example", 1),
		"depth":     strings.Replace(safeSVG, "</svg>", strings.Repeat("<g>", 32)+strings.Repeat("</g>", 32)+"</svg>", 1),
		"elements":  strings.Replace(safeSVG, "</svg>", strings.Repeat("<g/>", 4096)+"</svg>", 1),
		"paths":     strings.Replace(safeSVG, "M0 0L32 0L32 32Z", strings.Repeat("z", 16385), 1),
		"bytes":     safeSVG + strings.Repeat(" ", 32*1024),
		"nan":       strings.Replace(safeSVG, "0 0 32 32", "0 0 NaN 32", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, _, _, err := CheckAvatarImage([]byte(svg), "favicon"); err == nil {
				t.Fatal("unsafe SVG accepted")
			}
		})
	}
}

func TestAvatarICORejectsInvalidDirectory(t *testing.T) {
	for _, offset := range []uint32{0, 21, 0xffffffff} {
		data := icoOf(t, 32, 32, false)
		binary.LittleEndian.PutUint32(data[18:], offset)
		if _, _, _, _, err := CheckAvatarImage(data, "favicon"); err == nil {
			t.Fatalf("offset %d accepted", offset)
		}
	}
	data := icoOf(t, 16, 16, false)
	data[6], data[7] = 32, 32
	if _, _, _, _, err := CheckAvatarImage(data, "favicon"); err == nil {
		t.Fatal("directory dimensions override actual small PNG")
	}
}

func TestAvatarDownloadHTTPSAndLimits(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" || r.Header.Get("Referer") != "" || !strings.Contains(r.Header.Get("Accept"), "image/svg+xml") {
			t.Errorf("headers: %v", r.Header)
		}
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "http://127.0.0.1:1/x", 302)
		case "/big":
			w.Write(make([]byte, 256*1024+1))
		default:
			w.Write([]byte(safeSVG))
		}
	}))
	defer srv.Close()
	base := srv.Client()
	c := NewAvatarClient(base)
	if c == base || c.Transport != base.Transport {
		t.Fatal("base client not copied with transport preserved")
	}
	for _, src := range []string{"http://example.com/a", srv.URL + "/redirect", srv.URL + "/big"} {
		if _, err := DownloadAvatar(t.Context(), c, src); err == nil {
			t.Fatalf("accepted %s", src)
		}
	}
	if data, err := DownloadAvatar(t.Context(), c, srv.URL); err != nil || !bytes.Equal(data, []byte(safeSVG)) {
		t.Fatalf("download: %v", err)
	}
	sentinel := errors.New("base redirect restriction")
	base.CheckRedirect = func(*http.Request, []*http.Request) error { return sentinel }
	req, _ := http.NewRequest("GET", "https://example.com", nil)
	if err := NewAvatarClient(base).CheckRedirect(req, []*http.Request{req}); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if _, err := DownloadAvatar(t.Context(), NewAvatarClient(NewClient(false)), srv.URL); !errors.Is(err, ErrHost) {
		t.Fatalf("private dial: %v", err)
	}
}

func TestBIMIImageRequiresTinyPSVersion(t *testing.T) {
	for _, s := range []string{strings.Replace(safeSVG, `version="1.2"`, `version="1.1"`, 1), strings.Replace(safeSVG, `version="1.2"`, "", 1)} {
		if _, _, _, _, err := CheckAvatarImage([]byte(s), "bimi"); err == nil {
			t.Fatal("non Tiny PS version accepted")
		}
	}
}

func TestAvatarSVGBoundedCoordinates(t *testing.T) {
	for _, path := range []string{"MNaN 0L32 32", "M0 0L1e300 32", "M0 0LInf 32"} {
		svg := strings.Replace(safeSVG, "M0 0L32 0L32 32Z", path, 1)
		if _, _, _, _, err := CheckAvatarImage([]byte(svg), "favicon"); err == nil {
			t.Fatalf("unbounded path accepted: %s", path)
		}
	}
}

func TestAvatarSVGRejectsUnboundedShapes(t *testing.T) {
	for _, shape := range []string{`<circle r="1e300"/>`, `<rect width="NaN" height="32"/>`, `<g opacity="Inf"/>`, `<path stroke-width="1e300" d="M0 0L32 32"/>`} {
		svg := strings.Replace(safeSVG, "</svg>", shape+"</svg>", 1)
		if _, _, _, _, err := CheckAvatarImage([]byte(svg), "favicon"); err == nil {
			t.Fatalf("unbounded shape accepted: %s", shape)
		}
	}
}

func TestAvatarSVGIntrinsicSizeIndependentOfAttributeOrder(t *testing.T) {
	for _, attrs := range []string{`width="64" height="48" viewBox="0 0 32 32"`, `viewBox="0 0 32 32" width="64" height="48"`} {
		svg := `<svg xmlns="http://www.w3.org/2000/svg" ` + attrs + `><rect width="32" height="32"/></svg>`
		_, _, w, h, err := CheckAvatarImage([]byte(svg), "favicon")
		if err != nil || w != 64 || h != 48 {
			t.Fatalf("intrinsic size: %dx%d %v", w, h, err)
		}
	}
}

func TestAvatarRedirectPrivateDialRejected(t *testing.T) {
	private := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("redirect reached private server") }))
	defer private.Close()
	initial := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, private.URL, 302) }))
	defer initial.Close()
	base := NewClient(false)
	tr := base.Transport.(*Transport)
	publicDial := tr.DialContext
	initialURL, _ := url.Parse(initial.URL)
	tr.TLSClientConfig = initial.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address == "initial.example.com:443" {
			return (&net.Dialer{}).DialContext(ctx, network, initialURL.Host)
		}
		return publicDial(ctx, network, address)
	}
	if _, err := DownloadAvatar(t.Context(), NewAvatarClient(base), "https://initial.example.com/"); !errors.Is(err, ErrHost) {
		t.Fatalf("private redirect: %v", err)
	}
}

func TestAvatarRedirectLimitAndNoTracking(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("Cookie") != "" || r.Header.Get("Referer") != "" {
			t.Errorf("tracking headers: %v", r.Header)
		}
		w.Header().Set("Set-Cookie", "track=1")
		http.Redirect(w, r, "/again", 302)
	}))
	defer srv.Close()
	base := srv.Client()
	base.Jar, _ = cookiejar.New(nil)
	if _, err := DownloadAvatar(t.Context(), base, srv.URL); !errors.Is(err, errTooManyRedirects) {
		t.Fatal(err)
	}
	if hits.Load() != 6 {
		t.Fatalf("redirect requests: %d", hits.Load())
	}
	if base.Jar == nil || base.CheckRedirect != nil {
		t.Fatal("mutated parent client")
	}
}

func TestAvatarICOSelectsValidatedFrameAtItsOffset(t *testing.T) {
	small := pngOf(t, 16, 16)
	large := pngOf(t, 32, 32)
	data := make([]byte, 38+len(large)+len(small))
	binary.LittleEndian.PutUint16(data[2:], 1)
	binary.LittleEndian.PutUint16(data[4:], 2)
	// Directory order and payload order differ deliberately.
	data[6], data[7] = 16, 16
	binary.LittleEndian.PutUint32(data[14:], uint32(len(small)))
	binary.LittleEndian.PutUint32(data[18:], uint32(38+len(large)))
	data[22], data[23] = 32, 32
	binary.LittleEndian.PutUint32(data[30:], uint32(len(large)))
	binary.LittleEndian.PutUint32(data[34:], 38)
	copy(data[38:], large)
	copy(data[38+len(large):], small)
	out, format, w, h, err := CheckAvatarImage(data, "favicon")
	if err != nil || format != "png" || w != 32 || h != 32 || !bytes.Equal(out, large) {
		t.Fatalf("selected frame: %s %dx%d %v", format, w, h, err)
	}
}
