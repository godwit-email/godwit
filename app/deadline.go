package app

import (
	"strconv"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
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

// monthDay is a 回复日期's day without its year.
// shortcut: no year, so a date a year or more ahead reads as this year's; add it if dates that far show up.
func monthDay(day time.Time) string {
	return i18n.Tf("view.deadline.date", strconv.Itoa(int(day.Month())), strconv.Itoa(day.Day()))
}
