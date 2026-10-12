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
	"github.com/godwit-email/godwit/internal/imagecache"
	"github.com/godwit-email/godwit/internal/imagefetch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"
)

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
	wheels   []float64
	links    []string
	navs     []int
	searches int
	callers  []any // the page each of the above named
	cuts     map[int64]bool
	built    []emittedBody // OnBuilt's
	failed   []any         // the pages OnFailed named
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
		OnWheel: func(k any, dy float64) {
			f.record(func() { f.callers, f.wheels = append(f.callers, k), append(f.wheels, dy) })
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
		OnFailed: func(k any, _ int64, _ error) { f.record(func() { f.failed = append(f.failed, k) }) },
	})
	st, err := f.e.Svc.GetSettings(t.Context())
	require.NoError(t, err)
	st.RemoteImages = "ask" // the tests of the policy say otherwise
	_, err = f.e.Svc.SetSettings(t.Context(), st)
	require.NoError(t, err)
	f.r.SetService(f.e.Svc)
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
	}, 10*time.Second, 5*time.Millisecond, "no body emitted")
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
	assert.Equal(t, "重试", b.Text["common.retry"])
	assert.Equal(t, "邮件正文", b.Text["reader.body"])

	again, err := f.p.Load(pageCtx(k))
	require.NoError(t, err)
	assert.Equal(t, b.Version, again.Version, "a reload is the same body")

	plain := f.deliverOne(t, coretest.Msg("bob@example.com", f.acc.Email, "Plain", "just words"))
	pb, err := f.p.Load(pageCtx(f.page(plain.ID)))
	require.NoError(t, err)
	assert.Equal(t, "", pb.HTML)
	assert.Contains(t, pb.Plain, "just words")
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
		assert.ErrorIs(t, f.p.Wheel(ctx, 10), ErrWrongPage, name)
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
	require.NoError(t, f.p.Wheel(ctx, 120))
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
	assert.Equal(t, []float64{120}, f.wheels)
	assert.Equal(t, []string{"https://x.example/", "mailto:bob@example.com"}, f.links)
	assert.Equal(t, []int{-1}, f.navs)
	assert.Equal(t, 1, f.searches)
	assert.Equal(t, []any{k, k, k, k, k, k}, f.callers, "each callback names the calling page")
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
	f.r.getMessage = func(ctx context.Context, svc *core.Service, id int64, remote bool) (core.Message, error) {
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

func TestReaderImageFailureIs502(t *testing.T) {
	srv := newImageServer(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<html>")) })
	f := newReaderFixture(t)
	src := srv.URL + "/page.png"
	m := f.remoteMail(t, "hi", src)
	k := f.page(m.ID)
	f.r.Allow(m.ID)
	f.waitEmitted(t, k)
	assert.Equal(t, http.StatusBadGateway, f.image(t, m.ID, src).Code, "not an image")
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
	f.r.getMessage = func(_ context.Context, _ *core.Service, id int64, remote bool) (core.Message, error) {
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
