package avatar

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/godwit-email/godwit/internal/imagecache"
	"github.com/godwit-email/godwit/internal/imagefetch"
	"golang.org/x/net/html"
)

type faviconFlight struct {
	done    chan struct{}
	img     imagecache.Image
	found   bool
	aborted bool         // the owner cancelled or lost policy; not a result for anyone else
	waiters atomic.Int32 // followers blocked on done
}

var faviconFlights sync.Map

// FindFavicon checks message policy before cached sources and every discovery or
// candidate request. canFetch is nil only for callers without a message policy.
func FindFavicon(ctx context.Context, client *http.Client, cache *imagecache.Cache, host string, canFetch func() bool) (imagecache.Image, bool) {
	host = normalizeDomain(host)
	if host == "" || ctx.Err() != nil || canFetch != nil && !canFetch() {
		return imagecache.Image{}, false
	}
	for {
		gen := cache.AvatarGeneration()
		key := struct {
			cache *imagecache.Cache
			host  string
			gen   int
		}{cache, host, gen}
		v, waiting := faviconFlights.LoadOrStore(key, &faviconFlight{done: make(chan struct{})})
		flight := v.(*faviconFlight)
		if !waiting {
			// Run discovery on this caller so service shutdown still waits for it.
			// That caller's cancellation or revoked policy is not a shared result.
			func() {
				defer func() {
					faviconFlights.CompareAndDelete(key, flight)
					close(flight.done)
				}()
				flight.img, flight.found = findFavicon(ctx, client, cache, host, canFetch, gen)
				flight.aborted = !flight.found && (ctx.Err() != nil || canFetch != nil && !canFetch())
			}()
		} else {
			flight.waiters.Add(1)
			select {
			case <-flight.done:
				flight.waiters.Add(-1)
			case <-ctx.Done():
				flight.waiters.Add(-1)
				return imagecache.Image{}, false
			}
		}
		if ctx.Err() != nil || gen != cache.AvatarGeneration() || canFetch != nil && !canFetch() {
			return imagecache.Image{}, false
		}
		if waiting && flight.aborted {
			continue
		}
		return flight.img, flight.found
	}
}

func findFavicon(ctx context.Context, client *http.Client, cache *imagecache.Cache, host string, canFetch func() bool, gen int) (img imagecache.Image, found bool) {
	callerCtx := ctx
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	permitted := func() bool {
		if ctx.Err() != nil || gen != cache.AvatarGeneration() {
			cancel()
			return false
		}
		if canFetch != nil && !canFetch() {
			cancel()
			return false
		}
		return true
	}
	if !permitted() {
		return imagecache.Image{}, false
	}
	key := imagecache.AvatarKey("favicon", host)
	if img, ok, err := cache.LookupAvatar(ctx, key); ok {
		return img, true
	} else if ctx.Err() != nil || errors.Is(err, imagecache.ErrClosed) {
		return imagecache.Image{}, false
	}
	if cache.AvatarMiss(key) {
		return imagecache.Image{}, false
	}
	transient := false
	defer func() {
		if !found && (transient || errors.Is(ctx.Err(), context.DeadlineExceeded)) && callerCtx.Err() == nil && (canFetch == nil || canFetch()) {
			// A host that cannot be reached would spend the whole budget on every message.
			cache.RememberAvatarUnreachable(key, gen)
			img, found, _ = cache.FallbackAvatar(callerCtx, key, gen)
		}
	}()
	if cache.AvatarUnreachable(key) {
		// Not remembered again: the rest must run out for the site to be tried again.
		img, found, _ = cache.FallbackAvatar(callerCtx, key, gen)
		return img, found
	}
	base := &url.URL{Scheme: "https", Host: host, Path: "/"}
	try := func(src string) (imagecache.Image, bool) {
		if !permitted() {
			return imagecache.Image{}, false
		}
		img, err := cache.GetAvatar(ctx, src, "favicon", gen)
		if err == nil {
			if !permitted() {
				return imagecache.Image{}, false
			}
			img, err = cache.PutAvatar(ctx, key, img.Data, "favicon", gen)
			if err == nil {
				return img, true
			}
		}
		if !errors.Is(err, imagefetch.ErrAvatarMiss) {
			transient = true
		}
		return imagecache.Image{}, false
	}
	if img, ok := try(base.ResolveReference(&url.URL{Path: "/apple-touch-icon.png"}).String()); ok {
		return img, true
	}
	if !permitted() {
		return imagecache.Image{}, false
	}
	candidates, base, err := faviconLinks(ctx, imagefetch.NewAvatarClient(client), base)
	if err != nil && !errors.Is(err, imagefetch.ErrAvatarMiss) {
		transient = true
	}
	for _, src := range candidates {
		if ctx.Err() != nil {
			return imagecache.Image{}, false
		}
		if img, ok := try(src); ok {
			return img, true
		}
	}
	if ctx.Err() != nil {
		return imagecache.Image{}, false
	}
	if img, ok := try(base.ResolveReference(&url.URL{Path: "/favicon.ico"}).String()); ok {
		return img, true
	}
	if !transient && permitted() {
		_ = cache.RememberAvatarMiss(key, gen)
	}
	return imagecache.Image{}, false
}

func faviconLinks(ctx context.Context, client *http.Client, base *url.URL) ([]string, *url.URL, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return nil, base, err
	}
	if req.URL.Scheme != "https" || req.URL.User != nil {
		return nil, base, imagefetch.ErrScheme
	}
	req.Header.Set("User-Agent", imagefetch.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9")
	resp, err := client.Do(req)
	if err != nil {
		return nil, base, err
	}
	defer func() { cancel(); resp.Body.Close() }()
	base = resp.Request.URL
	if resp.StatusCode == http.StatusNotFound {
		return nil, base, imagefetch.ErrAvatarMiss
	}
	if resp.StatusCode != http.StatusOK {
		return nil, base, fmt.Errorf("favicon homepage: %s", resp.Status)
	}
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	z := html.NewTokenizer(bytes.NewReader(data))
	var touch, icons []string
	seenTouch, seenIcon := map[string]bool{}, map[string]bool{}
	for len(touch) < 3 {
		kind := z.Next()
		if kind == html.ErrorToken {
			break
		}
		if kind != html.StartTagToken && kind != html.SelfClosingTagToken {
			continue
		}
		raw := z.Raw()
		if len(raw) == 0 || raw[len(raw)-1] != '>' {
			continue
		} // Ignore tags cut off at the read limit.
		tok := z.Token()
		if tok.Data != "link" {
			continue
		}
		var rel, href string
		for _, attr := range tok.Attr {
			if attr.Key == "rel" && rel == "" {
				rel = attr.Val
			}
			if attr.Key == "href" && href == "" {
				href = attr.Val
			}
		}
		isTouch, isIcon := false, false
		for _, r := range strings.Fields(strings.ToLower(rel)) {
			if r == "apple-touch-icon" || r == "apple-touch-icon-precomposed" {
				isTouch = true
			} else if r == "icon" {
				isIcon = true
			}
		}
		if (!isTouch && !isIcon) || strings.TrimSpace(href) == "" {
			continue
		}
		u, err := base.Parse(strings.TrimSpace(href))
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
			continue
		}
		u.Fragment = ""
		src := u.String()
		if isTouch {
			if !seenTouch[src] {
				seenTouch[src] = true
				touch = append(touch, src)
			}
		} else if len(icons) < 3 && !seenIcon[src] {
			seenIcon[src] = true
			icons = append(icons, src)
		}
	}
	candidates := touch
	for _, src := range icons {
		if len(candidates) == 3 {
			break
		}
		if !seenTouch[src] {
			candidates = append(candidates, src)
		}
	}
	return candidates, base, readErr
}
