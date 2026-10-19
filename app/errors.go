package app

import (
	"context"
	"errors"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/godwit-email/godwit/internal/i18n"
)

// errorKeys are the core errors with a message of their own.
var errorKeys = []struct {
	err error
	key string
}{
	{core.ErrStaleDraft, "error.stale_draft"},
	{core.ErrInvalidState, "error.invalid_state"},
	{core.ErrRunActive, "error.run_active"},
	{core.ErrStaleApproval, "error.stale_approval"},
	{core.ErrNeedsGrant, "error.needs_grant"},
	{core.ErrTranslatorUnavailable, "error.translator_unavailable"},
	{core.ErrSystemLanguageNotInstalled, "error.system_language_not_installed"},
	{core.ErrSystemLanguageUnsupported, "error.system_language_unsupported"},
	{core.ErrModelUnavailable, "error.model_unavailable"},
	// A native plugin's failures: its stderr is the plugin's text, not the user's.
	{plugins.ErrNativeExited, "error.plugin_exited"},
	{plugins.ErrTimeout, "error.plugin_timeout"},
	{plugins.ErrNativeABI, "error.plugin_abi"},
	{core.ErrRegistryMismatch, "error.registry_mismatch"},
	{core.ErrRegistrySignature, "error.registry_signature"},
	{core.ErrAINotConfigured, "error.ai_not_configured"},
	{core.ErrModelNotFound, "error.model_not_found"},
	{core.ErrModelAuth, "error.model_auth"},
	{core.ErrModelReauth, "settings.models.login.expired"},
	{auth.ErrRedirectPortBusy, "settings.models.login.port_busy"},
	{core.ErrKeyNeedsConfirm, "settings.models.key_moved"},
	{core.ErrBaseURLInvalid, "error.base_url_invalid"},
	{core.ErrBaseURLScheme, "error.base_url_scheme"},
	{core.ErrTooLarge, "error.too_large"},
	{core.ErrKeychainUnavailable, "error.keychain"},
	{errFlushSlow, "error.flush_slow"},
	{errSignatureUnsaved, "error.signature_unsaved"},
}

// humanize says what err means to the user; "" for a cancellation, which
// the user asked for. An unknown error shows its own text.
func humanize(err error) string {
	if err == nil || errors.Is(err, context.Canceled) { // a canceled *ai.Error is context.Canceled
		return ""
	}
	var check *core.CheckError
	var blocked *core.SendBlockedError
	var aiErr *ai.Error
	switch {
	case errors.As(err, &check):
		for _, s := range check.Report.Steps {
			if !s.OK && !s.Skipped {
				return i18n.Tf("error.check."+s.Name, s.Detail)
			}
		}
	case errors.As(err, &blocked):
		return i18n.Tf("error.send_blocked", blocked.Plugin, blocked.Message)
	case errors.As(err, &aiErr):
		if aiErr.Detail != "" {
			if key := "error.ai." + string(aiErr.Code) + "." + aiErr.Detail; i18n.T(key) != key {
				return i18n.T(key)
			}
		}
		if key := "error.ai." + string(aiErr.Code); i18n.T(key) != key {
			return i18n.T(key)
		}
	}
	for _, k := range errorKeys {
		if errors.Is(err, k.err) {
			return i18n.T(k.key)
		}
	}
	return i18n.Tf("error.unknown", err.Error())
}
