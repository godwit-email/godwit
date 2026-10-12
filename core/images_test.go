package core

import (
	"bytes"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrefetchImagesOnArrival(t *testing.T) {
	var pic bytes.Buffer
	require.NoError(t, png.Encode(&pic, image.NewRGBA(image.Rect(0, 0, 8, 6))))
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write(pic.Bytes())
	}))
	defer srv.Close()
	body := func(name string) string {
		return `<p>hi</p><img src="` + srv.URL + `/` + name + `.png" width="300">` +
			`<img src="` + srv.URL + `/pixel.png" width="1" height="1">` +
			`<div style="background:url(` + srv.URL + `/` + name + `-bg.png)">x</div>`
	}
	e := newSyncEnv(t)
	require.NoError(t, e.im.User.Create("Junk", nil))
	e.sync(t) // the first sync is no arrival
	e.put(t, "Junk", htmlEml("j@x", body("junk")))
	e.put(t, "INBOX", htmlEml("a@x", body("a")))
	e.sync(t)
	cache := e.s.ImageCache()
	require.Eventually(t, func() bool { return cache.Cached(srv.URL+"/a.png") && cache.Cached(srv.URL+"/a-bg.png") }, 5*time.Second, 10*time.Millisecond)
	w, h, ok := cache.Dims(srv.URL + "/a.png")
	assert.True(t, ok && w == 8 && h == 6)
	assert.False(t, cache.Cached(srv.URL+"/pixel.png"), "a tracking pixel is not requested")
	assert.False(t, cache.Cached(srv.URL+"/junk.png"), "nothing from Junk")
	assert.EqualValues(t, 2, hits.Load())
	assert.Positive(t, e.s.ImageCacheSize())
	require.NoError(t, e.s.cleanOrphans(bg)) // a restart's sweep keeps the image cache
	assert.True(t, cache.Cached(srv.URL+"/a.png"))
	_, err := os.Stat(filepath.Join(e.dir, "cache", "images"))
	require.NoError(t, err)
	files, _ := os.ReadDir(filepath.Join(e.dir, "cache", "images"))
	assert.Len(t, files, 2)
	require.NoError(t, e.s.ClearImageCache())
	assert.Zero(t, e.s.ImageCacheSize())

	// "ask": nothing is fetched at arrival.
	st, err := e.s.GetSettings(bg)
	require.NoError(t, err)
	st.RemoteImages = "ask"
	_, err = e.s.SetSettings(bg, st)
	require.NoError(t, err)
	e.put(t, "INBOX", htmlEml("b@x", body("b")))
	e.sync(t)
	time.Sleep(300 * time.Millisecond)
	assert.False(t, cache.Cached(srv.URL+"/b.png"))
}

func htmlEml(id, body string) string {
	return strings.Replace(eml(id, "bob@example.com", "alice@example.com", "s", day, body), "text/plain", "text/html", 1)
}
