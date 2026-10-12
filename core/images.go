package core

import (
	"context"
	"path/filepath"

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
	go func() {
		defer end()
		defer unsub()
		for {
			select {
			case ev := <-ch:
				if ev.Type == events.MessageReceived {
					for _, id := range ev.MessageIDs {
						s.prefetchImages(ctx, id)
					}
				}
			case <-ctx.Done():
				return
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
		if img, err := s.images.Get(ctx, u); err == nil {
			total += int64(len(img.Data))
		}
	}
}
