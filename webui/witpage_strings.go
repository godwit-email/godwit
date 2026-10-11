package webui

import "github.com/godwit-email/godwit/internal/i18n"

// witKeys are the i18n keys the Wit page shows, sent in WitPageState.Text.
var witKeys = []string{
	"ask.citations", "ask.citation.suspect",
	"wit.input", "wit.placeholder", "wit.send", "wit.stop", "wit.loading", "wit.scroll_end", "wit.model",
	"wit.tools.one", "wit.tools.other", "wit.suspect", "wit.tool.create_draft",
	"wit.approval.wants", "wit.approval.to", "wit.approval.cc", "wit.approval.subject", "wit.approval.destinations",
	"wit.approval.allow", "wit.approval.allow_all", "wit.approval.reject", "wit.approval.send_note",
	"link.title", "link.open", "link.copy", "link.copied",
	"error.unknown", "error.invalid_state", "error.run_active", "error.stale_approval", "error.needs_grant",
	"error.ai_not_configured", "error.model_not_found", "error.model_auth", "error.keychain", "error.too_large",
	"error.stale_draft", "error.ai.unauthorized", "error.ai.rate_limited", "error.ai.unavailable",
	"error.ai.bad_response", "error.ai.internal", "error.ai.stale", "error.ai.incomplete",
	"mail.no_subject", "mail.list_sep", "mail.yesterday", "mail.weekday.0", "mail.weekday.1", "mail.weekday.2",
	"mail.weekday.3", "mail.weekday.4", "mail.weekday.5", "mail.weekday.6", "common.retry", "common.cancel",
}

// witText is witKeys in the UI language.
func witText() map[string]string {
	m := make(map[string]string, len(witKeys))
	for _, k := range witKeys {
		m[k] = i18n.T(k)
	}
	return m
}
