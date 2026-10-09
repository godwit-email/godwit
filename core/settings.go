package core

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/events"
)

// Settings are the app-wide preferences, stored as one JSON row.
// ReplyLanguage is "match" (the mail's language) or "reading".
type Settings struct {
	Theme, Density                                   string
	FontSize, BodyFontSize                           int
	UILanguage, ReadingLanguage, ReplyLanguage       string
	NoTranslate                                      []string
	LaunchAtLogin, KeepInBackground, UnreadInMenuBar bool
	NotifyNewMail, NotifySmartOnly, NotifyPreview    bool
	QuickReplies, Summary, ThreadView                bool
	DefaultModel, SmallModel                         string
	LocalAPI                                         bool // the GUI serves the Local API; off by default
}

func defaultSettings() Settings {
	return Settings{Theme: "system", Density: "compact", FontSize: 13, BodyFontSize: 14, ReplyLanguage: "match",
		KeepInBackground: true, NotifyNewMail: true, QuickReplies: true, Summary: true, ThreadView: true}
}

var langRE = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)

func (st Settings) validate() error {
	in := func(v string, ok ...string) bool {
		for _, o := range ok {
			if v == o {
				return true
			}
		}
		return false
	}
	switch {
	case !in(st.Theme, "system", "light", "dark"):
		return fmt.Errorf("invalid theme %q", st.Theme)
	case !in(st.Density, "compact", "comfortable"):
		return fmt.Errorf("invalid density %q", st.Density)
	case !in(st.ReplyLanguage, "match", "reading"):
		return fmt.Errorf("invalid reply language mode %q", st.ReplyLanguage)
	case st.FontSize < 10 || st.FontSize > 32, st.BodyFontSize < 10 || st.BodyFontSize > 32:
		return fmt.Errorf("font size must be between 10 and 32")
	}
	for _, r := range []string{st.DefaultModel, st.SmallModel} {
		if _, err := ai.ParseModelRef(r); r != "" && err != nil {
			return err
		}
	}
	for _, l := range append([]string{st.UILanguage, st.ReadingLanguage}, st.NoTranslate...) {
		if l != "" && !langRE.MatchString(l) {
			return fmt.Errorf("invalid language tag %q", l)
		}
	}
	return nil
}

// GetSettings returns the stored settings, defaults for anything unset. Fields
// a newer build added are ignored.
func (s *Service) GetSettings(ctx context.Context) (Settings, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return Settings{}, err
	}
	defer end()
	return s.loadSettings(ctx)
}

func (s *Service) loadSettings(ctx context.Context) (Settings, error) {
	st := defaultSettings()
	rows, err := s.db.Query(ctx, "SELECT value FROM settings WHERE key = 'app'")
	if err != nil || len(rows.Values) == 0 {
		return st, err
	}
	if err := json.Unmarshal([]byte(rows.Values[0][0].(string)), &st); err != nil {
		return Settings{}, fmt.Errorf("stored settings: %w", err)
	}
	return st, nil
}

// SetSettings validates and stores st, then announces settings.changed.
func (s *Service) SetSettings(ctx context.Context, st Settings) (Settings, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return Settings{}, err
	}
	defer end()
	if err := st.validate(); err != nil {
		return Settings{}, err
	}
	b, err := json.Marshal(st)
	if err != nil {
		return Settings{}, err
	}
	if _, err := s.db.Execute(ctx, `INSERT INTO settings(key, value) VALUES ('app', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, string(b)); err != nil {
		return Settings{}, err
	}
	s.bus.Publish(events.Event{Type: events.SettingsChanged})
	return st, nil
}

// readingLanguage is ReadingLanguage, else UILanguage, else the system language, else "en".
func (s *Service) readingLanguage(ctx context.Context) string {
	st, _ := s.loadSettings(ctx)
	for _, l := range []string{st.ReadingLanguage, st.UILanguage, s.opts.SystemLanguage, "en"} {
		if l != "" {
			return l
		}
	}
	return "en"
}
