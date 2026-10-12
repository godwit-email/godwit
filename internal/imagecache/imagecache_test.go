package imagecache

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
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
