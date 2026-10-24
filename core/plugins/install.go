package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"syscall"

	"github.com/evanw/esbuild/pkg/api"
)

// Limits caps a snapshot: each file, and all of them together, in bytes.
type Limits struct{ File, Total int64 }

// Snapshot limits by kind: a native plugin carries a shared library.
var (
	ScriptLimits = Limits{File: 5 << 20, Total: 20 << 20}
	NativeLimits = Limits{File: 64 << 20, Total: 80 << 20}
)

// LibraryName is the file a native plugin's shared library has in its
// directory on this platform.
func LibraryName() string {
	switch runtime.GOOS {
	case "darwin":
		return "plugin.dylib"
	case "windows":
		return "plugin.dll"
	}
	return "plugin.so"
}

// BuildDir is where a snapshot keeps its compiled code; it is not part of the snapshot.
const BuildDir = ".build"

// files lists the regular files under dir (slash paths, sorted), skipping
// BuildDir. Symlinks, devices and other special files, and oversized files or
// totals, are errors.
func files(dir string, lim Limits) ([]string, error) {
	var out []string
	var total int64
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if !fs.ValidPath(rel) {
			return fmt.Errorf("%s: not a path inside the plugin", rel)
		}
		if d.IsDir() {
			if rel == BuildDir {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s: only regular files are allowed (no symlinks or devices)", rel)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > lim.File {
			return fmt.Errorf("%s: larger than %d MB", rel, lim.File>>20)
		}
		if total += info.Size(); total > lim.Total {
			return fmt.Errorf("plugin is too large (over %d MB)", lim.Total>>20)
		}
		out = append(out, rel)
		return nil
	})
	slices.Sort(out)
	return out, err
}

// HashDir hashes the files of a plugin directory as Snapshot does.
func HashDir(dir string, lim Limits) (string, error) {
	names, err := files(dir, lim)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(n)))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", n, len(b))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Snapshot copies the plugin directory src to dst, which must not exist yet,
// and returns the hash of the copy. It copies into a sibling temp directory and
// renames that to dst, so dst is complete or absent.
func Snapshot(src, dst string, lim Limits) (sourceHash string, err error) {
	root, err := os.OpenRoot(src)
	if err != nil {
		return "", err
	}
	defer root.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dst), ".snapshot-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp) // gone once renamed to dst
	var total int64
	if err := copyTree(root, "", tmp, &total, lim); err != nil {
		return "", err
	}
	// Hash the copy, not src, which may change while we copy it.
	if sourceHash, err = HashDir(tmp, lim); err != nil {
		return "", err
	}
	return sourceHash, os.Rename(tmp, dst)
}

// beforeOpen, if set, runs after an entry was checked and before it is opened (tests).
var beforeOpen func(rel string)

// copyTree copies the directory r (at slash path prefix) into dst. Every entry
// is Lstat-ed (regular files and directories only) and then opened through r;
// the opened file must be the one checked (os.SameFile), so swapping in a
// symlink, or a symlinked parent, between check and use fails.
func copyTree(r *os.Root, prefix, dst string, total *int64, lim Limits) error {
	d, err := r.Open(".")
	if err != nil {
		return err
	}
	ents, err := d.ReadDir(-1)
	d.Close()
	if err != nil {
		return err
	}
	for _, e := range ents {
		name, rel := e.Name(), path.Join(prefix, e.Name())
		if rel == BuildDir {
			continue
		}
		fi, err := r.Lstat(name)
		if err != nil {
			return err
		}
		switch {
		case fi.IsDir():
			if beforeOpen != nil {
				beforeOpen(rel)
			}
			sub, err := r.OpenRoot(name)
			if err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}
			err = sameFile(rel, fi, func() (fs.FileInfo, error) { return sub.Stat(".") })
			if err == nil {
				if err = os.Mkdir(filepath.Join(dst, name), 0o700); err == nil {
					err = copyTree(sub, rel, filepath.Join(dst, name), total, lim)
				}
			}
			sub.Close()
			if err != nil {
				return err
			}
		case fi.Mode().IsRegular():
			if fi.Size() > lim.File {
				return fmt.Errorf("%s: larger than %d MB", rel, lim.File>>20)
			}
			if *total += fi.Size(); *total > lim.Total {
				return fmt.Errorf("plugin is too large (over %d MB)", lim.Total>>20)
			}
			if beforeOpen != nil {
				beforeOpen(rel)
			}
			if err := copyFile(r, name, rel, fi, filepath.Join(dst, name), lim.File); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%s: only regular files are allowed (no symlinks or devices)", rel)
		}
	}
	return nil
}

func sameFile(rel string, want fs.FileInfo, stat func() (fs.FileInfo, error)) error {
	got, err := stat()
	if err != nil {
		return fmt.Errorf("%s: %w", rel, err)
	}
	if !os.SameFile(want, got) {
		return fmt.Errorf("%s: changed while copying (no symlinks allowed)", rel)
	}
	return nil
}

func copyFile(r *os.Root, name, rel string, want fs.FileInfo, to string, max int64) error {
	// O_NONBLOCK: a FIFO swapped in after the check must not block the open.
	in, err := r.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("%s: %w", rel, err)
	}
	defer in.Close()
	if err := sameFile(rel, want, in.Stat); err != nil {
		return err
	}
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, io.LimitReader(in, max+1)) // grown since the check: HashDir rejects it
	return errors.Join(err, out.Close())
}

// Compiler turns a snapshot into the script the plugin VM runs.
type Compiler interface {
	// Key identifies the compiler version and options: builds cached under
	// another key are not reused.
	Key() string
	Compile(dir, main string) ([]byte, error)
}

// ESBuild bundles TypeScript or JavaScript with esbuild, for goja: an IIFE
// that sets `plugin`, with what goja lacks down-levelled.
var ESBuild Compiler = esbuildCompiler{}

// ESBuildModule bundles for moejs (MoeVM): one ES module whose default export
// is the plugin, without goja's down-levelling.
var ESBuildModule Compiler = esbuildCompiler{module: true}

type esbuildCompiler struct{ module bool }

func (c esbuildCompiler) options() api.BuildOptions {
	if c.module {
		return api.BuildOptions{Bundle: true, Write: false, Format: api.FormatESModule, Target: api.ES2022, Platform: api.PlatformNeutral}
	}
	return api.BuildOptions{
		Bundle: true, Write: false, Format: api.FormatIIFE, GlobalName: "plugin",
		Target: api.ES2022, Platform: api.PlatformNeutral,
		Supported: map[string]bool{"async-generator": false, "for-await": false},
	}
}

func esbuildVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, d := range bi.Deps {
			if d.Path == "github.com/evanw/esbuild" {
				if d.Replace != nil {
					d = d.Replace
				}
				return d.Version + d.Sum
			}
		}
	}
	return "unknown"
}

func (c esbuildCompiler) Key() string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s\x00%+v", esbuildVersion(), c.options()))
	return hex.EncodeToString(sum[:])
}

// Compile bundles main and what it imports, all from inside dir: a relative
// import that leaves dir, an absolute path or a bare module name fails. Type-only
// imports ("import type ... from '@godwit/sdk'") are erased before resolving.
func (c esbuildCompiler) Compile(dir, main string) ([]byte, error) {
	root, err := filepath.Abs(dir)
	if err == nil {
		root, err = filepath.EvalSymlinks(root) // esbuild resolves them in ResolveDir ("/var" is "/private/var" on macOS)
	}
	if err != nil {
		return nil, err
	}
	opts := c.options()
	opts.EntryPoints = []string{"./" + main}
	opts.AbsWorkingDir = root
	opts.Plugins = []api.Plugin{{Name: "snapshot", Setup: func(b api.PluginBuild) {
		b.OnResolve(api.OnResolveOptions{Filter: ".*"}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
			if !strings.HasPrefix(a.Path, "./") && !strings.HasPrefix(a.Path, "../") {
				return api.OnResolveResult{}, fmt.Errorf("cannot import %q: only relative imports inside the plugin", a.Path)
			}
			from := a.ResolveDir
			if from == "" {
				from = root
			}
			p, err := resolveIn(root, filepath.Join(from, filepath.FromSlash(a.Path)))
			if err != nil {
				return api.OnResolveResult{}, fmt.Errorf("cannot import %q: %w", a.Path, err)
			}
			return api.OnResolveResult{Path: p, Namespace: "snapshot"}, nil
		})
		b.OnLoad(api.OnLoadOptions{Filter: ".*", Namespace: "snapshot"}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
			loader, ok := loaders[filepath.Ext(a.Path)]
			if !ok {
				return api.OnLoadResult{}, fmt.Errorf("cannot import %s: unsupported file type", filepath.Base(a.Path))
			}
			b, err := os.ReadFile(a.Path)
			if err != nil {
				return api.OnLoadResult{}, err
			}
			s := string(b)
			return api.OnLoadResult{Contents: &s, Loader: loader, ResolveDir: filepath.Dir(a.Path)}, nil
		})
	}}}
	res := api.Build(opts)
	if len(res.Errors) > 0 {
		msgs := make([]string, 0, len(res.Errors))
		for _, e := range res.Errors {
			if e.Location != nil {
				file := strings.TrimPrefix(e.Location.File, "snapshot:")
				if rel, err := filepath.Rel(root, file); err == nil {
					file = filepath.ToSlash(rel) // no host paths in plugin errors
				}
				msgs = append(msgs, fmt.Sprintf("%s:%d: %s", file, e.Location.Line, e.Text))
			} else {
				msgs = append(msgs, e.Text)
			}
		}
		return nil, errors.New("build failed: " + strings.Join(msgs, "; "))
	}
	if len(res.OutputFiles) != 1 {
		return nil, fmt.Errorf("build failed: %d outputs", len(res.OutputFiles))
	}
	return res.OutputFiles[0].Contents, nil
}

var loaders = map[string]api.Loader{".ts": api.LoaderTS, ".js": api.LoaderJS, ".json": api.LoaderJSON}

// resolveIn finds the file p names (as is, or with .ts / .js, or its index),
// which must be inside root and not in BuildDir.
func resolveIn(root, p string) (string, error) {
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) ||
		rel == BuildDir || strings.HasPrefix(rel, BuildDir+string(filepath.Separator)) {
		return "", errors.New("outside the plugin")
	}
	for _, c := range []string{p, p + ".ts", p + ".js", filepath.Join(p, "index.ts"), filepath.Join(p, "index.js")} {
		if fi, err := os.Lstat(c); err == nil && fi.Mode().IsRegular() {
			return c, nil
		}
	}
	return "", errors.New("no such file in the plugin")
}

// Build compiles the snapshot in dir with ESBuild, reusing a cached build.
func Build(dir, main string) ([]byte, error) { return BuildWith(ESBuild, dir, main) }

// BuildWith compiles the snapshot in dir with c. The result is cached in
// dir/.build under the snapshot's hash, main and c.Key(); other builds there are removed.
func BuildWith(c Compiler, dir, main string) ([]byte, error) {
	h, err := HashDir(dir, ScriptLimits)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(h + "\x00" + main + "\x00" + c.Key()))
	cache := filepath.Join(dir, BuildDir)
	path := filepath.Join(cache, hex.EncodeToString(sum[:])+".js")
	if b, err := os.ReadFile(path); err == nil {
		return b, nil
	}
	out, err := c.Compile(dir, main)
	if err != nil {
		return nil, err
	}
	if err := os.RemoveAll(cache); err != nil {
		return nil, err
	}
	if err := os.Mkdir(cache, 0o700); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(cache, ".tmp-")
	if err != nil {
		return nil, err
	}
	_, err = tmp.Write(out)
	if err = errors.Join(err, tmp.Close()); err == nil {
		err = os.Rename(tmp.Name(), path) // a crash never leaves a partial build under its key
	}
	if err != nil {
		os.Remove(tmp.Name())
		return nil, err
	}
	return out, nil
}
