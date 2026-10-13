package imagecache

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godwit-email/godwit/internal/imagefetch"
)

func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func open(t *testing.T, dir string, max int64) *Cache {
	t.Helper()
	c, err := Open(dir, max, imagefetch.NewClient(true))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestGetCachesOnceAcrossCallers(t *testing.T) {
	pic := pngOf(t, 4, 3)
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		time.Sleep(50 * time.Millisecond)
		w.Write(pic)
	}))
	defer srv.Close()
	dir := t.TempDir()
	c := open(t, dir, 0)
	src := srv.URL + "/a.png"
	if _, _, ok := c.Dims(src); ok {
		t.Fatal("cached before the download")
	}
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			img, err := c.Get(t.Context(), src)
			if err != nil || !bytes.Equal(img.Data, pic) || img.Format != "png" || img.Width != 4 || img.Height != 3 {
				t.Errorf("get: %q %dx%d %v", img.Format, img.Width, img.Height, err)
			}
		}()
	}
	wg.Wait()
	if hits.Load() != 1 {
		t.Errorf("%d requests, want 1", hits.Load())
	}
	if w, h, ok := c.Dims(src); !ok || w != 4 || h != 3 {
		t.Errorf("dims %dx%d %v", w, h, ok)
	}
	// A new cache on the same directory finds it: offline, no request.
	srv.Close()
	c2 := open(t, dir, 0)
	if img, err := c2.Get(t.Context(), src); err != nil || !bytes.Equal(img.Data, pic) {
		t.Errorf("after restart: %v", err)
	}
	if c2.Size() != int64(len(pic)) {
		t.Errorf("size %d", c2.Size())
	}
}

func TestFailureIsRememberedBriefly(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte("<html>"))
	}))
	defer srv.Close()
	c := open(t, t.TempDir(), 0)
	for range 3 {
		if _, err := c.Get(t.Context(), srv.URL+"/x"); err == nil {
			t.Fatal("html accepted as an image")
		}
	}
	if hits.Load() != 1 {
		t.Errorf("%d requests, want 1", hits.Load())
	}
}

func TestEvictsLeastRecentlyUsed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Path[1:])
		w.Write(pngOf(t, n, n))
	}))
	defer srv.Close()
	pic := int64(len(pngOf(t, 40, 40)))
	c := open(t, t.TempDir(), pic*3+pic/2)
	get := func(n int) {
		if _, err := c.Get(t.Context(), srv.URL+"/"+strconv.Itoa(n+40)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	// 40x40 PNGs of all one colour are about the same size; the 4th goes over the limit.
	get(0)
	get(1)
	get(2)
	get(0) // 0 is now newer than 1
	get(3)
	if c.Cached(srv.URL+"/41") || !c.Cached(srv.URL+"/40") || !c.Cached(srv.URL+"/43") {
		t.Errorf("evicted the wrong one: 40=%v 41=%v 43=%v", c.Cached(srv.URL+"/40"), c.Cached(srv.URL+"/41"), c.Cached(srv.URL+"/43"))
	}
	if c.Size() > pic*4 {
		t.Errorf("size %d over limit", c.Size())
	}
	if err := c.Clear(); err != nil || c.Size() != 0 {
		t.Errorf("clear: %v, %d bytes", err, c.Size())
	}
	if des, _ := os.ReadDir(c.dir); len(des) != 0 {
		t.Errorf("%d files left", len(des))
	}
}

func TestRefusesLoopbackAndNonHTTP(t *testing.T) {
	c, err := Open(t.TempDir(), 0, imagefetch.NewClient(false))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(t.Context(), "http://127.0.0.1:1/a.png"); err == nil {
		t.Error("loopback fetched")
	}
	if _, err := c.Get(t.Context(), "file:///etc/passwd"); err == nil {
		t.Error("file: fetched")
	}
}

func TestClearDuringDownloadStoresNothing(t *testing.T) {
	pic := pngOf(t, 4, 3)
	started, release := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		w.Write(pic)
	}))
	defer srv.Close()
	c := open(t, t.TempDir(), 0)
	done := make(chan error)
	go func() { _, err := c.Get(t.Context(), srv.URL+"/a.png"); done <- err }()
	<-started
	if err := c.Clear(); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if c.Cached(srv.URL+"/a.png") || c.Size() != 0 {
		t.Error("a download that began before Clear was stored")
	}
	if des, _ := os.ReadDir(c.dir); len(des) != 0 {
		t.Errorf("%d files left", len(des))
	}
}

func TestCloseCancelsDownloads(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()
	c := open(t, t.TempDir(), 0)
	go c.Get(context.Background(), srv.URL+"/a.png")
	<-started
	done := make(chan struct{})
	go func() { c.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close waited for a slow download")
	}
	if _, err := c.Get(t.Context(), srv.URL+"/b.png"); err != ErrClosed {
		t.Errorf("after Close: %v", err)
	}
}

func TestFailedMemoryIsBounded(t *testing.T) {
	c := open(t, t.TempDir(), 0)
	c.mu.Lock()
	for i := range 3 * maxFailed {
		c.rememberFailure(strconv.Itoa(i), errors.New("x"))
	}
	n := len(c.failed)
	c.mu.Unlock()
	if n > maxFailed {
		t.Errorf("%d failures remembered", n)
	}
}

func TestHostLimiterEntriesGoAway(t *testing.T) {
	c := open(t, t.TempDir(), 0)
	_, release := c.host("a.example")
	_, release2 := c.host("a.example")
	release()
	c.mu.Lock()
	n := len(c.hosts)
	c.mu.Unlock()
	if n != 1 {
		t.Fatalf("%d entries while one download remains", n)
	}
	release2()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.hosts) != 0 {
		t.Errorf("%d entries left", len(c.hosts))
	}
}

func TestHostKeyCanonical(t *testing.T) {
	for in, want := range map[string]string{
		"http://EXAMPLE.com:8080/x": "example.com", "http://example.com./x": "example.com",
		"http://[2606:4700:0000::1111]/x": "2606:4700::1111", "http://[2606:4700::1111]/x": "2606:4700::1111",
		"http://[::ffff:1.2.3.4]/x": "1.2.3.4",
	} {
		u, err := url.Parse(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := hostKey(u); got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
}

func TestFailureDuringClearIsNotRemembered(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			close(started)
			<-release
		}
		w.Write([]byte("<html>"))
	}))
	defer srv.Close()
	c := open(t, t.TempDir(), 0)
	done := make(chan struct{})
	go func() { c.Get(t.Context(), srv.URL+"/a.png"); close(done) }()
	<-started
	if err := c.Clear(); err != nil {
		t.Fatal(err)
	}
	close(release)
	<-done
	c.Get(t.Context(), srv.URL+"/a.png")
	if hits.Load() != 2 {
		t.Errorf("%d requests: the failure of a download begun before Clear was remembered", hits.Load())
	}
}

func TestGetAfterCloseFailsEvenWhenCached(t *testing.T) {
	pic := pngOf(t, 4, 3)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(pic) }))
	defer srv.Close()
	c := open(t, t.TempDir(), 0)
	if _, err := c.Get(t.Context(), srv.URL+"/a.png"); err != nil {
		t.Fatal(err)
	}
	c.Close()
	if _, err := c.Get(t.Context(), srv.URL+"/a.png"); err != ErrClosed {
		t.Errorf("cached, after Close: %v", err)
	}
}

func TestAvatarPersistenceIsolationAndClear(t *testing.T) {
	dir := t.TempDir()
	c := open(t, dir, 0)
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><path d="M0 0L16 16"/></svg>`)
	key := AvatarKey("favicon", "example.com")
	if _, err := c.PutAvatar(t.Context(), key, svg, "favicon", c.AvatarGeneration()); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := c.LookupAvatar(t.Context(), AvatarKey("gravatar", "example.com")); err != nil || ok {
		t.Fatalf("cross-source hit: %v", err)
	}
	if c.Cached("example.com") {
		t.Fatal("avatar entered ordinary cache")
	}
	miss := AvatarKey("favicon", "missing.example")
	if err := c.RememberAvatarMiss(miss, c.AvatarGeneration()); err != nil {
		t.Fatal(err)
	}
	c.Close()
	c = open(t, dir, 0)
	defer c.Close()
	img, ok, err := c.LookupAvatar(t.Context(), key)
	if err != nil || !ok || img.Format != "svg" || !bytes.Equal(img.Data, svg) || !c.AvatarMiss(miss) {
		t.Fatalf("restart: %v %v %+v", ok, err, img)
	}
	if c.Size() != int64(len(svg)) {
		t.Fatal("avatar not counted")
	}
	if err := c.Clear(); err != nil {
		t.Fatal(err)
	}
	if c.AvatarMiss(miss) || c.Size() != 0 {
		t.Fatal("clear retained avatar")
	}
	if files, _ := os.ReadDir(dir); len(files) != 0 {
		t.Fatal("clear retained files")
	}
}

func TestAvatarCacheRejectsSmallPersistedFavicon(t *testing.T) {
	dir := t.TempDir()
	key := AvatarKey("favicon", "small.example")
	name := fmt.Sprintf("%s-png-32x32-%d", key, time.Now().Unix())
	if err := os.WriteFile(filepath.Join(dir, name), pngOf(t, 16, 16), 0600); err != nil {
		t.Fatal(err)
	}
	c := open(t, dir, 0)
	defer c.Close()
	if _, ok, err := c.LookupAvatar(t.Context(), key); ok || err == nil {
		t.Fatalf("small cached favicon accepted: %v %v", ok, err)
	}
	if _, err := c.PutAvatar(t.Context(), key, pngOf(t, 16, 16), "favicon", c.AvatarGeneration()); err == nil {
		t.Fatal("small favicon written")
	}
}

func TestAvatarCacheTTLAndTransientFailure(t *testing.T) {
	for _, status := range []int{200, 404, 429, 500} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var hits atomic.Int64
			pic := pngOf(t, 32, 32)
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.WriteHeader(status)
				if status == 200 {
					w.Write(pic)
				}
			}))
			defer srv.Close()
			c, err := Open(t.TempDir(), 0, srv.Client())
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			for range 2 {
				c.GetAvatar(t.Context(), srv.URL, "gravatar", c.AvatarGeneration())
			}
			if hits.Load() != 1 {
				t.Fatalf("hits %d", hits.Load())
			}
			key := AvatarKey("gravatar", srv.URL)
			c.mu.Lock()
			if e := c.files[key]; e != nil {
				e.written = time.Now().Add(-25 * time.Hour)
			}
			if f, ok := c.failed[key]; ok {
				f.at = time.Now().Add(-6 * time.Minute)
				c.failed[key] = f
			}
			c.mu.Unlock()
			if _, ok, _ := c.LookupAvatar(t.Context(), key); ok {
				t.Fatal("expired entry returned")
			}
			c.GetAvatar(t.Context(), srv.URL, "gravatar", c.AvatarGeneration())
			if hits.Load() != 2 {
				t.Fatalf("expired cache did not refresh: %d", hits.Load())
			}
		})
	}
}

func TestAvatarFallbackSafety(t *testing.T) {
	c := open(t, t.TempDir(), 0)
	defer c.Close()
	key, gen := AvatarKey("favicon", "example.com"), c.AvatarGeneration()
	pic := pngOf(t, 32, 32)
	if _, err := c.PutAvatar(t.Context(), key, pic, "favicon", gen); err != nil {
		t.Fatal(err)
	}
	c.files[key].written = time.Now().Add(-25 * time.Hour)
	if img, ok, err := c.FallbackAvatar(t.Context(), key, gen); err != nil || !ok || !bytes.Equal(img.Data, pic) {
		t.Fatalf("ordinary fallback: %v %v", ok, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, ok, err := c.FallbackAvatar(ctx, key, gen); ok || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled fallback: %v %v", ok, err)
	}
	if err := os.WriteFile(filepath.Join(c.dir, c.files[key].name), pngOf(t, 16, 16), 0600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := c.FallbackAvatar(t.Context(), key, gen); ok || err == nil {
		t.Fatalf("small persisted fallback accepted: %v %v", ok, err)
	}
	if err := c.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PutAvatar(t.Context(), key, pic, "favicon", c.AvatarGeneration()); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := c.FallbackAvatar(t.Context(), key, gen); ok {
		t.Fatal("old discovery read a post-clear fallback")
	}
	bimi := AvatarKey("bimi", "example.com")
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" version="1.2" baseProfile="tiny-ps" viewBox="0 0 32 32"><title>Logo</title><path d="M0 0L32 0L32 32Z"/></svg>`)
	if _, err := c.PutAvatar(t.Context(), bimi, svg, "bimi", c.AvatarGeneration()); err != nil {
		t.Fatal(err)
	}
	c.files[bimi].written = time.Now().Add(-25 * time.Hour)
	if _, ok, _ := c.FallbackAvatar(t.Context(), bimi, c.AvatarGeneration()); ok {
		t.Fatal("BIMI fallback accepted")
	}
	c.Close()
	if _, ok, err := c.FallbackAvatar(t.Context(), key, c.AvatarGeneration()); ok || !errors.Is(err, ErrClosed) {
		t.Fatalf("closed fallback: %v %v", ok, err)
	}
}

func TestAvatarSingleflightClearAndClose(t *testing.T) {
	for _, action := range []string{"clear", "close"} {
		t.Run(action, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			var hits atomic.Int64
			pic := pngOf(t, 32, 32)
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if hits.Add(1) == 1 {
					close(started)
				}
				select {
				case <-release:
					w.Write(pic)
				case <-r.Context().Done():
				}
			}))
			defer srv.Close()
			c, err := Open(t.TempDir(), 0, srv.Client())
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			var wg sync.WaitGroup
			for range 6 {
				wg.Add(1)
				go func() { defer wg.Done(); c.GetAvatar(t.Context(), srv.URL, "favicon", c.AvatarGeneration()) }()
			}
			<-started
			if action == "clear" {
				if err := c.Clear(); err != nil {
					t.Fatal(err)
				}
				close(release)
			} else {
				c.Close()
			}
			wg.Wait()
			if hits.Load() != 1 || c.Size() != 0 {
				t.Fatalf("hits=%d size=%d", hits.Load(), c.Size())
			}
			if action == "close" {
				if _, _, err := c.LookupAvatar(t.Context(), AvatarKey("favicon", srv.URL)); err != ErrClosed {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestAvatarFastSingleflight(t *testing.T) {
	pic := pngOf(t, 32, 32)
	var hits atomic.Int64
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.Write(pic) }))
	defer srv.Close()
	c, err := Open(t.TempDir(), 0, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.GetAvatar(t.Context(), srv.URL, "favicon", c.AvatarGeneration()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if hits.Load() != 1 {
		t.Fatalf("completed flight was downloaded again: %d requests", hits.Load())
	}
}

func TestAvatarOrdinaryGetRejectsICOAndSVG(t *testing.T) {
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32"><path d="M0 0L32 32"/></svg>`)
	// ICO containing one PNG; directory dimensions are 32x32.
	pic := pngOf(t, 32, 32)
	ico := make([]byte, 22)
	binary.LittleEndian.PutUint16(ico[2:], 1)
	binary.LittleEndian.PutUint16(ico[4:], 1)
	ico[6], ico[7] = 32, 32
	binary.LittleEndian.PutUint32(ico[14:], uint32(len(pic)))
	binary.LittleEndian.PutUint32(ico[18:], 22)
	ico = append(ico, pic...)
	for _, data := range [][]byte{svg, ico} {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(data) }))
		c, err := Open(t.TempDir(), 0, srv.Client())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.GetAvatar(t.Context(), srv.URL, "favicon", c.AvatarGeneration()); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Get(t.Context(), srv.URL); !errors.Is(err, imagefetch.ErrFormat) {
			t.Fatalf("ordinary Get accepted avatar: %v", err)
		}
		if _, _, ok := c.Dims(srv.URL); ok {
			t.Fatal("ordinary Dims found avatar")
		}
		c.Close()
		srv.Close()
	}
}

func TestAvatarPersistentTTLUsesWrittenNotLRU(t *testing.T) {
	dir := t.TempDir()
	pic := pngOf(t, 32, 32)
	for _, kind := range []string{"png", "miss"} {
		key := AvatarKey("favicon", kind)
		size := "32x32"
		data := pic
		if kind == "miss" {
			size = "0x0"
			data = nil
		}
		name := fmt.Sprintf("%s-%s-%s-%d", key, kind, size, time.Now().Add(-25*time.Hour).Unix())
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	c := open(t, dir, 0)
	defer c.Close()
	if _, ok, err := c.LookupAvatar(t.Context(), AvatarKey("favicon", "png")); ok || err != nil {
		t.Fatalf("old image revived by mtime: %v", err)
	}
	if c.AvatarMiss(AvatarKey("favicon", "miss")) {
		t.Fatal("old miss revived by mtime")
	}
}

func TestAvatarMissCapacityAndTotalEviction(t *testing.T) {
	dir := t.TempDir()
	c := open(t, dir, 0)
	for i := range 1002 {
		if err := c.RememberAvatarMiss(AvatarKey("favicon", strconv.Itoa(i)), c.AvatarGeneration()); err != nil {
			t.Fatal(err)
		}
	}
	if files, _ := os.ReadDir(dir); len(files) != 1000 {
		t.Fatalf("%d persisted misses", len(files))
	}
	c.Close()
	c = open(t, dir, 0)
	if len(c.files) != 1000 {
		t.Fatal("miss capacity changed on restart")
	}
	c.Close()
	c = open(t, t.TempDir(), 1)
	defer c.Close()
	if _, err := c.PutAvatar(t.Context(), AvatarKey("favicon", "large"), pngOf(t, 32, 32), "favicon", c.AvatarGeneration()); err != nil {
		t.Fatal(err)
	}
	if c.Size() != 0 {
		t.Fatal("avatar bypassed total limit")
	}
}

func TestAvatarFailurePersistenceByKind(t *testing.T) {
	for _, status := range []int{200, 404, 429, 500} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var hits atomic.Int64
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.WriteHeader(status)
				w.Write([]byte("<html>not an image</html>"))
			}))
			defer srv.Close()
			dir := t.TempDir()
			c, err := Open(dir, 0, srv.Client())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.GetAvatar(t.Context(), srv.URL, "gravatar", c.AvatarGeneration()); err == nil {
				t.Fatal("bad image accepted")
			}
			c.Close()
			c, err = Open(dir, 0, srv.Client())
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			_, err = c.GetAvatar(t.Context(), srv.URL, "gravatar", c.AvatarGeneration())
			want := int64(2)
			if status == 200 || status == 404 {
				want = 1
				if !errors.Is(err, imagefetch.ErrAvatarMiss) {
					t.Fatalf("lost deterministic miss: %v", err)
				}
			}
			if hits.Load() != want {
				t.Fatalf("hits=%d want=%d", hits.Load(), want)
			}
		})
	}
}

func TestAvatarFailureDuringClearIsNotRemembered(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var hits atomic.Int64
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			close(started)
			<-release
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	c, err := Open(t.TempDir(), 0, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	done := make(chan struct{})
	go func() { c.GetAvatar(t.Context(), srv.URL, "gravatar", c.AvatarGeneration()); close(done) }()
	<-started
	if err := c.Clear(); err != nil {
		t.Fatal(err)
	}
	close(release)
	<-done
	if c.AvatarMiss(AvatarKey("gravatar", srv.URL)) {
		t.Fatal("old flight restored miss after clear")
	}
	c.GetAvatar(t.Context(), srv.URL, "gravatar", c.AvatarGeneration())
	if hits.Load() != 2 {
		t.Fatal("old flight restored failure after clear")
	}
}

func TestAvatarFirstDeadlineCancelsSharedDownload(t *testing.T) {
	started, canceled := make(chan struct{}), make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(canceled)
	}))
	defer srv.Close()
	c, err := Open(t.TempDir(), 0, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	first := make(chan error, 1)
	go func() { _, err := c.GetAvatar(ctx, srv.URL, "favicon", c.AvatarGeneration()); first <- err }()
	<-started
	second := make(chan error, 1)
	go func() { _, err := c.GetAvatar(t.Context(), srv.URL, "favicon", c.AvatarGeneration()); second <- err }()
	if err := <-first; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first caller: %v", err)
	}
	select {
	case <-canceled:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("actual download survived first deadline")
	}
	select {
	case err := <-second:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("shared caller: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("shared caller did not inherit first deadline")
	}
	if c.AvatarMiss(AvatarKey("favicon", srv.URL)) {
		t.Fatal("deadline became persistent miss")
	}
}

func TestOrdinaryDeadlineLeavesSharedDownloadRunning(t *testing.T) {
	started, release, canceled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	pic := pngOf(t, 4, 3)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-release:
			w.Write(pic)
		case <-r.Context().Done():
			close(canceled)
		}
	}))
	defer srv.Close()
	c, err := Open(t.TempDir(), 0, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := c.Get(ctx, srv.URL); done <- err }()
	<-started
	if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ordinary caller: %v", err)
	}
	select {
	case <-canceled:
		t.Fatal("ordinary download inherited caller deadline")
	default:
	}
	close(release)
	img, err := c.Get(t.Context(), srv.URL)
	if err != nil || !bytes.Equal(img.Data, pic) {
		t.Fatalf("background download did not finish: %v", err)
	}
}

func TestAvatarDiscoveryGeneration(t *testing.T) {
	var hits atomic.Int64
	pic := pngOf(t, 32, 32)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.Write(pic) }))
	defer srv.Close()
	c, err := Open(t.TempDir(), 0, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	gen := c.AvatarGeneration()
	if err := c.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetAvatar(t.Context(), srv.URL, "favicon", gen); err == nil {
		t.Fatal("old discovery started a post-clear candidate")
	}
	key := AvatarKey("favicon", "example.com")
	if _, err := c.PutAvatar(t.Context(), key, pic, "favicon", gen); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := c.LookupAvatar(t.Context(), key); err != nil || ok {
		t.Fatalf("old discovery stored success: %v %v", ok, err)
	}
	if err := c.RememberAvatarMiss(key, gen); err != nil {
		t.Fatal(err)
	}
	if c.AvatarMiss(key) || c.Size() != 0 || hits.Load() != 0 {
		t.Fatal("old discovery repopulated cache or started network")
	}
	if _, err := c.GetAvatar(t.Context(), srv.URL, "favicon", c.AvatarGeneration()); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Fatalf("fresh discovery requests = %d", hits.Load())
	}
}
