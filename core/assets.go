package core

// Registry-hosted model files (docs/superpowers/specs/2026-10-17-plugin-model-provider-design.md
// 8.2, plan docs/superpowers/plans/2026-10-17-local-startlux-judge.md Task 3):
// each variant in DataDir/plugin-assets/<variant digest>/, named as the
// registry names its files, downloaded with resumption and checked against
// the signed registry's sizes and SHA-256 before every load.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/godwit-email/godwit/core/plugins"
)

// assetStall ends a download that has received nothing for that long; a
// model download has no overall time limit.
var assetStall = 30 * time.Second

// physicalMemory is the machine's memory in bytes (0 unknown); tests replace it.
var physicalMemory = sysPhysicalMemory

func (s *Service) assetsDir() string { return filepath.Join(s.dataDir, "plugin-assets") }

func newModelAssets(s *Service) modelAssets {
	return &hostedAssets{s: s, leases: map[string]int{}, downloads: map[string]*assetDownload{}}
}

type hostedAssets struct {
	s         *Service
	mu        sync.Mutex
	leases    map[string]int            // variant directory → holders
	downloads map[string]*assetDownload // destination file → the download writing it
}

// assetNeed is one variant a model needs, and where it lives.
type assetNeed struct {
	group, dir string
	fixed      bool
	v          plugins.AssetVariant
}

func modelErr(code, format string, args ...any) *ModelError {
	return &ModelError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// variantDir is where a variant's files live: named by its files, so an
// update that keeps them keeps the download.
func (h *hostedAssets) variantDir(v plugins.AssetVariant) string {
	lines := make([]string, len(v.Files))
	for i, f := range v.Files {
		lines[i] = f.Name + "\x00" + f.SHA256
	}
	slices.Sort(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return filepath.Join(h.s.assetsDir(), hex.EncodeToString(sum[:16]))
}

// resolve finds what a model of plugin's installed release needs on this
// platform. A *ModelError says why it cannot be used; checkMemory false skips
// the memory requirement (for Remove).
func (h *hostedAssets) resolve(plugin, release string, refs map[string]string, checkMemory bool) ([]assetNeed, error) {
	src := h.s.opts.Registry
	if src == nil {
		return nil, modelErr(codeModelUnavailable, "plugin %s: no plugin registry is configured", plugin)
	}
	h.s.registryMu.Lock()
	reg, err := h.s.cachedRegistry(src)
	h.s.registryMu.Unlock()
	if err != nil {
		return nil, modelErr(codeModelUnavailable, "plugin %s: the plugin registry cannot be read", plugin)
	}
	platform := plugins.PlatformKey()
	var rel *plugins.RegistryRelease
	for _, p := range reg.Plugins {
		if p.Name != plugin {
			continue
		}
		for i := range p.Releases {
			if plugins.AssetsDigest(p.Releases[i].AssetsFor(platform)) == release {
				rel = &p.Releases[i]
			}
		}
	}
	if rel == nil {
		return nil, modelErr(codeModelUnavailable, "plugin %s: its release is no longer in the plugin registry", plugin)
	}
	here := map[string]plugins.Asset{}
	for _, a := range rel.AssetsFor(platform) {
		here[a.Name] = a
	}
	var needs []assetNeed
	for _, a := range rel.Assets {
		_, named := refs[a.Name]
		if _, ok := here[a.Name]; !ok && (named || len(a.Variants) == 1) {
			return nil, modelErr(codeUnsupportedPlatform, "plugin %s: %s is not available for %s", plugin, a.Name, platform)
		}
	}
	for group, id := range refs {
		a, ok := here[group]
		i := slices.IndexFunc(a.Variants, func(v plugins.AssetVariant) bool { return v.ID == id })
		if !ok || i < 0 {
			return nil, modelErr(codeModelUnavailable, "plugin %s: its release has no %s %s", plugin, group, id)
		}
		needs = append(needs, assetNeed{group: group, v: a.Variants[i]})
	}
	for _, a := range here {
		if _, named := refs[a.Name]; !named && len(a.Variants) == 1 {
			needs = append(needs, assetNeed{group: a.Name, v: a.Variants[0], fixed: true})
		}
	}
	slices.SortFunc(needs, func(a, b assetNeed) int { return strings.Compare(a.group, b.group) })
	need := 0.0
	for i := range needs {
		needs[i].dir = h.variantDir(needs[i].v)
		need = max(need, needs[i].v.MinMemoryGB.For(platform))
	}
	// Decimal GB: a machine sold as 8 GB reports a little under 8 GiB on Linux.
	if have := float64(physicalMemory()) / 1e9; checkMemory && need > 0 && have > 0 && have < need {
		return nil, modelErr(codeInsufficientMemory, "plugin %s: this model needs %g GB of memory; this computer has %.1f GB", plugin, need, have)
	}
	return needs, nil
}

func dirsOf(needs []assetNeed) map[string]string {
	out := map[string]string{}
	for _, n := range needs {
		out[n.group] = n.dir
	}
	return out
}

func (h *hostedAssets) partial(n assetNeed, f plugins.AssetFile) string {
	return filepath.Join(h.s.assetsDir(), ".partial", filepath.Base(n.dir)+"-"+f.SHA256)
}

// present reports a file in place with its registry size (not its hash).
func present(path string, size int64) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode().IsRegular() && fi.Size() == size
}

func (h *hostedAssets) Status(ctx context.Context, plugin, release string, refs map[string]string) (ModelStatus, error) {
	if err := ctx.Err(); err != nil {
		return ModelStatus{}, err
	}
	needs, err := h.resolve(plugin, release, refs, true)
	var me *ModelError
	if errors.As(err, &me) {
		return ModelStatus{State: ModelErrorState, Error: me}, nil
	}
	st := ModelStatus{State: ModelDownloaded}
	seen := map[string]bool{}
	for _, n := range needs {
		for _, f := range n.v.Files {
			if p := filepath.Join(n.dir, f.Name); !seen[p] {
				seen[p] = true
				st.TotalBytes += f.Size
				if present(p, f.Size) {
					st.DownloadedBytes += f.Size
					continue
				}
				st.State = ModelNotDownloaded
				if fi, err := os.Stat(h.partial(n, f)); err == nil {
					st.DownloadedBytes += min(fi.Size(), f.Size)
				}
			}
		}
	}
	if st.State == ModelNotDownloaded {
		st.Error = modelErr(codeModelNotReady, "plugin %s: the model is not downloaded", plugin)
	}
	return st, nil
}

// lease holds dirs until the returned func runs (any number of times).
func (h *hostedAssets) lease(needs []assetNeed) func() {
	h.mu.Lock()
	for _, n := range needs {
		h.leases[n.dir]++
	}
	h.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			for _, n := range needs {
				if h.leases[n.dir]--; h.leases[n.dir] <= 0 {
					delete(h.leases, n.dir)
				}
			}
		})
	}
}

func (h *hostedAssets) Verify(ctx context.Context, plugin, release string, refs map[string]string) (map[string]string, func(), error) {
	needs, err := h.resolve(plugin, release, refs, true)
	if err != nil {
		return nil, nil, err
	}
	drop := h.lease(needs)
	for _, n := range needs {
		if err := verifyVariant(ctx, plugin, n); err != nil {
			drop()
			return nil, nil, err
		}
	}
	return dirsOf(needs), drop, nil
}

func (h *hostedAssets) Prepare(ctx context.Context, plugin, release string, refs map[string]string, progress func(ModelStatus)) (map[string]string, func(), error) {
	needs, err := h.resolve(plugin, release, refs, true)
	if err != nil {
		return nil, nil, err
	}
	if progress == nil {
		progress = func(ModelStatus) {}
	}
	drop := h.lease(needs)
	fail := func(err error) (map[string]string, func(), error) { drop(); return nil, nil, err }
	var total, done int64
	for _, n := range needs {
		for _, f := range n.v.Files {
			total += f.Size
		}
	}
	type missingFile struct {
		n   assetNeed
		f   plugins.AssetFile
		dst string
	}
	// Twice at most: a file that fails its check is downloaded again once.
	for attempt := 0; ; attempt++ {
		done = 0
		var missing []missingFile
		for _, n := range needs {
			for _, f := range n.v.Files {
				if dst := filepath.Join(n.dir, f.Name); present(dst, f.Size) {
					done += f.Size
				} else {
					missing = append(missing, missingFile{n, f, dst})
				}
			}
		}
		progress(ModelStatus{State: ModelDownloading, DownloadedBytes: done, TotalBytes: total})
		for _, m := range missing {
			err := h.fetch(ctx, m.n, m.f, m.dst, func(got int64) {
				progress(ModelStatus{State: ModelDownloading, DownloadedBytes: done + got, TotalBytes: total})
			})
			if err != nil {
				return fail(err)
			}
			done += m.f.Size
		}
		progress(ModelStatus{State: ModelVerifying, DownloadedBytes: total, TotalBytes: total})
		var bad error
		for _, n := range needs {
			if err := verifyVariant(ctx, plugin, n); err != nil {
				var me *ModelError
				if !errors.As(err, &me) || attempt > 0 {
					return fail(err)
				}
				bad = err
				os.RemoveAll(n.dir) // the user asked to download it: replace what fails
			}
		}
		if bad == nil {
			return dirsOf(needs), drop, nil
		}
	}
}

func (h *hostedAssets) Remove(ctx context.Context, plugin, release string, refs map[string]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	needs, err := h.resolve(plugin, release, refs, false)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, n := range needs {
		if !n.fixed && h.leases[n.dir] > 0 {
			return modelErr(codeModelUnavailable, "plugin %s: the model is in use", plugin)
		}
	}
	// shortcut: a variant another model of the plugin also names goes with this one, and a
	// directory Windows will not delete stays until the next Remove; track both if that matters.
	for _, n := range needs {
		if n.fixed {
			continue
		}
		if err := os.RemoveAll(n.dir); err != nil {
			return err
		}
		for _, f := range n.v.Files {
			os.Remove(h.partial(n, f))
		}
	}
	return nil
}

// verifyVariant checks n's directory holds exactly its files, each a plain
// file with its size and SHA-256, none quarantined: a missing file is
// model_not_ready, anything else verification_failed.
func verifyVariant(ctx context.Context, plugin string, n assetNeed) error {
	notReady := modelErr(codeModelNotReady, "plugin %s: the model is not downloaded", plugin)
	bad := func(name, why string) error {
		return modelErr(codeVerificationFailed, "plugin %s: %s %s: %s; download it again", plugin, n.group, name, why)
	}
	ents, err := os.ReadDir(n.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return notReady
	}
	if err != nil {
		return err
	}
	want := map[string]plugins.AssetFile{}
	for _, f := range n.v.Files {
		want[f.Name] = f
	}
	for _, e := range ents {
		if _, ok := want[e.Name()]; !ok {
			return bad(e.Name(), "a file the registry does not list")
		}
	}
	for _, f := range n.v.Files {
		p := filepath.Join(n.dir, f.Name)
		fi, err := os.Lstat(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return notReady
		case err != nil:
			return err
		case !fi.Mode().IsRegular():
			return bad(f.Name, "not a plain file")
		case fi.Size() != f.Size:
			return bad(f.Name, "the wrong size")
		case quarantined(p):
			return bad(f.Name, "marked as downloaded from the internet") // never cleared here: macOS refuses to load it
		}
		sum, err := fileSHA256(ctx, p)
		if err != nil {
			return err
		}
		if sum != f.SHA256 {
			return bad(f.Name, "its contents changed")
		}
	}
	return nil
}

// fileSHA256 hashes path, stopping when ctx ends.
func fileSHA256(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, ctxReader{ctx, f}); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// assetDownload is one file being downloaded for everyone who waits for it.
type assetDownload struct {
	waiters int
	cancel  context.CancelFunc
	got     atomic.Int64
	done    chan struct{}
	err     error
}

// fetch downloads f into dst, sharing one download among callers for the
// same file; a caller that gives up leaves it running for the others, and the
// last one to give up stops it. onBytes gets f's bytes so far.
func (h *hostedAssets) fetch(ctx context.Context, n assetNeed, f plugins.AssetFile, dst string, onBytes func(int64)) error {
	h.mu.Lock()
	d := h.downloads[dst]
	if d == nil {
		dctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		d = &assetDownload{cancel: cancel, done: make(chan struct{})}
		h.downloads[dst] = d
		go func() {
			d.err = h.download(dctx, n, f, dst, &d.got)
			cancel()
			h.mu.Lock()
			delete(h.downloads, dst)
			h.mu.Unlock()
			close(d.done)
		}()
	}
	d.waiters++
	h.mu.Unlock()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-d.done:
			onBytes(f.Size)
			return d.err
		case <-tick.C:
			onBytes(d.got.Load())
		case <-ctx.Done():
			h.mu.Lock()
			if d.waiters--; d.waiters == 0 {
				d.cancel()
			}
			h.mu.Unlock()
			return ctx.Err()
		}
	}
}

// download resumes f's partial file, checks it is exactly f, and moves it to dst.
func (h *hostedAssets) download(ctx context.Context, n assetNeed, f plugins.AssetFile, dst string, got *atomic.Int64) error {
	src := h.s.opts.Registry
	if src == nil {
		return ErrRegistryOff
	}
	if !strings.HasPrefix(f.URL, "https://") {
		return fmt.Errorf("plugin asset: %s is not https", f.URL)
	}
	part := h.partial(n, f)
	if err := os.MkdirAll(filepath.Dir(part), 0o700); err != nil {
		return err
	}
	// Twice at most: a server that will not resume (416, a wrong range) starts over once.
	for attempt := 0; ; attempt++ {
		err := h.downloadOnce(ctx, src, f, part, got)
		if err == errAssetRestart && attempt == 0 {
			os.Remove(part)
			continue
		}
		if err != nil {
			if errors.Is(err, ErrRegistryMismatch) || errors.Is(err, errAssetWrite) {
				os.Remove(part) // not worth resuming: not the registry's file, or no room for it
			}
			return err
		}
		break
	}
	sum, err := fileSHA256(ctx, part)
	if err != nil {
		return err
	}
	if sum != f.SHA256 {
		os.Remove(part)
		return ErrRegistryMismatch
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	return os.Rename(part, dst)
}

var (
	errAssetRestart = errors.New("plugin asset: the server cannot resume this download")
	errAssetStalled = errors.New("plugin asset: the download stopped receiving data")
	errAssetWrite   = errors.New("plugin asset: cannot write the download")
)

func (h *hostedAssets) downloadOnce(ctx context.Context, src *RegistrySource, f plugins.AssetFile, part string, got *atomic.Int64) error {
	out, err := os.OpenFile(part, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	have, err := out.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	if have > f.Size {
		return errAssetRestart
	}
	got.Store(have)
	if have == f.Size {
		return nil
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stall := time.AfterFunc(assetStall, func() { cancel(errAssetStalled) })
	defer stall.Stop()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.URL, nil)
	if err != nil {
		return err
	}
	if have > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(have, 10)+"-")
	}
	c := src.Client
	if c == nil {
		c = &http.Client{}
	}
	resp, err := c.Do(req)
	if err != nil {
		return errors.Join(context.Cause(ctx), fmt.Errorf("plugin asset: %w", err))
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusPartialContent && have > 0:
		if resp.Header.Get("Content-Range") != fmt.Sprintf("bytes %d-%d/%d", have, f.Size-1, f.Size) {
			return errAssetRestart
		}
	case resp.StatusCode == http.StatusOK:
		if err := out.Truncate(0); err != nil {
			return err
		}
		if _, err := out.Seek(0, io.SeekStart); err != nil {
			return err
		}
		have = 0
		got.Store(0)
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		return errAssetRestart
	default:
		return fmt.Errorf("plugin asset: %s answered %s", f.URL, resp.Status)
	}
	buf := make([]byte, 256<<10)
	for {
		k, rerr := resp.Body.Read(buf)
		if k > 0 {
			stall.Reset(assetStall)
			if have+int64(k) > f.Size {
				return ErrRegistryMismatch // larger than the registry says
			}
			if _, err := out.Write(buf[:k]); err != nil {
				return errors.Join(errAssetWrite, err)
			}
			have += int64(k)
			got.Store(have)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return errors.Join(context.Cause(ctx), fmt.Errorf("plugin asset: %w", rerr))
		}
	}
	if have != f.Size {
		return fmt.Errorf("plugin asset: %s ended after %d of %d bytes", f.URL, have, f.Size)
	}
	return nil
}
