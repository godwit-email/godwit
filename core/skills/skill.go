// Package skills loads SKILL.md directories (Agent Skills standard). It never
// executes scripts/ and only reads references/ and assets/.
package skills

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/goccy/go-yaml"
)

//go:embed builtin
var builtinFS embed.FS

// MaxFileBytes is the largest file ReadFile returns.
const MaxFileBytes = 256 << 10

type Skill struct {
	Name, Description, Body string
	On                      []string
	Output, Model           string
	Builtin                 bool
	Version                 string // first 16 hex digits of SHA-256 of SKILL.md
	FS                      fs.FS  // the skill directory (built-ins)
	dir                     string // real directory (user skills); read through a fresh os.Root per call
}

var (
	nameRE  = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	onSet   = []string{"message", "thread", "selection", "draft"}
	outSet  = []string{"panel", "replace-body", "replace-draft"}
	errRead = errors.New("skill file not readable")
)

// ValidName reports whether name is a valid skill (or plugin) name: lowercase
// letters, digits and single hyphens between them.
func ValidName(name string) bool { return nameRE.MatchString(name) }

// Parse reads SKILL.md from fsys, the skill directory named name. The warnings
// are non-fatal (a scripts/ directory, which is never run).
func Parse(name string, fsys fs.FS) (Skill, []string, error) {
	if !ValidName(name) {
		return Skill{}, nil, fmt.Errorf("skill name %q: lowercase letters, digits and hyphens only", name)
	}
	raw, err := fs.ReadFile(fsys, "SKILL.md")
	if err != nil {
		return Skill{}, nil, fmt.Errorf("skill %s: %w", name, err)
	}
	front, body, ok := bytes.Cut(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")), []byte("\n---"))
	if !ok || !bytes.HasPrefix(front, []byte("---")) {
		return Skill{}, nil, fmt.Errorf("skill %s: SKILL.md needs --- front matter", name)
	}
	var fm struct {
		Name, Description string
		Metadata          map[string]string
	}
	if err := yaml.Unmarshal(bytes.TrimPrefix(front, []byte("---")), &fm); err != nil {
		return Skill{}, nil, fmt.Errorf("skill %s: front matter: %w", name, err)
	}
	if fm.Name != name {
		return Skill{}, nil, fmt.Errorf("skill %s: name %q must match its directory", name, fm.Name)
	}
	if strings.TrimSpace(fm.Description) == "" {
		return Skill{}, nil, fmt.Errorf("skill %s: description is required", name)
	}
	_, rest, _ := strings.Cut(string(body), "\n") // after the closing --- line
	sum := sha256.Sum256(raw)
	s := Skill{
		Name: name, Description: strings.TrimSpace(fm.Description), Body: strings.TrimSpace(rest),
		Output: fm.Metadata["godwit-action-output"], Model: fm.Metadata["godwit-model"],
		Version: hex.EncodeToString(sum[:])[:16], FS: fsys,
	}
	if on := fm.Metadata["godwit-action-on"]; on != "" {
		for _, v := range strings.Split(on, ",") {
			v = strings.TrimSpace(v)
			if !slices.Contains(onSet, v) {
				return Skill{}, nil, fmt.Errorf("skill %s: godwit-action-on %q is not one of %v", name, v, onSet)
			}
			s.On = append(s.On, v)
		}
	}
	if s.Output != "" && !slices.Contains(outSet, s.Output) {
		return Skill{}, nil, fmt.Errorf("skill %s: godwit-action-output %q is not one of %v", name, s.Output, outSet)
	}
	if (len(s.On) == 0) != (s.Output == "") {
		return Skill{}, nil, fmt.Errorf("skill %s: godwit-action-on and godwit-action-output go together", name)
	}
	var warns []string
	if st, err := fs.Stat(fsys, "scripts"); err == nil && st.IsDir() {
		warns = append(warns, "scripts/ is ignored: Godwit never runs skill scripts")
	}
	return s, warns, nil
}

// ReadFile returns a file under references/ or assets/ of the skill directory.
// For user skills every path component must be a real directory or file inside
// the skill: symlinks are refused, so links cannot reach scripts/ or other skills.
func (s Skill) ReadFile(p string) ([]byte, error) {
	if strings.HasPrefix(p, "/") || strings.Contains(p, `\`) || slices.Contains(strings.Split(p, "/"), "..") {
		return nil, fmt.Errorf("%w: %q", errRead, p)
	}
	p = path.Clean(p)
	if !strings.HasPrefix(p, "references/") && !strings.HasPrefix(p, "assets/") {
		return nil, fmt.Errorf("%w: only references/ and assets/ can be read", errRead)
	}
	fsys := s.FS
	if s.dir != "" {
		root, err := os.OpenRoot(s.dir)
		if err != nil {
			return nil, err
		}
		defer root.Close()
		for i := 0; i < len(p); i++ {
			if i == len(p)-1 || p[i+1] == '/' {
				st, err := root.Lstat(p[:i+1])
				if err != nil {
					return nil, err
				}
				if st.Mode()&fs.ModeSymlink != 0 {
					return nil, fmt.Errorf("%w: %q contains a symlink", errRead, p)
				}
			}
		}
		fsys = root.FS()
	}
	f, err := fsys.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %q is not a regular file", errRead, p)
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxFileBytes {
		return nil, fmt.Errorf("%w: %q exceeds %d bytes", errRead, p, MaxFileBytes)
	}
	return b, nil
}

// References lists the regular files under references/, as paths ReadFile
// takes. Symlinks are left out (ReadFile refuses them).
func (s Skill) References() ([]string, error) {
	fsys := s.FS
	if s.dir != "" {
		root, err := os.OpenRoot(s.dir)
		if err != nil {
			return nil, err
		}
		defer root.Close()
		fsys = root.FS()
	}
	var out []string
	err := fs.WalkDir(fsys, "references", func(p string, d fs.DirEntry, err error) error {
		switch {
		case p == "references" && errors.Is(err, fs.ErrNotExist):
			return fs.SkipAll
		case err != nil:
			return err
		case d.Type().IsRegular():
			out = append(out, p)
		}
		return nil
	})
	return out, err
}

// Load parses every directory of root as a skill. Directories that fail are
// returned in failed, keyed by name.
func Load(root fs.FS, builtin bool) (ok []Skill, failed map[string]error) {
	failed = map[string]error{}
	ents, _ := fs.ReadDir(root, ".")
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		sub, err := fs.Sub(root, e.Name())
		if err == nil {
			var s Skill
			if s, _, err = Parse(e.Name(), sub); err == nil {
				s.Builtin = builtin
				ok = append(ok, s)
				continue
			}
		}
		failed[e.Name()] = err
	}
	return ok, failed
}

// LoadDir loads the user skills in dir. A missing dir means none; any other
// error reading it is returned. Per-skill failures go in failed.
func LoadDir(dir string) (ok []Skill, failed map[string]error, err error) {
	failed = map[string]error{}
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, failed, nil
	}
	if err != nil {
		return nil, nil, err
	}
	for _, e := range ents {
		if !e.IsDir() { // a symlinked skill directory is not a directory entry here
			continue
		}
		sd := filepath.Join(dir, e.Name())
		root, err := os.OpenRoot(sd)
		if err != nil {
			failed[e.Name()] = err
			continue
		}
		sk, _, err := Parse(e.Name(), root.FS())
		root.Close()
		if err != nil {
			failed[e.Name()] = err
			continue
		}
		sk.FS, sk.dir = nil, sd
		ok = append(ok, sk)
	}
	return ok, failed, nil
}

// Builtins returns the skills compiled into the binary.
func Builtins() []Skill {
	root, _ := fs.Sub(builtinFS, "builtin")
	ok, _ := Load(root, true)
	return ok
}
