package core

import (
	"context"
	"crypto/rand"
	"errors"
	"iter"

	"github.com/godwit-email/godwit/core/ai/mask"
	"github.com/godwit-email/godwit/core/store"
)

// What 脱敏 does with a category's values (MaskCategory.Action): 遮住 sends
// placeholders (the default; "" too); 仅警告 sends them as they are and notes
// them; 拦截 sends nothing. Only 遮住 acts yet: the others are kept as the
// user chose them for when they do, and mask meanwhile.
const (
	MaskHide  = "mask"
	MaskWarn  = "warn"
	MaskBlock = "block"
)

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
	c := mask.Config{Words: st.MaskWords, WordsCase: st.MaskWordsCase, WordsWhole: st.MaskWordsWhole, Rules: st.MaskRules, Allow: st.MaskAllow}
	for name, cat := range st.MaskCategories.All() {
		if !cat.On {
			c.Off = append(c.Off, name)
		}
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
	if err := m.Configure(st.maskConfig()); err != nil { // a rule stored by hand: nothing goes out unmasked
		return nil, err
	}
	m.SetOn(st.Masking)
	s.mask = m
	return m, nil
}

// maskingChanged masks as st says from the next request on, once the
// masker exists.
func (s *Service) maskingChanged(st Settings) {
	s.maskMu.Lock()
	defer s.maskMu.Unlock()
	if s.mask != nil {
		_ = s.mask.Configure(st.maskConfig()) // SetSettings checked it; a failure keeps the last
		s.mask.SetOn(st.Masking)
	}
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
