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
	"compose.note", "compose.discard", "compose.discard.title", "compose.discard.body",
	"compose.discard.confirm",
	"common.cancel", "common.close", "common.retry", "close.unsaved.title",
	"error.stale_draft", "error.too_large", "error.unknown", "mail.no_subject",
}

// composeText is composeKeys in the UI language.
func composeText() map[string]string {
	m := make(map[string]string, len(composeKeys))
	for _, k := range composeKeys {
		m[k] = i18n.T(k)
	}
	return m
}
