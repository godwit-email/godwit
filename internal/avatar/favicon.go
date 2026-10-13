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
	"time"

	"github.com/godwit-email/godwit/internal/imagecache"
	"github.com/godwit-email/godwit/internal/imagefetch"
	"golang.org/x/net/html"
)

// FindFavicon checks one site's persistent result before sequential discovery.
func FindFavicon(ctx context.Context, client *http.Client, cache *imagecache.Cache, host string) (imagecache.Image, bool) {
	host = normalizeDomain(host)
	if host == "" {
		return imagecache.Image{}, false
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	key := imagecache.AvatarKey("favicon", host)
	if img, ok, err := cache.LookupAvatar(ctx, key); ok {
		return img, true
	} else if ctx.Err() != nil || errors.Is(err, imagecache.ErrClosed) {
		return imagecache.Image{}, false
	}
	if cache.AvatarMiss(key) {
		return imagecache.Image{}, false
	}
	base := &url.URL{Scheme: "https", Host: host, Path: "/"}
	transient := false
	try := func(src string) (imagecache.Image, bool) {
		img, err := cache.GetAvatar(ctx, src, "favicon")
		if err == nil {
			img, err = cache.PutAvatar(ctx, key, img.Data, "favicon")
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
	if ctx.Err() != nil {
		return imagecache.Image{}, false
	}
	candidates, err := faviconLinks(ctx, imagefetch.NewAvatarClient(client), base)
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
	if !transient && ctx.Err() == nil {
		_ = cache.RememberAvatarMiss(key)
	}
	return imagecache.Image{}, false
}

func faviconLinks(ctx context.Context, client *http.Client, base *url.URL) ([]string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return nil, err
	}
	if req.URL.Scheme != "https" || req.URL.User != nil {
		return nil, imagefetch.ErrScheme
	}
	req.Header.Set("User-Agent", imagefetch.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { cancel(); resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, imagefetch.ErrAvatarMiss
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("favicon homepage: %s", resp.Status)
	}
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	z := html.NewTokenizer(bytes.NewReader(data))
	candidates := make([]string, 0, 3)
	seen := map[string]bool{}
	for len(candidates) < 3 {
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
		icon := false
		for _, r := range strings.Fields(strings.ToLower(rel)) {
			if r == "apple-touch-icon" || r == "apple-touch-icon-precomposed" || r == "icon" {
				icon = true
			}
		}
		if !icon || strings.TrimSpace(href) == "" {
			continue
		}
		u, err := resp.Request.URL.Parse(strings.TrimSpace(href))
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
			continue
		}
		u.Fragment = ""
		src := u.String()
		if !seen[src] {
			seen[src] = true
			candidates = append(candidates, src)
		}
	}
	return candidates, readErr
}
