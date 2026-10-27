package core

import (
	"context"
	"crypto/rand"
	"errors"
	"iter"
	"log/slog"
	"strings"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/ai/mask"
	"github.com/godwit-email/godwit/core/store"
)

// What 脱敏 does with a category's values (MaskCategory.Action): 遮住 sends
// placeholders (the default; "" too); 仅警告 sends them as they are and
// records them; 拦截 sends nothing of the request.
const (
	MaskHide  = mask.Hide
	MaskWarn  = mask.Warn
	MaskBlock = mask.Block
)

// The features that send to a model service, as the masking log names
// them (MaskRecord.Feature).
const (
	featureWit       = "wit"
	featureCompose   = "compose" // Wit in a draft's compose window, and its skills
	featureSkill     = "skill"   // a skill on mail: summary, translation …
	featureReplies   = "quick_replies"
	featureTranslate = "translate"
	featureLabels    = "labels"
	featureCompact   = "compact"
	featurePlugin    = "plugin"
)

// maskLogDays is how long the masking log keeps a request's record.
const maskLogDays = 30

// MaskRecord is a value a request to a model service or a translation
// plugin held (脱敏记录): which feature sent it, its kind and category ("":
// the user's own), its first character and length, never the value; and
// what was done with it (MaskHide, MaskWarn or MaskBlock).
type MaskRecord struct {
	At                             time.Time
	Feature, Kind, Category, First string
	Len                            int
	Action                         string
}

// MaskingLog is the masking log of the last 30 days, newest first.
func (s *Service) MaskingLog(ctx context.Context) ([]MaskRecord, error) {
	if err := s.exec(ctx, pruneMaskLog()); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, "SELECT at, feature, kind, category, first, len, action FROM mask_log ORDER BY id DESC")
	if err != nil {
		return nil, err
	}
	out := make([]MaskRecord, 0, len(rows.Values))
	for _, r := range rows.Values {
		out = append(out, MaskRecord{At: time.Unix(store.Int(r[0]), 0), Feature: store.Str(r[1]), Kind: store.Str(r[2]), Category: store.Str(r[3]),
			First: store.Str(r[4]), Len: int(store.Int(r[5])), Action: store.Str(r[6])})
	}
	return out, nil
}

func pruneMaskLog() sqlite.Statement {
	return sqlite.Statement{SQL: "DELETE FROM mask_log WHERE at < ?", Args: []any{time.Now().AddDate(0, 0, -maskLogDays).Unix()}}
}

// recordMasking adds what a request of feature held to the masking log.
// A failure is logged: the request has gone, or been blocked, either way.
func (s *Service) recordMasking(feature string, hits []mask.Hit) {
	now := time.Now().Unix()
	stmts := []sqlite.Statement{pruneMaskLog()}
	for _, h := range hits {
		stmts = append(stmts, sqlite.Statement{SQL: "INSERT INTO mask_log(at, feature, kind, category, first, len, action) VALUES (?, ?, ?, ?, ?, ?, ?)",
			Args: []any{now, feature, h.Kind, h.Category, h.First, h.Len, h.Action}})
	}
	if _, err := s.db.Transaction(context.Background(), stmts); err != nil {
		slog.Default().Warn("could not record what masking found", "err", err)
	}
}

// MaskBlocked is the categories (mask's names) whose values blocked err's
// request (拦截), nil if masking did not block it.
func MaskBlocked(err error) []string {
	var b *mask.BlockedError
	if errors.As(err, &b) {
		return b.Categories
	}
	var ae *ai.Error
	if errors.As(err, &ae) && ae.Code == ai.CodeBlocked {
		return strings.Split(ae.Detail, ",")
	}
	return nil
}

// The user's rules (自定义正则) as mask has them, for the UI, which reaches
// core only through its own package.
type (
	// MaskRule is one of the user's patterns, masking what it finds as its kind.
	MaskRule = mask.Rule
	// MaskRuleError says which rule can't be used: ErrMaskKind,
	// ErrMaskEmptyMatch, or the pattern RE2 can't compile.
	MaskRuleError = mask.RuleError
)

var (
	ErrMaskKind         = mask.ErrKind
	ErrMaskEmptyMatch   = mask.ErrEmptyMatch
	ErrMaskTooManyRules = mask.ErrTooManyRules
	ErrMaskTooManyAllow = mask.ErrTooManyAllow
)

const (
	MaxMaskRules = mask.MaxRules
	MaxMaskAllow = mask.MaxAllow
)

// CheckMasking says what is wrong with rules and allow, as SetSettings would.
func CheckMasking(rules []MaskRule, allow []string) error {
	return mask.Check(mask.Config{Rules: rules, Allow: allow})
}

// MaskCategory is one category of 脱敏: whether it is found, and what is
// done with its values (MaskHide, MaskWarn or MaskBlock).
type MaskCategory struct {
	On     bool
	Action string
}

// MaskCategories are 脱敏's categories, as mask names them.
type MaskCategories struct{ Secrets, IDs, Codes, Phones, Emails, Names MaskCategory }

// All is each category by its mask name (mask.Secrets …), in the order the
// settings show them, to read or change.
func (c *MaskCategories) All() iter.Seq2[string, *MaskCategory] {
	return func(yield func(string, *MaskCategory) bool) {
		for _, n := range []struct {
			name string
			c    *MaskCategory
		}{{mask.Secrets, &c.Secrets}, {mask.IDs, &c.IDs}, {mask.Codes, &c.Codes}, {mask.Phones, &c.Phones}, {mask.Emails, &c.Emails}, {mask.Names, &c.Names}} {
			if !yield(n.name, n.c) {
				return
			}
		}
	}
}

// defaultMaskCategories masks all but names: a display name is often a
// brand's (docs/adr/0025-mask-at-cloud-egress.md).
func defaultMaskCategories() MaskCategories {
	var c MaskCategories
	for name, cat := range c.All() {
		*cat = MaskCategory{On: name != mask.Names, Action: MaskHide}
	}
	return c
}

// maskConfig is what st sets the masker to.
func (st Settings) maskConfig() mask.Config {
	c := mask.Config{Words: st.MaskWords, WordsCase: st.MaskWordsCase, WordsWhole: st.MaskWordsWhole, Rules: st.MaskRules, Allow: st.MaskAllow,
		Actions: map[string]string{}}
	for name, cat := range st.MaskCategories.All() {
		if !cat.On {
			c.Off = append(c.Off, name)
		}
		c.Actions[name] = cat.Action
	}
	return c
}

// masker is the Service's 脱敏 (docs/adr/0025-mask-at-cloud-egress.md),
// created on first use. Its key is kept in the database: a value keeps its
// placeholder across restarts, and the key moves with the data directory,
// whose database holds the mail anyway.
func (s *Service) masker() (*mask.Masker, error) {
	s.maskMu.Lock()
	defer s.maskMu.Unlock()
	if s.mask != nil {
		return s.mask, nil
	}
	ctx := context.Background()
	if _, err := s.db.Execute(ctx, "INSERT INTO settings(key, value) VALUES ('mask_key', ?) ON CONFLICT(key) DO NOTHING", rand.Text()); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, "SELECT value FROM settings WHERE key = 'mask_key'")
	if err != nil {
		return nil, err
	}
	if len(rows.Values) == 0 || store.Str(rows.Values[0][0]) == "" {
		return nil, errors.New("the masking key is missing")
	}
	st, err := s.loadSettings(ctx)
	if err != nil {
		return nil, err
	}
	m := mask.New([]byte(store.Str(rows.Values[0][0])))
	m.Record = s.recordMasking
	if err := st.setMasker(m); err != nil { // a rule stored by hand: nothing goes out unmasked
		return nil, err
	}
	s.mask = m
	return m, nil
}

// maskingChanged masks as st says from the next request on, once the
// masker exists.
func (s *Service) maskingChanged(st Settings) {
	s.maskMu.Lock()
	defer s.maskMu.Unlock()
	if s.mask != nil {
		_ = st.setMasker(s.mask) // SetSettings checked it; a failure keeps the last
	}
}

// setMasker masks with m as st says: a config m refuses leaves its last.
func (st Settings) setMasker(m *mask.Masker) error {
	m.SetOn(st.Masking)
	return m.Configure(st.maskConfig())
}

// PreviewMasking is text as it would go to a model off this device under
// st's 脱敏 settings, saved or not (实时预览). It stores nothing and leaves
// the Service's masker alone: its placeholders are a throwaway key's.
func (s *Service) PreviewMasking(ctx context.Context, st Settings, text string) (string, error) {
	m := mask.New([]byte(rand.Text()))
	if err := m.Configure(st.maskConfig()); err != nil {
		return "", err
	}
	if !st.Masking {
		return text, nil
	}
	return m.Text(text)
}
