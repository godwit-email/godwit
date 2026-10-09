package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/egoist/mygo/ui"
)

// The budget of one message's images (global constraints).
const (
	maxImages      = 50       // remote images
	maxImageBytes  = 5 << 20  // one image
	maxImagesBytes = 20 << 20 // all remote images together
	imageWorkers   = 4        // downloads at once
	maxPixels      = 40_000_000
	imageTimeout   = 15 * time.Second
	maxRedirects   = 5
)

var (
	errTooManyImages  = errors.New("too many remote images")
	errImageTooLarge  = errors.New("image too large")
	errImagesTooLarge = errors.New("images too large together")
	errImagePixels    = errors.New("image has too many pixels")
	errImageFormat    = errors.New("not a PNG, JPEG, GIF or WebP image")
	errImageScheme    = errors.New("image address is not http or https")
	errImageHost      = errors.New("image host is not public")
)

// imageFormats are the formats shown, as image.DecodeConfig names them.
var imageFormats = map[string]bool{"png": true, "jpeg": true, "gif": true, "webp": true}

// imageLoader loads one message's images within its budget. Load, cancel
// and imgs belong to the UI thread; downloads post their results to it.
type imageLoader struct {
	ctx           context.Context // ends with cancel
	stop          context.CancelFunc
	post          func(func())
	client        *http.Client
	sem           chan struct{}
	left          atomic.Int64 // bytes the budget has left
	remote        int          // remote images asked for
	closed        bool
	imgs          map[string]*loadedImage
	allowLoopback bool // tests: dial the httptest server
}

type loadedImage struct {
	bm   *ui.Bitmap
	err  error
	done bool
}

// newImageLoader makes a loader; allowLoopback lets tests reach httptest
// servers, which listen on loopback.
func newImageLoader(parent context.Context, post func(func()), allowLoopback bool) *imageLoader {
	ctx, stop := context.WithCancel(parent)
	l := &imageLoader{ctx: ctx, stop: stop, post: post, sem: make(chan struct{}, imageWorkers),
		imgs: map[string]*loadedImage{}, allowLoopback: allowLoopback}
	l.left.Store(maxImagesBytes)
	l.client = l.newClient()
	return l
}

// cancel stops the downloads; what they bring is dropped.
func (l *imageLoader) cancel() {
	l.closed = true
	l.stop()
}

// newClient is a client of its own: no proxy, no cookie jar, no Referer,
// public hosts only (checked on the address dialed, after DNS), http(s)
// redirects only and at most maxRedirects of them.
func (l *imageLoader) newClient() *http.Client {
	d := &net.Dialer{Timeout: imageTimeout, Control: func(_, address string, _ syscall.RawConn) error {
		if l.allowLoopback {
			return nil
		}
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return errImageHost
		}
		ip, err := netip.ParseAddr(host)
		if err != nil || !publicIP(ip.Unmap()) {
			return errImageHost
		}
		return nil
	}}
	tr := &http.Transport{Proxy: nil, DialContext: d.DialContext, DisableKeepAlives: true,
		TLSHandshakeTimeout: imageTimeout, ResponseHeaderTimeout: imageTimeout, MaxResponseHeaderBytes: 64 << 10}
	return &http.Client{Transport: tr, Timeout: imageTimeout, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > maxRedirects {
			return fmt.Errorf("more than %d redirects", maxRedirects)
		}
		if !isHTTP(req.URL) {
			return errImageScheme
		}
		req.Header.Del("Referer")
		return nil
	}}
}

// publicIP is false for loopback, private, link-local, unspecified and
// multicast addresses.
func publicIP(ip netip.Addr) bool {
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified())
}

func isHTTP(u *url.URL) bool { return u.Scheme == "http" || u.Scheme == "https" }

// Load returns src's image once it has loaded; until then, and when it
// fails, nil (the renderer shows a placeholder). The first call starts the
// load: data: URLs decode on a goroutine, http(s) ones download.
func (l *imageLoader) Load(src string) *ui.Bitmap {
	if l == nil || l.closed {
		return nil
	}
	if e, ok := l.imgs[src]; ok {
		return e.bm
	}
	e := &loadedImage{}
	l.imgs[src] = e
	var fetch func() ([]byte, error)
	u, err := url.Parse(src)
	switch {
	case err == nil && strings.EqualFold(u.Scheme, "data"):
		fetch = func() ([]byte, error) { return dataURL(src) }
	case err == nil && isHTTP(u):
		if l.remote >= maxImages {
			e.err, e.done = errTooManyImages, true
			return nil
		}
		l.remote++
		fetch = func() ([]byte, error) { return l.download(src) }
	default:
		e.err, e.done = errImageScheme, true
		return nil
	}
	go func() {
		data, err := fetch()
		var bm *ui.Bitmap
		if err == nil {
			bm, err = decodeImage(data)
		}
		l.post(func() {
			if l.closed {
				bm, err = nil, context.Canceled
			}
			e.bm, e.err, e.done = bm, err, true
		})
	}()
	return nil
}

func (l *imageLoader) download(src string) ([]byte, error) {
	select {
	case l.sem <- struct{}{}:
		defer func() { <-l.sem }()
	case <-l.ctx.Done():
		return nil, l.ctx.Err()
	}
	if l.left.Load() <= 0 {
		return nil, errImagesTooLarge // spent: no request goes out
	}
	req, err := http.NewRequestWithContext(l.ctx, http.MethodGet, src, nil)
	if err != nil {
		return nil, err
	}
	resp, err := l.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("image: %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(budgetReader{resp.Body, &l.left}, maxImageBytes+1))
	switch {
	case err != nil:
		return nil, err
	case len(data) > maxImageBytes:
		return nil, errImageTooLarge
	}
	return data, nil
}

// budgetReader spends left on every byte read and fails once it is gone;
// reads at the same time may overrun it by a buffer each.
type budgetReader struct {
	r    io.Reader
	left *atomic.Int64
}

func (b budgetReader) Read(p []byte) (int, error) {
	left := b.left.Load()
	if left <= 0 {
		return 0, errImagesTooLarge
	}
	if int64(len(p)) > left {
		p = p[:left]
	}
	n, err := b.r.Read(p)
	b.left.Add(-int64(n))
	return n, err
}

// dataURL decodes a data: URL's bytes.
func dataURL(src string) ([]byte, error) {
	meta, payload, ok := strings.Cut(src[len("data:"):], ",")
	if !ok {
		return nil, errImageFormat
	}
	if len(payload) > maxImageBytes/3*4+4 {
		return nil, errImageTooLarge
	}
	var data []byte
	var err error
	if strings.HasSuffix(strings.ToLower(meta), ";base64") {
		data, err = base64.StdEncoding.DecodeString(strings.Join(strings.Fields(payload), ""))
	} else {
		var s string
		s, err = url.PathUnescape(payload)
		data = []byte(s)
	}
	if err != nil {
		return nil, errImageFormat
	}
	if len(data) > maxImageBytes {
		return nil, errImageTooLarge
	}
	return data, nil
}

// decodeImage decodes a PNG, JPEG, GIF or WebP image, known by its content,
// of at most maxPixels; the size is read before the pixels.
func decodeImage(data []byte) (*ui.Bitmap, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || !imageFormats[format] {
		return nil, errImageFormat
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxPixels {
		return nil, errImagePixels
	}
	return ui.DecodeBitmap(data)
}
