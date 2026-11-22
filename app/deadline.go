package app

import (
	"slices"
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// deadlineText is a row's 回复日期 after its status, and whether it is
// overdue (only then in Danger): 今天, 明天前, this week's day, next week's,
// else the date.
func deadlineText(now time.Time, d core.Deadline) (string, bool) {
	day, err := time.ParseInLocation(time.DateOnly, d.Date, now.Location())
	if err != nil {
		return "", false
	}
	if d.Overdue {
		if ago := viewDaysAgo(now, day); ago > 0 {
			return i18n.Plural("view.deadline.overdue", ago), true
		}
		return i18n.T("view.deadline.overdue_today"), true
	}
	n, sunday := viewDaysAgo(day, now), 6-(int(now.Weekday())+6)%7 // days ahead; to this week's end, a week from Monday
	var when string
	switch {
	case n <= 0:
		if d.Time == "" {
			return i18n.T("view.deadline.due_today"), false
		}
		when = i18n.T("view.deadline.today")
	case n == 1:
		when = i18n.T("view.deadline.tomorrow")
	case n <= sunday:
		when = i18n.T("mail.weekday." + strconv.Itoa(int(day.Weekday())))
	case n <= sunday+7:
		when = i18n.Tf("view.deadline.next_week", i18n.T("mail.weekday."+strconv.Itoa(int(day.Weekday()))))
	default:
		when = monthDay(day)
	}
	if d.Time != "" {
		return i18n.Tf("view.deadline.by_at", when, d.Time), false
	}
	return i18n.Tf("view.deadline.by", when), false
}

// deadlineSentence is the reader's 回复日期 line: the sentence, the sender's
// quote after it ("" for none), and who set it ("" for the user). More than 7
// days overdue, it says how long ago instead of quoting.
func deadlineSentence(now time.Time, d core.Deadline) (lead, quote, source string) {
	day, err := time.ParseInLocation(time.DateOnly, d.Date, now.Location())
	if err != nil {
		return "", "", ""
	}
	key := "reader.deadline.asked"
	switch d.Source {
	case core.DeadlineManual:
		key = "reader.deadline.mine"
	case core.DeadlineAI:
		source = i18n.T("reader.deadline.source.ai")
	case core.DeadlineWit:
		source = i18n.T("reader.deadline.source.wit")
	default:
		source = i18n.T("reader.deadline.source.program")
	}
	if ago := viewDaysAgo(now, day); d.Overdue && ago > 7 {
		return i18n.Tf(key, monthDay(day)) + i18n.Plural("reader.deadline.passed", ago), "", source
	}
	when := i18n.Tf("reader.deadline.day", i18n.T("mail.weekday."+strconv.Itoa(int(day.Weekday()))), monthDay(day))
	if d.Time != "" {
		lead = i18n.Tf(key+"_at", when, d.Time)
	} else {
		lead = i18n.Tf(key, when)
	}
	if d.Quote != "" {
		quote = i18n.Tf("reader.deadline.quoted", d.Quote)
	}
	return lead, quote, source
}

// monthDay is a 回复日期's day without its year.
// shortcut: no year, so a date a year or more ahead reads as this year's; add it if dates that far show up.
func monthDay(day time.Time) string {
	return i18n.Tf("view.deadline.date", strconv.Itoa(int(day.Month())), strconv.Itoa(day.Day()))
}

// deadlineLine is the 回复日期 sentence, the sender's quote in it (which opens
// the message that asks), and under it who set it.
func (r *reader) deadlineLine(c *ui.Context) {
	d := *r.deadline
	t, th := theme.Of(c), c.Theme()
	lead, quote, source := deadlineSentence(r.p.a.clock.Now(), d)
	ui.Column(c).FillWidth().Gap(th.Space(0.5)).Children(func() {
		ui.RichText(c).LineHeight(theme.LineReading).TextColor(t.Foreground).Shrink(1).Children(func() {
			ui.RichText(c, ui.Span{Text: lead})
			if quote == "" {
				return
			}
			if !slices.ContainsFunc(r.msgs, func(m core.MessageSummary) bool { return m.ID == d.MessageID }) {
				ui.RichText(c, ui.Span{Text: quote})
				return
			}
			if ui.RichText(c, ui.Span{Text: quote}).Role(ui.RoleLink).Label(quote).Focusable().Cursor(ui.CursorPointer).Clicked() {
				r.reveal(d.MessageID)
			}
		})
		if source != "" {
			kit.Text(c, source, t.Text.Xs).TextColor(t.Muted)
		}
	})
}

// deadlineCard is the 回复日期 where no summary shows: the summary's card with
// only its 等你处理 line.
func (r *reader) deadlineCard(c *ui.Context) {
	if r.deadline == nil {
		return
	}
	t, th := theme.Of(c), c.Theme()
	kit.Block(c, ui.Column(c), kit.ToneCard).Radius(t.Radius.Xl).FillWidth().Padding(th.Space(3), th.Space(4)).Children(func() {
		r.todoRow(c, false, func() { r.deadlineLine(c) })
	})
}
