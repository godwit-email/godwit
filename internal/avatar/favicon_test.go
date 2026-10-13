package avatar

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godwit-email/godwit/internal/imagecache"
	"github.com/godwit-email/godwit/internal/imagefetch"
)

type faviconFixture struct {
	client *http.Client
	cache  *imagecache.Cache
	dir    string
	mu     sync.Mutex
	paths  []string
}

func faviconPNG(t *testing.T, size int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, size, size))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func newFaviconFixture(t *testing.T, handler http.HandlerFunc) *faviconFixture {
	t.Helper()
	f := &faviconFixture{dir: t.TempDir()}
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.Path)
		f.mu.Unlock()
		if r.Header.Get("Cookie") != "" || r.Header.Get("Referer") != "" || r.Header.Get("User-Agent") != imagefetch.UserAgent {
			t.Errorf("unsafe request headers: %v", r.Header)
		}
		if r.URL.Path == "/" && !strings.Contains(r.Header.Get("Accept"), "text/html") {
			t.Error("homepage lacks HTML Accept")
		}
		handler(w, r)
	}))
	t.Cleanup(s.Close)
	u, _ := url.Parse(s.URL)
	tr := s.Client().Transport.(*http.Transport).Clone()
	tr.TLSClientConfig.ServerName = u.Hostname()
	public := imagefetch.NewClient(false).Transport.(*http.Transport)
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, _ := net.SplitHostPort(addr)
		if host == "example.com" || host == "mail.example.co.uk" || host == "example.co.uk" || host == "cdn.example.com" {
			return (&net.Dialer{}).DialContext(ctx, network, u.Host)
		}
		return public.DialContext(ctx, network, addr)
	}
	f.client = &http.Client{Transport: tr}
	t.Cleanup(tr.CloseIdleConnections)
	f.reopen(t)
	return f
}

func (f *faviconFixture) reopen(t *testing.T) {
	t.Helper()
	if f.cache != nil {
		f.cache.Close()
	}
	var err error
	f.cache, err = imagecache.Open(f.dir, 0, f.client)
	if err != nil {
		t.Fatal(err)
	}
	c := f.cache
	t.Cleanup(c.Close)
}

func (f *faviconFixture) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.paths...)
}

func TestFaviconOrderRelativeRedirectAndRestart(t *testing.T) {
	small, good := faviconPNG(t, 16), faviconPNG(t, 32)
	f := newFaviconFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			http.Redirect(w, r, "/site/home", http.StatusFound)
		case "/site/home":
			w.Write([]byte(`<link href="small.png" sizes="128x128" rel="ICON"><link type="image/png" rel="alternate apple-touch-icon" href="../good.png">`))
		case "/site/small.png":
			w.Write(small)
		case "/good.png":
			w.Write(good)
		default:
			http.NotFound(w, r)
		}
	})
	img, ok := FindFavicon(context.Background(), f.client, f.cache, "EXAMPLE.COM.", nil)
	if !ok || img.Width != 32 || !bytes.Equal(img.Data, good) {
		t.Fatalf("favicon = %+v, %v", img, ok)
	}
	want := []string{"/apple-touch-icon.png", "/", "/site/home", "/site/small.png", "/good.png"}
	if got := f.requests(); !reflect.DeepEqual(got, want) {
		t.Fatalf("requests = %v, want %v", got, want)
	}
	f.reopen(t)
	img, ok = FindFavicon(context.Background(), f.client, f.cache, "example.com", nil)
	if !ok || img.Width != 32 || !reflect.DeepEqual(f.requests(), want) {
		t.Fatal("site success was not reused after restart")
	}
}

func TestFaviconAppleStopsDiscovery(t *testing.T) {
	good := faviconPNG(t, 32)
	f := newFaviconFixture(t, func(w http.ResponseWriter, r *http.Request) { w.Write(good) })
	if _, ok := FindFavicon(context.Background(), f.client, f.cache, "example.com", nil); !ok {
		t.Fatal("missing apple icon")
	}
	if got := f.requests(); !reflect.DeepEqual(got, []string{"/apple-touch-icon.png"}) {
		t.Fatalf("extra requests: %v", got)
	}
}

func TestFaviconFallbackUsesRedirectedOrigin(t *testing.T) {
	good := faviconPNG(t, 32)
	f := newFaviconFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Host == "example.com" && r.URL.Path == "/":
			http.Redirect(w, r, "https://cdn.example.com/home", http.StatusFound)
		case r.Host == "cdn.example.com" && r.URL.Path == "/home":
			w.Write([]byte("<html></html>"))
		case r.Host == "cdn.example.com" && r.URL.Path == "/favicon.ico":
			w.Write(good)
		default:
			http.NotFound(w, r)
		}
	})
	if img, ok := FindFavicon(t.Context(), f.client, f.cache, "example.com", nil); !ok || !bytes.Equal(img.Data, good) {
		t.Fatal("fallback did not use the redirected homepage origin")
	}
}

func TestFaviconCoalescesConcurrentDiscovery(t *testing.T) {
	good := faviconPNG(t, 32)
	pages, release := make(chan struct{}, 20), make(chan struct{})
	f := newFaviconFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			pages <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			w.Write([]byte(`<link rel="icon" href="/logo.png">`))
		case "/logo.png":
			w.Write(good)
		default:
			http.NotFound(w, r)
		}
	})
	var unblock sync.Once
	t.Cleanup(func() { unblock.Do(func() { close(release) }) })
	var callers sync.WaitGroup
	callers.Add(1)
	go func() {
		defer callers.Done()
		if _, ok := FindFavicon(t.Context(), f.client, f.cache, "example.com", nil); !ok {
			t.Error("first caller lost favicon")
		}
	}()
	select {
	case <-pages:
	case <-time.After(time.Second):
		t.Fatal("homepage request missing")
	}
	for range 10 {
		callers.Add(1)
		go func() {
			defer callers.Done()
			if _, ok := FindFavicon(t.Context(), f.client, f.cache, "EXAMPLE.COM.", nil); !ok {
				t.Error("shared caller lost favicon")
			}
		}()
	}
	ctx, cancel := context.WithCancel(t.Context())
	joined, cancelled := make(chan struct{}), make(chan struct{})
	var once sync.Once
	go func() {
		defer close(cancelled)
		if _, ok := FindFavicon(ctx, f.client, f.cache, "example.com", func() bool { once.Do(func() { close(joined) }); return true }); ok {
			t.Error("cancelled follower received favicon")
		}
	}()
	<-joined
	cancel()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Error("cancelled follower waited for the homepage")
	}
	var allowed atomic.Bool
	allowed.Store(true)
	checked, policyDone := make(chan struct{}), make(chan struct{})
	var policyOnce sync.Once
	go func() {
		defer close(policyDone)
		if _, ok := FindFavicon(t.Context(), f.client, f.cache, "example.com", func() bool {
			ok := allowed.Load()
			policyOnce.Do(func() { close(checked) })
			return ok
		}); ok {
			t.Error("shared favicon bypassed a follower's revoked policy")
		}
	}()
	<-checked
	allowed.Store(false)
	select {
	case <-pages:
		t.Error("simultaneous duplicate homepage request")
	case <-time.After(100 * time.Millisecond):
	}
	unblock.Do(func() { close(release) })
	callers.Wait()
	<-policyDone
	var homes int
	for _, path := range f.requests() {
		if path == "/" {
			homes++
		}
	}
	if homes != 1 {
		t.Fatalf("homepage requests = %d, want 1", homes)
	}
}

func TestFaviconFollowerRestartsAfterOwnerDrops(t *testing.T) {
	good := faviconPNG(t, 32)
	for _, policy := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "policy"}[policy], func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			var once, releaseOnce sync.Once
			var homes atomic.Int32
			var ownerAllowed atomic.Bool
			ownerAllowed.Store(true)
			f := newFaviconFixture(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/":
					if homes.Add(1) == 1 {
						once.Do(func() { close(started) })
						if policy {
							<-release
						} else {
							<-r.Context().Done()
							return
						}
					}
					w.Write([]byte(`<link rel="icon" href="/logo.png">`))
				case "/logo.png":
					w.Write(good)
				default:
					http.NotFound(w, r)
				}
			})
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
			ownerCtx, ownerCancel := context.WithCancel(t.Context())
			t.Cleanup(ownerCancel)
			ownerDone := make(chan bool, 1)
			go func() {
				_, ok := FindFavicon(ownerCtx, f.client, f.cache, "example.com", func() bool { return ownerAllowed.Load() })
				ownerDone <- ok
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("homepage request missing")
			}
			followerDone := make(chan imagecache.Image, 1)
			go func() {
				img, ok := FindFavicon(t.Context(), f.client, f.cache, "example.com", func() bool { return true })
				if ok {
					followerDone <- img
				} else {
					followerDone <- imagecache.Image{}
				}
			}()
			waitForFaviconFollower(t, f.cache, "example.com")
			if policy {
				ownerAllowed.Store(false)
				releaseOnce.Do(func() { close(release) })
			} else {
				ownerCancel()
			}
			select {
			case ok := <-ownerDone:
				if ok {
					t.Fatal("dropped owner received a favicon")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("dropped owner did not return")
			}
			select {
			case img := <-followerDone:
				if len(img.Data) == 0 || !bytes.Equal(img.Data, good) {
					t.Fatal("follower lost the favicon after the owner dropped")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("follower did not rediscover")
			}
			if homes.Load() < 2 {
				t.Fatalf("follower did not rediscover: homepages=%d", homes.Load())
			}
			if f.cache.AvatarMiss(imagecache.AvatarKey("favicon", "example.com")) {
				t.Fatal("owner drop recorded a site miss")
			}
		})
	}
}

func waitForFaviconFollower(t *testing.T, cache *imagecache.Cache, host string) {
	t.Helper()
	key := struct {
		cache *imagecache.Cache
		host  string
		gen   int
	}{cache, host, cache.AvatarGeneration()}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if v, ok := faviconFlights.Load(key); ok && v.(*faviconFlight).waiters.Load() > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("follower did not join favicon discovery")
}

func TestFaviconStaleFallback(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			good := faviconPNG(t, 32)
			f := newFaviconFixture(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) })
			key := imagecache.AvatarKey("favicon", "example.com")
			name := fmt.Sprintf("%s-png-32x32-%d", key, time.Now().Add(-25*time.Hour).Unix())
			if err := os.WriteFile(filepath.Join(f.dir, name), good, 0600); err != nil {
				t.Fatal(err)
			}
			f.reopen(t)
			img, ok := FindFavicon(t.Context(), f.client, f.cache, "example.com", nil)
			if ok != (status != http.StatusNotFound) || ok && !bytes.Equal(img.Data, good) {
				t.Fatalf("stale fallback: found=%v bytes=%d", ok, len(img.Data))
			}
			if _, fresh, _ := f.cache.LookupAvatar(t.Context(), key); fresh {
				t.Fatal("failed refresh reset the freshness deadline")
			}
			if _, ok := FindFavicon(t.Context(), f.client, f.cache, "example.com", func() bool { return false }); ok {
				t.Fatal("stale avatar bypassed policy")
			}
		})
	}
}

func TestFaviconPolicyBeforeCachedSite(t *testing.T) {
	good := faviconPNG(t, 32)
	f := newFaviconFixture(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected request") })
	_, err := f.cache.PutAvatar(t.Context(), imagecache.AvatarKey("favicon", "example.com"), good, "favicon", f.cache.AvatarGeneration())
	if err != nil {
		t.Fatal(err)
	}
	img, ok := FindFavicon(t.Context(), f.client, f.cache, "example.com", func() bool { return false })
	if ok || len(img.Data) > 0 {
		t.Fatal("policy denied but cached site was returned")
	}
	if got := f.requests(); len(got) != 0 {
		t.Fatalf("requests = %v", got)
	}
}

func TestFaviconPolicyBeforeCachedCandidate(t *testing.T) {
	good := faviconPNG(t, 32)
	var allowed atomic.Bool
	allowed.Store(true)
	f := newFaviconFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			allowed.Store(false)
			w.Write([]byte(`<link rel="icon" href="/cached.png">`))
		} else {
			http.NotFound(w, r)
		}
	})
	_, err := f.cache.PutAvatar(t.Context(), imagecache.AvatarKey("favicon", "https://example.com/cached.png"), good, "favicon", f.cache.AvatarGeneration())
	if err != nil {
		t.Fatal(err)
	}
	img, ok := FindFavicon(t.Context(), f.client, f.cache, "example.com", allowed.Load)
	if ok || len(img.Data) > 0 {
		t.Fatal("cached candidate bypassed the changed policy")
	}
	want := []string{"/apple-touch-icon.png", "/"}
	if got := f.requests(); !reflect.DeepEqual(got, want) {
		t.Fatalf("requests = %v, want %v", got, want)
	}
	if f.cache.AvatarMiss(imagecache.AvatarKey("favicon", "example.com")) {
		t.Fatal("policy denial recorded a site miss")
	}
}

func TestFaviconNoDeclarationStillTriesICO(t *testing.T) {
	good := faviconPNG(t, 32)
	for _, status := range []int{http.StatusOK, http.StatusNotFound, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := newFaviconFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/favicon.ico" {
					w.Write(good)
					return
				}
				if r.URL.Path == "/" {
					w.WriteHeader(status)
					w.Write([]byte("<html></html>"))
					return
				}
				http.NotFound(w, r)
			})
			if _, ok := FindFavicon(context.Background(), f.client, f.cache, "example.com", nil); !ok {
				t.Fatal("ICO fallback missed")
			}
			if got := f.requests(); !reflect.DeepEqual(got, []string{"/apple-touch-icon.png", "/", "/favicon.ico"}) {
				t.Fatalf("requests = %v", got)
			}
		})
	}
}

func TestFaviconDeclarationLimitAndUnsafeURLs(t *testing.T) {
	f := newFaviconFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.Write([]byte(`<link rel="icon" href="http://example.com/downgrade"><link rel="icon" href="javascript:alert(1)"><link rel="icon" href="https://user:pass@example.com/credentials"><link rel="icon" href="data:image/png;base64,AA"><link rel="icon" href="/one"><link href="/two" rel="apple-touch-icon-precomposed"><link rel="icon" href="/three"><link rel="icon" href="/four">`))
			return
		}
		http.NotFound(w, r)
	})
	if _, ok := FindFavicon(context.Background(), f.client, f.cache, "example.com", nil); ok {
		t.Fatal("expected miss")
	}
	want := []string{"/apple-touch-icon.png", "/", "/one", "/two", "/three", "/favicon.ico"}
	if got := f.requests(); !reflect.DeepEqual(got, want) {
		t.Fatalf("requests = %v, want %v", got, want)
	}
}

func TestFaviconLimitedCompleteHTML(t *testing.T) {
	for _, tt := range []struct {
		name, html string
		want       []string
	}{
		{"complete", `<link rel="icon" href="/first">` + strings.Repeat(" ", 256<<10) + `<link rel="icon" href="/late">`, []string{"/apple-touch-icon.png", "/", "/first", "/favicon.ico"}},
		{"partial", strings.Repeat(" ", (256<<10)-len(`<link rel="icon" href="/partial"`)) + `<link rel="icon" href="/partial">`, []string{"/apple-touch-icon.png", "/", "/favicon.ico"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFaviconFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/" {
					w.Write([]byte(tt.html))
					return
				}
				http.NotFound(w, r)
			})
			FindFavicon(context.Background(), f.client, f.cache, "example.com", nil)
			if got := f.requests(); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("requests = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFaviconMissRestartAndTransient(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := newFaviconFixture(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) })
			if _, ok := FindFavicon(context.Background(), f.client, f.cache, "example.com", nil); ok {
				t.Fatal("expected miss")
			}
			key := imagecache.AvatarKey("favicon", "example.com")
			if f.cache.AvatarMiss(key) != (status == http.StatusNotFound) {
				t.Fatal("incorrect persistent miss")
			}
			n := len(f.requests())
			f.reopen(t)
			FindFavicon(context.Background(), f.client, f.cache, "example.com", nil)
			if (len(f.requests()) == n) != (status == http.StatusNotFound) {
				t.Fatal("incorrect restart retry")
			}
		})
	}
}

func TestFaviconAllTooSmallAndOrganizationalFallback(t *testing.T) {
	small, good := faviconPNG(t, 16), faviconPNG(t, 32)
	f := newFaviconFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "mail.example.co.uk" {
			w.Write(small)
			return
		}
		w.Write(good)
	})
	var ok bool
	var img imagecache.Image
	for _, site := range FaviconSites("mail.example.co.uk") {
		img, ok = FindFavicon(context.Background(), f.client, f.cache, site, nil)
		if ok {
			break
		}
	}
	if !ok || img.Width != 32 {
		t.Fatal("organizational fallback failed")
	}
	if !f.cache.AvatarMiss(imagecache.AvatarKey("favicon", "mail.example.co.uk")) {
		t.Fatal("small site not recorded as miss")
	}
}

func TestFaviconCancellationNoPersistentMiss(t *testing.T) {
	f := newFaviconFixture(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, ok := FindFavicon(ctx, f.client, f.cache, "example.com", nil); ok {
		t.Fatal("canceled call returned image")
	}
	if f.cache.AvatarMiss(imagecache.AvatarKey("favicon", "example.com")) {
		t.Fatal("cancellation became permanent miss")
	}
	// Closing the cache also cancels outstanding requests, including a failed implementation.
	f.cache.Close()
}

func TestFaviconHomepageReadLimitClosesStream(t *testing.T) {
	closed := make(chan struct{})
	f := newFaviconFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.Write([]byte(strings.Repeat(" ", 256<<10)))
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			close(closed)
			return
		}
		http.NotFound(w, r)
	})
	FindFavicon(t.Context(), f.client, f.cache, "example.com", nil)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("homepage read beyond limit or body remained open")
	}
	if got := f.requests(); !reflect.DeepEqual(got, []string{"/apple-touch-icon.png", "/", "/favicon.ico"}) {
		t.Fatalf("requests = %v", got)
	}
}

func TestFaviconSiteDeadlineStopsActualRequest(t *testing.T) {
	canceled := make(chan struct{})
	f := newFaviconFixture(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done(); close(canceled) })
	start := time.Now()
	if _, ok := FindFavicon(t.Context(), f.client, f.cache, "example.com", nil); ok {
		t.Fatal("slow request succeeded")
	}
	if elapsed := time.Since(start); elapsed < 4*time.Second || elapsed > 6*time.Second {
		t.Fatalf("site deadline elapsed %v", elapsed)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("site request survived deadline")
	}
	if f.cache.AvatarMiss(imagecache.AvatarKey("favicon", "example.com")) {
		t.Fatal("site timeout became permanent miss")
	}
	if got := f.requests(); !reflect.DeepEqual(got, []string{"/apple-touch-icon.png"}) {
		t.Fatalf("requests continued after deadline: %v", got)
	}
}

func TestFaviconHTTPSRedirectPoliciesAndPrivateHost(t *testing.T) {
	for _, target := range []string{"http://example.com/evil", "https://user:pass@example.com/evil", "https://127.0.0.1/evil", "https://10.0.0.1/evil"} {
		t.Run(target, func(t *testing.T) {
			f := newFaviconFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/" {
					http.Redirect(w, r, target, http.StatusFound)
					return
				}
				http.NotFound(w, r)
			})
			if _, ok := FindFavicon(t.Context(), f.client, f.cache, "example.com", nil); ok {
				t.Fatal("unsafe redirect accepted")
			}
			if got := f.requests(); !reflect.DeepEqual(got, []string{"/apple-touch-icon.png", "/", "/favicon.ico"}) {
				t.Fatalf("unsafe redirect requested: %v", got)
			}
			if f.cache.AvatarMiss(imagecache.AvatarKey("favicon", "example.com")) {
				t.Fatal("failed homepage incorrectly became permanent miss")
			}
		})
	}
}

func TestFaviconInvalidSiteNeverRequests(t *testing.T) {
	f := newFaviconFixture(t, func(w http.ResponseWriter, r *http.Request) { t.Error("invalid site requested") })
	for _, host := range []string{"127.0.0.1", "localhost", "https://example.com", "user@example.com", "co.uk"} {
		if _, ok := FindFavicon(t.Context(), f.client, f.cache, host, nil); ok {
			t.Errorf("invalid site accepted: %s", host)
		}
	}
	if len(f.requests()) != 0 {
		t.Fatal("invalid site performed networking")
	}
}

func TestFaviconNetworkHomepageFailureStillFallsBack(t *testing.T) {
	good := faviconPNG(t, 32)
	f := newFaviconFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close()
		case "/favicon.ico":
			w.Write(good)
		default:
			http.NotFound(w, r)
		}
	})
	if _, ok := FindFavicon(t.Context(), f.client, f.cache, "example.com", nil); !ok {
		t.Fatal("network failure suppressed ICO fallback")
	}
	if got := f.requests(); got[len(got)-1] != "/favicon.ico" {
		t.Fatalf("requests = %v", got)
	}
}

func TestFaviconDeclaredCDNAndDeduplication(t *testing.T) {
	good := faviconPNG(t, 32)
	f := newFaviconFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Write([]byte(`<link rel="icon" href="/bad#one"><link rel="icon" href="/bad#two"><link rel="icon" href="https://10.0.0.1/private"><link rel="icon" href="//cdn.example.com/good">`))
		case "/good":
			if r.Host != "cdn.example.com" {
				t.Errorf("wrong CDN host: %s", r.Host)
			}
			w.Write(good)
		default:
			http.NotFound(w, r)
		}
	})
	if _, ok := FindFavicon(t.Context(), f.client, f.cache, "example.com", nil); !ok {
		t.Fatal("public CDN candidate unavailable")
	}
	if got := f.requests(); !reflect.DeepEqual(got, []string{"/apple-touch-icon.png", "/", "/bad", "/good"}) {
		t.Fatalf("candidate deduplication/private address checks failed: %v", got)
	}
}

func TestFaviconCandidateTransientDoesNotCacheSiteMiss(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := newFaviconFixture(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/":
					w.Write([]byte(`<link rel="icon" href="/declared">`))
				case "/declared":
					w.WriteHeader(status)
				default:
					http.NotFound(w, r)
				}
			})
			for range 2 {
				if _, ok := FindFavicon(t.Context(), f.client, f.cache, "example.com", nil); ok {
					t.Fatal("transient candidate succeeded")
				}
				if f.cache.AvatarMiss(imagecache.AvatarKey("favicon", "example.com")) {
					t.Fatal("transient candidate became permanent site miss")
				}
			}
			// URL failures are cached briefly; homepage discovery has no transient cache API.
			if got := f.requests(); !reflect.DeepEqual(got, []string{"/apple-touch-icon.png", "/", "/declared", "/favicon.ico", "/"}) {
				t.Fatalf("transient caching requests = %v", got)
			}
		})
	}
}

func TestFaviconClearDuringDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name, blocked string
		success       bool
	}{
		{"success", "/apple-touch-icon.png", true},
		{"homepage", "/", true},
		{"miss", "/favicon.ico", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			good := faviconPNG(t, 32)
			started, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			f := newFaviconFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == tc.blocked {
					once.Do(func() { close(started); <-release })
				}
				if tc.success && (tc.blocked == "/apple-touch-icon.png" || r.URL.Path == "/favicon.ico") {
					w.Write(good)
				} else {
					http.NotFound(w, r)
				}
			})
			done := make(chan struct{})
			go func() { FindFavicon(context.Background(), f.client, f.cache, "example.com", nil); close(done) }()
			<-started
			err := f.cache.Clear()
			close(release)
			<-done
			if err != nil {
				t.Fatal(err)
			}
			key := imagecache.AvatarKey("favicon", "example.com")
			if _, ok, err := f.cache.LookupAvatar(t.Context(), key); err != nil || ok || f.cache.AvatarMiss(key) {
				t.Fatalf("pre-clear discovery repopulated site entry: found=%v miss=%v err=%v bytes=%d", ok, f.cache.AvatarMiss(key), err, f.cache.Size())
			}
			if f.cache.Size() != 0 {
				t.Fatalf("pre-clear discovery repopulated URL bytes: %d", f.cache.Size())
			}
			if got := f.requests(); tc.blocked == "/" && !reflect.DeepEqual(got, []string{"/apple-touch-icon.png", "/"}) {
				t.Fatalf("old discovery continued after clear: %v", got)
			}
			_, ok := FindFavicon(t.Context(), f.client, f.cache, "example.com", nil)
			if ok != tc.success || f.cache.AvatarMiss(key) == tc.success {
				t.Fatalf("new discovery success=%v miss=%v", ok, f.cache.AvatarMiss(key))
			}
			if tc.success {
				if _, ok, err := f.cache.LookupAvatar(t.Context(), key); err != nil || !ok {
					t.Fatalf("fresh site entry found=%v err=%v", ok, err)
				}
			}
		})
	}
}
