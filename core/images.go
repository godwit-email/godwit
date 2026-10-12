package core

import (
	"context"
	"path/filepath"
	"sync"

	"github.com/godwit-email/godwit/core/events"
	gomail "github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/store"
	"github.com/godwit-email/godwit/internal/imagecache"
)

// Prefetch limits of one message's remote images.
const (
	prefetchImages = 30
	prefetchBytes  = 15 << 20
)

// ImageCache is the shared disk cache of remote mail images, which the
// reader's image proxy serves from and prefetch fills.
func (s *Service) ImageCache() *imagecache.Cache { return s.images }

// ImageCacheSize is the bytes the cached remote images take on disk.
func (s *Service) ImageCacheSize() int64 { return s.images.Size() }

// ClearImageCache deletes the cached remote images.
func (s *Service) ClearImageCache() error { return s.images.Clear() }

func (s *Service) openImageCache() error {
	var err error
	s.images, err = imagecache.Open(filepath.Join(s.dataDir, "cache", "images"), 0, s.opts.ImageClient)
	return err
}

// startImagePrefetch fetches the remote images of each new message the
// settings let load (ImagesAllowed) into the cache, one message at a time,
// so that opening it is instant and the sender sees the fetch at arrival,
// not when it is read.
func (s *Service) startImagePrefetch() {
	ctx, end, err := s.begin(context.Background(), 0)
	if err != nil {
		return
	}
	ch, unsub := s.bus.Subscribe()
	// The bus is lossy and a download can take long: a reader moves the
	// arrivals into a queue at once, a worker fetches from it.
	var mu sync.Mutex
	var queue []int64
	wake := make(chan struct{}, 1)
	go func() {
		defer unsub()
		for {
			select {
			case ev, ok := <-ch:
				if !ok {
					return
				}
				if ev.Type == events.MessageReceived {
					mu.Lock()
					queue = append(queue, ev.MessageIDs...)
					mu.Unlock()
					select {
					case wake <- struct{}{}:
					default:
					}
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		defer end()
		for {
			select {
			case <-wake:
			case <-ctx.Done():
				return
			}
			for ctx.Err() == nil {
				mu.Lock()
				if len(queue) == 0 {
					mu.Unlock()
					break
				}
				id := queue[0]
				queue = queue[1:]
				mu.Unlock()
				s.prefetchImages(ctx, id)
			}
		}
	}()
}

// prefetchImages caches message's remote images, within prefetchImages
// images and about prefetchBytes bytes: the last image may pass it, by at
// most one image (imagefetch.MaxImageBytes), as its size is known only once fetched.
func (s *Service) prefetchImages(ctx context.Context, message int64) {
	if ok, err := s.ImagesAllowed(ctx, message); err != nil || !ok {
		return
	}
	rows, err := s.db.Query(ctx, "SELECT body_html FROM messages WHERE id = ?", message)
	if err != nil || len(rows.Values) == 0 || store.Str(rows.Values[0][0]) == "" {
		return
	}
	safe, _ := gomail.Sanitize(store.Str(rows.Values[0][0]), nil, true)
	urls := gomail.RemoteImages(safe)
	if len(urls) > prefetchImages {
		urls = urls[:prefetchImages]
	}
	var total int64
	for _, u := range urls {
		if total >= prefetchBytes || ctx.Err() != nil {
			return
		}
		cached := s.images.Cached(u) // a hit transfers nothing
		if img, err := s.images.Get(ctx, u); err == nil && !cached {
			total += int64(len(img.Data))
		}
	}
}
