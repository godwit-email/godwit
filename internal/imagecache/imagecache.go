// Package imagecache is the privacy proxy's store for remote mail images: a
// directory of files named by their URL's hash, shared by all messages,
// bounded in size and evicted least recently used first. Get downloads an
// image once (through imagefetch: public hosts, no cookies, no Referer) and
// serves it from disk after that, offline too.
package imagecache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/godwit-email/godwit/internal/imagefetch"
)

// Limits of the fetching proxy.
const (
	MaxBytes   = 256 << 20       // the cache's size
	GlobalJobs = 8               // downloads at once
	HostJobs   = 3               // downloads at once from one host
	failedTTL  = 5 * time.Minute // a URL that failed is not tried again for this long
	queueWait  = time.Minute     // the longest a download waits for its turn
)

// Image is a cached image.
type Image struct {
	Data          []byte
	Format        string // as imagefetch.Dims names it: png, jpeg, gif or webp
	Width, Height int
}

// Cache is a directory of images; it is safe for concurrent use.
type Cache struct {
	dir    string
	max    int64
	client *http.Client
	global chan struct{}
	ctx    context.Context // ends at Close, with the downloads in flight
	stop   context.CancelFunc
	wg     sync.WaitGroup

	mu     sync.Mutex
	files  map[string]*entry // by key
	total  int64
	gen    int // bumped by Clear: a download that began before it is not stored
	flight map[string]*call
	failed map[string]failure
	hosts  map[string]*hostSem // hosts with a download in flight
}

// entry is a cached file: <key>-<format>-<width>x<height>.
type entry struct {
	name          string
	size          int64
	used          time.Time
	format        string
	width, height int
}

type call struct {
	done chan struct{}
	img  Image
	err  error
	gen  int // the cache's generation when it began
}

type failure struct {
	err error
	at  time.Time
}

// ErrClosed is returned by Get after Close.
var ErrClosed = errors.New("image cache is closed")

// maxFailed bounds the memory of failed URLs.
const maxFailed = 1000

// rememberFailure notes that key failed, dropping the notes that expired
// (and all of them when still too many). The caller holds c.mu.
func (c *Cache) rememberFailure(key string, err error) {
	for k, f := range c.failed {
		if time.Since(f.at) >= failedTTL {
			delete(c.failed, k)
		}
	}
	if len(c.failed) >= maxFailed {
		clear(c.failed)
	}
	c.failed[key] = failure{err, time.Now()}
}

// Close cancels the downloads in flight and waits for them; Get fails after.
func (c *Cache) Close() {
	c.mu.Lock()
	c.stop()
	c.mu.Unlock()
	c.wg.Wait()
}

// Key is the cache key of a URL: 32 hex digits of its SHA-256.
func Key(src string) string {
	h := sha256.Sum256([]byte(src))
	return hex.EncodeToString(h[:16])
}

// Open is the cache in dir (made if need be), at most max bytes (MaxBytes
// when 0), downloading with client.
func Open(dir string, max int64, client *http.Client) (*Cache, error) {
	if max <= 0 {
		max = MaxBytes
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	c := &Cache{dir: dir, max: max, client: client, global: make(chan struct{}, GlobalJobs),
		files: map[string]*entry{}, flight: map[string]*call{}, failed: map[string]failure{}, hosts: map[string]*hostSem{}}
	c.ctx, c.stop = context.WithCancel(context.Background())
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, de := range des {
		info, err := de.Info()
		key, e, ok := parseName(de.Name())
		if err != nil || !ok {
			os.Remove(filepath.Join(dir, de.Name())) // a partial write or a stranger
			continue
		}
		e.size, e.used = info.Size(), info.ModTime()
		c.files[key] = e
		c.total += e.size
	}
	c.evict()
	return c, nil
}

func parseName(name string) (key string, e *entry, ok bool) {
	parts := strings.Split(name, "-")
	if len(parts) != 3 || len(parts[0]) != 32 || !imagefetch.Formats[parts[1]] {
		return "", nil, false
	}
	w, h, ok := strings.Cut(parts[2], "x")
	if !ok {
		return "", nil, false
	}
	wi, err1 := strconv.Atoi(w)
	hi, err2 := strconv.Atoi(h)
	if err1 != nil || err2 != nil {
		return "", nil, false
	}
	return parts[0], &entry{name: name, format: parts[1], width: wi, height: hi}, true
}

// Dims is the size of src's image if it is cached; it reads no file.
func (c *Cache) Dims(src string) (width, height int, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.files[Key(src)]
	if e == nil {
		return 0, 0, false
	}
	return e.width, e.height, true
}

// Cached reports whether src's image is on disk.
func (c *Cache) Cached(src string) bool {
	_, _, ok := c.Dims(src)
	return ok
}

// Size is the bytes the cache takes on disk.
func (c *Cache) Size() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.total
}

// Clear deletes every cached image.
func (c *Cache) Clear() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var errs []error
	for k, e := range c.files {
		if err := os.Remove(filepath.Join(c.dir, e.name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
			continue
		}
		c.total -= e.size
		delete(c.files, k)
	}
	clear(c.failed)
	c.gen++
	return errors.Join(errs...)
}

// Get is src's image: from disk, or downloaded now (once, however many ask
// at the same time). A download goes on when its caller gives up, so the next
// ask finds it; a URL that failed fails at once for a few minutes.
func (c *Cache) Get(ctx context.Context, src string) (Image, error) {
	key := Key(src)
	c.mu.Lock()
	if c.ctx.Err() != nil {
		c.mu.Unlock()
		return Image{}, ErrClosed
	}
	if e := c.files[key]; e != nil {
		e.used = time.Now()
		name := e.name
		c.wg.Add(1) // Close waits for the disk work below
		c.mu.Unlock()
		data, err := os.ReadFile(filepath.Join(c.dir, name))
		if err == nil {
			now := time.Now()
			_ = os.Chtimes(filepath.Join(c.dir, name), now, now)
			c.wg.Done()
			return Image{Data: data, Format: e.format, Width: e.width, Height: e.height}, nil
		}
		// Forget the file only once it is gone (or deleted); else it stays counted.
		rerr := os.Remove(filepath.Join(c.dir, name))
		c.wg.Done()
		if rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			return Image{}, err
		}
		c.mu.Lock()
		if c.files[key] == e {
			c.total -= e.size
			delete(c.files, key)
		}
	}
	if f, ok := c.failed[key]; ok {
		if time.Since(f.at) < failedTTL {
			c.mu.Unlock()
			return Image{}, f.err
		}
		delete(c.failed, key)
	}
	cl := c.flight[key]
	if cl == nil {
		cl = &call{done: make(chan struct{}), gen: c.gen}
		c.flight[key] = cl
		c.wg.Add(1)
		go c.download(key, src, cl, c.gen)
	}
	c.mu.Unlock()
	select {
	case <-cl.done:
		return cl.img, cl.err
	case <-ctx.Done():
		return Image{}, ctx.Err()
	}
}

func (c *Cache) download(key, src string, cl *call, gen int) {
	defer c.wg.Done()
	cl.img, cl.err = c.fetch(key, src, gen)
	c.mu.Lock()
	delete(c.flight, key)
	if cl.err != nil && c.ctx.Err() == nil && cl.gen == c.gen {
		c.rememberFailure(key, cl.err)
	}
	c.mu.Unlock()
	close(cl.done)
}

func (c *Cache) fetch(key, src string, gen int) (Image, error) {
	u, err := url.Parse(src)
	if err != nil || !imagefetch.IsHTTP(u) {
		return Image{}, imagefetch.ErrScheme
	}
	ctx, cancel := context.WithTimeout(c.ctx, queueWait+imagefetch.Timeout)
	defer cancel()
	host, release := c.host(hostKey(u))
	defer release()
	for _, sem := range []chan struct{}{host, c.global} {
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
		case <-time.After(queueWait):
			return Image{}, errors.New("image: too many downloads")
		case <-ctx.Done():
			return Image{}, ctx.Err()
		}
	}
	data, format, w, h, err := imagefetch.Fetch(ctx, c.client, src)
	if err != nil {
		return Image{}, err
	}
	img := Image{Data: data, Format: format, Width: w, Height: h}
	return img, c.put(key, img, gen)
}

// hostKey names u's host for the limiter: lower case, no trailing dot, an IP
// literal in its canonical form.
func hostKey(u *url.URL) string {
	h := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if ip, err := netip.ParseAddr(h); err == nil {
		return ip.Unmap().String()
	}
	return h
}

// hostSem limits the downloads from one host; n counts those using it.
type hostSem struct {
	ch chan struct{}
	n  int
}

// host is the semaphore of h and the func that gives it up: the entry goes
// with its last download, so hosts a sender invents do not pile up.
func (c *Cache) host(h string) (chan struct{}, func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	hs := c.hosts[h]
	if hs == nil {
		hs = &hostSem{ch: make(chan struct{}, HostJobs)}
		c.hosts[h] = hs
	}
	hs.n++
	return hs.ch, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if hs.n--; hs.n == 0 {
			delete(c.hosts, h)
		}
	}
}

// put writes img for key, atomically, and evicts what is over the limit,
// unless the cache was cleared since gen. An image the disk could not take is
// still served: the error is not the download's.
func (c *Cache) put(key string, img Image, gen int) error {
	name := fmt.Sprintf("%s-%s-%dx%d", key, img.Format, img.Width, img.Height)
	tmp, err := os.CreateTemp(c.dir, "tmp-")
	if err != nil {
		return nil
	}
	_, err = tmp.Write(img.Data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil && c.gen == gen {
		err = os.Rename(tmp.Name(), filepath.Join(c.dir, name))
	} else {
		err = errors.New("not stored")
	}
	if err != nil {
		os.Remove(tmp.Name())
		return nil
	}
	if old := c.files[key]; old != nil {
		c.total -= old.size
	}
	c.files[key] = &entry{name: name, size: int64(len(img.Data)), used: time.Now(), format: img.Format, width: img.Width, height: img.Height}
	c.total += int64(len(img.Data))
	c.evict()
	return nil
}

// evict deletes the least recently used files until the cache is within 90%
// of its limit, once it is over the limit. The caller holds c.mu (or is Open).
func (c *Cache) evict() {
	if c.total <= c.max {
		return
	}
	keys := make([]string, 0, len(c.files))
	for k := range c.files {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b string) int { return c.files[a].used.Compare(c.files[b].used) })
	for _, k := range keys {
		if c.total <= c.max/10*9 {
			return
		}
		e := c.files[k]
		if err := os.Remove(filepath.Join(c.dir, e.name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			continue // still on disk: keep it counted, try the next
		}
		c.total -= e.size
		delete(c.files, k)
	}
}
