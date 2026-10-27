package core

import (
	"context"
	"crypto/rand"
	"errors"

	"github.com/godwit-email/godwit/core/ai/mask"
	"github.com/godwit-email/godwit/core/store"
)

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
	s.mask = mask.New([]byte(store.Str(rows.Values[0][0])))
	s.mask.SetOn(st.Masking)
	s.mask.SetCategories(st.MaskCategories)
	return s.mask, nil
}

// maskingChanged turns masking and its categories on or off as st says,
// once it exists.
func (s *Service) maskingChanged(st Settings) {
	s.maskMu.Lock()
	defer s.maskMu.Unlock()
	if s.mask != nil {
		s.mask.SetOn(st.Masking)
		s.mask.SetCategories(st.MaskCategories)
	}
}
