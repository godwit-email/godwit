package webui

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/internal/imagecache"
	"github.com/godwit-email/godwit/internal/imagefetch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"
)

type countingImageDims struct {
	Service
	calls map[string]int
}

func (s *countingImageDims) CachedImageDims(src string) core.ImageDims {
	s.calls[src]++
	if strings.HasSuffix(src, "/cached.png") {
		return core.ImageDims{Width: 20, Height: 10, OK: true}
	}
	return core.ImageDims{}
}

func TestReaderBuildLooksUpEachImageSizeOnce(t *testing.T) {
	f := newReaderFixture(t)
	svc := &countingImageDims{Service: f.r.svc, calls: map[string]int{}}
	f.r.getMessage = func(context.Context, Service, int64, bool) (core.Message, error) {
		return core.Message{HTML: strings.Repeat(`<img src="https://example.com/cached.png"><img src="https://example.com/missing.png">`, 100)}, nil
	}
	body, err := f.r.build(t.Context(), svc, 1, 1, false)
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"https://example.com/cached.png": 1, "https://example.com/missing.png": 1}, svc.calls)
	assert.Equal(t, 100, strings.Count(body.HTML, `width="20"`))
}

// readerFixture is a Reader on an Env with one account; every native
// callback and emitted body is recorded.
type readerFixture struct {
	e     *coretest.Env
	acc   core.Account
	pages *Pages
	r     *Reader
	p     *ReaderPage

	mu       sync.Mutex
	emitted  []emittedBody
	heights  [][2]int64
	links    []string
	navs     []int
	searches int
	callers  []any // the page each of the above named
	cuts     map[int64]bool
	built    []emittedBody // OnBuilt's
	failed   []any         // the pages OnFailed named
	imgFails []int         // OnImagesFailed's counts
}

type emittedBody struct {
	page any
	body ReaderBody
}

func newReaderFixture(t *testing.T) *readerFixture {
	t.Helper()
	f := &readerFixture{e: coretest.New(t, coretest.WithoutAutoSync()), cuts: map[int64]bool{}}
	f.acc = f.e.AddAccount(t, "alice@example.com")
	f.pages = NewPages(func(ctx context.Context) any { return ctx.Value(pageKey{}) })
	f.r = NewReader(f.pages, ReaderDeps{
		Emit: func(page any, b ReaderBody) { f.record(func() { f.emitted = append(f.emitted, emittedBody{page, b}) }) },
		OnHeight: func(k any, m int64, px int) {
			f.record(func() { f.callers, f.heights = append(f.callers, k), append(f.heights, [2]int64{m, int64(px)}) })
		},
		OnLink: func(k any, u string) {
			f.record(func() { f.callers, f.links = append(f.callers, k), append(f.links, u) })
		},
		OnNavigate: func(k any, dir int) {
			f.record(func() { f.callers, f.navs = append(f.callers, k), append(f.navs, dir) })
		},
		OnSearch: func(k any) { f.record(func() { f.callers, f.searches = append(f.callers, k), f.searches+1 }) },
		OnBuilt: func(k any, b ReaderBody) {
			f.record(func() { f.cuts[b.Message], f.built = b.Cut, append(f.built, emittedBody{k, b}) })
		},
		OnFailed:       func(k any, _ int64, _ error) { f.record(func() { f.failed = append(f.failed, k) }) },
		OnImagesFailed: func(_ any, _ int64, n int) { f.record(func() { f.imgFails = append(f.imgFails, n) }) },
	})
	st, err := f.e.Svc.GetSettings(t.Context())
	require.NoError(t, err)
	st.RemoteImages = "ask" // the tests of the policy say otherwise
	_, err = f.e.Svc.SetSettings(t.Context(), st)
	require.NoError(t, err)
	f.r.SetService(uiService(t, f.e))
	f.p = NewReaderPage(f.r)
	return f
}

func (f *readerFixture) record(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

// page registers a reader page bound to message and returns its key.
func (f *readerFixture) page(message int64) any {
	k := new(int)
	f.pages.Register(k, "reader")
	f.r.Bind(k, message)
	return k
}

// waitEmitted waits for a body emitted to page and returns the newest.
func (f *readerFixture) waitEmitted(t *testing.T, page any) ReaderBody {
	t.Helper()
	var got ReaderBody
	require.Eventually(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, e := range f.emitted {
			if e.page == page {
				got = e.body
			}
		}
		return got.Version != 0
	}, 30*time.Second, 5*time.Millisecond, "no body emitted")
	return got
}

func (f *readerFixture) deliverOne(t *testing.T, raw []byte) core.MessageSummary {
	t.Helper()
	f.e.Deliver(t, f.acc.ID, "INBOX", raw)
	page, err := f.e.Svc.ListMessages(t.Context(), core.View{Unified: "inbox"}, "", 200)
	require.NoError(t, err)
	require.NotEmpty(t, page.Items)
	return page.Items[0]
}

// imageServer counts its requests and keeps their headers; it sets a cookie.
type imageServer struct {
	*httptest.Server
	mu   sync.Mutex
	hdrs []http.Header
}

func newImageServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *imageServer {
	s := &imageServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.hdrs = append(s.hdrs, r.Header.Clone())
		s.mu.Unlock()
		w.Header().Set("Set-Cookie", "track=1")
		handler(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *imageServer) hits() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.hdrs) }

func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))))
	return b.Bytes()
}

// pngHeader is a PNG's signature and a valid IHDR chunk declaring w×h.
func pngHeader(w, h uint32) []byte {
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	data := binary.BigEndian.AppendUint32(nil, w)
	data = binary.BigEndian.AppendUint32(data, h)
	data = append(data, 8, 6, 0, 0, 0)
	chunk := append([]byte("IHDR"), data...)
	b.Write(binary.BigEndian.AppendUint32(nil, uint32(len(data))))
	b.Write(chunk)
	b.Write(binary.BigEndian.AppendUint32(nil, crc32.ChecksumIEEE(chunk)))
	return b.Bytes()
}

func dataPNG(b []byte) string { return "data:image/png;base64," + base64.StdEncoding.EncodeToString(b) }

// image is the proxy's answer for src, an image of message.
func (f *readerFixture) image(t *testing.T, message int64, src string) *httptest.ResponseRecorder {
	t.Helper()
	return f.imageAt(t, fmt.Sprintf("godwit-img://m%d/%s", message, imagecache.Key(src)))
}

func (f *readerFixture) imageAt(t *testing.T, u string) *httptest.ResponseRecorder {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, u, nil)
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	f.r.ImageHandler().ServeHTTP(rec, req)
	return rec
}

// proxied is src as the body holds it.
func proxied(message int64, src string) string {
	return fmt.Sprintf("godwit-img://m%d/%s", message, imagecache.Key(src))
}

// remoteMail is a message with text and one remote image at src.
func (f *readerFixture) remoteMail(t *testing.T, text, src string) core.MessageSummary {
	return f.deliverOne(t, coretest.Msg("bob@example.com", f.acc.Email, "News: "+text, text,
		coretest.HTML(`<p>`+text+`</p><img src="`+src+`" alt="pic" width="2" height="2">`)))
}

func TestReaderPageLoad(t *testing.T) {
	f := newReaderFixture(t)
	m := f.remoteMail(t, "Hello reader", "https://tracker.example/pixel.png")
	k := f.page(m.ID)
	b, err := f.p.Load(pageCtx(k))
	require.NoError(t, err)
	assert.Equal(t, m.ID, b.Message)
	assert.NotZero(t, b.Version)
	assert.Contains(t, b.HTML, "Hello reader")
	assert.NotContains(t, b.HTML, "tracker.example")
	assert.False(t, b.Remote)
	assert.Equal(t, 1, b.Blocked)
	assert.False(t, b.Cut)
	assert.Empty(t, b.Plain)
	assert.EqualValues(t, 14, b.Theme.BodyFontSize)
	assert.Equal(t, "zh-Hans", b.Locale)

	again, err := f.p.Load(pageCtx(k))
	require.NoError(t, err)
	assert.Equal(t, b.Version, again.Version, "a reload is the same body")

	plain := f.deliverOne(t, coretest.Msg("bob@example.com", f.acc.Email, "Plain", "just words"))
	pb, err := f.p.Load(pageCtx(f.page(plain.ID)))
	require.NoError(t, err)
	assert.Equal(t, "", pb.HTML)
	assert.Contains(t, pb.Plain, "just words")
}

func TestReaderTranslationKeepsBodyAndImagePolicy(t *testing.T) {
	f := newReaderFixture(t)
	m := f.remoteMail(t, "Hello reader", "https://tracker.example/pixel.png")
	k := f.page(m.ID)
	original, err := f.p.Load(pageCtx(k))
	require.NoError(t, err)
	f.r.Translate(m.ID, map[string]string{"Hello reader": `你好 <img src="https://evil.test/x">`})
	translated, err := f.p.Load(pageCtx(k))
	require.NoError(t, err)
	assert.Greater(t, translated.Version, original.Version)
	assert.Contains(t, translated.HTML, `你好 &lt;img`)
	assert.Equal(t, strings.Count(original.HTML, "<img"), strings.Count(translated.HTML, "<img"))
	assert.False(t, translated.Remote)
	assert.Equal(t, original.Blocked, translated.Blocked)
	f.r.Allow(m.ID)
	withImages, err := f.p.Load(pageCtx(k))
	require.NoError(t, err)
	assert.True(t, withImages.Remote)
	assert.Contains(t, withImages.HTML, `你好 &lt;img`)
	assert.Contains(t, withImages.HTML, `godwit-img:`)
	f.r.Translate(m.ID, nil)
	restored, err := f.p.Load(pageCtx(k))
	require.NoError(t, err)
	assert.Contains(t, restored.HTML, "Hello reader")
	assert.NotContains(t, restored.HTML, "你好")
	assert.Contains(t, restored.HTML, `godwit-img:`)
	assert.True(t, restored.Remote, "switching back keeps image consent")
}

func TestReaderTranslationNeedsNoReread(t *testing.T) {
	f := newReaderFixture(t)
	m := f.remoteMail(t, "Hello reader", "https://tracker.example/pixel.png")
	k := f.page(m.ID)
	original, err := f.p.Load(pageCtx(k))
	require.NoError(t, err)
	f.r.getMessage = func(context.Context, Service, int64, bool) (core.Message, error) {
		return core.Message{}, errors.New("mail unavailable")
	}
	f.r.Translate(m.ID, map[string]string{"Hello reader": "你好"})
	translated, err := f.p.Load(pageCtx(k))
	require.NoError(t, err)
	assert.Contains(t, translated.HTML, "你好")
	f.r.Translate(m.ID, nil)
	restored, err := f.p.Load(pageCtx(k))
	require.NoError(t, err)
	assert.Equal(t, original.HTML, restored.HTML)
	assert.Greater(t, restored.Version, translated.Version)
}

func TestReaderTranslationMatchesSanitizedHTML(t *testing.T) {
	f := newReaderFixture(t)
	llm := f.e.LLM(t)
	t.Cleanup(llm.Release)
	llm.Script(coretest.Turn{Text: []string{`{"texts":["你好","阅读更多"],"source":"en"}`}})
	m := f.deliverOne(t, coretest.Msg("bob@example.com", f.acc.Email, "News", "fallback",
		coretest.HTML(`<p>Hello &amp; welcome&nbsp;<img src="https://images.example/logo.png" width="2" height="2"></p><a href="https://example.com?q=a&amp;b=c"><span>Read more</span></a>`)))
	k := f.page(m.ID)
	original, err := f.p.Load(pageCtx(k))
	require.NoError(t, err)
	events, err := f.e.Svc.RunSkill(t.Context(), "translate", core.Target{Kind: "message", ID: m.ID, PreserveHTML: true})
	require.NoError(t, err)
	var texts map[string]string
	for event := range events {
		require.Nil(t, event.Err)
		if event.Kind == "text" {
			texts = event.Translations
		}
	}
	require.Equal(t, map[string]string{"Hello & welcome\u00a0": "你好\u00a0", "Read more": "阅读更多"}, texts)
	f.r.Translate(m.ID, texts)
	translated, err := f.p.Load(pageCtx(k))
	require.NoError(t, err)
	assert.Contains(t, translated.HTML, "你好\u00a0")
	assert.Contains(t, translated.HTML, `<span>阅读更多</span>`)
	assert.Contains(t, translated.HTML, `data-href="https://example.com?q=a&amp;b=c"`)
	assert.Equal(t, original.Blocked, translated.Blocked)
	f.r.Allow(m.ID)
	withImages, err := f.p.Load(pageCtx(k))
	require.NoError(t, err)
	assert.Contains(t, withImages.HTML, "你好\u00a0")
	assert.Contains(t, withImages.HTML, `<span>阅读更多</span>`)
	assert.Contains(t, withImages.HTML, `src="`+proxied(m.ID, "https://images.example/logo.png")+`"`)
	assert.True(t, withImages.Remote)
}

func TestReaderTranslationDuringImageReload(t *testing.T) {
	for _, tc := range []struct {
		name          string
		restore, fail bool
	}{
		{name: "translated"},
		{name: "original", restore: true},
		{name: "translated after failure", fail: true},
		{name: "original after failure", restore: true, fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReaderFixture(t)
			m := f.remoteMail(t, "Hello reader", "https://tracker.example/pixel.png")
			k := f.page(m.ID)
			original, err := f.p.Load(pageCtx(k))
			require.NoError(t, err)
			started, release := make(chan struct{}), make(chan struct{})
			real := f.r.getMessage
			f.r.getMessage = func(ctx context.Context, svc Service, id int64, remote bool) (core.Message, error) {
				close(started)
				<-release
				if tc.fail {
					return core.Message{}, errors.New("mail unavailable")
				}
				return real(ctx, svc, id, remote)
			}
			f.r.Allow(m.ID)
			<-started
			f.r.Translate(m.ID, map[string]string{"Hello reader": "你好"})
			if tc.restore {
				f.r.Translate(m.ID, nil)
			}
			f.mu.Lock()
			assert.Empty(t, f.emitted, "no stale body is emitted during the reload")
			assert.Len(t, f.built, 1, "no stale image policy is reported during the reload")
			f.mu.Unlock()
			close(release)
			require.Eventually(t, func() bool {
				f.mu.Lock()
				defer f.mu.Unlock()
				return len(f.emitted) == 1 && (!tc.fail || len(f.failed) == 1)
			}, 10*time.Second, 5*time.Millisecond)
			body := f.waitEmitted(t, k)
			assert.Equal(t, !tc.fail, body.Remote)
			assert.Greater(t, body.Version, original.Version)
			if tc.restore {
				assert.Contains(t, body.HTML, "Hello reader")
				assert.NotContains(t, body.HTML, "你好")
			} else {
				assert.Contains(t, body.HTML, "你好")
			}
			if tc.fail {
				f.r.getMessage = func(context.Context, Service, int64, bool) (core.Message, error) {
					t.Error("switching after a failed reload must use the cached original")
					return core.Message{}, errors.New("mail unavailable")
				}
				f.r.Translate(m.ID, nil)
				restored, err := f.p.Load(pageCtx(k))
				require.NoError(t, err)
				assert.Equal(t, original.HTML, restored.HTML)
			}
		})
	}
}

func TestReaderPageScoped(t *testing.T) {
	pic := pngOf(t, 2, 2)
	srv := newImageServer(t, func(w http.ResponseWriter, r *http.Request) { w.Write(pic) })
	f := newReaderFixture(t)
	m := f.remoteMail(t, "for M", srv.URL+"/m.png")
	n := f.remoteMail(t, "for N", srv.URL+"/n.png")
	km, kn := f.page(m.ID), f.page(n.ID)

	b, err := f.p.Load(pageCtx(km))
	require.NoError(t, err)
	assert.Contains(t, b.HTML, "for M")
	assert.NotContains(t, b.HTML, "for N")
	_, err = f.p.Load(pageCtx(kn))
	require.NoError(t, err)
	f.r.Allow(m.ID)
	f.r.Allow(n.ID)
	f.waitEmitted(t, km)
	f.waitEmitted(t, kn)
	assert.Equal(t, http.StatusNotFound, f.image(t, m.ID, srv.URL+"/n.png").Code, "N's image under M")
	assert.Equal(t, http.StatusOK, f.image(t, m.ID, srv.URL+"/m.png").Code)
	assert.Equal(t, http.StatusOK, f.image(t, n.ID, srv.URL+"/n.png").Code)
	assert.Equal(t, 2, srv.hits())

	wit := new(int)
	f.pages.Register(wit, "wit")
	unbound := new(int)
	f.pages.Register(unbound, "reader") // a reader page not bound to a message
	for name, ctx := range map[string]context.Context{
		"unregistered": pageCtx(new(int)), "window": context.Background(), "wit": pageCtx(wit), "unbound": pageCtx(unbound),
	} {
		_, err := f.p.Load(ctx)
		assert.ErrorIs(t, err, ErrWrongPage, name)
		assert.ErrorIs(t, f.p.Height(ctx, 10), ErrWrongPage, name)
		assert.ErrorIs(t, f.p.OpenLink(ctx, "https://x.example/"), ErrWrongPage, name)
		assert.ErrorIs(t, f.p.Navigate(ctx, 1), ErrWrongPage, name)
		assert.ErrorIs(t, f.p.Search(ctx), ErrWrongPage, name)
	}
	assert.Equal(t, 2, srv.hits())

	got, ok := f.r.Bound(km)
	assert.True(t, ok)
	assert.Equal(t, m.ID, got)
	f.r.Unbind(km)
	_, err = f.p.Load(pageCtx(km))
	assert.ErrorIs(t, err, ErrWrongPage, "unbound after Unbind")
	_, ok = f.r.Bound(km)
	assert.False(t, ok)
}

func TestReaderPageCallbacks(t *testing.T) {
	f := newReaderFixture(t)
	m := f.remoteMail(t, "hi", "https://x.example/a.png")
	k := f.page(m.ID)
	ctx := pageCtx(k)
	require.NoError(t, f.p.Height(ctx, 640))
	require.NoError(t, f.p.OpenLink(ctx, "https://x.example/"))
	require.NoError(t, f.p.OpenLink(ctx, "mailto:bob@example.com"))
	assert.ErrorIs(t, f.p.OpenLink(ctx, "javascript:alert(1)"), ErrBadLink)
	require.NoError(t, f.p.Navigate(ctx, -1))
	assert.ErrorIs(t, f.p.Navigate(ctx, 2), ErrBadDirection)
	require.NoError(t, f.p.Search(ctx))
	assert.Error(t, f.p.Height(ctx, -1))
	f.mu.Lock()
	defer f.mu.Unlock()
	assert.Equal(t, [][2]int64{{m.ID, 640}}, f.heights)
	assert.Equal(t, []string{"https://x.example/", "mailto:bob@example.com"}, f.links)
	assert.Equal(t, []int{-1}, f.navs)
	assert.Equal(t, 1, f.searches)
	assert.Equal(t, []any{k, k, k, k, k}, f.callers, "each callback names the calling page")
}

// Each body built as current is told to the pages bound to its message
// (OnBuilt), Remote once allowed; a failed Allow is told too (OnFailed).
func TestReaderBuiltAndFailed(t *testing.T) {
	f := newReaderFixture(t)
	m := f.remoteMail(t, "hi", "https://img.example/a.png")
	other := f.remoteMail(t, "other", "https://img.example/b.png")
	k, ko := f.page(m.ID), f.page(other.ID)
	_, err := f.p.Load(pageCtx(k))
	require.NoError(t, err)
	f.r.Allow(m.ID)
	require.Eventually(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.built) == 2
	}, 10*time.Second, 5*time.Millisecond)
	f.mu.Lock()
	assert.Equal(t, k, f.built[0].page)
	assert.False(t, f.built[0].body.Remote)
	assert.Equal(t, k, f.built[1].page)
	assert.True(t, f.built[1].body.Remote)
	f.mu.Unlock()

	real := f.r.getMessage
	f.r.getMessage = func(ctx context.Context, svc Service, id int64, remote bool) (core.Message, error) {
		if remote {
			return core.Message{}, errors.New("server went away")
		}
		return real(ctx, svc, id, remote)
	}
	f.r.Allow(m.ID)
	require.Eventually(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.failed) == 1
	}, 10*time.Second, 5*time.Millisecond)
	f.mu.Lock()
	defer f.mu.Unlock()
	assert.Equal(t, []any{k}, f.failed)
	for _, b := range f.built {
		assert.True(t, b.page != ko, "the other page told of M's body")
	}
}

func TestReaderImageRefusedUntilAllowed(t *testing.T) {
	pic := pngOf(t, 2, 2)
	srv := newImageServer(t, func(w http.ResponseWriter, r *http.Request) { w.Write(pic) })
	f := newReaderFixture(t)
	src := srv.URL + "/pixel.png"
	m := f.remoteMail(t, "hi", src)
	k := f.page(m.ID)
	b, err := f.p.Load(pageCtx(k))
	require.NoError(t, err)
	assert.NotContains(t, b.HTML, "godwit-img")
	assert.Equal(t, http.StatusNotFound, f.image(t, m.ID, src).Code)
	assert.Zero(t, srv.hits())
}

func TestReaderImageAfterAllow(t *testing.T) {
	pic := pngOf(t, 2, 2)
	srv := newImageServer(t, func(w http.ResponseWriter, r *http.Request) { w.Write(pic) })
	f := newReaderFixture(t)
	src := srv.URL + "/pixel.png"
	m := f.remoteMail(t, "hi", src)
	k := f.page(m.ID)
	f.r.Allow(m.ID)
	b := f.waitEmitted(t, k)
	require.True(t, b.Remote)
	assert.Contains(t, b.HTML, `src="`+proxied(m.ID, src)+`"`)
	assert.NotContains(t, b.HTML, srv.URL, "the page never sees the remote address")
	for range 2 {
		rec := f.image(t, m.ID, src)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, pic, rec.Body.Bytes())
		assert.Equal(t, "image/png", rec.Header().Get("Content-Type"))
		assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
		assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"), "each body load must pass the checks")
	}
	require.Equal(t, 1, srv.hits(), "one request for the image: the second is from the cache")
	for _, h := range []string{"Cookie", "Referer"} {
		assert.Empty(t, srv.hdrs[0].Get(h), h)
	}
}

func TestReaderImageRefusedNotInBody(t *testing.T) {
	pic := pngOf(t, 2, 2)
	srv := newImageServer(t, func(w http.ResponseWriter, r *http.Request) { w.Write(pic) })
	f := newReaderFixture(t)
	m := f.remoteMail(t, "hi", srv.URL+"/pixel.png")
	k := f.page(m.ID)
	f.r.Allow(m.ID)
	f.waitEmitted(t, k)
	for _, u := range []string{srv.URL + "/other.png", "http://127.0.0.1:1/x.png", "file:///etc/passwd"} {
		assert.Equal(t, http.StatusNotFound, f.image(t, m.ID, u).Code, u)
	}
	for _, u := range []string{"godwit-img://m999/" + imagecache.Key(srv.URL+"/pixel.png"), "godwit-img://x/y", "godwit-img://m1/", "godwit-img://m1/a/b"} {
		assert.Equal(t, http.StatusNotFound, f.imageAt(t, u).Code, u)
	}
	assert.Zero(t, srv.hits())
}

func TestReaderRetryRefetchesFailedImage(t *testing.T) {
	pic := pngOf(t, 2, 2)
	var up atomic.Bool
	srv := newImageServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !up.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		w.Write(pic)
	})
	f := newReaderFixture(t)
	src := srv.URL + "/pixel.png"
	m := f.remoteMail(t, "hi", src)
	k := f.page(m.ID)
	f.r.Allow(m.ID)
	first := f.waitEmitted(t, k).Version
	require.Equal(t, http.StatusBadGateway, f.image(t, m.ID, src).Code)
	up.Store(true)
	require.Equal(t, http.StatusBadGateway, f.image(t, m.ID, src).Code, "the failure is remembered")
	f.r.Allow(m.ID) // the retry
	require.Eventually(t, func() bool { return f.waitEmitted(t, k).Version > first }, 10*time.Second, 5*time.Millisecond)
	rec := f.image(t, m.ID, src)
	require.Equal(t, http.StatusOK, rec.Code, "a retry tries again at once")
	assert.Equal(t, pic, rec.Body.Bytes())
}

func TestReaderImageFailureIs502(t *testing.T) {
	srv := newImageServer(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<html>")) })
	f := newReaderFixture(t)
	src := srv.URL + "/page.png"
	m := f.remoteMail(t, "hi", src)
	k := f.page(m.ID)
	f.r.Allow(m.ID)
	first := f.waitEmitted(t, k).Version
	assert.Equal(t, http.StatusBadGateway, f.image(t, m.ID, src).Code, "not an image")
	f.image(t, m.ID, src)
	f.record(func() { assert.Equal(t, []int{1}, f.imgFails, "the page is told once per image") })
	f.r.Allow(m.ID) // the retry
	require.Eventually(t, func() bool { return f.waitEmitted(t, k).Version > first }, 10*time.Second, 5*time.Millisecond)
	f.image(t, m.ID, src)
	f.record(func() { assert.Equal(t, []int{1, 1}, f.imgFails, "counted again after a retry") })
}

func TestReaderAvatar(t *testing.T) {
	f := newReaderFixture(t)
	m := f.remoteMail(t, "hi", "https://tracker.example/pixel.png")
	assert.Equal(t, http.StatusNotFound, f.imageAt(t, fmt.Sprintf("godwit-img://a%d", m.ID)).Code,
		"the message's policy (ask) blocks its avatar, with no page bound")

	var asked []int64
	f.r.getAvatar = func(_ context.Context, _ Service, id int64) (core.Avatar, error) {
		asked = append(asked, id)
		return core.Avatar{Data: []byte("<svg/>"), Format: "svg"}, nil
	}
	rec := f.imageAt(t, fmt.Sprintf("godwit-img://a%d/", m.ID))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "image/svg+xml", rec.Header().Get("Content-Type"))
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "<svg/>", rec.Body.String())
	for _, u := range []string{"godwit-img://a", "godwit-img://ax", fmt.Sprintf("godwit-img://a%d/x", m.ID)} {
		assert.Equal(t, http.StatusNotFound, f.imageAt(t, u).Code, u)
	}
	assert.Equal(t, []int64{m.ID}, asked)
}

func TestReaderPolicyLoadsImagesAtOnce(t *testing.T) {
	pic := pngOf(t, 2, 2)
	srv := newImageServer(t, func(w http.ResponseWriter, r *http.Request) { w.Write(pic) })
	f := newReaderFixture(t)
	setMode := func(mode string) {
		st, err := f.e.Svc.GetSettings(t.Context())
		require.NoError(t, err)
		st.RemoteImages = mode
		_, err = f.e.Svc.SetSettings(t.Context(), st)
		require.NoError(t, err)
	}
	load := func(text, src string) ReaderBody {
		m := f.remoteMail(t, text, src)
		b, err := f.p.Load(pageCtx(f.page(m.ID)))
		require.NoError(t, err)
		return b
	}
	setMode("auto")
	src := srv.URL + "/a.png"
	b := load("auto", src)
	assert.True(t, b.Remote, "automatic: no asking")
	assert.Contains(t, b.HTML, "godwit-img://")
	assert.Equal(t, http.StatusOK, f.image(t, b.Message, src).Code)

	setMode("trusted")
	assert.False(t, load("trusted", srv.URL+"/b.png").Remote, "trusted only: not this sender yet")
	require.NoError(t, f.e.Svc.TrustImageSender(t.Context(), "Bob@Example.com"))
	assert.True(t, load("trusted 2", srv.URL+"/c.png").Remote)
	require.NoError(t, f.e.Svc.UntrustImageSender(t.Context(), "bob@example.com"))
	setMode("ask")
	assert.False(t, load("ask", srv.URL+"/d.png").Remote)
}

func TestReaderAllowReloads(t *testing.T) {
	f := newReaderFixture(t)
	m := f.remoteMail(t, "for M", "https://img.example/m.png")
	n := f.remoteMail(t, "for N", "https://img.example/n.png")
	km, kn := f.page(m.ID), f.page(n.ID)
	before, err := f.p.Load(pageCtx(km))
	require.NoError(t, err)
	f.r.Allow(m.ID)
	b := f.waitEmitted(t, km)
	assert.True(t, b.Remote)
	assert.Greater(t, b.Version, before.Version)
	assert.Equal(t, m.ID, b.Message)
	assert.Contains(t, b.HTML, `src="`+proxied(m.ID, "https://img.example/m.png")+`"`)
	after, err := f.p.Load(pageCtx(km))
	require.NoError(t, err)
	assert.Equal(t, b.Version, after.Version)
	assert.True(t, after.Remote)
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.emitted {
		assert.True(t, e.page != kn, "N's page told of M's body") // pointers: not by value
	}
}

// fakeMessage makes the reader read message bodies from fn.
func (f *readerFixture) fakeMessage(fn func(id int64, remote bool) core.Message) {
	f.r.getMessage = func(_ context.Context, _ Service, id int64, remote bool) (core.Message, error) {
		m := fn(id, remote)
		m.ID = id
		return m, nil
	}
}

func TestReaderInlineImageBudgets(t *testing.T) {
	small := dataPNG(pngOf(t, 1, 1))
	big := dataPNG(append(pngHeader(10, 10), make([]byte, 6<<20)...))
	huge := dataPNG(pngHeader(10000, 5000)) // 50 MP
	var sb strings.Builder
	fmt.Fprintf(&sb, `<p>inline</p><img src="%s" alt="big"><img src="%s" alt="huge">`, big, huge)
	for i := range 51 {
		fmt.Fprintf(&sb, `<img src="%s" alt="n%d">`, small, i)
	}
	inline := sb.String()

	pic := pngOf(t, 2, 2)
	srv := newImageServer(t, func(w http.ResponseWriter, r *http.Request) { w.Write(pic) })
	f := newReaderFixture(t)
	f.fakeMessage(func(id int64, remote bool) core.Message {
		h := inline
		if remote {
			h += `<img src="` + srv.URL + `/r.png" alt="remote">`
		}
		return core.Message{HTML: h}
	})
	k := f.page(7)
	b, err := f.p.Load(pageCtx(k))
	require.NoError(t, err)
	assert.Equal(t, 50, strings.Count(b.HTML, `src="data:`), "50 inline images kept")
	doc, err := html.Parse(strings.NewReader(b.HTML))
	require.NoError(t, err)
	srcOf := map[string]string{}
	for n := range doc.Descendants() {
		if n.Data == "img" {
			alt, src := attr(n, "alt"), attr(n, "src")
			srcOf[alt] = src
		}
	}
	for _, alt := range []string{"big", "huge", "n50"} {
		s, ok := srcOf[alt]
		assert.True(t, ok, "%s kept with its alt", alt)
		assert.Empty(t, s, "%s has no src", alt)
	}
	assert.NotEmpty(t, srcOf["n49"])

	f.r.Allow(7)
	b = f.waitEmitted(t, k)
	assert.NotContains(t, b.HTML, "godwit-img", "remote images share the message's image count: the inline ones used it up")
	assert.Zero(t, srv.hits())
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// TestReaderBodyWhole: a body over the old structural limits comes back
// TestReaderBodyKeepsDarkMark: a body written again for the image proxy still
// starts with the sanitizer's mark of dark styles, as the reader looks for it
// (web/src/reader/frame.ts): else it inverts a mail that styles its own dark.
func TestReaderBodyKeepsDarkMark(t *testing.T) {
	safe, _, _ := mail.Sanitize(`<style>@media (prefers-color-scheme: dark) { .a { color: #eee } }</style><p class="a">x</p><img src="https://img.example/a.png">`, nil, true)
	out, tokens := prepareBody(safe, imagefetch.NewBudget(), 1, noDims)
	require.Len(t, tokens, 1, "the image went through the proxy: the body was written again")
	assert.True(t, strings.HasPrefix(out, `<style data-dark="">`), out)
}

// whole; only the Service's storage truncation (Message.Truncated) cuts.
func TestReaderBodyWhole(t *testing.T) {
	t.Run("paragraphs", func(t *testing.T) {
		in := strings.Repeat("<p>x</p>", 6000) + "<p>tail</p>"
		out, _ := prepareBody(in, imagefetch.NewBudget(), 1, noDims)
		assert.Equal(t, in, out)
	})
	t.Run("cells", func(t *testing.T) {
		in := "<table><tbody><tr>" + strings.Repeat("<td>c</td>", 3000) + "</tr></tbody></table><p>after</p>"
		out, _ := prepareBody(in, imagefetch.NewBudget(), 1, noDims)
		assert.Equal(t, in, out)
	})
	t.Run("depth and inlines", func(t *testing.T) {
		in := strings.Repeat("<div>", 30) + strings.Repeat("<b>x</b>", 25000) + strings.Repeat("</div>", 30) + "<p>after</p>"
		out, _ := prepareBody(in, imagefetch.NewBudget(), 1, noDims)
		assert.Equal(t, in, out)
	})
	t.Run("reported", func(t *testing.T) {
		f := newReaderFixture(t)
		f.fakeMessage(func(id int64, _ bool) core.Message {
			switch id {
			case 1:
				return core.Message{HTML: strings.Repeat("<p>x</p>", 6000)}
			case 2:
				return core.Message{HTML: "<p>x</p>", Truncated: true}
			}
			return core.Message{HTML: "<p>x</p>"}
		})
		for id, want := range map[int64]bool{1: false, 2: true, 3: false} {
			b, err := f.p.Load(pageCtx(f.page(id)))
			require.NoError(t, err)
			assert.Equal(t, want, b.Cut, "message %d", id)
		}
		f.mu.Lock()
		assert.Equal(t, map[int64]bool{1: false, 2: true, 3: false}, f.cuts)
		f.mu.Unlock()
	})
}

// TestReaderInlineImageBytes: inline images take their bytes from the
// budget the remote ones share.
func TestReaderInlineImageBytes(t *testing.T) {
	const size = 4<<20 + 512<<10 // 4.5 MiB: four fit in 20 MiB, five do not
	inline := dataPNG(append(pngHeader(10, 10), make([]byte, size)...))
	var sb strings.Builder
	for i := range 5 {
		fmt.Fprintf(&sb, `<img src="%s" alt="i%d">`, inline, i)
	}
	srv := newImageServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(append(pngHeader(10, 10), make([]byte, 3<<20)...))
	})
	f := newReaderFixture(t)
	f.fakeMessage(func(_ int64, remote bool) core.Message {
		h := sb.String()
		if remote {
			h += `<img src="` + srv.URL + `/r.png">`
		}
		return core.Message{HTML: h}
	})
	k := f.page(1)
	b, err := f.p.Load(pageCtx(k))
	require.NoError(t, err)
	assert.Equal(t, 4, strings.Count(b.HTML, `src="data:`), "the fifth inline image is over the bytes")
	f.r.Allow(1)
	b = f.waitEmitted(t, k)
	assert.Equal(t, 4, strings.Count(b.HTML, `src="data:`))
	assert.Equal(t, http.StatusOK, f.image(t, 1, srv.URL+"/r.png").Code, "a 3 MiB remote image is no longer cut by the inline budget")
}

// TestReaderEmitFollowsBinding: a body goes only to pages bound to its
// message as it is sent, though pages are rebound meanwhile.
func TestReaderEmitFollowsBinding(t *testing.T) {
	f := newReaderFixture(t)
	f.fakeMessage(func(id int64, _ bool) core.Message { return core.Message{HTML: fmt.Sprintf("<p>%d</p>", id)} })
	var wrong atomic.Int64
	f.r.d.Emit = func(page any, b ReaderBody) {
		if f.r.bound[page] != b.Message { // the lock is held
			wrong.Add(1)
		}
	}
	k := f.page(1)
	f.page(1) // keeps message 1 bound while k moves
	end := time.Now().Add(300 * time.Millisecond)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; time.Now().Before(end); i++ {
			f.r.Bind(k, int64(1+i%2))
		}
	}()
	for time.Now().Before(end) {
		f.r.Allow(1)
		time.Sleep(time.Millisecond)
	}
	<-done
	time.Sleep(200 * time.Millisecond) // the Allows' goroutines finish
	assert.Zero(t, wrong.Load(), "a page got another message's body")
}

func noDims(string) (int, int, bool) { return 0, 0, false }

// An inline image is left for the page to ask for, whatever the remote-image
// policy, and all of them come from one download however many requests there are.
func TestReaderInlineImagesLoadThroughProxyOnce(t *testing.T) {
	pic := pngOf(t, 2, 2)
	f := newReaderFixture(t)
	f.fakeMessage(func(int64, bool) core.Message {
		return core.Message{HTML: `<img src="cid:a"><img src="cid:b"><img src="cid:gone">`}
	})
	var calls atomic.Int32
	f.r.getInline = func(_ context.Context, _ Service, _ int64, each func(string, mail.Attachment)) error {
		calls.Add(1)
		each("a", mail.Attachment{Data: pic})
		each("b", mail.Attachment{Data: []byte("not an image")})
		return nil
	}
	k := f.page(7)
	b, err := f.p.Load(pageCtx(k))
	require.NoError(t, err)
	assert.Contains(t, b.HTML, `src="`+proxied(7, "cid:a")+`"`)
	assert.False(t, b.Remote)
	assert.Zero(t, calls.Load(), "nothing downloads until the page asks")

	rec := f.image(t, 7, "cid:a")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, pic, rec.Body.Bytes())
	assert.Equal(t, "image/png", rec.Header().Get("Content-Type"))
	assert.Equal(t, http.StatusNotFound, f.image(t, 7, "cid:b").Code, "not an image the page may show")
	assert.Equal(t, http.StatusNotFound, f.image(t, 7, "cid:gone").Code, "the download ended without it")
	assert.Equal(t, http.StatusNotFound, f.image(t, 7, "cid:never").Code, "not in this body")
	assert.Equal(t, http.StatusOK, f.image(t, 7, "cid:a").Code, "asked again")
	assert.EqualValues(t, 1, calls.Load())
}

// Whoever asks waits for its own image only, and closing the last page stops the download.
func TestReaderInlineImageServedBeforeTheRest(t *testing.T) {
	pic := pngOf(t, 2, 2)
	f := newReaderFixture(t)
	f.fakeMessage(func(int64, bool) core.Message {
		return core.Message{HTML: `<img src="cid:a"><img src="cid:b">`}
	})
	stopped := make(chan struct{})
	f.r.getInline = func(ctx context.Context, _ Service, _ int64, each func(string, mail.Attachment)) error {
		each("a", mail.Attachment{Data: pic})
		<-ctx.Done() // b never arrives
		close(stopped)
		return ctx.Err()
	}
	k := f.page(7)
	_, err := f.p.Load(pageCtx(k))
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, f.image(t, 7, "cid:a").Code)
	f.r.Unbind(k)
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("the download went on after the last page closed")
	}
}

// An inline image the download ended without is told, for native's retry; one
// the download gave, even if the page may not show it, is not.
func TestReaderInlineImageDownloadFailureIsTold(t *testing.T) {
	f := newReaderFixture(t)
	f.fakeMessage(func(int64, bool) core.Message {
		return core.Message{HTML: `<img src="cid:a"><img src="cid:b"><img src="cid:c">`}
	})
	pic := pngOf(t, 2, 2)
	f.r.getInline = func(_ context.Context, _ Service, _ int64, each func(string, mail.Attachment)) error {
		each("a", mail.Attachment{Data: pic})
		each("b", mail.Attachment{Data: []byte("not an image")})
		return context.DeadlineExceeded
	}
	_, err := f.p.Load(pageCtx(f.page(7)))
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, f.image(t, 7, "cid:c").Code)
	f.mu.Lock()
	defer f.mu.Unlock()
	assert.Equal(t, []int{1}, f.imgFails)
}

// A download a retry stopped has not failed: its images are on their way again.
func TestReaderInlineImageDownloadStoppedIsNotTold(t *testing.T) {
	pic := pngOf(t, 2, 2)
	f := newReaderFixture(t)
	f.fakeMessage(func(int64, bool) core.Message { return core.Message{HTML: `<img src="cid:a">`} })
	running := make(chan struct{})
	var calls atomic.Int32
	f.r.getInline = func(ctx context.Context, _ Service, _ int64, each func(string, mail.Attachment)) error {
		if calls.Add(1) > 1 {
			each("a", mail.Attachment{Data: pic})
			return nil
		}
		close(running)
		<-ctx.Done()
		return ctx.Err()
	}
	k := f.page(7)
	_, err := f.p.Load(pageCtx(k))
	require.NoError(t, err)
	first := make(chan int, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodGet, proxied(7, "cid:a"), nil)
		rec := httptest.NewRecorder()
		f.r.ImageHandler().ServeHTTP(rec, req)
		first <- rec.Code
	}()
	<-running
	f.r.Allow(7)
	assert.Equal(t, http.StatusNotFound, <-first)
	f.waitEmitted(t, k)
	assert.Equal(t, http.StatusOK, f.image(t, 7, "cid:a").Code)
	f.mu.Lock()
	defer f.mu.Unlock()
	assert.Empty(t, f.imgFails)
}

// A retry (Allow) tries the inline images missed again.
func TestReaderRetryRefetchesInlineImages(t *testing.T) {
	pic := pngOf(t, 2, 2)
	f := newReaderFixture(t)
	f.fakeMessage(func(int64, bool) core.Message { return core.Message{HTML: `<img src="cid:a">`} })
	var calls atomic.Int32
	f.r.getInline = func(_ context.Context, _ Service, _ int64, each func(string, mail.Attachment)) error {
		if calls.Add(1) > 1 {
			each("a", mail.Attachment{Data: pic})
		}
		return nil
	}
	k := f.page(7)
	_, err := f.p.Load(pageCtx(k))
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, f.image(t, 7, "cid:a").Code)
	f.r.Allow(7)
	f.waitEmitted(t, k)
	assert.Equal(t, http.StatusOK, f.image(t, 7, "cid:a").Code)
	assert.EqualValues(t, 2, calls.Load())
}

// Inline images come out of the message's budget as they are served: one past the
// per-image limits, or past the pixels the message has left, is refused.
func TestReaderInlineImageOverBudgetRefused(t *testing.T) {
	big40 := pngHeader(8000, 5000) // 40 MP: three are past the message's 100 MP
	images := map[string][]byte{
		"huge": pngHeader(10000, 5000), // 50 MP: past one image's
		"big":  append(pngHeader(10, 10), make([]byte, 6<<20)...),
		"p0":   big40, "p1": big40, "p2": big40,
	}
	f := newReaderFixture(t)
	f.fakeMessage(func(int64, bool) core.Message {
		return core.Message{HTML: `<img src="cid:huge"><img src="cid:big"><img src="cid:p0"><img src="cid:p1"><img src="cid:p2">`}
	})
	f.r.getInline = func(_ context.Context, _ Service, _ int64, each func(string, mail.Attachment)) error {
		for cid, data := range images {
			each(cid, mail.Attachment{Data: data})
		}
		return nil
	}
	_, err := f.p.Load(pageCtx(f.page(7)))
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, f.image(t, 7, "cid:huge").Code)
	assert.Equal(t, http.StatusNotFound, f.image(t, 7, "cid:big").Code, "past one image's bytes")
	assert.Equal(t, http.StatusOK, f.image(t, 7, "cid:p0").Code)
	assert.Equal(t, http.StatusOK, f.image(t, 7, "cid:p1").Code)
	assert.Equal(t, http.StatusForbidden, f.image(t, 7, "cid:p2").Code)
	assert.Equal(t, http.StatusOK, f.image(t, 7, "cid:p0").Code, "charged once")
}

// The message's image count is taken as the body is prepared, in its order: past it
// an inline image has no src, so the page never asks for it, and one already counted
// is not counted again as it is served.
func TestReaderInlineImageCountReservedInOrder(t *testing.T) {
	pic := pngOf(t, 1, 1)
	var in strings.Builder
	in.WriteString(`<img src="cid:n0" alt="first"><img src="cid:n0" alt="again">`)
	for i := 1; i < imagefetch.MaxImages; i++ {
		fmt.Fprintf(&in, `<img src="https://cdn.example/%d.png">`, i)
	}
	in.WriteString(`<img src="https://cdn.example/late.png" alt="late"><img src="cid:late" alt="late inline">`)
	out, tokens := prepareBody(in.String(), imagefetch.NewBudget(), 7, noDims)
	assert.Len(t, tokens, imagefetch.MaxImages)
	assert.Equal(t, 2, strings.Count(out, proxied(7, "cid:n0")), "the first inline image keeps its place, and its second use costs nothing")
	assert.Contains(t, out, `<img alt="late"/>`)
	assert.Contains(t, out, `<img alt="late inline"/>`)

	var all strings.Builder
	for i := range imagefetch.MaxImages + 1 {
		fmt.Fprintf(&all, `<img src="cid:n%d">`, i)
	}
	f := newReaderFixture(t)
	f.fakeMessage(func(int64, bool) core.Message { return core.Message{HTML: all.String()} })
	f.r.getInline = func(_ context.Context, _ Service, _ int64, each func(string, mail.Attachment)) error {
		for i := range imagefetch.MaxImages + 1 {
			each(fmt.Sprintf("n%d", i), mail.Attachment{Data: pic})
		}
		return nil
	}
	b, err := f.p.Load(pageCtx(f.page(7)))
	require.NoError(t, err)
	assert.Equal(t, imagefetch.MaxImages, strings.Count(b.HTML, ImageScheme+":"))
	for i := range imagefetch.MaxImages {
		require.Equal(t, http.StatusOK, f.image(t, 7, fmt.Sprintf("cid:n%d", i)).Code, i)
	}
	assert.Equal(t, http.StatusNotFound, f.image(t, 7, fmt.Sprintf("cid:n%d", imagefetch.MaxImages)).Code)
}

// A real message: the body comes first, and its image through the proxy.
func TestReaderInlineImageEndToEnd(t *testing.T) {
	pic := pngOf(t, 2, 2)
	f := newReaderFixture(t)
	raw := "From: bob@example.com\r\nTo: " + f.acc.Email + "\r\nSubject: Logo\r\nDate: Mon, 05 Oct 2026 10:00:00 +0000\r\nMessage-ID: <logo@x>\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: multipart/related; boundary=b1\r\n\r\n--b1\r\nContent-Type: text/html\r\n\r\n<img src=\"cid:logo\">\r\n" +
		"--b1\r\nContent-Type: image/png\r\nContent-ID: <logo>\r\nContent-Disposition: inline; filename=\"l.png\"\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
		base64.StdEncoding.EncodeToString(pic) + "\r\n--b1--\r\n"
	m := f.deliverOne(t, []byte(raw))
	b, err := f.p.Load(pageCtx(f.page(m.ID)))
	require.NoError(t, err)
	assert.Contains(t, b.HTML, proxied(m.ID, "cid:logo"))
	rec := f.image(t, m.ID, "cid:logo")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, pic, rec.Body.Bytes())
}

// A message with an inline data:image bitmap shows it at once, even when
// remote images ask first (#706).
func TestReaderInlineDataImageEndToEnd(t *testing.T) {
	const blueSquare = "iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAIAAACQkWg2AAAAFklEQVR4nGPQKLpFEmIY1TCqYfhqAAASZ3QQWUKpQAAAAABJRU5ErkJggg=="
	f := newReaderFixture(t)
	raw := "From: Sender <sender@example.com>\r\nTo: " + f.acc.Email + "\r\nDate: Thu, 12 Nov 2026 12:00:00 +0800\r\n" +
		"Subject: Inline Base64 PNG\r\nMessage-ID: <b64@example.com>\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n" +
		"<p>Inline Base64 PNG:</p>\r\n<img alt=\"Blue square\" width=\"16\" height=\"16\" src=\"data:image/png;base64," + blueSquare + "\">\r\n"
	m := f.deliverOne(t, []byte(raw))
	b, err := f.p.Load(pageCtx(f.page(m.ID)))
	require.NoError(t, err)
	assert.False(t, b.Remote)
	assert.Zero(t, b.Blocked)
	assert.Contains(t, b.HTML, `src="data:image/png;base64,`+blueSquare+`"`)
}

func TestPrepareBodyProxiesRemoteImages(t *testing.T) {
	dims := func(src string) (int, int, bool) { return 40, 30, src == "https://cdn.example/known.png" }
	in := `<img src="https://cdn.example/known.png" alt="a"><img src="https://cdn.example/sized.png" width="9">` +
		`<img src="https://cdn.example/new.png" alt="n"><img src="https://cdn.example/styled.png" style="width:50%">` +
		`<table data-bg="https://cdn.example/bg.jpg" style="color: red"><tbody><tr><td data-bg="javascript:1">x</td></tr></tbody></table>`
	out, tokens := prepareBody(in, imagefetch.NewBudget(), 12, dims)
	assert.NotContains(t, out, "cdn.example")
	assert.NotContains(t, out, "data-bg")
	assert.Len(t, tokens, 5)
	assert.Equal(t, "https://cdn.example/bg.jpg", tokens[imagecache.Key("https://cdn.example/bg.jpg")])
	assert.Contains(t, out, `<img src="`+proxied(12, "https://cdn.example/known.png")+`" alt="a" width="40" height="30"/>`, "the cached size reserves the space")
	assert.Contains(t, out, `src="`+proxied(12, "https://cdn.example/sized.png")+`" width="9"/>`, "a declared size stays")
	assert.Contains(t, out, `src="`+proxied(12, "https://cdn.example/new.png")+`" alt="n"/>`)
	assert.Contains(t, out, `style="color: red;background-image:url(`+proxied(12, "https://cdn.example/bg.jpg")+`)"`)
	assert.NotContains(t, out, "--godwit-bg", "no unused inversion layer is emitted")
	assert.NotContains(t, out, "javascript")
}

func TestPrepareBodyCapsRemoteImages(t *testing.T) {
	var in strings.Builder
	for i := range imagefetch.MaxImages + 5 {
		fmt.Fprintf(&in, `<img src="https://cdn.example/%d.png" alt="i%d"><img src="https://cdn.example/0.png">`, i, i)
	}
	out, tokens := prepareBody(in.String(), imagefetch.NewBudget(), 3, noDims)
	assert.Len(t, tokens, imagefetch.MaxImages, "the same URL again costs nothing; the ones past the count lose their src")
	assert.Equal(t, 5, strings.Count(out, "<img alt="), "five images lost their src")
}

func TestPrepareBodyKeepsCSSSizedImages(t *testing.T) {
	dims := func(string) (int, int, bool) { return 40, 30, true }
	out, _ := prepareBody(`<img src="https://a.example/h.png" style="height:50px"><img src="https://a.example/w.png" style="width:50%">`, imagefetch.NewBudget(), 1, dims)
	assert.NotContains(t, out, `width="40"`)
	assert.NotContains(t, out, `height="30"`)
}

func TestReaderImagePixelsAreChargedPerMessage(t *testing.T) {
	pic := pngOf(t, 6000, 6000) // 36 MP; three exceed the 100 MP message budget
	srv := newImageServer(t, func(w http.ResponseWriter, r *http.Request) { w.Write(pic) })
	f := newReaderFixture(t)
	f.fakeMessage(func(int64, bool) core.Message {
		return core.Message{HTML: `<img src="` + srv.URL + `/1.png"><img src="` + srv.URL + `/2.png"><img src="` + srv.URL + `/3.png">`}
	})
	k := f.page(5)
	f.r.Allow(5)
	f.waitEmitted(t, k)
	codes := []int{}
	for _, n := range []string{"1", "2", "3", "1"} {
		codes = append(codes, f.image(t, 5, srv.URL+"/"+n+".png").Code)
	}
	assert.Equal(t, []int{200, 200, http.StatusForbidden, 200}, codes, "the third pushes past 100 MP; one already charged is served again")
}

func TestReaderInlinePixelsCarryIntoTheProxyBudget(t *testing.T) {
	inline := dataPNG(pngHeader(6000, 6000)) // 36 MP each
	pic := pngOf(t, 6000, 6000)
	srv := newImageServer(t, func(w http.ResponseWriter, r *http.Request) { w.Write(pic) })
	f := newReaderFixture(t)
	f.fakeMessage(func(int64, bool) core.Message {
		return core.Message{HTML: `<img src="` + inline + `"><img src="` + inline + `"><img src="` + srv.URL + `/r.png">`}
	})
	k := f.page(6)
	f.r.Allow(6)
	f.waitEmitted(t, k)
	assert.Equal(t, http.StatusForbidden, f.image(t, 6, srv.URL+"/r.png").Code, "72 MP inline + 36 MP remote is over 100 MP")
}

// The reader page tells the frame whether to fold quoted text, as the setting says,
// and propagates changes to already-loaded messages immediately.
func TestReaderBodyCarriesCollapseQuotes(t *testing.T) {
	f := newReaderFixture(t)
	m := f.remoteMail(t, "Re: lunch", "https://tracker.example/pixel.png")
	k := f.page(m.ID)
	b, err := f.p.Load(pageCtx(k))
	require.NoError(t, err)
	assert.False(t, b.CollapseQuotes, "off by default")

	st, err := f.e.Svc.GetSettings(t.Context())
	require.NoError(t, err)
	st.CollapseQuotes = true
	st, err = f.e.Svc.SetSettings(t.Context(), st)
	require.NoError(t, err)
	f.r.SettingsChanged(st)

	// Already-bound page receives the updated preference and cached Load reflects it too.
	emitted := f.waitEmitted(t, k)
	assert.True(t, emitted.CollapseQuotes)
	assert.Greater(t, emitted.Version, b.Version)
	cached, err := f.p.Load(pageCtx(k))
	require.NoError(t, err)
	assert.True(t, cached.CollapseQuotes)
	assert.Equal(t, emitted.Version, cached.Version)

	// Unrelated settings changes update Theme without re-emitting the body.
	f.mu.Lock()
	f.emitted = nil
	f.mu.Unlock()
	st.Theme = "dark"
	f.r.SettingsChanged(st)
	f.mu.Lock()
	assert.Empty(t, f.emitted)
	f.mu.Unlock()
	cached, err = f.p.Load(pageCtx(k))
	require.NoError(t, err)
	assert.Equal(t, "dark", cached.Theme.Mode)

	// A change made while a reload is in flight survives the reload failing: the cached original
	// it falls back on carries the new preference.
	started, release := make(chan struct{}), make(chan struct{})
	f.r.getMessage = func(context.Context, Service, int64, bool) (core.Message, error) {
		close(started)
		<-release
		return core.Message{}, errors.New("mail unavailable")
	}
	f.mu.Lock()
	f.emitted = nil
	f.mu.Unlock()
	f.r.Allow(m.ID)
	<-started
	st.CollapseQuotes = false
	f.r.SettingsChanged(st)
	close(release)
	assert.False(t, f.waitEmitted(t, k).CollapseQuotes)
	st.CollapseQuotes = true
	f.r.SettingsChanged(st)
	cached = f.waitEmitted(t, k)
	require.True(t, cached.CollapseQuotes)
	f.r.getMessage = func(ctx context.Context, svc Service, id int64, remote bool) (core.Message, error) {
		return svc.GetMessageLazyImages(ctx, id, remote)
	}

	// Turning CollapseQuotes back off unfolds the already-loaded message too.
	st.CollapseQuotes = false
	f.r.SettingsChanged(st)
	unfolded := f.waitEmitted(t, k)
	assert.False(t, unfolded.CollapseQuotes)
	assert.Greater(t, unfolded.Version, cached.Version)

	// Opening a new message while an async settings save is still pending in the DB (DB still has
	// CollapseQuotes: true, while in-memory SettingsChanged already flipped to false, or vice versa)
	// uses the latest in-memory SettingsChanged snapshot.
	m2 := f.remoteMail(t, "Re: dinner", "https://tracker.example/pixel2.png")
	b, err = f.p.Load(pageCtx(f.page(m2.ID)))
	require.NoError(t, err)
	assert.False(t, b.CollapseQuotes, "newly started build honors pending SettingsChanged snapshot ahead of stale DB")

	st.CollapseQuotes = true
	f.r.SettingsChanged(st) // DB was not updated yet
	m3 := f.remoteMail(t, "Re: brunch", "https://tracker.example/pixel3.png")
	b, err = f.p.Load(pageCtx(f.page(m3.ID)))
	require.NoError(t, err)
	assert.True(t, b.CollapseQuotes)
}
