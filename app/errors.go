package app

import (
	"context"
	"errors"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"strings"

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

// issueTextMax caps the error in a report link, in UTF-8 bytes: escaped,
// each takes up to 3, and the link must stay under issueURLMax.
const issueTextMax = 2000

// issueURLMax is about what GitHub takes of a new-issue link; the log's
// oldest lines go first to fit.
const issueURLMax = 8000

// emailRE and hostRE find what in an error or the log names the user: their
// addresses and their mail servers. Filenames like godwit.db go too, a small loss.
var (
	emailRE = regexp.MustCompile(`[\w.+-]+@[\w-]+(\.[\w-]+)+`)
	hostRE  = regexp.MustCompile(`(?i)\b([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,}\b`)
)

// maskReport hides the home folder, addresses and hosts in text already rid of
// secrets: a report link is in the browser's history and the issue is public.
func maskReport(text string) string {
	if home, _ := os.UserHomeDir(); home != "" {
		text = strings.ReplaceAll(text, home, "~")
	}
	return hostRE.ReplaceAllString(emailRE.ReplaceAllString(text, "<email>"), "<host>")
}

// issueURL is a GitHub new-issue link reporting an error with the log's
// recent lines, both rid of secrets; the user reads it before submitting.
func issueURL(errText, logText string) string {
	errText = maskReport(errText)
	if len(errText) > issueTextMax {
		errText = strings.ToValidUTF8(errText[:issueTextMax], "") + "…"
	}
	title, _, _ := strings.Cut(errText, "\n")
	if r := []rune(title); len(r) > 80 {
		title = string(r[:80]) + "…"
	}
	lines := strings.Split(maskReport(strings.TrimSpace(logText)), "\n")
	for {
		body := "```\n" + errText + "\n```\n\n"
		if len(lines) > 0 && lines[0] != "" {
			body += "<details><summary>Log</summary>\n\n```\n" + strings.Join(lines, "\n") + "\n```\n</details>\n\n"
		}
		u := "https://github.com/godwit-email/godwit/issues/new?" + url.Values{"title": {title}, "body": {body + "Godwit " + Version + " (" + runtime.GOOS + "/" + runtime.GOARCH + ")\n"}}.Encode()
		if len(u) <= issueURLMax || len(lines) == 0 {
			return u
		}
		lines = lines[1:]
	}
}
