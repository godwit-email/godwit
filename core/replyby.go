package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/godwit-email/godwit/core/store"
)

// replyBy is the date by which the newest email from someone else wants the
// user's reply, as a summary gives it in replyByFormat's two lines: a day of
// the user's local calendar, maybe a time, and the sentence that asks for it.
type replyBy struct {
	Date  string // YYYY-MM-DD
	Time  string // HH:MM; "" is by the end of the day
	Quote string
}

// parseReplyBy takes replyByFormat's two lines out of a summary. rest is the
// summary without them, r is zero for "none", and err says why lines that
// should be there do not read (r is then zero too: no date).
func parseReplyBy(text string) (r replyBy, rest string, err error) {
	var dates, quotes, keep []string
	for line := range strings.Lines(text) {
		t := strings.TrimSpace(line)
		if v, ok := cutPrefixFold(t, "REPLY BY:"); ok {
			dates = append(dates, strings.TrimSpace(v))
		} else if v, ok := cutPrefixFold(t, "QUOTE:"); ok {
			quotes = append(quotes, strings.Trim(v, " \t\"'“”「」『』"))
		} else {
			keep = append(keep, line)
		}
	}
	rest = strings.TrimSpace(strings.Join(keep, ""))
	switch {
	case len(dates) == 0:
		return replyBy{}, rest, errors.New("no REPLY BY line")
	case len(dates) > 1:
		return replyBy{}, rest, errors.New("more than one REPLY BY line")
	case strings.EqualFold(dates[0], "none"):
		return replyBy{}, rest, nil
	case dates[0] == "": // trimmed: blank, or spaces alone
		return replyBy{}, rest, errors.New("empty REPLY BY line")
	}
	f := strings.Fields(dates[0])
	r.Date = f[0]
	if d, err := time.Parse(time.DateOnly, r.Date); err != nil || d.Format(time.DateOnly) != r.Date {
		return replyBy{}, rest, fmt.Errorf("date %q is not YYYY-MM-DD", dates[0])
	}
	if len(f) > 1 {
		r.Time = strings.Join(f[1:], " ")
		if t, err := time.Parse("15:04", r.Time); err != nil || t.Format("15:04") != r.Time {
			return replyBy{}, rest, fmt.Errorf("time %q is not HH:MM", r.Time)
		}
	}
	switch {
	case len(quotes) > 1:
		return replyBy{}, rest, errors.New("more than one QUOTE line")
	case len(quotes) == 0 || strings.TrimSpace(quotes[0]) == "":
		return replyBy{}, rest, errors.New("no QUOTE line")
	}
	r.Quote = quotes[0]
	return r, rest, nil
}

// check is nil when r holds against newest, the newest message from someone
// else as it went to the model: the quote is in its text (links as
// references, cut to budget; mask placeholders are back in both), spaces
// aside, and the date is between the day before the message's date and a
// year after it, on loc's calendar (the user's).
func (r replyBy) check(newest mailItem, loc *time.Location) error {
	// The reply's links were expanded: linkRefs writes them back as they were sent.
	if !strings.Contains(noSpace(newest.Block), noSpace(linkRefs(r.Quote, newest.MessageID))) {
		return errors.New("quote not in the newest message")
	}
	sent := newest.Date.In(loc)
	from := time.Date(sent.Year(), sent.Month(), sent.Day()-1, 0, 0, 0, 0, time.UTC)
	to := time.Date(sent.Year()+1, sent.Month(), sent.Day(), 0, 0, 0, 0, time.UTC)
	if d, err := time.Parse(time.DateOnly, r.Date); err != nil || d.Before(from) || d.After(to) {
		return fmt.Errorf("%s out of range: %s to %s", r.Date, from.Format(time.DateOnly), to.Format(time.DateOnly))
	}
	return nil
}

func noSpace(s string) string { return strings.Join(strings.Fields(s), "") }

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return s[len(prefix):], true
	}
	return "", false
}

// writeReplyBy makes a summary's valid reply date the 期限 of the
// conversation of items (the mail the model got), source ai, checked against
// the conversation's newest counted message as 回复状态 has it (a draft synced
// from elsewhere is not one), which must be among items: a summary of an older
// message writes nothing. A date that does not read or check, or none,
// changes nothing: a follow-up asking for no date keeps the one before.
// setDeadline refusing it (a higher source in effect, no longer waiting on the
// user) is no error of the summary.
func (s *Service) writeReplyBy(ctx context.Context, text string, items []mailItem, loc *time.Location) {
	r, _, err := parseReplyBy(text)
	if err != nil || r.Date == "" || len(items) == 0 {
		return
	}
	rows, err := s.db.Query(ctx, "SELECT thread_id FROM messages WHERE id = ?", items[0].MessageID)
	var statuses map[int64]replyStatus
	if err == nil && len(rows.Values) > 0 {
		statuses, err = s.replyStatuses(ctx, s.userNow(ctx).Unix())
	}
	if err == nil && len(rows.Values) > 0 {
		tid := store.Int(rows.Values[0][0])
		if i := slices.IndexFunc(items, func(it mailItem) bool { return it.MessageID == statuses[tid].Newest }); i >= 0 && r.check(items[i], loc) == nil {
			err = s.setDeadline(ctx, DeadlineAI, tid, Deadline{Date: r.Date, Time: r.Time, MessageID: items[i].MessageID, Quote: r.Quote})
		}
	}
	if err != nil {
		slog.Default().Debug("summary reply date not written", "err", err) // the errors carry no mail content
	}
}

// zoneName is now's time zone for replyByFormat: its IANA name, else (Go's
// time.Local has none) its UTC offset.
// shortcut: an offset misses a DST change before the date; upgrade if SetTimeZone ever goes unset in the app.
func zoneName(now time.Time) string {
	if name := now.Location().String(); name != "Local" {
		return name
	}
	return now.Format("UTC-07:00")
}

// stripReplyBy is a whole summary without replyByFormat's two lines, as a
// replyByHold streams it.
func stripReplyBy(text string) string {
	var h replyByHold
	return h.feed(text) + h.flush()
}

// replyByHold passes a summary on as it streams, without replyByFormat's
// two lines (parseReplyBy's REPLY BY: and QUOTE:): a line that may become
// one is held until it reads otherwise. mid: part of the current line has
// been passed on, so what follows up to its end is that line's, never one
// of the two.
type replyByHold struct {
	held string
	mid  bool
}

func (h *replyByHold) feed(s string) string {
	h.held += s
	var out strings.Builder
	for {
		line, rest, ok := strings.Cut(h.held, "\n")
		if !ok {
			break
		}
		if h.mid || !replyByLine(line, true) {
			out.WriteString(line + "\n")
		}
		h.held, h.mid = rest, false
	}
	if h.held != "" && (h.mid || !replyByLine(h.held, false)) {
		out.WriteString(h.held)
		h.held, h.mid = "", true
	}
	return out.String()
}

// flush is what is held when the summary ends.
func (h *replyByHold) flush() string {
	s, mid := h.held, h.mid
	h.held, h.mid = "", false
	if !mid && replyByLine(s, true) {
		return ""
	}
	return s
}

// replyByLine is whether line is one of the two lines, or, when it is not
// whole yet, may still become one.
func replyByLine(line string, whole bool) bool {
	t := strings.TrimLeft(line, " \t")
	for _, p := range []string{"REPLY BY:", "QUOTE:"} {
		if _, ok := cutPrefixFold(t, p); ok || !whole && len(t) < len(p) && strings.EqualFold(p[:len(t)], t) {
			return true
		}
	}
	return false
}
