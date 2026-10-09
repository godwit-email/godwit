package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

// Snapshot limits.
const (
	MaxFileBytes  = 5 << 20
	MaxTotalBytes = 20 << 20
)

// BuildDir is where a snapshot keeps its compiled code; it is not part of the snapshot.
const BuildDir = ".build"

// files lists the regular files under dir (slash paths, sorted), skipping
// BuildDir. Symlinks, devices and other special files, and oversized files or
// totals, are errors.
func files(dir string) ([]string, error) {
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
		if info.Size() > MaxFileBytes {
			return fmt.Errorf("%s: larger than %d MB", rel, MaxFileBytes>>20)
		}
		if total += info.Size(); total > MaxTotalBytes {
			return fmt.Errorf("plugin is too large (over %d MB)", MaxTotalBytes>>20)
		}
		out = append(out, rel)
		return nil
	})
	slices.Sort(out)
	return out, err
}

// HashDir hashes the files of a plugin directory as Snapshot does.
func HashDir(dir string) (string, error) {
	names, err := files(dir)
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

// Snapshot copies the plugin directory src to dst, replacing dst as a whole,
// and returns the hash of the copy. A failed snapshot leaves dst as it was.
func Snapshot(src, dst string) (sourceHash string, err error) {
	names, err := files(src)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dst), ".snapshot-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp) // gone after Replace renamed it
	for _, n := range names {
		if err := copyFile(filepath.Join(src, filepath.FromSlash(n)), filepath.Join(tmp, filepath.FromSlash(n))); err != nil {
			return "", err
		}
	}
	// Hash the copy, not src, which may have changed while we copied it.
	if sourceHash, err = HashDir(tmp); err != nil {
		return "", err
	}
	return sourceHash, Replace(tmp, dst)
}

func copyFile(from, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		return err
	}
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, io.LimitReader(in, MaxFileBytes+1)) // grown since files(): HashDir rejects it
	return errors.Join(err, out.Close())
}

// Replace moves the directory dir to dst, replacing whatever is there. dst is
// either the old or the new directory at any time, never a mix; on Windows a
// rename cannot replace a directory, so the old one is moved aside first.
func Replace(dir, dst string) error {
	old := ""
	if _, err := os.Lstat(dst); err == nil {
		aside, err := os.MkdirTemp(filepath.Dir(dst), ".old-")
		if err != nil {
			return err
		}
		old = filepath.Join(aside, "d")
		if err := os.Rename(dst, old); err != nil {
			os.Remove(aside)
			return err
		}
		defer os.RemoveAll(aside)
	}
	if err := os.Rename(dir, dst); err != nil {
		if old != "" {
			err = errors.Join(err, os.Rename(old, dst))
		}
		return err
	}
	return nil
}

// Compiler turns a snapshot into the script the plugin VM runs.
type Compiler interface {
	// Key identifies the compiler version and options: builds cached under
	// another key are not reused.
	Key() string
	Compile(dir, main string) ([]byte, error)
}

// ESBuild bundles TypeScript or JavaScript with esbuild.
var ESBuild Compiler = esbuildCompiler{}

type esbuildCompiler struct{}

func esbuildOptions() api.BuildOptions {
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

func (esbuildCompiler) Key() string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s\x00%+v", esbuildVersion(), esbuildOptions()))
	return hex.EncodeToString(sum[:])
}

// Compile bundles main and what it imports, all from inside dir: a relative
// import that leaves dir, an absolute path or a bare module name fails. Type-only
// imports ("import type ... from '@godwit/sdk'") are erased before resolving.
func (esbuildCompiler) Compile(dir, main string) ([]byte, error) {
	root, err := filepath.Abs(dir)
	if err == nil {
		root, err = filepath.EvalSymlinks(root) // esbuild resolves them in ResolveDir ("/var" is "/private/var" on macOS)
	}
	if err != nil {
		return nil, err
	}
	opts := esbuildOptions()
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
	h, err := HashDir(dir)
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
