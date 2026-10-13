package core

import (
	"bytes"
	"fmt"
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

	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/internal/imagecache"
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

func TestPrefetchAvatarOnArrival(t *testing.T) {
	for _, tc := range []struct {
		name, html, mode string
		junk, want       bool
	}{
		{"plaintext", "", "auto", false, true},
		{"HTML body failure", `<img src="https://body.example.com/broken">`, "auto", false, true},
		{"body count limit", func() string {
			var b strings.Builder
			for i := 0; i < prefetchImages+1; i++ {
				fmt.Fprintf(&b, `<img src="https://body.example.com/broken%d">`, i)
			}
			return b.String()
		}(), "auto", false, true},
		{"ask", "", "ask", false, false},
		{"junk", "", "auto", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			good := avatarPNG(t)
			bodyReached := make(chan struct{}, 1)
			f := newAvatarFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Host == "body.example.com" {
					select {
					case bodyReached <- struct{}{}:
					default:
					}
					http.Error(w, "failed", 500)
					return
				}
				w.Write(good)
			})
			f.policy(t, tc.mode, false, tc.junk)
			id := addMessage(t, f.s, "s", "bob@example.com", "plain", tc.html, 1)
			f.s.bus.Publish(events.Event{Type: events.MessageReceived, MessageIDs: []int64{id}})
			if tc.want {
				require.Eventually(t, func() bool {
					_, ok, _ := f.s.images.LookupAvatar(bg, imagecache.AvatarKey("favicon", "example.com"))
					return ok
				}, 2*time.Second, 10*time.Millisecond)
				if tc.html != "" {
					select {
					case <-bodyReached:
					case <-time.After(time.Second):
						t.Fatal("body request missing")
					}
					wantBodies := 1
					if tc.name == "body count limit" {
						wantBodies = prefetchImages
					}
					require.Eventually(t, func() bool { return len(f.hits()) == 1+wantBodies }, time.Second, 10*time.Millisecond)
				}
				before := f.hits()
				a, err := f.s.SenderAvatar(t.Context(), id, false)
				require.NoError(t, err)
				assert.NotEmpty(t, a.Data)
				assert.Equal(t, before, f.hits(), "reading a prefetched avatar does not request the site again")
			} else {
				time.Sleep(100 * time.Millisecond)
				assert.Empty(t, f.hits())
			}
		})
	}
}

func TestPrefetchAvatarBodyByteLimit(t *testing.T) {
	good := avatarPNG(t)
	large := append(append([]byte(nil), good...), make([]byte, (4<<20)-len(good))...)
	f := newAvatarFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "body.example.com" {
			w.Write(large)
		} else {
			w.Write(good)
		}
	})
	var body strings.Builder
	for i := 0; i < 5; i++ {
		fmt.Fprintf(&body, `<img src="https://body.example.com/%d">`, i)
	}
	id := addMessage(t, f.s, "s", "bob@example.com", "", body.String(), 1)
	f.s.prefetchImages(t.Context(), id)
	want := []string{"example.com/apple-touch-icon.png", "body.example.com/0", "body.example.com/1", "body.example.com/2", "body.example.com/3"}
	assert.Equal(t, want, f.hits(), "avatar precedes body; 16 MiB stops the fifth body request")
	a, err := f.s.SenderAvatar(t.Context(), id, false)
	require.NoError(t, err)
	assert.NotEmpty(t, a.Data)
	assert.Equal(t, want, f.hits())
}

func TestPrefetchAvatarBodyReadFailure(t *testing.T) {
	good := avatarPNG(t)
	f := newAvatarFixture(t, func(w http.ResponseWriter, r *http.Request) { w.Write(good) })
	id := addMessage(t, f.s, "s", "bob@example.com", "plain", "", 1)
	// Fail only the body read; sender/account data remains available.
	_, err := f.s.db.Execute(t.Context(), "ALTER TABLE messages DROP COLUMN body_html")
	require.NoError(t, err)
	f.s.prefetchImages(t.Context(), id)
	assert.Equal(t, []string{"example.com/apple-touch-icon.png"}, f.hits())
	a, err := f.s.SenderAvatar(t.Context(), id, false)
	require.NoError(t, err)
	assert.NotEmpty(t, a.Data)
	assert.Len(t, f.hits(), 1, "avatar survived the independent body read failure")
}

func TestAvatarCancellationPrefetchBodyAfterAccountDelete(t *testing.T) {
	good := avatarPNG(t)
	started, release := make(chan struct{}), make(chan struct{})
	f := newAvatarFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "body.example.com" {
			close(started)
			<-release
		}
		w.Write(good)
	})
	defer close(release)
	id := addMessage(t, f.s, "s", "bob@example.com", "", `<img src="https://body.example.com/one"><img src="https://body.example.com/two">`, 1)
	done := make(chan struct{})
	go func() { f.s.prefetchImages(bg, id); close(done) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("body never started")
	}
	require.NoError(t, f.s.RemoveAccount(t.Context(), 1))
	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("account deletion did not cancel the message prefetch worker")
	}
	assert.Equal(t, []string{"example.com/apple-touch-icon.png", "body.example.com/one"}, f.hits(), "no subsequent body request after deleting its account")
}
