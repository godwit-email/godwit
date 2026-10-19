package core

import (
	"context"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeSystemTranslator struct {
	version string
	err     error
	calls   int
}

func (f *fakeSystemTranslator) Version() string { return f.version }

func (f *fakeSystemTranslator) Translate(_ context.Context, texts []string, _, target string) ([]string, string, error) {
	f.calls++
	if f.err != nil {
		return nil, "", f.err
	}
	out := make([]string, len(texts))
	for i, t := range texts {
		out[i] = "[" + target + "] " + t
	}
	return out, "en", nil
}

func isListed(t *testing.T, s *Service) bool {
	ts, err := s.ListTranslators(t.Context())
	require.NoError(t, err)
	return slices.ContainsFunc(ts, func(x TranslatorInfo) bool {
		return x.ID == "plugin/"+SystemTranslationPlugin && x.Builtin && x.Running
	})
}

// The built-in system translation is a translator only where the app has
// the system's: its translations are cached until the system changes, and
// it stops when switched off.
func TestSystemTranslation(t *testing.T) {
	e := newSkillEnv(t)
	assert.False(t, isListed(t, e.s), "no system translator: not offered")
	ps, err := e.s.ListPlugins(t.Context())
	require.NoError(t, err)
	assert.False(t, slices.ContainsFunc(ps, func(p PluginInfo) bool { return p.Name == SystemTranslationPlugin }))

	sys := &fakeSystemTranslator{version: "26A1"}
	e.s.opts.SystemTranslator = sys
	assert.True(t, isListed(t, e.s))
	st, err := e.s.GetSettings(t.Context())
	require.NoError(t, err)
	st.Translator = "plugin/" + SystemTranslationPlugin
	_, err = e.s.SetSettings(t.Context(), st)
	require.NoError(t, err)

	setBody(t, e, "Hello\n\nSee you", "")
	tg := Target{Kind: "message", ID: e.msg}
	want := []string{"[zh-CN] Hello\n\n[zh-CN] See you"}
	assert.Equal(t, want, texts(e.run(t, "translate", tg)))
	assert.Equal(t, want, texts(e.run(t, "translate", tg)))
	assert.Equal(t, 1, sys.calls, "cached")
	sys.version = "26B1"
	e.run(t, "translate", tg)
	assert.Equal(t, 2, sys.calls, "a system update translates again")
	assert.Empty(t, e.srv.Requests(), "no model was asked")

	require.NoError(t, e.s.SetPluginEnabled(t.Context(), SystemTranslationPlugin, false))
	assert.False(t, isListed(t, e.s), "listed as off")
	_, err = e.s.RunSkill(t.Context(), "translate", tg)
	assert.ErrorIs(t, err, ErrTranslatorUnavailable)
}

func TestSystemTranslationNotInstalled(t *testing.T) {
	e := newSkillEnv(t)
	e.s.opts.SystemTranslator = &fakeSystemTranslator{err: ErrSystemLanguageNotInstalled}
	st, err := e.s.GetSettings(t.Context())
	require.NoError(t, err)
	st.Translator = "plugin/" + SystemTranslationPlugin
	_, err = e.s.SetSettings(t.Context(), st)
	require.NoError(t, err)
	_, err = e.s.RunSkill(t.Context(), "translate", Target{Kind: "message", ID: e.msg})
	assert.ErrorIs(t, err, ErrSystemLanguageNotInstalled)
	assert.Zero(t, e.cacheRows(t), "a failure is not cached")
}
