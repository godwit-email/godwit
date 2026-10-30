package app

import (
	"context"
	"errors"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/rpcbind"
)

// errorKeys are the app's own errors with a message; core's are rpcbind.Key's.
var errorKeys = []struct {
	err error
	key string
}{
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
	switch cats := core.MaskBlocked(err); {
	case cats != nil:
		return i18n.Tf("error.ai.blocked", maskCategories(cats))
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
	if key := rpcbind.Key(err); key != "" && i18n.T(key) != key {
		return i18n.T(key)
	}
	return i18n.Tf("error.unknown", err.Error())
}
