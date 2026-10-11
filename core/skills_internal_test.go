package core

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

func TestSkillLookup(t *testing.T) {
	keyring.MockInit()
	s, err := New(t.Context(), Options{DataDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })

	sk, err := s.skill(t.Context(), "summarize")
	require.NoError(t, err)
	require.Equal(t, "summarize", sk.Name)
	require.ErrorIs(t, func() error { _, err := s.skill(t.Context(), "nope"); return err }(), ErrSkillNotFound)
	require.NoError(t, s.SetSkillEnabled(t.Context(), "summarize", false))
	_, err = s.skill(t.Context(), "summarize")
	require.ErrorIs(t, err, ErrSkillDisabled)
}

func TestUserSkillResourceReadable(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()
	s, err := New(t.Context(), Options{DataDir: dir})
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	sd := filepath.Join(dir, "skills", "res")
	require.NoError(t, os.MkdirAll(filepath.Join(sd, "references"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sd, "SKILL.md"), []byte("---\nname: res\ndescription: d\n---\nx"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sd, "references", "a.md"), []byte("ref"), 0o644))
	sk, err := s.skill(t.Context(), "res")
	require.NoError(t, err)
	b, err := sk.ReadFile("references/a.md")
	require.NoError(t, err)
	require.Equal(t, "ref", string(b))
}
