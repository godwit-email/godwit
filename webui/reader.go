package webui

import (
	"context"
	"errors"
	"log"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/imagefetch"
)

var (
	// ErrImageRefused is returned for an image of a message whose remote
	// images the user has not allowed (加载图片), or one not in its body.
	ErrImageRefused = errors.New("that image may not be loaded")
	// ErrImageDeadline is returned once a message's images ran out of time.
	ErrImageDeadline = errors.New("the message's images took too long")
	// ErrBadHeight is returned for a negative or absurd height.
	ErrBadHeight = errors.New("the height is out of range")
	// ErrBadWheel is returned for a wheel delta that is not a number.
	ErrBadWheel = errors.New("the wheel delta is not a number")
)

// imageDeadline is how long a message's remote images may take, all of
// them, from 加载图片; each request also has imagefetch.Timeout.
const imageDeadline = 60 * time.Second

// maxBodyHeight bounds a reported height, in CSS pixels.
const maxBodyHeight = 1 << 20

// ReaderBody is a message's body as the reader page shows it.
type ReaderBody struct {
	Message int64 `json:"message"`
	// Version orders the bodies of a page: it applies only a newer one, so
	// a Load that resolves after a ReaderReload is dropped.
	Version int64 `json:"version"`
	// HTML is the sanitized body (remote images kept when Remote), its
	// inline images within the message's budget and its structure within
	// the limits; "" when the mail has no HTML, which Plain then holds.
	HTML  string `json:"html"`
	Plain string `json:"plain"`
	// Remote is that the user allowed the remote images (加载图片): the page
	// gets each http(s) src of HTML through Image.
	Remote bool `json:"remote"`
	// Blocked is the remote images left out while not Remote.
	Blocked int `json:"blocked"`
	// Cut is that the body is only its start: the Service kept only that,
	// or the limits cut it.
	Cut   bool              `json:"cut"`
	Theme Theme             `json:"theme"`
	Text  map[string]string `json:"text"`
}

// ReaderReload tells a reader page its body changed (加载图片, a retry).
var ReaderReload = mygo.NewEvent[ReaderBody]("readerBody")

// ReaderDeps is what Reader needs from the platform. The callbacks come on
// the page's call: they post to the UI thread themselves.
type ReaderDeps struct {
	// Emit sends a body to the page with key page (ReaderReload.EmitPage,
	// which only queues it). It runs with the Reader's lock held, so the
	// page is bound to b's message as it is sent: it must not block or call
	// back into the Reader.
	Emit func(page any, b ReaderBody)
	// OnHeight is the height of message's body, in CSS pixels.
	OnHeight func(message int64, px int)
	// OnWheel is a wheel delta the body did not use: the reader scrolls.
	OnWheel func(dy float64)
	// OnLink opens a link of a body (the native confirmation; mailto
	// composes).
	OnLink func(url string)
	// OnNavigate goes back (-1) or forward (1), as ⌘[ and ⌘] do natively.
	OnNavigate func(dir int)
}

// Reader keeps the reader pages (role "reader"), each bound to a message:
// its body, version and images. It is not bound: the pages reach it only
// through ReaderPage; native calls Bind, Unbind and Allow.
type Reader struct {
	pages *Pages
	d     ReaderDeps

	// Test seams.
	allowLoopback bool
	imageDeadline time.Duration
	getMessage    func(ctx context.Context, svc *core.Service, id int64, remote bool) (core.Message, error)

	mu      sync.Mutex
	svc     *core.Service
	version int64             // the last version given out
	bound   map[any]int64     // page key → its message
	msgs    map[int64]*bodyOf // per message with a page bound
}

// bodyOf is a bound message's state.
type bodyOf struct {
	pages   int // bound to it
	allowed bool
	version int64
	body    *ReaderBody     // the body of version, once built
	remote  map[string]bool // its http(s) image sources
	budget  *imagefetch.Budget

	// The remote images, from Allow: imgs share ctx, which ends at the
	// deadline, the next Allow or the last Unbind, and at most
	// imagefetch.Workers fetch at once.
	ctx    context.Context
	stop   context.CancelFunc
	client *http.Client
	sem    chan struct{}
	imgs   map[string]*fetched
}

// fetched is one remote image: url or err once done is closed.
type fetched struct {
	done chan struct{}
	url  string
	err  error
}

// NewReader keeps the reader pages of pages; SetService opens it up.
func NewReader(pages *Pages, d ReaderDeps) *Reader {
	return &Reader{pages: pages, d: d, imageDeadline: imageDeadline, bound: map[any]int64{}, msgs: map[int64]*bodyOf{},
		getMessage: func(ctx context.Context, svc *core.Service, id int64, remote bool) (core.Message, error) {
			return svc.GetMessage(ctx, id, remote)
		}}
}

// SetService gives the pages the service, once the data is open.
func (r *Reader) SetService(svc *core.Service) {
	r.mu.Lock()
	r.svc = svc
	r.mu.Unlock()
}

// Bind makes the page with key (registered as "reader") show message; a
// page shows one message at a time.
func (r *Reader) Bind(key any, message int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.unbind(key)
	r.bound[key] = message
	st := r.msgs[message]
	if st == nil {
		r.version++
		st = &bodyOf{version: r.version}
		r.msgs[message] = st
	}
	st.pages++
}

// Unbind forgets the page with key; the last page of a message takes its
// state along (allowed images, fetches in flight).
func (r *Reader) Unbind(key any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.unbind(key)
}

func (r *Reader) unbind(key any) {
	m, ok := r.bound[key]
	if !ok {
		return
	}
	delete(r.bound, key)
	st := r.msgs[m]
	if st.pages--; st.pages == 0 {
		if st.stop != nil {
			st.stop()
		}
		delete(r.msgs, m)
	}
}

// Allow lets message's remote images load (加载图片), or loads its body
// again (a retry): its pages get the new body, with Remote, by Emit. A
// message no page shows is left alone. It returns at once: the body is
// read on a goroutine.
func (r *Reader) Allow(message int64) {
	r.mu.Lock()
	st := r.msgs[message]
	svc := r.svc
	if st == nil || svc == nil {
		r.mu.Unlock()
		return
	}
	r.version++
	v := r.version
	if st.stop != nil {
		st.stop()
	}
	st.allowed, st.version, st.body, st.remote, st.budget = true, v, nil, nil, nil
	st.ctx, st.stop = context.WithTimeout(context.Background(), r.imageDeadline)
	st.client, st.sem, st.imgs = imagefetch.NewClient(r.allowLoopback), make(chan struct{}, imagefetch.Workers), map[string]*fetched{}
	r.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		b, err := r.build(ctx, svc, message, v, true)
		if err != nil {
			log.Printf("reader: message %d: %v", message, err)
			return
		}
		// Sent under the lock: a page rebound meanwhile, or a newer
		// version, gets nothing of this body.
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.msgs[message] != st || st.version != v {
			return
		}
		for k, m := range r.bound {
			if m == message {
				r.d.Emit(k, b)
			}
		}
	}()
}

// build reads message's body for version v and keeps it, if v is still
// the message's version.
func (r *Reader) build(ctx context.Context, svc *core.Service, message, v int64, allowed bool) (ReaderBody, error) {
	m, err := r.getMessage(ctx, svc, message, allowed)
	if err != nil {
		return ReaderBody{}, err
	}
	st, err := svc.GetSettings(ctx)
	if err != nil {
		return ReaderBody{}, err
	}
	b := ReaderBody{Message: message, Version: v, Remote: allowed, Blocked: m.RemoteImagesBlocked, Cut: m.Truncated,
		Theme: ThemeOf(st), Text: readerText()}
	budget := imagefetch.NewBudget()
	remote := map[string]bool{}
	if m.HTML != "" {
		var cut bool
		b.HTML, remote, cut = prepareBody(m.HTML, budget)
		b.Cut = b.Cut || cut
	} else {
		b.Plain = m.Text
	}
	r.mu.Lock()
	if s := r.msgs[message]; s != nil && s.version == v && s.body == nil {
		s.body, s.remote, s.budget = &b, remote, budget
	}
	r.mu.Unlock()
	return b, nil
}

// ReaderPage is what a reader page (role "reader", one per expanded
// message) calls (mygo.Bind). Each acts on the caller's own message.
type ReaderPage struct{ r *Reader }

// NewReaderPage is the bound facade of r.
func NewReaderPage(r *Reader) *ReaderPage { return &ReaderPage{r: r} }

// caller is the calling page's message, if it is a reader page bound to
// one, and the service.
func (r *Reader) caller(ctx context.Context) (message int64, svc *core.Service, err error) {
	id, err := r.pages.Caller(ctx, "reader")
	if err != nil {
		return 0, nil, err
	}
	key, ok := r.pages.Key(id)
	if !ok {
		return 0, nil, ErrWrongPage
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	message, ok = r.bound[key]
	if !ok {
		return 0, nil, ErrWrongPage
	}
	if r.svc == nil {
		return 0, nil, ErrNotReady
	}
	return message, r.svc, nil
}

// Load is the caller's message's body, as of its version.
func (p *ReaderPage) Load(ctx context.Context) (ReaderBody, error) {
	message, svc, err := p.r.caller(ctx)
	if err != nil {
		return ReaderBody{}, err
	}
	p.r.mu.Lock()
	st := p.r.msgs[message]
	if st == nil { // unbound since
		p.r.mu.Unlock()
		return ReaderBody{}, ErrWrongPage
	}
	if st.body != nil {
		b := *st.body
		p.r.mu.Unlock()
		return b, nil
	}
	v, allowed := st.version, st.allowed
	p.r.mu.Unlock()
	return p.r.build(ctx, svc, message, v, allowed)
}

// Height is the body's height in CSS pixels, for the native layout.
func (p *ReaderPage) Height(ctx context.Context, px int) error {
	message, _, err := p.r.caller(ctx)
	if err != nil {
		return err
	}
	if px < 0 || px > maxBodyHeight {
		return ErrBadHeight
	}
	p.r.d.OnHeight(message, px)
	return nil
}

// Wheel is a vertical wheel delta the body did not use: the reader
// scrolls by it.
func (p *ReaderPage) Wheel(ctx context.Context, dy float64) error {
	if _, _, err := p.r.caller(ctx); err != nil {
		return err
	}
	if math.IsNaN(dy) || math.IsInf(dy, 0) {
		return ErrBadWheel
	}
	p.r.d.OnWheel(dy)
	return nil
}

// OpenLink opens a link of the body (its data-href) natively: an http or
// https one after the confirmation, a mailto one in a compose window. Any
// other is ErrBadLink.
func (p *ReaderPage) OpenLink(ctx context.Context, link string) error {
	if _, _, err := p.r.caller(ctx); err != nil {
		return err
	}
	u, err := url.Parse(link)
	if err != nil {
		return ErrBadLink
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "mailto":
	default:
		return ErrBadLink
	}
	p.r.d.OnLink(link)
	return nil
}

// Navigate goes back (-1) or forward (1), as ⌘[ and ⌘] do natively.
func (p *ReaderPage) Navigate(ctx context.Context, dir int) error {
	if _, _, err := p.r.caller(ctx); err != nil {
		return err
	}
	if dir != -1 && dir != 1 {
		return ErrBadDirection
	}
	p.r.d.OnNavigate(dir)
	return nil
}

// Image is the remote image at url as a data: URL. It is refused
// (ErrImageRefused) unless the user allowed the caller's message's images
// and url is an image of its body as last built. The message's images
// share its budget (with its inline ones), at most imagefetch.Workers at
// once, and its deadline; each url is fetched once.
func (p *ReaderPage) Image(ctx context.Context, src string) (string, error) {
	message, _, err := p.r.caller(ctx)
	if err != nil {
		return "", err
	}
	p.r.mu.Lock()
	st := p.r.msgs[message]
	if st == nil { // unbound since
		p.r.mu.Unlock()
		return "", ErrWrongPage
	}
	if !st.allowed || st.body == nil || !st.body.Remote || !st.remote[src] {
		p.r.mu.Unlock()
		return "", ErrImageRefused
	}
	f := st.imgs[src]
	if f == nil {
		if st.ctx.Err() != nil {
			p.r.mu.Unlock()
			return "", ErrImageDeadline
		}
		f = &fetched{done: make(chan struct{})}
		st.imgs[src] = f
		go f.fetch(st.ctx, st.client, st.sem, st.budget, src)
	}
	p.r.mu.Unlock()
	select {
	case <-f.done:
		return f.url, f.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// fetch downloads and checks src once a worker is free, within budget.
func (f *fetched) fetch(ctx context.Context, c *http.Client, sem chan struct{}, budget *imagefetch.Budget, src string) {
	defer close(f.done)
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-ctx.Done():
		f.err = ErrImageDeadline
		return
	}
	if f.err = budget.Take(); f.err != nil {
		return
	}
	data, err := budget.Download(ctx, c, src)
	if err != nil {
		if ctx.Err() != nil {
			err = errors.Join(ErrImageDeadline, err)
		}
		f.err = err
		return
	}
	format, _, err := budget.Check(data)
	if err != nil {
		f.err = err
		return
	}
	f.url = imagefetch.Encode(format, data)
}
