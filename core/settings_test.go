package core

import (
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

func settingsService(t *testing.T, dir, sys string) *Service {
	t.Helper()
	keyring.MockInit()
	s, err := New(t.Context(), Options{DataDir: dir, SystemLanguage: sys})
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSettingsDefaults(t *testing.T) {
	s := settingsService(t, t.TempDir(), "")
	got, err := s.GetSettings(t.Context())
	require.NoError(t, err)
	assert.Equal(t, Settings{Theme: "system", Density: "compact", FontSize: 14, SettingsVersion: 3, ReplyLanguage: "match",
		KeepInBackground: true, NotifyNewMail: true, ThreadView: true}, got)
}

func TestSettingsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := settingsService(t, dir, "")
	ch, stop := s.Subscribe()
	defer stop()
	in, _ := s.GetSettings(t.Context())
	in.Theme, in.UILanguage, in.NoTranslate, in.DefaultModel = "dark", "zh-CN", []string{"en", "fr"}, "svc/m"
	_, err := s.SetSettings(t.Context(), in)
	require.NoError(t, err)
	select {
	case ev := <-ch:
		assert.Equal(t, events.SettingsChanged, ev.Type)
	case <-time.After(5 * time.Second):
		t.Fatal("no settings.changed event")
	}
	require.NoError(t, s.Close())

	s = settingsService(t, dir, "")
	got, err := s.GetSettings(t.Context())
	require.NoError(t, err)
	assert.Equal(t, in, got)

	// A newer build's extra field does not break reading.
	_, err = s.db.Execute(t.Context(), "UPDATE settings SET value = ? WHERE key = 'app'", `{"Theme":"light","Future":1}`)
	require.NoError(t, err)
	got, err = s.GetSettings(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "light", got.Theme)
	assert.Equal(t, 14, got.FontSize)
}

func TestSettingsValidation(t *testing.T) {
	s := settingsService(t, t.TempDir(), "")
	base, _ := s.GetSettings(t.Context())
	base.Theme, base.FontSize, base.UILanguage, base.SidebarWidth = "dark", 20, "fr", 480
	_, err := s.SetSettings(t.Context(), base)
	require.NoError(t, err)
	for name, mod := range map[string]func(*Settings){
		"theme":    func(x *Settings) { x.Theme = "neon" },
		"density":  func(x *Settings) { x.Density = "tiny" },
		"reply":    func(x *Settings) { x.ReplyLanguage = "other" },
		"font":     func(x *Settings) { x.FontSize = 9 },
		"bodyfont": func(x *Settings) { x.BodyFontSize = 33 },
		"lang":     func(x *Settings) { x.UILanguage = "zh_CN!" },
		"notrans":  func(x *Settings) { x.NoTranslate = []string{"en", "x y"} },
		"narrow":   func(x *Settings) { x.SidebarWidth = 239 },
		"wide":     func(x *Settings) { x.SidebarWidth = 481 },
		"list":     func(x *Settings) { x.ListWidth = 279 },
		"listwide": func(x *Settings) { x.ListWidth = 561 },
	} {
		in := base
		mod(&in)
		_, err := s.SetSettings(t.Context(), in)
		assert.Error(t, err, name)
	}
	got, err := s.GetSettings(t.Context())
	require.NoError(t, err)
	assert.Equal(t, base, got)
}

func TestReadingLanguageFallback(t *testing.T) {
	s := settingsService(t, t.TempDir(), "")
	assert.Equal(t, "en", s.readingLanguage(t.Context()))
	s = settingsService(t, t.TempDir(), "de")
	assert.Equal(t, "de", s.readingLanguage(t.Context()))
	in, _ := s.GetSettings(t.Context())
	in.UILanguage = "fr"
	_, err := s.SetSettings(t.Context(), in)
	require.NoError(t, err)
	assert.Equal(t, "fr", s.readingLanguage(t.Context()))
	in.ReadingLanguage = "ja"
	_, err = s.SetSettings(t.Context(), in)
	require.NoError(t, err)
	assert.Equal(t, "ja", s.readingLanguage(t.Context()))
}

func TestSettingsValidateModelRefs(t *testing.T) {
	s := settingsService(t, t.TempDir(), "")
	st, _ := s.GetSettings(t.Context())
	st.DefaultModel, st.SmallModel = "nowhere/m1", "nowhere/m2" // format only, no lookup
	_, err := s.SetSettings(t.Context(), st)
	require.NoError(t, err)
	for _, bad := range []string{"nomodel", "/m", "svc/"} {
		in := st
		in.DefaultModel = bad
		_, err = s.SetSettings(t.Context(), in)
		assert.Error(t, err, "default %q", bad)
		in = st
		in.SmallModel = bad
		_, err = s.SetSettings(t.Context(), in)
		assert.Error(t, err, "small %q", bad)
	}
}

func TestSettingsFontMigration(t *testing.T) {
	for stored, want := range map[string][2]int{
		`{"FontSize":13,"BodyFontSize":15}`: {14, 15},
		`{"Theme":"light"}`:                 {14, 0},
		`{"FontSize":32}`:                   {32, 0},
		`{"FontSize":16}`:                   {17, 0},
		`{"FontSize":13,"BodyFontSize":15,"SettingsVersion":2}`: {13, 15},
		`{"FontSize":14,"BodyFontSize":14,"SettingsVersion":2}`: {14, 0},
		`{"FontSize":13,"BodyFontSize":14}`:                     {14, 0},
		`{"FontSize":16,"BodyFontSize":14,"SettingsVersion":3}`: {16, 14},
	} {
		s := settingsService(t, t.TempDir(), "")
		_, err := s.db.Execute(t.Context(), "INSERT INTO settings(key, value) VALUES ('app', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", stored)
		require.NoError(t, err)
		got, err := s.GetSettings(t.Context())
		require.NoError(t, err)
		assert.Equal(t, want, [2]int{got.FontSize, got.BodyFontSize}, stored)
		assert.Equal(t, 3, got.SettingsVersion, stored)
	}
}

func TestSettingsVersionStamped(t *testing.T) {
	s := settingsService(t, t.TempDir(), "")
	in, _ := s.GetSettings(t.Context())
	in.FontSize, in.SettingsVersion = 15, 0
	_, err := s.SetSettings(t.Context(), in)
	require.NoError(t, err)
	for range 2 { // load -> save -> load is stable
		got, err := s.GetSettings(t.Context())
		require.NoError(t, err)
		assert.Equal(t, 15, got.FontSize)
		_, err = s.SetSettings(t.Context(), got)
		require.NoError(t, err)
	}
}

func TestSettingsTypeMismatchErrors(t *testing.T) {
	s := settingsService(t, t.TempDir(), "")
	_, err := s.db.Execute(t.Context(), "INSERT INTO settings(key, value) VALUES ('app', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", `{"FontSize":14,"Theme":5}`)
	require.NoError(t, err)
	_, err = s.GetSettings(t.Context())
	assert.ErrorContains(t, err, "stored settings")
}
