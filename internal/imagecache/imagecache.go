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
	"io"
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
	Format        string // png, jpeg, gif, webp; avatar entries also allow svg
	Width, Height int
}

// Cache is a directory of images; it is safe for concurrent use.
type Cache struct {
	dir          string
	max          int64
	client       *http.Client
	avatarClient *http.Client
	global       chan struct{}
	ctx          context.Context // ends at Close, with the downloads in flight
	stop         context.CancelFunc
	wg           sync.WaitGroup

	mu     sync.Mutex
	files  map[string]*entry // by key
	total  int64
	gen    int // bumped by Clear: a download that began before it is not stored
	flight map[string]*call
	failed map[string]failure
	hosts  map[string]*hostSem // hosts with a download in flight
}

// entry is a cached file: <key>-<format>-<width>x<height>[-<avatarWrittenUnix>].
type entry struct {
	name          string
	size          int64
	used          time.Time
	format        string
	width, height int
	written       time.Time // avatar refresh time; used remains LRU only
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
	c := &Cache{dir: dir, max: max, client: client, avatarClient: imagefetch.NewAvatarClient(client), global: make(chan struct{}, GlobalJobs),
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
		if !e.written.IsZero() && (e.size > imagefetch.MaxAvatarBytes || (e.format == "miss" && e.size != 0)) {
			os.Remove(filepath.Join(dir, e.name))
			continue
		}
		if old := c.files[key]; old != nil {
			if old.written.After(e.written) {
				os.Remove(filepath.Join(dir, e.name))
				continue
			}
			os.Remove(filepath.Join(dir, old.name))
			c.total -= old.size
		}
		c.files[key] = e
		c.total += e.size
	}
	c.evict()
	return c, nil
}

func parseName(name string) (key string, e *entry, ok bool) {
	parts := strings.Split(name, "-")
	if len(parts) != 3 && len(parts) != 4 {
		return "", nil, false
	}
	avatar := len(parts) == 4
	if avatar {
		if avatarSource(parts[0]) == "" {
			return "", nil, false
		}
	} else if len(parts[0]) != 32 {
		return "", nil, false
	}
	if !imagefetch.Formats[parts[1]] && !(avatar && (parts[1] == "svg" || parts[1] == "miss")) {
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
	e = &entry{name: name, format: parts[1], width: wi, height: hi}
	if avatar {
		unix, err := strconv.ParseInt(parts[3], 10, 64)
		if err != nil || unix <= 0 || unix > time.Now().Unix()+60 {
			return "", nil, false
		}
		e.written = time.Unix(unix, 0)
		if e.format == "miss" {
			if wi != 0 || hi != 0 {
				return "", nil, false
			}
		} else if wi <= 0 || hi <= 0 {
			return "", nil, false
		}
	}
	return parts[0], e, true
}

const avatarTTL = 24 * time.Hour

// AvatarKey identifies a source and URL (or normalized website host). Pass this
// complete key to PutAvatar, LookupAvatar and the miss methods.
func AvatarKey(source, identity string) string {
	if source != "bimi" && source != "favicon" && source != "gravatar" {
		return ""
	}
	return "avatar_" + source + "_" + Key(identity)
}

func avatarSource(key string) string {
	parts := strings.Split(key, "_")
	if len(parts) != 3 || parts[0] != "avatar" || len(parts[2]) != 32 {
		return ""
	}
	if _, err := hex.DecodeString(parts[2]); err != nil {
		return ""
	}
	if parts[1] != "bimi" && parts[1] != "favicon" && parts[1] != "gravatar" {
		return ""
	}
	return parts[1]
}

// LookupAvatar reads only a fresh, revalidated avatar entry, without networking.
func (c *Cache) LookupAvatar(ctx context.Context, key string) (Image, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lookupAvatar(ctx, key)
}

// lookupAvatar holds c.mu across bounded file validation so Clear and Close wait.
func (c *Cache) lookupAvatar(ctx context.Context, key string) (Image, bool, error) {
	if c.ctx.Err() != nil {
		return Image{}, false, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return Image{}, false, err
	}
	source := avatarSource(key)
	if source == "" {
		return Image{}, false, imagefetch.ErrFormat
	}
	e := c.files[key]
	if e == nil || e.format == "miss" || time.Since(e.written) >= avatarTTL {
		return Image{}, false, nil
	}
	f, err := os.Open(filepath.Join(c.dir, e.name))
	var data []byte
	if err == nil {
		data, err = io.ReadAll(io.LimitReader(f, imagefetch.MaxAvatarBytes+1))
		f.Close()
	}
	var img Image
	if err == nil {
		img.Data, img.Format, img.Width, img.Height, err = imagefetch.CheckAvatarImage(data, source)
	}
	if err != nil {
		c.remove(key)
		return Image{}, false, err
	}
	e.used = time.Now()
	_ = os.Chtimes(filepath.Join(c.dir, e.name), e.used, e.used)
	return img, true, nil
}

// PutAvatar stores validated bytes under a source-specific key.
func (c *Cache) PutAvatar(ctx context.Context, key string, data []byte, source string) (Image, error) {
	if source == "" || avatarSource(key) != source {
		return Image{}, imagefetch.ErrFormat
	}
	c.mu.Lock()
	if c.ctx.Err() != nil {
		c.mu.Unlock()
		return Image{}, ErrClosed
	}
	gen := c.gen
	c.wg.Add(1)
	c.mu.Unlock()
	defer c.wg.Done()
	if err := ctx.Err(); err != nil {
		return Image{}, err
	}
	out, format, w, h, err := imagefetch.CheckAvatarImage(data, source)
	if err != nil {
		return Image{}, err
	}
	img := Image{Data: out, Format: format, Width: w, Height: h}
	return img, c.put(key, img, gen)
}

// AvatarMiss reports a persistent deterministic miss, for at most 24 hours.
func (c *Cache) AvatarMiss(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ctx.Err() != nil || avatarSource(key) == "" {
		return false
	}
	e := c.files[key]
	if e == nil || e.format != "miss" {
		return false
	}
	if time.Since(e.written) >= avatarTTL {
		c.remove(key)
		return false
	}
	return true
}

func (c *Cache) RememberAvatarMiss(key string) error {
	if avatarSource(key) == "" {
		return imagefetch.ErrFormat
	}
	c.mu.Lock()
	if c.ctx.Err() != nil {
		c.mu.Unlock()
		return ErrClosed
	}
	gen := c.gen
	c.wg.Add(1)
	c.mu.Unlock()
	defer c.wg.Done()
	return c.put(key, Image{Format: "miss"}, gen)
}

// remove forgets a file only when it is gone. The caller holds c.mu.
func (c *Cache) remove(key string) {
	e := c.files[key]
	if e == nil {
		return
	}
	if err := os.Remove(filepath.Join(c.dir, e.name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return
	}
	c.total -= e.size
	delete(c.files, key)
}

// GetAvatar shares the ordinary cache's jobs, singleflight and lifecycle.
// The first caller's deadline bounds the shared download; cancellation stops waiting.
func (c *Cache) GetAvatar(ctx context.Context, src, source string) (Image, error) {
	key := AvatarKey(source, src)
	if key == "" {
		return Image{}, imagefetch.ErrFormat
	}
	return c.get(ctx, key, src, source)
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
	return c.get(ctx, Key(src), src, "")
}

func (c *Cache) get(ctx context.Context, key, src, source string) (Image, error) {
	c.mu.Lock()
	if c.ctx.Err() != nil {
		c.mu.Unlock()
		return Image{}, ErrClosed
	}
	if source != "" {
		if img, ok, err := c.lookupAvatar(ctx, key); ok || errors.Is(err, ErrClosed) || ctx.Err() != nil {
			c.mu.Unlock()
			return img, err
		}
		if e := c.files[key]; e != nil && e.format == "miss" && time.Since(e.written) < avatarTTL {
			c.mu.Unlock()
			return Image{}, imagefetch.ErrAvatarMiss
		}
	}
	if e := c.files[key]; e != nil && source == "" {
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
		var deadline time.Time
		if source != "" {
			deadline, _ = ctx.Deadline()
		}
		go c.download(key, src, source, cl, c.gen, deadline)
	}
	c.mu.Unlock()
	select {
	case <-cl.done:
		return cl.img, cl.err
	case <-ctx.Done():
		return Image{}, ctx.Err()
	}
}

func (c *Cache) download(key, src, source string, cl *call, gen int, deadline time.Time) {
	defer c.wg.Done()
	cl.img, cl.err = c.fetch(key, src, source, gen, deadline)
	if source != "" && errors.Is(cl.err, imagefetch.ErrAvatarMiss) {
		c.put(key, Image{Format: "miss"}, gen)
	}
	c.mu.Lock()
	delete(c.flight, key)
	if cl.err != nil && c.ctx.Err() == nil && cl.gen == c.gen {
		c.rememberFailure(key, cl.err)
	}
	c.mu.Unlock()
	close(cl.done)
}

func (c *Cache) fetch(key, src, source string, gen int, deadline time.Time) (Image, error) {
	u, err := url.Parse(src)
	if err != nil || !imagefetch.IsHTTP(u) {
		return Image{}, imagefetch.ErrScheme
	}
	ctx, cancel := context.WithTimeout(c.ctx, queueWait+imagefetch.Timeout)
	defer cancel()
	if !deadline.IsZero() {
		var stop context.CancelFunc
		ctx, stop = context.WithDeadline(ctx, deadline)
		defer stop()
	}
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
	var data []byte
	var format string
	var w, h int
	if source == "" {
		data, format, w, h, err = imagefetch.Fetch(ctx, c.client, src)
	} else {
		data, err = imagefetch.DownloadAvatar(ctx, c.avatarClient, src)
		if err == nil {
			data, format, w, h, err = imagefetch.CheckAvatarImage(data, source)
		}
	}
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
	var written time.Time
	if avatarSource(key) != "" {
		written = time.Now().Truncate(time.Second)
		name += fmt.Sprintf("-%d", written.Unix())
	}
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
	if err == nil && c.gen == gen && c.ctx.Err() == nil {
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
		if old.name != name {
			os.Remove(filepath.Join(c.dir, old.name))
		}
	}
	c.files[key] = &entry{name: name, size: int64(len(img.Data)), used: time.Now(), format: img.Format, width: img.Width, height: img.Height, written: written}
	c.total += int64(len(img.Data))
	c.evict()
	return nil
}

// evict deletes the least recently used files until the cache is within 90%
// of its limit, once it is over the limit. The caller holds c.mu (or is Open).
func (c *Cache) evict() {
	misses := make([]string, 0)
	for k, e := range c.files {
		if e.format == "miss" {
			if time.Since(e.written) >= avatarTTL {
				c.remove(k)
			} else {
				misses = append(misses, k)
			}
		}
	}
	slices.SortFunc(misses, func(a, b string) int { return c.files[a].written.Compare(c.files[b].written) })
	for len(misses) > maxFailed {
		c.remove(misses[0])
		misses = misses[1:]
	}
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
