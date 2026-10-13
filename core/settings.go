package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/mail"
	"regexp"
	"slices"
	"strings"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/store"
)

// Settings are the app-wide preferences, stored as one JSON row.
// ReplyLanguage is "match" (the mail's language) or "reading".
type Settings struct {
	Theme, Density                                   string
	FontSize, BodyFontSize                           int // BodyFontSize 0 follows FontSize
	UILanguage, ReadingLanguage, ReplyLanguage       string
	NoTranslate                                      []string
	LaunchAtLogin, KeepInBackground, UnreadInMenuBar bool
	UnreadOnAppIcon                                  bool
	NotifyNewMail, NotifySmartOnly, NotifyPreview    bool
	QuickReplies, Summary, ThreadView                bool
	DefaultModel, SmallModel                         string
	// JudgeService is the slug of the service whose judge answers typed
	// questions; "" (or one gone or with judge off): the DefaultModel does.
	JudgeService string
	LocalAPI     bool // the GUI serves the Local API; off by default
	// AutoLabel lets the classifier label new Inbox mail; on by default.
	// A rescan runs either way.
	AutoLabel bool
	// SidebarWidth is the page sidebars' width in DIPs, the user's drag:
	// 0 for the default, else 240 to 480.
	SidebarWidth int
	// ListWidth is the mail list's width in DIPs, the user's drag: 0 for the
	// default, else 280 to 560.
	ListWidth int
	// TrustedImageSenders are the lower-cased From addresses whose remote
	// images load at once (始终信任此发件人). Only TrustImageSender and
	// UntrustImageSender change it; SetSettings keeps the stored list.
	TrustedImageSenders []string
	// RemoteImages is when a message's remote images load, through the image
	// proxy: "auto" (default, "" too) at once, "trusted" for the trusted
	// senders only, "ask" after 加载图片. Mail in Junk never loads them by itself.
	RemoteImages string
	// SettingsVersion is 2 once the default text grew from 13 to 14 px and a
	// stored FontSize was raised by one to keep its sizes; 3 once a stored
	// BodyFontSize of 14, the old default, became 0 (follow FontSize).
	SettingsVersion int
}

const settingsVersion = 3

func defaultSettings() Settings {
	return Settings{Theme: "system", Density: "compact", FontSize: 14, SettingsVersion: settingsVersion, ReplyLanguage: "match", RemoteImages: "auto",
		KeepInBackground: true, UnreadOnAppIcon: true, NotifyNewMail: true, NotifyPreview: true, ThreadView: true, AutoLabel: true}
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
	case !in(st.RemoteImages, "", "auto", "trusted", "ask"):
		return fmt.Errorf("invalid remote images mode %q", st.RemoteImages)
	case !in(st.ReplyLanguage, "match", "reading"):
		return fmt.Errorf("invalid reply language mode %q", st.ReplyLanguage)
	case st.FontSize < 10 || st.FontSize > 32, st.BodyFontSize != 0 && (st.BodyFontSize < 10 || st.BodyFontSize > 32):
		return fmt.Errorf("font size must be between 10 and 32")
	case st.SidebarWidth != 0 && (st.SidebarWidth < 240 || st.SidebarWidth > 480):
		return fmt.Errorf("sidebar width must be between 240 and 480")
	case st.ListWidth != 0 && (st.ListWidth < 280 || st.ListWidth > 560):
		return fmt.Errorf("list width must be between 280 and 560")
	}
	if strings.Contains(st.JudgeService, "/") {
		return fmt.Errorf("invalid judge service %q", st.JudgeService)
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
	raw := []byte(rows.Values[0][0].(string))
	var stored struct{ FontSize, SettingsVersion int } // the defaults must not stand in for the stored version
	if err := json.Unmarshal(raw, &stored); err != nil {
		return Settings{}, fmt.Errorf("stored settings: %w", err)
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return Settings{}, fmt.Errorf("stored settings: %w", err)
	}
	if stored.SettingsVersion < 2 && stored.FontSize > 0 { // an absent size keeps the new default
		st.FontSize = min(stored.FontSize+1, 32)
	}
	if stored.SettingsVersion < 3 && st.BodyFontSize == 14 {
		st.BodyFontSize = 0
	}
	st.SettingsVersion = settingsVersion
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
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	stored, err := s.loadSettings(ctx)
	if err != nil {
		return Settings{}, err
	}
	st.TrustedImageSenders = stored.TrustedImageSenders // a page's stale snapshot must not undo a trust
	return s.storeSettings(ctx, st)
}

// storeSettings writes st and announces settings.changed; the caller holds settingsMu.
func (s *Service) storeSettings(ctx context.Context, st Settings) (Settings, error) {
	st.SettingsVersion = settingsVersion // a client's 0 must not make the next load raise FontSize again
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

// ReadingLanguage is the language Wit reads and writes in: the setting,
// else the UI language, else the system language, else "en".
func (s *Service) ReadingLanguage(ctx context.Context) string { return s.readingLanguage(ctx) }

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

const maxTrustedSenders = 1000

// TrustImageSender trusts the address of from (a From header) to load
// remote images; from must parse as an address.
func (s *Service) TrustImageSender(ctx context.Context, from string) error {
	a, err := mail.ParseAddress(from)
	if err != nil {
		return fmt.Errorf("invalid sender address: %w", err)
	}
	addr := strings.ToLower(a.Address)
	return s.editTrusted(ctx, func(l []string) ([]string, error) {
		if slices.Contains(l, addr) {
			return l, nil
		}
		if len(l) >= maxTrustedSenders {
			return nil, fmt.Errorf("at most %d trusted senders", maxTrustedSenders)
		}
		return append(l, addr), nil
	})
}

// UntrustImageSender stops trusting addr.
func (s *Service) UntrustImageSender(ctx context.Context, addr string) error {
	addr = strings.ToLower(strings.TrimSpace(addr))
	return s.editTrusted(ctx, func(l []string) ([]string, error) {
		return slices.DeleteFunc(l, func(a string) bool { return a == addr }), nil
	})
}

// editTrusted changes the trusted senders under settingsMu.
func (s *Service) editTrusted(ctx context.Context, edit func([]string) ([]string, error)) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	st, err := s.loadSettings(ctx)
	if err != nil {
		return err
	}
	l, err := edit(slices.Clone(st.TrustedImageSenders))
	if err != nil {
		return err
	}
	st.TrustedImageSenders = l
	_, err = s.storeSettings(ctx, st)
	return err
}

// inJunk is whether folder f is junk, detected or given by the user, even
// where another folder holds the role: ImagesAllowed and Message.InJunk agree.
const inJunk = "'junk' IN (f.role, f.role_override)"

// ImagesAllowed is whether message's remote images load without asking, by
// the RemoteImages setting: never in a junk folder (From is unauthenticated);
// "auto" otherwise; "trusted" for the trusted From addresses; "ask" never.
func (s *Service) ImagesAllowed(ctx context.Context, message int64) (bool, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return false, err
	}
	defer end()
	rows, err := s.db.Query(ctx, "SELECT m.from_addr, "+inJunk+" FROM messages m JOIN folders f ON f.id = m.folder_id WHERE m.id = ?", message)
	if err != nil || len(rows.Values) == 0 || store.Int(rows.Values[0][1]) != 0 {
		return false, err
	}
	st, err := s.loadSettings(ctx)
	if err != nil {
		return false, err
	}
	switch st.RemoteImages {
	case "", "auto":
		return true, nil
	case "trusted":
		a, err := mail.ParseAddress(store.Str(rows.Values[0][0]))
		return err == nil && slices.Contains(st.TrustedImageSenders, strings.ToLower(a.Address)), nil
	}
	return false, nil
}
