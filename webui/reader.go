package webui

import (
	"context"
	"errors"
	"log"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/imagecache"
	"github.com/godwit-email/godwit/internal/imagefetch"
)

var (
	// ErrBadHeight is returned for a negative or absurd height.
	ErrBadHeight = errors.New("the height is out of range")
)

// maxBodyHeight bounds a reported height, in CSS pixels.
const maxBodyHeight = 1 << 20

// ReaderBody is a message's body as the reader page shows it.
type ReaderBody struct {
	Message int64 `json:"message"`
	// Version orders the bodies of a page: it applies only a newer one, so
	// a Load that resolves after a ReaderReload is dropped.
	Version int64 `json:"version"`
	// HTML is the sanitized body (remote images kept when Remote), its
	// inline images godwit-img: URLs the image proxy serves as they download,
	// and its structure within the limits; "" when the mail has no HTML, which Plain then holds.
	HTML  string `json:"html"`
	Plain string `json:"plain"`
	// Remote is that the message's remote images may load (the setting, or
	// the user's 加载图片): HTML holds them as godwit-img: URLs, which the
	// image proxy (ImageHandler) serves.
	Remote bool `json:"remote"`
	// Blocked is the remote images left out while not Remote.
	Blocked int `json:"blocked"`
	// Cut is that the body is only its start: the Service kept only that,
	// or the limits cut it.
	Cut    bool   `json:"cut"`
	Theme  Theme  `json:"theme"`
	Locale string `json:"locale"`
	// CollapseQuotes is Settings.CollapseQuotes: the frame folds quoted text.
	CollapseQuotes bool `json:"collapseQuotes"`
}

// ReaderReload tells a reader page its body changed (加载图片, a retry).
var ReaderReload = mygo.NewEvent[ReaderBody]("readerBody")

// ReaderGlass is how much of a reader page, in CSS pixels, the reader's
// bars cover: Top from its top, Bottom from its bottom. The page blurs there
// itself, as the bars' blur cannot see a web view.
type ReaderGlass struct {
	Top    int `json:"top"`
	Bottom int `json:"bottom"`
}

// ReaderGlassed tells a reader page what the bars cover, as it scrolls.
var ReaderGlassed = mygo.NewEvent[ReaderGlass]("readerGlass")

// ReaderDeps is what Reader needs from the platform. The callbacks post to
// the UI thread themselves. Each names the page (its key) it is about, bound
// as the call was made: by the time the post runs the page may be closed or
// rebound, which the UI thread checks.
type ReaderDeps struct {
	// Emit sends a body to the page with key page (ReaderReload.EmitPage,
	// which only queues it). It runs with the Reader's lock held, so the
	// page is bound to b's message as it is sent: it must not block or call
	// back into the Reader.
	Emit func(page any, b ReaderBody)
	// OnHeight is the height of message's body in page, in CSS pixels.
	OnHeight func(page any, message int64, px int)
	// OnLink opens a link of page's body (the native confirmation; mailto
	// composes).
	OnLink func(page any, url string)
	// OnNavigate goes back (-1) or forward (1), as ⌘[ and ⌘] do natively.
	OnNavigate func(page any, dir int)
	// OnSearch opens the mail list's search, as ⌘F does natively.
	OnSearch func(page any)
	// OnSave saves the current message, as ⌘S does natively.
	OnSave func(page any)
	// OnPrint prints the current message, as ⌘P does natively.
	OnPrint func(page any)
	// OnBuilt is told, for each page bound to b.Message, each body built
	// as the message's current version: native shows the truncated notice
	// by b.Cut and hides the remote-image notice once b.Remote. It runs
	// with the Reader's lock held, as Emit does (so a page is told the
	// bodies in their versions' order): it must not block or call back.
	OnBuilt func(page any, b ReaderBody)
	// OnFailed is told, for each page bound to message, that the body
	// Allow read failed: native offers 加载图片 again. Under the lock too.
	OnFailed func(page any, message int64, err error)
	// OnImagesFailed is told, for each page bound to message, how many of
	// its images the proxy could not get: native offers a retry
	// (Allow). Under the lock too.
	OnImagesFailed func(page any, message int64, n int)
}

// Reader keeps the reader pages (role "reader"), each bound to a message:
// its body, version and images. It is not bound: the pages reach it only
// through ReaderPage; native calls Bind, Unbind, Allow and SettingsChanged.
type Reader struct {
	pages *Pages
	d     ReaderDeps

	// Test seams.
	getMessage func(ctx context.Context, svc *core.Service, id int64, remote bool) (core.Message, error)
	getAvatar  func(ctx context.Context, svc *core.Service, id int64) (core.Avatar, error)
	getInline  func(ctx context.Context, svc *core.Service, id int64, each func(cid string, a mail.Attachment)) error

	mu       sync.Mutex
	svc      *core.Service
	version  int64             // the last version given out
	settings *core.Settings    // latest in-memory SettingsChanged snapshot (ahead of async DB saves)
	bound    map[any]int64     // page key → its message
	msgs     map[int64]*bodyOf // per message with a page bound
}

// bodyOf is a bound message's state.
type bodyOf struct {
	pages      int // bound to it
	allowed    bool
	generation int64
	body       *ReaderBody       // the current displayed body, once built
	original   *ReaderBody       // safe, prepared original for switching without rereading mail
	tokens     map[string]string // its image tokens (godwit-img: paths) → remote URLs
	// The pixels the images served for it take, from the message-wide budget:
	// the page decodes every image at once. charged is the tokens counted.
	pixels       *imagefetch.Budget
	charged      map[string]bool
	failed       map[string]bool   // the tokens the proxy could not get since the last Allow
	translations map[string]string // display only, applied to the original body's text
	// The inline images, one download for all of them (a login per image is what
	// makes a first open slow), begun by the page's first request for one.
	inline     map[string]*inlineImage // by cid:<id>
	stopInline context.CancelFunc
}

// inlineImage is an inline image downloading: ready closes when data is what
// there will be, nil if it could not be had.
type inlineImage struct {
	ready chan struct{}
	done  bool // ready is closed
	data  []byte
}

func (i *inlineImage) finish(data []byte) {
	if !i.done {
		i.done, i.data = true, data
		close(i.ready)
	}
}

// NewReader keeps the reader pages of pages; SetService opens it up.
func NewReader(pages *Pages, d ReaderDeps) *Reader {
	return &Reader{pages: pages, d: d, bound: map[any]int64{}, msgs: map[int64]*bodyOf{},
		getMessage: func(ctx context.Context, svc *core.Service, id int64, remote bool) (core.Message, error) {
			return svc.GetMessageLazyImages(ctx, id, remote)
		},
		getInline: func(ctx context.Context, svc *core.Service, id int64, each func(cid string, a mail.Attachment)) error {
			return svc.InlineImages(ctx, id, each)
		},
		getAvatar: func(ctx context.Context, svc *core.Service, id int64) (core.Avatar, error) {
			return svc.SenderAvatar(ctx, id, false)
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
		st = &bodyOf{generation: r.version}
		r.msgs[message] = st
	}
	st.pages++
}

// Bound is the message the page with key shows, while it is bound.
func (r *Reader) Bound(key any) (message int64, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	message, ok = r.bound[key]
	return message, ok
}

// Unbind forgets the page with key; the last page of a message takes its
// state along (allowed images, tokens).
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
		if st.stopInline != nil {
			st.stopInline()
		}
		delete(r.msgs, m)
	}
}

// Allow lets message's remote images load (加载图片), or loads its body
// again (a retry): its pages get the new body, with Remote, by Emit. A
// message no page shows is left alone. It returns at once: the body is
// read on a goroutine.
func (r *Reader) Allow(message int64) {
	r.reload(message, func(st *bodyOf) {
		for token := range st.failed { // a retry: try them again now
			r.svc.ImageCache().ForgetFailure(st.tokens[token])
		}
		st.allowed, st.failed = true, nil
		// Inline images missed are tried again; those had come from the attachment cache.
		if st.stopInline != nil {
			st.stopInline()
		}
		st.inline, st.stopInline = nil, nil
	})
}

// Translate displays translated text in the original body; nil restores the original.
func (r *Reader) Translate(message int64, texts map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.msgs[message]
	if st == nil {
		return
	}
	st.translations = maps.Clone(texts)
	if st.original == nil || st.body == nil {
		return // the pending build will apply the latest translation choice
	}
	r.emitTranslation(message, st)
}

// SettingsChanged updates cached reader bodies for st and re-emits bound pages
// when CollapseQuotes changes so open messages fold or unfold immediately.
func (r *Reader) SettingsChanged(st core.Settings) {
	theme := ThemeOf(st)
	locale := i18n.Language()
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := st
	r.settings = &cp
	for message, s := range r.msgs {
		// The original too while a reload is in flight: a failed reload re-emits it.
		if s.original != nil {
			s.original.Theme, s.original.Locale, s.original.CollapseQuotes = theme, locale, st.CollapseQuotes
		}
		if s.body == nil {
			continue
		}
		s.body.Theme, s.body.Locale = theme, locale
		if s.body.CollapseQuotes == st.CollapseQuotes {
			continue
		}
		r.version++
		b := *s.body
		b.Version = r.version
		b.CollapseQuotes = st.CollapseQuotes
		s.body = &b
		for k, m := range r.bound {
			if m == message {
				r.d.Emit(k, b)
			}
		}
	}
}

// emitTranslation applies the current choice to the cached original, with r.mu held.
func (r *Reader) emitTranslation(message int64, st *bodyOf) {
	b := translateBody(*st.original, st.translations)
	r.version++
	b.Version = r.version
	st.body = &b
	for k, m := range r.bound {
		if m == message {
			r.d.OnBuilt(k, b)
			r.d.Emit(k, b)
		}
	}
}

func translateBody(b ReaderBody, texts map[string]string) ReaderBody {
	if len(texts) == 0 {
		return b
	}
	if b.HTML != "" {
		b.HTML = mail.MapHTMLText(b.HTML, func(text string) string {
			if translated, ok := texts[text]; ok {
				return translated
			}
			return text
		})
	} else if text, ok := texts[b.Plain]; ok {
		b.Plain = text
	}
	return b
}

func (r *Reader) reload(message int64, change func(*bodyOf)) {
	r.mu.Lock()
	st := r.msgs[message]
	svc := r.svc
	if st == nil || svc == nil {
		r.mu.Unlock()
		return
	}
	change(st)
	allowed := st.allowed
	r.version++
	v := r.version
	st.generation, st.body = v, nil
	r.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		b, err := r.build(ctx, svc, message, v, allowed)
		if err != nil {
			log.Printf("reader: message %d: %v", message, err)
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.msgs[message] == st && st.generation == v {
				if st.original != nil {
					r.emitTranslation(message, st)
				}
				for k, m := range r.bound {
					if m == message {
						r.d.OnFailed(k, message, err)
					}
				}
			}
			return
		}
		// Sent under the lock: a page rebound meanwhile, or a newer
		// version, gets nothing of this body.
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.msgs[message] != st || st.generation != v {
			return
		}
		for k, m := range r.bound {
			if m == message {
				r.d.Emit(k, b)
			}
		}
	}()
}

// build keeps the body only while its build generation v is current.
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
		Theme: ThemeOf(st), Locale: i18n.Language(), CollapseQuotes: st.CollapseQuotes}
	var tokens map[string]string
	budget := imagefetch.NewBudget() // the message's images, inline and remote
	if m.HTML != "" {
		b.HTML, tokens = prepareBody(m.HTML, budget, message, svc.ImageCache().Dims)
	} else {
		b.Plain = m.Text
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.settings != nil {
		b.Theme = ThemeOf(*r.settings)
		b.Locale = i18n.Language()
		b.CollapseQuotes = r.settings.CollapseQuotes
	}
	if s := r.msgs[message]; s != nil && s.generation == v {
		if s.body == nil {
			r.version++
			b.Version = r.version
			original := b
			s.original = &original
			b = translateBody(b, s.translations)
			s.body, s.tokens = &b, tokens
			s.pixels, s.charged = budget, map[string]bool{}
		} else {
			b = *s.body
		}
		for k, m := range r.bound {
			if m == message {
				r.d.OnBuilt(k, b)
			}
		}
	}
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
	_, message, svc, err = r.callerKey(ctx)
	return message, svc, err
}

// callerKey is caller with the page's key.
func (r *Reader) callerKey(ctx context.Context) (key any, message int64, svc *core.Service, err error) {
	id, err := r.pages.Caller(ctx, "reader")
	if err != nil {
		return nil, 0, nil, err
	}
	key, ok := r.pages.Key(id)
	if !ok {
		return nil, 0, nil, ErrWrongPage
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	message, ok = r.bound[key]
	if !ok {
		return nil, 0, nil, ErrWrongPage
	}
	if r.svc == nil {
		return nil, 0, nil, ErrNotReady
	}
	return key, message, r.svc, nil
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
	v, allowed := st.generation, st.allowed
	p.r.mu.Unlock()
	if !allowed { // the setting may let them load at once (core decides)
		ok, err := svc.ImagesAllowed(ctx, message)
		if err != nil {
			return ReaderBody{}, err
		}
		if ok {
			p.r.mu.Lock()
			if p.r.msgs[message] == st && st.generation == v {
				st.allowed = true
			}
			allowed = st.allowed
			p.r.mu.Unlock()
		}
	}
	return p.r.build(ctx, svc, message, v, allowed)
}

// Height is the body's height in CSS pixels, for the native layout.
func (p *ReaderPage) Height(ctx context.Context, px int) error {
	key, message, _, err := p.r.callerKey(ctx)
	if err != nil {
		return err
	}
	if px < 0 || px > maxBodyHeight {
		return ErrBadHeight
	}
	p.r.d.OnHeight(key, message, px)
	return nil
}

// OpenLink opens a link of the body (its data-href) natively: an http or
// https one after the confirmation, a mailto one in a compose window. Any
// other is ErrBadLink.
func (p *ReaderPage) OpenLink(ctx context.Context, link string) error {
	key, _, _, err := p.r.callerKey(ctx)
	if err != nil {
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
	p.r.d.OnLink(key, link)
	return nil
}

// Navigate goes back (-1) or forward (1), as ⌘[ and ⌘] do natively.
func (p *ReaderPage) Navigate(ctx context.Context, dir int) error {
	key, _, _, err := p.r.callerKey(ctx)
	if err != nil {
		return err
	}
	if dir != -1 && dir != 1 {
		return ErrBadDirection
	}
	p.r.d.OnNavigate(key, dir)
	return nil
}

// Search opens the mail list's search, as ⌘F does natively: a page that
// has the keyboard misses the native shortcut.
func (p *ReaderPage) Search(ctx context.Context) error {
	key, _, _, err := p.r.callerKey(ctx)
	if err != nil {
		return err
	}
	p.r.d.OnSearch(key)
	return nil
}

// Save saves the message, as ⌘S does natively: a page that has the
// keyboard misses the native shortcut.
func (p *ReaderPage) Save(ctx context.Context) error {
	key, _, _, err := p.r.callerKey(ctx)
	if err != nil {
		return err
	}
	if p.r.d.OnSave != nil {
		p.r.d.OnSave(key)
	}
	return nil
}

// Print prints the message, as ⌘P does natively: a page that has the
// keyboard misses the native shortcut.
func (p *ReaderPage) Print(ctx context.Context) error {
	key, _, _, err := p.r.callerKey(ctx)
	if err != nil {
		return err
	}
	if p.r.d.OnPrint != nil {
		p.r.d.OnPrint(key)
	}
	return nil
}

// ImageScheme is the URL scheme of a message's remote images in its body,
// served by ImageHandler: godwit-img://m<message>/<token>; and of its sender's
// avatar: godwit-img://a<message>.
const ImageScheme = "godwit-img"

// ImageHandler is the image proxy the pages load images from. It serves the
// image of a token of a message a page shows: an inline image, from the mail
// server; a remote one if its images may load (Remote), from the cache: downloaded
// once through core's client (public hosts only, no cookies, no Referer), kept on
// disk, shared by messages.
func (r *Reader) ImageHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if message, ok := parseAvatarURL(req.URL); ok {
			r.serveAvatar(w, req, message)
			return
		}
		message, token, ok := parseImageURL(req.URL)
		if !ok {
			http.NotFound(w, req)
			return
		}
		r.mu.Lock()
		var src string
		var inline *inlineImage
		st := r.msgs[message]
		svc := r.svc
		if st != nil {
			src = st.tokens[token]
			if strings.HasPrefix(src, cidPrefix) {
				if svc != nil {
					inline = r.inlineImage(st, message, svc, src)
				}
			} else if !st.allowed {
				src = ""
			}
		}
		r.mu.Unlock()
		if inline != nil {
			r.serveInline(w, req, message, token, inline)
			return
		}
		if src == "" || svc == nil {
			http.NotFound(w, req)
			return
		}
		img, err := svc.ImageCache().Get(req.Context(), src)
		if err != nil {
			if req.Context().Err() == nil { // not the page going away
				log.Printf("image proxy: message %d: %v", message, err)
				r.imageFailed(message, token)
			}
			http.Error(w, "image unavailable", http.StatusBadGateway)
			return
		}
		if !r.chargePixels(message, token, img) {
			http.Error(w, "the message's images have too many pixels", http.StatusForbidden)
			return
		}
		writeImage(w, img.Format, img.Data)
	})
}

// inlineImage is st's inline image src, the download of them all begun at the
// first asked for. It runs with r.mu held.
func (r *Reader) inlineImage(st *bodyOf, message int64, svc *core.Service, src string) *inlineImage {
	if st.inline == nil {
		st.inline = map[string]*inlineImage{}
		for _, s := range st.tokens {
			if strings.HasPrefix(s, cidPrefix) {
				st.inline[s] = &inlineImage{ready: make(chan struct{})}
			}
		}
		// Not the request's: the others wait on it too. Bounded, as it holds the account lock.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		st.stopInline = cancel
		images := st.inline
		go func() {
			defer cancel()
			err := r.getInline(ctx, svc, message, func(cid string, a mail.Attachment) {
				r.mu.Lock()
				defer r.mu.Unlock()
				if i := images[cidPrefix+cid]; i != nil {
					i.finish(a.Data)
				}
			})
			if err != nil && ctx.Err() == nil {
				log.Printf("inline images: message %d: %v", message, err)
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			// Told, so native offers a retry; not when a closed page or a retry stopped it.
			stopped := errors.Is(ctx.Err(), context.Canceled)
			for src, i := range images {
				if !i.done && !stopped {
					r.imageFailedLocked(message, imagecache.Key(src))
				}
				i.finish(nil)
			}
		}()
	}
	return st.inline[src]
}

// serveInline answers with an inline image once it has downloaded. It must be an
// image the page may show, within what the message has left: a data: image of the
// body would have had to be as well (preparer.inlineImage).
func (r *Reader) serveInline(w http.ResponseWriter, req *http.Request, message int64, token string, i *inlineImage) {
	select {
	case <-i.ready:
	case <-req.Context().Done():
		return
	}
	if len(i.data) == 0 || len(i.data) > imagefetch.MaxImageBytes {
		http.NotFound(w, req)
		return
	}
	format, px, err := imagefetch.Config(i.data)
	if err != nil {
		http.NotFound(w, req)
		return
	}
	if !r.chargeInline(message, token, int64(len(i.data)), px) {
		http.Error(w, "the message's images are over its budget", http.StatusForbidden)
		return
	}
	writeImage(w, format, i.data)
}

// chargeInline takes an inline image's bytes and pixels from message's budget
// (prepareBody counted it), once per token; false when the message is gone or has
// too little left.
func (r *Reader) chargeInline(message int64, token string, size, px int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.msgs[message]
	if st == nil || st.pixels == nil {
		return false
	}
	if st.charged[token] {
		return true
	}
	if st.pixels.TakeSize(size, px) != nil {
		return false
	}
	st.charged[token] = true
	return true
}

// serveAvatar serves message's sender avatar under the message's own
// remote-image policy (core SenderAvatar), 404 when there is none.
func (r *Reader) serveAvatar(w http.ResponseWriter, req *http.Request, message int64) {
	r.mu.Lock()
	svc := r.svc
	r.mu.Unlock()
	if svc == nil {
		http.NotFound(w, req)
		return
	}
	av, err := r.getAvatar(req.Context(), svc, message)
	if err != nil && req.Context().Err() == nil {
		log.Printf("avatar: message %d: %v", message, err)
	}
	if err != nil || len(av.Data) == 0 {
		http.NotFound(w, req)
		return
	}
	writeImage(w, av.Format, av.Data)
}

// writeImage answers with an image of format (imagecache's: png, jpeg, gif,
// webp, svg).
func writeImage(w http.ResponseWriter, format string, data []byte) {
	if format == "svg" {
		format = "svg+xml"
	}
	h := w.Header()
	h.Set("Content-Type", "image/"+format)
	h.Set("Content-Length", strconv.Itoa(len(data)))
	h.Set("Cache-Control", "no-store") // every load comes through the checks above; the disk cache is the cache
	h.Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

// imageFailed counts token as one of message's images the proxy could not
// get, and tells its pages.
func (r *Reader) imageFailed(message int64, token string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.imageFailedLocked(message, token)
}

func (r *Reader) imageFailedLocked(message int64, token string) {
	st := r.msgs[message]
	if st == nil || st.failed[token] {
		return
	}
	if st.failed == nil {
		st.failed = map[string]bool{}
	}
	st.failed[token] = true
	for k, m := range r.bound {
		if m == message && r.d.OnImagesFailed != nil {
			r.d.OnImagesFailed(k, message, len(st.failed))
		}
	}
}

// chargePixels takes img's pixels from message's budget, once per token; false
// when the message is gone or has none left.
func (r *Reader) chargePixels(message int64, token string, img imagecache.Image) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.msgs[message]
	if st == nil || st.pixels == nil {
		return false
	}
	if st.charged[token] {
		return true
	}
	if st.pixels.TakePixels(int64(img.Width)*int64(img.Height)) != nil {
		return false
	}
	st.charged[token] = true
	return true
}

// parseAvatarURL is the message of a godwit-img: avatar URL.
func parseAvatarURL(u *url.URL) (message int64, ok bool) {
	h, found := strings.CutPrefix(u.Host, "a")
	message, err := strconv.ParseInt(h, 10, 64)
	return message, found && err == nil && (u.Path == "" || u.Path == "/")
}

// parseImageURL is the message and token of a godwit-img: URL.
func parseImageURL(u *url.URL) (message int64, token string, ok bool) {
	h, found := strings.CutPrefix(u.Host, "m")
	message, err := strconv.ParseInt(h, 10, 64)
	token = strings.TrimPrefix(u.Path, "/")
	return message, token, found && err == nil && token != "" && !strings.Contains(token, "/")
}
