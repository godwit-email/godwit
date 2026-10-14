package webui

import "github.com/godwit-email/godwit/internal/i18n"

// composeKeys are the i18n keys a compose page shows, sent in ComposeState.Text.
var composeKeys = []string{
	"compose.new", "compose.status.saving", "compose.status.saved", "compose.status.unsaved",
	"compose.from", "compose.from.select", "compose.to", "compose.to.input", "compose.cc_bcc",
	"compose.cc", "compose.cc.input", "compose.bcc", "compose.bcc.input", "compose.reply_to",
	"compose.reply_to.input", "compose.subject", "compose.subject.input", "compose.invalid",
	"compose.body", "compose.bold", "compose.format", "compose.italic", "compose.link",
	"compose.link.url", "compose.link.apply", "compose.list", "compose.list_ordered", "compose.quote",
	"compose.attach", "compose.remove", "compose.attach.too_large", "compose.attach.failed", "compose.quote.show",
	"compose.conflict", "compose.conflict.keep", "compose.conflict.latest", "compose.warn",
	"compose.warn.from", "compose.send_anyway", "compose.send", "compose.sending.locked",
	"common.cancel", "common.close", "common.retry", "close.unsaved.title",
	"error.stale_draft", "error.too_large", "error.unknown", "mail.no_subject", "mail.list_sep",
	// Wit
	"error.invalid_state", "error.run_active", "error.stale_approval", "error.needs_grant", "error.ai_not_configured",
	"error.model_not_found", "error.model_auth", "error.keychain", "error.ai.unauthorized", "error.ai.rate_limited",
	"error.ai.unavailable", "error.ai.bad_response", "error.ai.internal", "error.ai.stale", "error.ai.incomplete",
	"link.title", "link.open", "link.copy", "link.copied",
	"wit.input", "wit.placeholder", "wit.send", "wit.stop", "wit.loading", "wit.scroll_end", "wit.thinking", "wit.tools.one", "wit.tools.other", "wit.tools.called", "wit.suspect", "wit.suspect_quote", "wit.model",
	"wit.approval.wants", "wit.approval.to", "wit.approval.cc", "wit.approval.subject", "wit.approval.destinations",
	"wit.approval.allow", "wit.approval.allow_all", "wit.approval.reject", "wit.approval.send_note",
	"wit.add", "wit.quote", "wit.quote.search", "wit.quote.none", "wit.quote.remove", "wit.approval.allow_session",
	"wit.compose.input", "wit.compose.placeholder", "wit.compose.empty", "wit.compose.writing_banner", "wit.compose.working",
	"wit.compose.locked", "wit.compose.pending.one", "wit.compose.pending.other",
	"wit.compose.accept_all", "wit.compose.reject_all", "wit.compose.accept", "wit.compose.reject",
	"wit.compose.link", "wit.compose.rewrite", "wit.compose.rewrite_plain", "wit.compose.compare",
	"wit.compose.back", "wit.compose.original", "wit.compose.new", "wit.compose.review",
	"wit.compose.review.inline.one", "wit.compose.review.inline.other", "wit.compose.review.rewrite",
	"wit.compose.review.rewrite_plain", "wit.compose.review_body", "wit.compose.applied", "wit.compose.undo",
	"wit.compose.cmdk", "wit.compose.cmdk.input", "wit.compose.cmdk.esc", "wit.compose.cmdk.go",
	"wit.compose.adjust", "wit.compose.translate", "wit.compose.translate_to", "wit.compose.source",
	"wit.compose.translate_only", "wit.compose.translate_only.models",
	"wit.compose.history", "shell.resize", "wit.tool.update_draft", "wit.tool.update_selection", "wit.tool.create_draft", "wit.tool.classify_mail",
}

// composeText is composeKeys in the UI language.
func composeText() map[string]string {
	m := make(map[string]string, len(composeKeys))
	for _, k := range composeKeys {
		m[k] = i18n.T(k)
	}
	return m
}
