package core

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"time"

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

// CachedImageFile is a remote mail image cached on disk.
type CachedImageFile struct {
	Path          string
	Format        string
	Width, Height int
}

// ImageDims is the cached pixel size of a remote mail image, when on disk.
type ImageDims struct {
	Width, Height int
	OK            bool
}

// ImageCache is the shared disk cache of remote mail images, which the
// reader's image proxy serves from and prefetch fills.
func (s *Service) ImageCache() *imagecache.Cache { return s.images }

// CachedImage returns src's cached image file on disk, downloading it first if needed.
func (s *Service) CachedImage(ctx context.Context, src string) (CachedImageFile, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return CachedImageFile{}, err
	}
	defer end()
	path, format, w, h, err := s.images.GetFile(ctx, src)
	var ns *imagecache.NotStoredError
	if errors.As(err, &ns) { // shown all the same, as in process
		// shortcut: the file stays in raw-out until the next start; serve the bytes over the wire if disk-full turns out common.
		img := ns.Image
		if path, err = s.outFile(img.Data, "*.img"); err == nil {
			return CachedImageFile{Path: path, Format: img.Format, Width: img.Width, Height: img.Height}, nil
		}
	}
	if err != nil {
		return CachedImageFile{}, err
	}
	return CachedImageFile{Path: path, Format: format, Width: w, Height: h}, nil
}

// CachedImageDims returns src's pixel size when src is already cached on disk.
func (s *Service) CachedImageDims(src string) ImageDims {
	w, h, ok := s.images.Dims(src)
	return ImageDims{Width: w, Height: h, OK: ok}
}

// ForgetImageFailure clears cached download failures for urls so the next
// CachedImage call retries them at once.
func (s *Service) ForgetImageFailure(urls []string) {
	for _, u := range urls {
		s.images.ForgetFailure(u)
	}
}

// ImageCacheSize is the bytes the cached remote images take on disk.
func (s *Service) ImageCacheSize() int64 { return s.images.Size() }

// ClearImageCache deletes the cached remote images.
func (s *Service) ClearImageCache() error { return s.images.Clear() }

func (s *Service) openImageCache() error {
	var err error
	s.images, err = imagecache.Open(filepath.Join(s.dataDir, "cache", "images"), 0, s.opts.ImageClient)
	return err
}

// startImagePrefetch caches permitted body images and sender avatars at arrival,
// with independent serial workers. Reads can still fetch when prefetch is incomplete or stale.
func (s *Service) startImagePrefetch() {
	ctx, end, err := s.begin(context.Background(), 0)
	if err != nil {
		return
	}
	ch, unsub := s.bus.Subscribe()
	// The bus is lossy and a download can take long: a reader moves the
	// arrivals into queues at once; body and avatar workers drain independently.
	var mu sync.Mutex
	var queue [2][]int64
	wake := [2]chan struct{}{make(chan struct{}, 1), make(chan struct{}, 1)}
	var workers sync.WaitGroup
	workers.Add(len(queue) + 1)
	go func() {
		defer unsub()
		defer workers.Done()
		for {
			select {
			case ev, ok := <-ch:
				if !ok {
					return
				}
				if ev.Type == events.MessageReceived {
					mu.Lock()
					for i := range queue {
						queue[i] = append(queue[i], ev.MessageIDs...)
					}
					mu.Unlock()
					for _, w := range wake {
						select {
						case w <- struct{}{}:
						default:
						}
					}
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	for i := range queue {
		go func() {
			defer workers.Done()
			for {
				select {
				case <-wake[i]:
				case <-ctx.Done():
					return
				}
				for ctx.Err() == nil {
					mu.Lock()
					if len(queue[i]) == 0 {
						mu.Unlock()
						break
					}
					id := queue[i][0]
					queue[i] = queue[i][1:]
					mu.Unlock()
					if i == 0 {
						s.prefetchImages(ctx, id)
					} else {
						_, _ = s.SenderAvatar(ctx, id, false)
					}
				}
			}
		}()
	}
	go func() {
		workers.Wait()
		end()
	}()
}

// prefetchImages caches message's remote images, within prefetchImages
// images and about prefetchBytes bytes: the last image may pass it, by at
// most one image (imagefetch.MaxImageBytes), as its size is known only once fetched.
func (s *Service) prefetchImages(ctx context.Context, message int64) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return
	}
	defer end()
	rows, err := s.db.Query(ctx, "SELECT account_id FROM messages WHERE id = ?", message)
	if err != nil || len(rows.Values) == 0 {
		return
	}
	if err := s.bind(ctx, store.Int(rows.Values[0][0])); err != nil {
		return
	}
	if ok, err := s.ImagesAllowed(ctx, message); err != nil || !ok {
		return
	}
	rows, err = s.db.Query(ctx, "SELECT body_html FROM messages WHERE id = ?", message)
	if err != nil || len(rows.Values) == 0 || store.Str(rows.Values[0][0]) == "" {
		return
	}
	safe, _, _ := gomail.Sanitize(store.Str(rows.Values[0][0]), nil, true)
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

// imageRetentionEvery is how often the cached images are checked against
// Settings.ImageRetentionDays, once at start and then on this beat.
const imageRetentionEvery = 6 * time.Hour

// startImageRetention deletes the cached remote images that have gone unread for
// Settings.ImageRetentionDays, when the user set one.
func (s *Service) startImageRetention() {
	ctx, end, err := s.begin(context.Background(), 0)
	if err != nil {
		return
	}
	go func() {
		defer end()
		t := time.NewTicker(imageRetentionEvery)
		defer t.Stop()
		for {
			s.pruneImageCache(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// pruneImageCache deletes the images unread for the retention the settings name;
// none when it is 0.
func (s *Service) pruneImageCache(ctx context.Context) int {
	st, err := s.loadSettings(ctx)
	if err != nil || st.ImageRetentionDays <= 0 {
		return 0
	}
	return s.images.PruneUnused(time.Now().AddDate(0, 0, -st.ImageRetentionDays))
}
