package core

import (
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
