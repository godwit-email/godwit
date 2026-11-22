package core

import (
	"errors"
	"fmt"
	"strings"
	"time"
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
