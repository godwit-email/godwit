package app

import (
	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/internal/i18n"
)

// actFeatures are the 协议能力 the mail acts need (ADR 0037).
var actFeatures = map[string]backend.Feature{
	"inbox": backend.FeatureMove, "archive": backend.FeatureMove, "junk": backend.FeatureMove, "delete": backend.FeatureMove,
	"purge": backend.FeaturePurge,
	"read":  backend.FeatureSeen, "unread": backend.FeatureSeen, "toggle_read": backend.FeatureSeen,
	"star": backend.FeatureFlagged, "unstar": backend.FeatureFlagged, "toggle_star": backend.FeatureFlagged,
}

// unsupported is why the account's server cannot do f, in the UI's language,
// or "" when it can or is not known yet.
func (a *App) unsupported(acc int64, f backend.Feature) string {
	for _, x := range a.accounts {
		if r := x.Unsupported(f); x.ID == acc && r != "" {
			return i18n.T("feature.reason." + r)
		}
	}
	return ""
}

// cannot is why act cannot apply to one of rows, "" when it can: a control
// for it is greyed out with this as its reason. Delete in Trash is a purge.
func (p *mailPage) cannot(rows []mailItem, act string) string {
	if act == "delete" && p.currentFolderRole() == "trash" {
		act = "purge"
	}
	f, ok := actFeatures[act]
	if !ok {
		return ""
	}
	for _, it := range rows {
		if r := p.a.unsupported(p.accountOf(it.Msg.FolderID), f); r != "" {
			return r
		}
	}
	return ""
}
