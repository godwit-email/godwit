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
	MaskMask  = mask.Mask
	MaskWarn  = mask.Warn
	MaskBlock = mask.Block
)

// What found a masking record's value (MaskRecord.Source): a rule, or 识别模型.
const (
	MaskSourceRule       = mask.SourceRule
	MaskSourceRecognizer = mask.SourceRecognizer
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
// plugin held (脱敏记录): which request (the values of one share it; 0 for
// records kept from before requests were told apart) and feature sent it,
// its kind and category ("": the user's own), its first character and
// length, never the value; what was done with it (MaskMask, MaskWarn or
// MaskBlock); and what found it (MaskSourceRule or MaskSourceRecognizer). A value
// of maskShortLen characters or fewer keeps no first character: of a
// two-character name it would be half.
type MaskRecord struct {
	At                             time.Time
	Request                        int64
	Feature, Kind, Category, First string
	Len                            int
	Action, Source                 string
}

const maskShortLen = 3

// MaskingLog is the masking log of the last 30 days, newest first.
func (s *Service) MaskingLog(ctx context.Context) ([]MaskRecord, error) {
	if err := s.exec(ctx, pruneMaskLog()); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, "SELECT at, feature, kind, category, first, len, action, request, source FROM mask_log ORDER BY id DESC")
	if err != nil {
		return nil, err
	}
	out := make([]MaskRecord, 0, len(rows.Values))
	for _, r := range rows.Values {
		out = append(out, MaskRecord{At: time.Unix(store.Int(r[0]), 0), Feature: store.Str(r[1]), Kind: store.Str(r[2]), Category: store.Str(r[3]),
			First: store.Str(r[4]), Len: int(store.Int(r[5])), Action: store.Str(r[6]), Request: store.Int(r[7]), Source: store.Str(r[8])})
	}
	return out, nil
}

// ClearMaskingLog deletes the masking log (清空记录).
func (s *Service) ClearMaskingLog(ctx context.Context) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	_, err = s.db.Execute(ctx, "DELETE FROM mask_log")
	return err
}

func pruneMaskLog() sqlite.Statement {
	return sqlite.Statement{SQL: "DELETE FROM mask_log WHERE at < ?", Args: []any{time.Now().AddDate(0, 0, -maskLogDays).Unix()}}
}

// recordMasking adds what a request of feature held to the masking log.
// A failure is logged: the request has gone, or been blocked, either way.
func (s *Service) recordMasking(feature string, hits []mask.Hit) {
	now := time.Now().Unix()
	stmts := []sqlite.Statement{pruneMaskLog()}
	for i, h := range hits {
		request := "(SELECT MAX(request) FROM mask_log)" // the one its first value began, in this transaction
		if i == 0 {
			request = "(SELECT COALESCE(MAX(request), 0) + 1 FROM mask_log)"
		}
		first := h.First
		if h.Len <= maskShortLen {
			first = ""
		}
		stmts = append(stmts, sqlite.Statement{SQL: "INSERT INTO mask_log(at, feature, kind, category, first, len, action, source, request) VALUES (?, ?, ?, ?, ?, ?, ?, ?, " + request + ")",
			Args: []any{now, feature, h.Kind, h.Category, first, h.Len, h.Action, h.Source}})
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
	// ErrMaskReservedKind, ErrMaskEmptyMatch, or the pattern RE2 can't compile.
	MaskRuleError = mask.RuleError
)

var (
	ErrMaskKind         = mask.ErrKind
	ErrMaskEmptyMatch   = mask.ErrEmptyMatch
	ErrMaskReservedKind = mask.ErrReservedKind
	ErrMaskTooManyRules = mask.ErrTooManyRules
	ErrMaskTooManyAllow = mask.ErrTooManyAllow
)

const (
	MaxMaskRules = mask.MaxRules
	MaxMaskAllow = mask.MaxAllow
)

// CheckMasking says what is wrong with rules and allow, as SetSettings would;
// a rule in kept, the ones stored, may keep a built-in kind.
func CheckMasking(rules []MaskRule, allow []string, kept ...MaskRule) error {
	return mask.Check(mask.Config{Rules: rules, Allow: allow}, kept...)
}

// MaskCategory is one category of 脱敏: whether it is found, and what is
// done with its values (MaskMask, MaskWarn or MaskBlock).
type MaskCategory struct {
	On     bool
	Action string
}

// MaskCategories are 脱敏's categories, as mask names them.
type MaskCategories struct {
	Secrets, IDs, Codes, Phones, Emails, Names MaskCategory
	// Addresses and Accounts only 识别模型 finds: off, they find nothing.
	Addresses, Accounts MaskCategory
}

// All is each category by its mask name (mask.Secrets …), in the order the
// settings show them, to read or change.
func (c *MaskCategories) All() iter.Seq2[string, *MaskCategory] {
	return func(yield func(string, *MaskCategory) bool) {
		for _, n := range []struct {
			name string
			c    *MaskCategory
		}{{mask.Secrets, &c.Secrets}, {mask.IDs, &c.IDs}, {mask.Codes, &c.Codes}, {mask.Phones, &c.Phones}, {mask.Emails, &c.Emails}, {mask.Names, &c.Names},
			{mask.Addresses, &c.Addresses}, {mask.Accounts, &c.Accounts}} {
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
		*cat = MaskCategory{On: name != mask.Names, Action: MaskMask}
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
	if _, err := s.db.Execute(ctx, "INSERT INTO meta(key, value) VALUES ('mask_key', ?) ON CONFLICT(key) DO NOTHING", rand.Text()); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, "SELECT value FROM meta WHERE key = 'mask_key'")
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
	if err := st.setMasker(m, s.recognizer(st)); err != nil { // a rule stored by hand: nothing goes out unmasked
		return nil, err
	}
	s.mask = m
	return m, nil
}

// maskingChanged masks as st says from the next request on, once the
// masker exists; 识别模型 turned off lets go of it.
func (s *Service) maskingChanged(st Settings) {
	s.maskMu.Lock()
	defer s.maskMu.Unlock()
	if !st.recognizes() {
		s.guard = nil
		s.recognitionModelErr.Store(nil)
	}
	if s.mask != nil {
		_ = st.setMasker(s.mask, s.recognizer(st)) // SetSettings checked it; a failure keeps the last
	}
}

// setMasker masks with m as st says, with r as 识别模型: a config m
// refuses leaves its last.
func (st Settings) setMasker(m *mask.Masker, r mask.Recognizer) error {
	m.SetOn(st.Masking)
	c := st.maskConfig()
	c.Recognizer = r
	return m.Configure(c)
}

// PreviewMasking is text as it would go to a model off this device under
// st's 脱敏 settings, saved or not (实时预览). It stores nothing and leaves
// the Service's masker alone: its placeholders are a throwaway key's.
func (s *Service) PreviewMasking(ctx context.Context, st Settings, text string) (string, error) {
	s.maskMu.Lock()
	r := s.recognizer(st)
	s.maskMu.Unlock()
	m := mask.New([]byte(rand.Text()))
	if err := st.setMasker(m, r); err != nil {
		return "", err
	}
	if !st.Masking {
		return text, nil
	}
	return m.Text(text)
}
