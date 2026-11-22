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
	{errViewUnsaved, "view.leave.desc"},
	{ErrLoginItemOff, "settings.login_item_off"},
}

// ErrLoginItemOff is the platform's SetOpenAtLogin failing because the
// system keeps Godwit's login item off until the user allows it there, as
// macOS does with one the user turned off in Login Items (#875). It is the
// user's choice, not a fault to report.
var ErrLoginItemOff = errors.New("open at login is off in the system's settings")

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

// maskReportLog 只保留带来源标记的已脱敏 trace，避免改变其字节计数。
func maskReportLog(text string) string {
	var out strings.Builder
	for line := range strings.Lines(text) {
		if trace, ok := strings.CutPrefix(line, core.TracePrefix); ok {
			out.WriteString(trace)
		} else if i := escapedTrace(line); i >= 0 {
			// One line per trace line, so issueURL drops the oldest first.
			out.WriteString(maskReport(line[:i]))
			out.WriteString(strings.ReplaceAll(line[i:], `\n`+core.TracePrefix, "\n"))
		} else {
			out.WriteString(maskReport(line))
		}
	}
	return out.String()
}

// escapedTrace is where the masked trace starts in line, -1 if it has none.
// The backend logs through slog's text handler, which writes the trace's
// record as one line with its line breaks escaped (#801), so the trace is
// what follows the record's first escaped line break: one that is not the
// escaped backslash and n of an error text that %q quoted. Within the trace,
// which is masked, an escaped line break before the prefix is always one.
func escapedTrace(line string) int {
	if !strings.Contains(line, `msg="imap: traffic before `) {
		return -1
	}
	for from := 0; ; {
		j := strings.Index(line[from:], `\n`+core.TracePrefix)
		if j < 0 {
			return -1
		}
		i := from + j
		k := i
		for k > 0 && line[k-1] == '\\' {
			k--
		}
		if (i-k)%2 == 0 { // the backslash at i is not itself escaped
			return i
		}
		from = i + 1
	}
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
	lines := strings.Split(maskReportLog(strings.TrimSpace(logText)), "\n")
	for {
		// The keys are field ids in .github/ISSUE_TEMPLATE/bug_report.yml; logs renders as code there.
		u := "https://github.com/godwit-email/godwit/issues/new?" + url.Values{
			"template":      {"bug_report.yml"},
			"title":         {title},
			"version":       {Version},
			"os":            {runtime.GOOS + "/" + runtime.GOARCH},
			"what-happened": {"```\n" + errText + "\n```"},
			"logs":          {strings.TrimSpace(strings.Join(lines, "\n"))},
		}.Encode()
		if len(u) <= issueURLMax || len(lines) == 0 {
			return u
		}
		lines = lines[1:]
	}
}
