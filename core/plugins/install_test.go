package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		p = filepath.Join(dir, filepath.FromSlash(p))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
		require.NoError(t, os.WriteFile(p, []byte(c), 0o600))
	}
}

func TestSnapshotRejects(t *testing.T) {
	base := map[string]string{"plugin.json": "{}", "index.ts": "export default function () {}"}

	src := t.TempDir()
	writeFiles(t, src, base)
	require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(src, "link")))
	dst := filepath.Join(t.TempDir(), "p")
	_, err := Snapshot(src, dst)
	assert.ErrorContains(t, err, "link")
	assert.NoDirExists(t, dst)

	src = t.TempDir()
	writeFiles(t, src, base)
	writeFiles(t, src, map[string]string{"big.bin": strings.Repeat("x", MaxFileBytes+1)})
	_, err = Snapshot(src, dst)
	assert.ErrorContains(t, err, "big.bin")
	assert.NoDirExists(t, dst)

	src = t.TempDir()
	writeFiles(t, src, base)
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		writeFiles(t, src, map[string]string{n + ".bin": strings.Repeat("x", MaxFileBytes-1)})
	}
	_, err = Snapshot(src, dst)
	assert.ErrorContains(t, err, "too large")
	assert.NoDirExists(t, dst)

	// A good one: .build is skipped, the hash covers the snapshot, a second
	// snapshot replaces the first.
	src = t.TempDir()
	writeFiles(t, src, base)
	writeFiles(t, src, map[string]string{"lib/util.ts": "export const x = 1", ".build/old.js": "junk"})
	h, err := Snapshot(src, dst)
	require.NoError(t, err)
	assert.NoDirExists(t, filepath.Join(dst, ".build"))
	assert.FileExists(t, filepath.Join(dst, "lib", "util.ts"))
	h2, err := HashDir(dst)
	require.NoError(t, err)
	assert.Equal(t, h, h2)

	writeFiles(t, src, map[string]string{"lib/util.ts": "export const x = 2"})
	h3, err := Snapshot(src, dst)
	require.NoError(t, err)
	assert.NotEqual(t, h, h3)
	b, _ := os.ReadFile(filepath.Join(dst, "lib", "util.ts"))
	assert.Equal(t, "export const x = 2", string(b))
	left, _ := os.ReadDir(filepath.Dir(dst))
	assert.Len(t, left, 1, "no temp directories left behind")
}

func TestBuildFromSnapshotOnly(t *testing.T) {
	src := t.TempDir()
	writeFiles(t, src, map[string]string{
		"index.ts":    `import type { Ctx } from "@godwit/sdk"; import { word } from "./lib/util"; export default function (ctx: Ctx) { return word }`,
		"lib/util.ts": `export const word = "snapshot-one"`,
	})
	dst := filepath.Join(t.TempDir(), "p")
	_, err := Snapshot(src, dst)
	require.NoError(t, err)
	writeFiles(t, src, map[string]string{"lib/util.ts": `export const word = "changed-later"`})
	out, err := Build(dst, "index.ts")
	require.NoError(t, err)
	assert.Contains(t, string(out), "snapshot-one")
	assert.NotContains(t, string(out), "changed-later")
	assert.Contains(t, string(out), "var plugin")

	for name, code := range map[string]string{
		"escape":   `import x from "../../etc/x"; export default x`,
		"sibling":  `import x from "../outside"; export default x`,
		"bare":     `import { on } from "@godwit/sdk"; export default on`,
		"absolute": `import x from "/etc/x"; export default x`,
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeFiles(t, root, map[string]string{"outside.ts": `export default 1`})
			src := t.TempDir()
			writeFiles(t, src, map[string]string{"index.ts": code})
			dst := filepath.Join(root, "p")
			_, err := Snapshot(src, dst)
			require.NoError(t, err)
			_, err = Build(dst, "index.ts")
			assert.ErrorContains(t, err, "index.ts:1: cannot import")
			assert.NotContains(t, err.Error(), root, "no host paths")
		})
	}
}

type countingCompiler struct {
	n   int
	key string
}

func (c *countingCompiler) Key() string { return c.key }

func (c *countingCompiler) Compile(dir, main string) ([]byte, error) {
	c.n++
	return ESBuild.Compile(dir, main)
}

func TestBuildCache(t *testing.T) {
	src := t.TempDir()
	writeFiles(t, src, map[string]string{"index.ts": `export default function () { return "v1" }`})
	dst := filepath.Join(t.TempDir(), "p")
	_, err := Snapshot(src, dst)
	require.NoError(t, err)

	c := &countingCompiler{key: "opts-a"}
	a, err := BuildWith(c, dst, "index.ts")
	require.NoError(t, err)
	b, err := BuildWith(c, dst, "index.ts")
	require.NoError(t, err)
	assert.Equal(t, 1, c.n, "same snapshot, same options: cached")
	assert.Equal(t, a, b)

	c.key = "opts-b"
	_, err = BuildWith(c, dst, "index.ts")
	require.NoError(t, err)
	assert.Equal(t, 2, c.n, "other options: compiled again")

	writeFiles(t, dst, map[string]string{"index.ts": `export default function () { return "v2" }`})
	out, err := BuildWith(c, dst, "index.ts")
	require.NoError(t, err)
	assert.Equal(t, 3, c.n, "other sources: compiled again")
	assert.Contains(t, string(out), "v2")
	cached, _ := os.ReadDir(filepath.Join(dst, ".build"))
	assert.Len(t, cached, 1, "only the current build is kept")

	assert.NotEmpty(t, ESBuild.Key())
}
