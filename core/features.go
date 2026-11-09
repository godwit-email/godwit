package core

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/core/store"
	"github.com/godwit-email/godwit/internal/i18n"
)

// Unsupported is the reason key (backend.Support.Reason, such as
// "imap.no_uidplus") why the account's server cannot do f, or "" when it can
// or is not known yet: nothing is refused before the first sign-in.
func (a Account) Unsupported(f backend.Feature) string {
	if s := a.Features[f]; s.Level == backend.LevelUnsupported {
		return s.Reason
	}
	return ""
}

// ActionFeatures are the 协议能力 the mail actions need (ADR 0037), by the
// action: the UI greys out one the account's server cannot do, and Wit's
// tool for it answers "unsupported:".
var ActionFeatures = map[string]backend.Feature{
	"inbox": backend.FeatureMove, "archive": backend.FeatureMove, "junk": backend.FeatureMove, "delete": backend.FeatureMove,
	"purge": backend.FeaturePurge,
	"read":  backend.FeatureSeen, "unread": backend.FeatureSeen,
	"star": backend.FeatureFlagged, "unstar": backend.FeatureFlagged,
}

// requireFeature fails with "unsupported: <why>" when the account's server cannot
// do f: Wit's tools stay the same on every account and answer this instead
// (ADR 0037). The reason is English, as for the model.
func (s *Service) requireFeature(ctx context.Context, accountID int64, f backend.Feature) error {
	acc, err := s.account(ctx, accountID)
	if err != nil {
		return err
	}
	if r := acc.Unsupported(f); r != "" {
		return fmt.Errorf("unsupported: %s", i18n.TIn("en", "feature.reason."+r))
	}
	return nil
}

// encodeFeatures is accounts.features for what the receiving backend declared
// (ADR 0037): NULL when nothing was, as for a failed check.
func encodeFeatures(d backend.Description) any {
	if d.Identity == "" {
		return nil
	}
	b, err := json.Marshal(d)
	if err != nil {
		panic(err) // plain strings and numbers
	}
	return string(b)
}

// decodeFeatures reads accounts.features; ok is false for NULL or a value it
// cannot read, which counts as not seen yet.
func decodeFeatures(v any) (d backend.Description, ok bool) {
	if v == nil || json.Unmarshal([]byte(store.Str(v)), &d) != nil || d.Identity == "" {
		return backend.Description{}, false
	}
	return d, true
}
