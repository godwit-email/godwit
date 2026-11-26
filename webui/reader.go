package webui

import (
	"context"
	"errors"
	"log"
	"maps"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/imagefetch"
)

var (
	// ErrBadHeight is returned for a negative or absurd height.
	ErrBadHeight = errors.New("the height is out of range")
)

// maxBodyHeight bounds a reported height, in CSS pixels.
const maxBodyHeight = 1 << 20

// ReaderBinding identifies one assignment of a reader shell. Releasing it
// sends the same ID inactive; the next assignment always has a higher ID.
type ReaderBinding struct {
	ID     int  `json:"id"`
	Active bool `json:"active"`
}

// ReaderBound tells a reader shell to load its new assignment or clear it.
var ReaderBound = mygo.NewEvent[ReaderBinding]("readerBinding")

// ReaderBody is a message's body as the reader page shows it.
type ReaderBody struct {
	Binding int   `json:"binding"`
	Message int64 `json:"message"`
	// Version orders the bodies of a page: it applies only a newer one, so
	// a Load that resolves after a ReaderReload is dropped.
	Version int64 `json:"version"`
	// HTML is the sanitized body (remote images kept when Remote), its
	// inline images godwit-img: URLs the image proxy serves as they download,
	// and its structure within the limits; "" when the mail has no HTML, which Plain then holds.
	HTML  string `json:"html"`
	Plain string `json:"plain"`
	// Remote is whether the body was built with remote images. ImageHandler
	// checks permission again when the page requests their godwit-img: URLs.
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

// ReaderGlassRect is a frosted surface's visible overlap with a reader page, in
// CSS pixels, rounded by R.
type ReaderGlassRect struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
	R int `json:"r"`
}

// ReaderGlass is what the reader's frosted surfaces cover of a reader page, in
// CSS pixels: Top for the collapsing sticky header, and Bottom for the floating
// reply card. Used as the fallback when system native glass is unavailable.
type ReaderGlass struct {
	Binding int             `json:"binding"`
	Top     ReaderGlassRect `json:"top"`
	Bottom  ReaderGlassRect `json:"bottom"`
}

// ReaderGlassed tells a reader page what the bars cover, as it scrolls.
var ReaderGlassed = mygo.NewEvent[ReaderGlass]("readerGlass")

// ReaderDeps is what Reader needs from the platform. The callbacks post to
// the UI thread themselves. Each names a registration, not a physical page:
// the UI thread drops the post if that registration has been released.
type ReaderDeps struct {
	// Emit sends a body to the registered page (ReaderReload.EmitPage,
	// which only queues it). It runs with the Reader's lock held, so the
	// page is bound to b's message as it is sent: it must not block or call
	// back into the Reader.
	Emit func(id int, b ReaderBody)
	// OnHeight is the height of message's body in page, in CSS pixels.
	OnHeight func(id int, message int64, px int)
	// OnPainted acknowledges the first drawable content of the assignment.
	OnPainted func(id int, message int64)
	// OnLink opens a link of page's body (the native confirmation; mailto
	// composes).
	OnLink func(id int, url string)
	// OnNavigate goes back (-1) or forward (1), as ⌘[ and ⌘] do natively.
	OnNavigate func(id int, dir int)
	// OnSearch opens the mail list's search, as ⌘F does natively.
	OnSearch func(id int)
	// OnSave saves the current message, as ⌘S does natively.
	OnSave func(id int)
	// OnPrint prints the current message, as ⌘P does natively.
	OnPrint func(id int)
	// OnBuilt is told, for each page bound to b.Message, each body built
	// as the message's current version: native shows the truncated notice
	// by b.Cut and hides the remote-image notice once b.Remote. It runs
	// with the Reader's lock held, as Emit does (so a page is told the
	// bodies in their versions' order): it must not block or call back.
	OnBuilt func(id int, b ReaderBody)
	// OnFailed is told, for each page bound to message, that reloading the
	// body failed: native offers a retry. Under the lock too.
	OnFailed func(id int, message int64, err error)
	// OnImagesFailed is told, for each page bound to message, how many of
	// its images the proxy could not get: native offers a retry
	// (ReloadImages). Under the lock too.
	OnImagesFailed func(id int, message int64, n int)
}

// Reader keeps the reader pages (role "reader"), each bound to a message:
// its body, version and images. It is not bound: the pages reach it only
// through ReaderPage; native calls Bind, Unbind, ReloadImages and SettingsChanged.
type Reader struct {
	pages *Pages
	d     ReaderDeps

	// Test seams.
	getMessage func(ctx context.Context, svc Service, id int64, remote bool) (core.Message, error)
	getAvatar  func(ctx context.Context, svc Service, id int64) (core.Avatar, error)
	getInline  func(ctx context.Context, svc Service, id int64, each func(cid string, a mail.Attachment)) error

	mu       sync.Mutex
	svc      Service
	version  int64          // the last version given out
	settings *core.Settings // latest in-memory SettingsChanged snapshot (ahead of async DB saves)
	bound    map[int]int64  // registration ID → its message
	idlePage any            // the one idle shell, known only to Binding
	idleID   int
	msgs     map[int64]*bodyOf // per message with a page bound
}

// bodyOf is a bound message's state.
type bodyOf struct {
	owner      int  // first registration of this message lifetime; namespaces its image tokens
	pages      int  // bound to it
	manual     bool // 加载图片 was clicked during this message lifetime
	generation int64
	initial    *core.Message     // first open’s canonical GetMessageLazyImages(message, false)
	body       *ReaderBody       // the current displayed body, once built
	original   *ReaderBody       // safe, prepared original for switching without rereading mail
	tokens     map[string]string // its image tokens (godwit-img: paths) → remote URLs
	// The pixels the images served for it take, from the message-wide budget:
	// the page decodes every image at once. charged is the tokens counted.
	pixels       *imagefetch.Budget
	charged      map[string]bool
	failed       map[string]bool   // the tokens the proxy could not get since the last reload
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
	return &Reader{pages: pages, d: d, bound: map[int]int64{}, msgs: map[int64]*bodyOf{},
		getMessage: func(ctx context.Context, svc Service, id int64, remote bool) (core.Message, error) {
			return svc.GetMessageLazyImages(ctx, id, remote)
		},
		getInline: func(ctx context.Context, svc Service, id int64, each func(cid string, a mail.Attachment)) error {
			return svc.InlineImages(ctx, id, each)
		},
		getAvatar: func(ctx context.Context, svc Service, id int64) (core.Avatar, error) {
			return svc.SenderAvatar(ctx, id, false)
		}}
}

// SetService gives the pages the service, once the data is open.
func (r *Reader) SetService(svc Service) {
	r.mu.Lock()
	r.svc = svc
	r.mu.Unlock()
}

// Bind makes registration id (a new Pages.Register as "reader") show message.
func (r *Reader) Bind(id int, message int64, initial *core.Message) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if key, ok := r.pages.Key(id); ok && key == r.idlePage {
		r.idlePage, r.idleID = nil, 0
	}
	r.bound[id] = message
	st := r.msgs[message]
	if st == nil {
		r.version++
		st = &bodyOf{owner: id, generation: r.version}
		if initial != nil && initial.ID == message {
			st.initial = initial
		}
		r.msgs[message] = st
	}
	st.pages++
}

// Bound is the message registration id shows, while it is bound.
func (r *Reader) Bound(id int) (message int64, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	message, ok = r.bound[id]
	return message, ok
}

// Unbind revokes id, before Pages.Unregister. An idle shell can query its
// inactive ID but has no grants. Unbind(id, false) forgets it on destruction.
func (r *Reader) Unbind(id int, idle bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.idleID == id {
		r.idlePage, r.idleID = nil, 0
	}
	m, ok := r.bound[id]
	if !ok {
		return
	}
	if idle {
		if key, ok := r.pages.Key(id); ok {
			r.idlePage, r.idleID = key, id
		}
	}
	delete(r.bound, id)
	st := r.msgs[m]
	if st.pages--; st.pages == 0 {
		if st.stopInline != nil {
			st.stopInline()
		}
		delete(r.msgs, m)
	}
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
	r.emitTranslation(st)
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
	for _, s := range r.msgs {
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
		r.tellPages(b, r.d.Emit)
	}
}

// emitTranslation applies the current choice to the cached original, with r.mu held.
func (r *Reader) emitTranslation(st *bodyOf) {
	b := translateBody(*st.original, st.translations)
	r.version++
	b.Version = r.version
	st.body = &b
	r.tellPages(b, func(id int, b ReaderBody) {
		r.d.OnBuilt(id, b)
		r.d.Emit(id, b)
	})
}

// tellPages stamps a copy for each current registration. It runs under r.mu.
func (r *Reader) tellPages(b ReaderBody, tell func(int, ReaderBody)) {
	for id, m := range r.bound {
		if m == b.Message {
			b.Binding = id
			tell(id, b)
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

// ReloadImages rebuilds the bound message's body and retries its failed images.
// Only a manual 加载图片 click grants remote images for this message lifetime.
func (r *Reader) ReloadImages(message int64, manual bool) {
	r.mu.Lock()
	st := r.msgs[message]
	svc := r.svc
	if st == nil || svc == nil {
		r.mu.Unlock()
		return
	}
	manual = manual || st.manual
	st.manual = manual
	var urls []string
	for token := range st.failed {
		if u := st.tokens[token]; u != "" {
			urls = append(urls, u)
		}
	}
	st.failed = nil
	if st.stopInline != nil {
		st.stopInline()
	}
	st.inline, st.stopInline = nil, nil
	r.version++
	v := r.version
	st.generation, st.body, st.initial = v, nil, nil
	r.mu.Unlock()
	go func() {
		if len(urls) > 0 {
			svc.ForgetImageFailure(urls)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_, err := r.build(ctx, svc, message, st, v, manual, 0)
		if err != nil {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.msgs[message] == st && st.generation == v {
				log.Printf("reader: message %d: %v", message, err)
				if st.original != nil {
					r.emitTranslation(st)
				}
				for k, m := range r.bound {
					if m == message {
						r.d.OnFailed(k, message, err)
					}
				}
			}
		}
	}()
}

var errBodyChanged = errors.New("the reader body changed during the load")

// checkBuild runs under r.mu, before fetching and again before publishing.
func (r *Reader) checkBuild(message int64, state *bodyOf, v int64, id int) error {
	if id != 0 {
		if _, ok := r.pages.Key(id); !ok || r.bound[id] != message {
			return ErrWrongPage
		}
	}
	if r.msgs[message] != state || state.generation != v {
		return errBodyChanged
	}
	return nil
}

func imagesAllowed(ctx context.Context, svc Service, message int64, manual bool) (bool, error) {
	if manual {
		return true, nil
	}
	return svc.ImagesAllowed(ctx, message)
}

// build publishes only while the requesting registration (0 for a reload)
// and message generation are still live. A revoked Load returns no content.
func (r *Reader) build(ctx context.Context, svc Service, message int64, state *bodyOf, v int64, manual bool, id int) (ReaderBody, error) {
	allowed, err := imagesAllowed(ctx, svc, message, manual)
	if err != nil {
		return ReaderBody{}, err
	}
	r.mu.Lock()
	if err := r.checkBuild(message, state, v, id); err != nil {
		r.mu.Unlock()
		return ReaderBody{}, err
	}
	initial := state.initial
	state.initial = nil
	r.mu.Unlock()
	var m core.Message
	// SanitizeLazy counts remote resources even when blocked; with none its
	// two policies produce the same body. Reloads always read afresh.
	if initial != nil && (!allowed || initial.RemoteImagesBlocked == 0) {
		m = *initial
	} else if m, err = r.getMessage(ctx, svc, message, allowed); err != nil {
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
		dims := map[string]core.ImageDims{}
		b.HTML, tokens = prepareBody(m.HTML, budget, message, state.owner, func(src string) (int, int, bool) {
			d, ok := dims[src]
			if !ok {
				d = svc.CachedImageDims(src)
				dims[src] = d
			}
			return d.Width, d.Height, d.OK
		})
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
	if err := r.checkBuild(message, state, v, id); err != nil {
		return ReaderBody{}, err
	}
	if state.body == nil {
		r.version++
		b.Version = r.version
		original := b
		state.original = &original
		b = translateBody(b, state.translations)
		cached := b
		state.body, state.tokens = &cached, tokens
		state.pixels, state.charged = budget, map[string]bool{}
		r.tellPages(b, r.d.OnBuilt)
	}
	b = *state.body
	if id == 0 {
		r.tellPages(b, r.d.Emit)
	}
	b.Binding = id
	return b, nil
}

// ReaderPage is what a reader page (role "reader", one per expanded
// message) calls (mygo.Bind). Each acts on the caller's own message.
type ReaderPage struct{ r *Reader }

// NewReaderPage is the bound facade of r.
func NewReaderPage(r *Reader) *ReaderPage { return &ReaderPage{r: r} }

// Binding bootstraps a page after it subscribes to ReaderBound. Only the
// one idle shell can query its revoked ID; unrelated callers get no access.
func (p *ReaderPage) Binding(ctx context.Context) (ReaderBinding, error) {
	r := p.r
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.idleID != 0 && r.pages.pageOf(ctx) == r.idlePage {
		return ReaderBinding{ID: r.idleID}, nil
	}
	id, err := r.pages.Caller(ctx, "reader")
	if _, ok := r.bound[id]; err != nil || !ok {
		return ReaderBinding{}, ErrWrongPage
	}
	return ReaderBinding{ID: id, Active: true}, nil
}

func (r *Reader) caller(ctx context.Context, id int) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.callerLocked(ctx, id)
}

// The ID supplied by the page must match the physical IPC caller.
func (r *Reader) callerLocked(ctx context.Context, id int) (int64, error) {
	actual, err := r.pages.Caller(ctx, "reader")
	message, ok := r.bound[actual]
	if err != nil || actual != id || !ok {
		return 0, ErrWrongPage
	}
	if r.svc == nil {
		return 0, ErrNotReady
	}
	return message, nil
}

// Load returns only the body for this assignment, even across a slow read.
func (p *ReaderPage) Load(ctx context.Context, id int) (ReaderBody, error) {
	r := p.r
	for {
		r.mu.Lock()
		message, err := r.callerLocked(ctx, id)
		if err != nil {
			r.mu.Unlock()
			return ReaderBody{}, err
		}
		st, svc := r.msgs[message], r.svc
		if st.body != nil {
			b := *st.body
			b.Binding = id
			r.mu.Unlock()
			return b, nil
		}
		v, manual := st.generation, st.manual
		r.mu.Unlock()
		b, err := r.build(ctx, svc, message, st, v, manual, id)
		if err != errBodyChanged { // a concurrent reload superseded the build; read its version
			return b, err
		}
	}
}

// Height is the body's height in CSS pixels, for the native layout.
func (p *ReaderPage) Height(ctx context.Context, id, px int) error {
	message, err := p.r.caller(ctx, id)
	if err != nil {
		return err
	}
	if px < 0 || px > maxBodyHeight {
		return ErrBadHeight
	}
	p.r.d.OnHeight(id, message, px)
	return nil
}

// Painted is the page's acknowledgement of drawable content for id.
func (p *ReaderPage) Painted(ctx context.Context, id int) error {
	message, err := p.r.caller(ctx, id)
	if err == nil {
		p.r.d.OnPainted(id, message)
	}
	return err
}

// OpenLink opens a link of the body (its data-href) natively: an http or
// https one after the confirmation, a mailto one in a compose window. Any
// other is ErrBadLink.
func (p *ReaderPage) OpenLink(ctx context.Context, id int, link string) error {
	_, err := p.r.caller(ctx, id)
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
	p.r.d.OnLink(id, link)
	return nil
}

// Navigate goes back (-1) or forward (1), as ⌘[ and ⌘] do natively.
func (p *ReaderPage) Navigate(ctx context.Context, id, dir int) error {
	_, err := p.r.caller(ctx, id)
	if err != nil {
		return err
	}
	if dir != -1 && dir != 1 {
		return ErrBadDirection
	}
	p.r.d.OnNavigate(id, dir)
	return nil
}

// Search opens the mail list's search, as ⌘F does natively: a page that
// has the keyboard misses the native shortcut.
func (p *ReaderPage) Search(ctx context.Context, id int) error {
	_, err := p.r.caller(ctx, id)
	if err != nil {
		return err
	}
	p.r.d.OnSearch(id)
	return nil
}

// Save saves the message, as ⌘S does natively: a page that has the
// keyboard misses the native shortcut.
func (p *ReaderPage) Save(ctx context.Context, id int) error {
	_, err := p.r.caller(ctx, id)
	if err != nil {
		return err
	}
	if p.r.d.OnSave != nil {
		p.r.d.OnSave(id)
	}
	return nil
}

// Print prints the message, as ⌘P does natively: a page that has the
// keyboard misses the native shortcut.
func (p *ReaderPage) Print(ctx context.Context, id int) error {
	_, err := p.r.caller(ctx, id)
	if err != nil {
		return err
	}
	if p.r.d.OnPrint != nil {
		p.r.d.OnPrint(id)
	}
	return nil
}

// ImageScheme is the URL scheme of a message's remote images in its body,
// served by ImageHandler: godwit-img://m<message>/<token>; and of its sender's
// avatar: godwit-img://a<message>.
const ImageScheme = "godwit-img"

// imageURL is the URL a page loads godwit-img://<host><path> at.
func imageURL(host, path string) string { return imageURLOn(runtime.GOOS, host, path) }

// imageURLOn is imageURL on the system goos. WebView2 (Windows) requests no
// custom scheme a page names: MyGo serves an app scheme there at
// http://<host>.<scheme>.localhost<path>, a loopback origin, and hands
// ImageHandler the godwit-img: URL, so only the page's URL differs.
func imageURLOn(goos, host, path string) string {
	if goos == "windows" {
		return (&url.URL{Scheme: "http", Host: host + "." + ImageScheme + ".localhost", Path: path}).String()
	}
	return (&url.URL{Scheme: ImageScheme, Host: host, Path: path}).String()
}

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
		st, svc := r.msgs[message], r.svc
		if st == nil || svc == nil || st.tokens[token] == "" {
			r.mu.Unlock()
			http.NotFound(w, req)
			return
		}
		src, manual, generation := st.tokens[token], st.manual, st.generation
		if strings.HasPrefix(src, cidPrefix) {
			inline := r.inlineImage(st, message, svc, src)
			r.mu.Unlock()
			if inline == nil {
				http.NotFound(w, req)
				return
			}
			r.serveInline(w, req, message, token, inline)
			return
		}
		r.mu.Unlock()
		mayServe := func() bool {
			allowed, err := imagesAllowed(req.Context(), svc, message, manual)
			r.mu.Lock()
			defer r.mu.Unlock()
			// A reload changes generation before it replaces the old URLs.
			if r.msgs[message] != st || st.generation != generation || st.tokens[token] != src {
				return false
			}
			if err != nil {
				if req.Context().Err() == nil {
					r.imageFailedLocked(message, token) // fail closed, but let this page retry
				}
				return false
			}
			return allowed
		}
		if !mayServe() {
			http.NotFound(w, req)
			return
		}
		img, err := svc.CachedImage(req.Context(), src)
		var data []byte
		if err == nil {
			data, err = os.ReadFile(img.Path)
		}
		if !mayServe() { // the policy or binding may have changed during the download
			http.NotFound(w, req)
			return
		}
		if err != nil {
			if req.Context().Err() == nil { // not the page going away
				log.Printf("image proxy: message %d: %v", message, err)
				r.imageFailed(message, token)
			}
			http.Error(w, "image unavailable", http.StatusBadGateway)
			return
		}
		if !r.chargePixels(message, token, img.Width, img.Height) {
			http.Error(w, "the message's images have too many pixels", http.StatusForbidden)
			return
		}
		writeImage(w, img.Format, data)
	})
}

// inlineImage is st's inline image src, the download of them all begun at the
// first asked for. It runs with r.mu held.
func (r *Reader) inlineImage(st *bodyOf, message int64, svc Service, src string) *inlineImage {
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
					r.imageFailedLocked(message, imageToken(st.owner, src))
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
	if st == nil || st.tokens[token] == "" || st.pixels == nil {
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
	if st == nil || st.tokens[token] == "" || st.failed[token] {
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
func (r *Reader) chargePixels(message int64, token string, width, height int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.msgs[message]
	if st == nil || st.tokens[token] == "" || st.pixels == nil {
		return false
	}
	if st.charged[token] {
		return true
	}
	if st.pixels.TakePixels(int64(width)*int64(height)) != nil {
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
