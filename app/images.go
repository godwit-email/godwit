package app

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/imagefetch"
)

// The budget of one message's images (global constraints), as
// imagefetch keeps it.
const (
	maxImages      = imagefetch.MaxImages
	maxImagesBytes = imagefetch.MaxTotalBytes
	imageWorkers   = imagefetch.Workers
)

var (
	errTooManyImages  = imagefetch.ErrTooMany
	errImageTooLarge  = imagefetch.ErrTooLarge
	errImagesTooLarge = imagefetch.ErrTotalTooLarge
	errImagePixels    = imagefetch.ErrPixels
	errImagesPixels   = imagefetch.ErrTotalPixels
	errImageFormat    = imagefetch.ErrFormat
	errImageScheme    = imagefetch.ErrScheme
	errImageHost      = imagefetch.ErrHost
)

// imageLoader loads one message's images within its budget. Load, cancel
// and imgs belong to the UI thread; downloads post their results to it.
type imageLoader struct {
	ctx    context.Context // ends with cancel
	stop   context.CancelFunc
	post   func(func())
	client *http.Client
	sem    chan struct{}
	budget *imagefetch.Budget
	closed bool
	imgs   map[string]*loadedImage
	decode func([]byte) (*ui.Bitmap, error) // the pixels, once the size is checked; tests swap it
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
	return &imageLoader{ctx: ctx, stop: stop, post: post, sem: make(chan struct{}, imageWorkers),
		budget: imagefetch.NewBudget(), imgs: map[string]*loadedImage{},
		client: imagefetch.NewClient(allowLoopback), decode: ui.DecodeBitmap}
}

// cancel stops the downloads and decodes; what they bring is dropped.
func (l *imageLoader) cancel() {
	l.closed = true
	l.stop()
}

// Load returns src's image once it has loaded; until then, and when it
// fails, nil (the renderer shows a placeholder). The first call starts the
// load on a goroutine, at most imageWorkers at once: data: URLs decode,
// http(s) ones download first.
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
		fetch = func() ([]byte, error) { return imagefetch.DataURL(src) }
	case err == nil && imagefetch.IsHTTP(u):
		fetch = func() ([]byte, error) { return l.budget.Download(l.ctx, l.client, src) }
	default:
		e.err, e.done = errImageScheme, true
		return nil
	}
	if err := l.budget.Take(); err != nil {
		e.err, e.done = err, true
		return nil
	}
	go func() {
		bm, err := l.load(fetch)
		l.post(func() {
			if l.closed {
				bm, err = nil, context.Canceled
			}
			e.bm, e.err, e.done = bm, err, true
		})
	}()
	return nil
}

// load fetches and decodes an image once a worker is free, unless the
// loader was cancelled by then.
func (l *imageLoader) load(fetch func() ([]byte, error)) (*ui.Bitmap, error) {
	select {
	case l.sem <- struct{}{}:
		defer func() { <-l.sem }()
	case <-l.ctx.Done():
		return nil, l.ctx.Err()
	}
	if err := l.ctx.Err(); err != nil {
		return nil, err
	}
	data, err := fetch()
	if err != nil {
		return nil, err
	}
	return l.decodeImage(data)
}

// decodeImage decodes a PNG, JPEG, GIF or WebP image within the budget's
// pixels; the size is read before the pixels.
func (l *imageLoader) decodeImage(data []byte) (*ui.Bitmap, error) {
	_, px, err := l.budget.Check(data)
	if err != nil {
		return nil, err
	}
	bm, err := l.decode(data)
	if err != nil {
		l.budget.GivePixels(px)
	}
	return bm, err
}
