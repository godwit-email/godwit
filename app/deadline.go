package app

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// rowStatus is a row's third line: its status (viewLine) and its 回复日期
// (deadlineText), the date alone with no icon where the row has no status.
func rowStatus(now time.Time, it mailItem) (icon, line, due string, overdue bool) {
	icon, line = viewLine(it)
	if it.Deadline != nil {
		due, overdue = deadlineText(now, *it.Deadline)
		if it.Status == "" {
			icon, line = "", ""
		}
	}
	return icon, line, due, overdue
}

// deadlineText is a row's 回复日期 after its status, and whether it is
// overdue (only then in DangerText): 今天, 明天前, this week's day, next week's,
// else the date.
func deadlineText(now time.Time, d core.Deadline) (string, bool) {
	day, err := time.ParseInLocation(time.DateOnly, d.Date, now.Location())
	if err != nil {
		return "", false
	}
	if d.Overdue {
		if ago := viewDaysAgo(now, day); ago > 0 {
			return i18n.Plural("view.deadline.overdue_days", ago), true
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

// deadlineSentence is the reader's 回复日期 line: the sentence (with the
// colon a quote follows), the sender's quote after it ("" for none), and who
// set it ("" for the user). More than 7 days overdue, it says how long ago
// instead of quoting.
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
	// the row's 今天 and 明天: today's a sentence of its own, as 今天前回复 would not read
	when, today := weekdayDay(day), false
	switch viewDaysAgo(day, now) {
	case 0:
		today = true
	case 1:
		when = i18n.Tf("reader.deadline.day", i18n.T("view.deadline.tomorrow"), monthDay(day))
	}
	switch {
	case today && d.Time != "":
		lead = i18n.Tf(key+"_today_at", d.Time)
	case today:
		lead = i18n.T(key + "_today")
	case d.Time != "":
		lead = i18n.Tf(key+"_at", when, d.Time)
	default:
		lead = i18n.Tf(key, when)
	}
	if d.Quote != "" {
		lead, quote = i18n.Tf("reader.deadline.quoted", lead), d.Quote
	}
	return lead, quote, source
}

// monthDay is a 回复日期's day without its year.
// shortcut: no year, so a date a year or more ahead reads as this year's; add it if dates that far show up.
func monthDay(day time.Time) string {
	return i18n.Tf("view.deadline.date", strconv.Itoa(int(day.Month())), strconv.Itoa(day.Day()))
}

// weekdayDay is a 回复日期's day as the reader's line says it: 周五（11月27日）.
func weekdayDay(day time.Time) string {
	return i18n.Tf("reader.deadline.day", i18n.T("mail.weekday."+strconv.Itoa(int(day.Weekday()))), monthDay(day))
}

// deadlineLine is the 回复日期 sentence, the sender's quote in it in a chip
// (which opens the message that asks), and under it status ("" for none)
// and who set it, both small.
func (r *reader) deadlineLine(c *ui.Context, status string) {
	d := *r.deadline
	t, th := theme.Of(c), c.Theme()
	lead, quote, source := deadlineSentence(r.p.a.clock.Now(), d)
	ui.Column(c).FillWidth().Gap(th.Space(0.5)).Children(func() {
		ui.RichText(c).LineHeight(theme.LineReading).TextColor(t.Foreground).Shrink(1).Children(func() {
			ui.RichText(c, ui.Span{Text: lead})
			if quote == "" {
				return
			}
			chip := ui.RichText(c, ui.Span{Text: quote, Color: t.Subdued, Background: t.HoverTint, Radius: t.Radius.Sm, PaddingX: th.Space(1)})
			if slices.ContainsFunc(r.msgs, func(m core.MessageSummary) bool { return m.ID == d.MessageID }) &&
				chip.Role(ui.RoleLink).Label(quote).Focusable().Cursor(ui.CursorPointer).Clicked() {
				r.reveal(d.MessageID)
			}
		})
		for _, small := range []string{status, source} {
			if small != "" {
				kit.Text(c, small, t.Text.Xs).TextColor(t.Muted)
			}
		}
	})
}

// dateInBand is whether the 待跟进 band says the 回复日期, in a view or not:
// a conversation waiting on someone else's reply has no 等你处理 line.
func (r *reader) dateInBand() bool { return r.deadline != nil && r.awaited }

// dated is whether the 等你处理 line says a 回复日期.
func (r *reader) dated() bool { return r.deadline != nil && !r.dateInBand() }

// todoShown is whether the 等你处理 line shows: with a 回复日期, or on a
// conversation waiting on the user.
func (r *reader) todoShown() bool { return r.dated() || r.unreplied }

// replyDate is the 等你处理 line's 回复日期 and, on a conversation waiting on
// the user, its 回复状态 (unless todo, the summary's to-do, says what waits):
// under the date, or alone where there is none.
func (r *reader) replyDate(c *ui.Context, todo bool) {
	status := ""
	if r.unreplied && !todo {
		status = r.unrepliedText()
	}
	if r.dated() {
		r.deadlineLine(c, status)
		return
	}
	if status != "" {
		ui.Text(c, status).LineHeight(theme.LineReading).TextColor(theme.Of(c).Foreground).Shrink(1)
	}
}

// dateButton is 改日期 at the 等你处理 line's right end, 设回复日期 with no
// date on a conversation waiting on the user: replyDateMenu.
func (r *reader) dateButton(c *ui.Context) {
	if r.todoShown() {
		r.dateMenuButton(c)
	}
}

// dateMenuButton is 改日期, or 设回复日期 with no date, opening replyDateMenu.
func (r *reader) dateMenuButton(c *ui.Context) {
	a, tid, d := r.p.a, r.key[0], r.deadline
	label := i18n.T("reader.deadline.set")
	if d != nil {
		label = i18n.T("reader.deadline.change")
	}
	kit.Button(c, label, kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeXs}).Shrink(0).Menu(func(m *ui.Menu) { a.replyDateMenu(m, tid, d) })
}

// deadlineCard is the 等你处理 line where no summary shows: the summary's
// card with only that line, its text inset 12 as the title is from the
// column's edge.
func (r *reader) deadlineCard(c *ui.Context) {
	if !r.todoShown() {
		return
	}
	t, th := theme.Of(c), c.Theme()
	kit.Block(c, ui.Column(c), kit.ToneCard).Radius(t.Radius.Xl).FillWidth().Padding(th.Space(3)).Children(func() {
		r.todoRow(c, false, func() { r.replyDate(c, false) })
	})
}

// replyDatePreset is one of the date menu's days.
type replyDatePreset struct {
	label string
	day   time.Time
}

// is whether d, a conversation's 回复日期, is the preset: its day, no time.
func (p replyDatePreset) is(d *core.Deadline) bool {
	return d != nil && d.Time == "" && d.Date == p.day.Format(time.DateOnly)
}

// replyDatePresets are 今天, 明天, this week's 周五 and 下周一 from now, each
// day once: 周五 drops when it is today or tomorrow, 下周一 when it is tomorrow.
func replyDatePresets(now time.Time) []replyDatePreset {
	wd := int(now.Weekday())
	var out []replyDatePreset
	for _, p := range []struct {
		label string
		days  int
	}{
		{i18n.T("view.deadline.today"), 0},
		{i18n.T("view.deadline.tomorrow"), 1},
		{i18n.T("mail.weekday.5"), (5 - wd + 7) % 7},
		{i18n.Tf("view.deadline.next_week", i18n.T("mail.weekday.1")), 7 - (wd+6)%7},
	} {
		day := time.Date(now.Year(), now.Month(), now.Day()+p.days, 0, 0, 0, 0, now.Location())
		if !slices.ContainsFunc(out, func(o replyDatePreset) bool { return o.day.Equal(day) }) {
			out = append(out, replyDatePreset{p.label, day})
		}
	}
	return out
}

// replyDateMenu is the menu 改日期 and 设回复日期 open: the presets, each
// setting its day with no time, the one d is checked; 选日期… for any day
// and time; and 去掉 when d, the conversation's date, is set.
func (a *App) replyDateMenu(m *ui.Menu, threadID int64, d *core.Deadline) {
	for _, p := range replyDatePresets(a.clock.Now()) {
		// the day in the label: the system's menus show no Trailing
		if m.Item(p.label + " · " + monthDay(p.day)).Checked(p.is(d)).Chosen() {
			a.setReplyDate(threadID, core.Deadline{Date: p.day.Format(time.DateOnly)})
		}
	}
	m.Separator()
	if m.Item(i18n.T("reader.deadline.pick")).Chosen() {
		a.pickReplyDate(threadID, d)
	}
	if d != nil {
		m.Separator()
		if m.Item(i18n.T("reader.deadline.remove")).Chosen() {
			a.setReplyDate(threadID, core.Deadline{})
		}
	}
}

// replyDateAsk is 选日期…'s dialog: the conversation, the day and time
// chosen, and whether the time is left out (the end of that day).
type replyDateAsk struct {
	open, untimed bool
	thread        int64
	at            time.Time
}

// deadline is the 回复日期 the dialog has chosen.
func (q *replyDateAsk) deadline() core.Deadline {
	d := core.Deadline{Date: q.at.Format(time.DateOnly)}
	if !q.untimed {
		d.Time = q.at.Format("15:04")
	}
	return d
}

// pickReplyDate opens the dialog on the conversation's date d (nil: today)
// and its time, if any; a date gone by opens on today.
func (a *App) pickReplyDate(threadID int64, d *core.Deadline) {
	now := a.clock.Now()
	day, hour, minute, untimed := now, 18, 0, true // a time put in starts at a working day's end
	if d != nil {
		if set, err := time.ParseInLocation(time.DateOnly, d.Date, now.Location()); err == nil && set.After(now) {
			day = set
		}
		if t, err := time.Parse("15:04", d.Time); err == nil {
			hour, minute, untimed = t.Hour(), t.Minute(), false
		}
	}
	a.dateAsk = replyDateAsk{open: true, untimed: untimed, thread: threadID,
		at: time.Date(day.Year(), day.Month(), day.Day(), hour, minute, 0, 0, now.Location())}
}

// replyDateDialog is 选日期…'s dialog over the window: a day from today, any
// time of it unless one is put in (then not one gone by), read out as the
// reader's line says it, with the note about 收到就总结.
func (a *App) replyDateDialog(c *ui.Context) {
	q := &a.dateAsk
	if !q.open {
		return
	}
	kit.DateTimeDialog(c, &q.open, &q.at, kit.DateTimeDialogOpts{
		DateTimeOpts: kit.DateTimeOpts{
			Min: a.clock.Now(), Date: i18n.T("reader.deadline.date_field"), Time: i18n.T("reader.deadline.time_field"),
			PrevMonth: i18n.T("common.previous_month"), NextMonth: i18n.T("common.next_month"),
			Untimed: &q.untimed, AnyTime: i18n.T("reader.deadline.any_time"),
			Locale: i18n.Locale(), // the interface language, as the strings and the readout
		},
		Title: i18n.T("reader.deadline.title"), Note: arrivalNote(a),
		Confirm: i18n.T("reader.deadline.apply"), Cancel: i18n.T("common.cancel"), Close: i18n.T("common.close"),
		Readout: func(time.Time) string { return replyDateReadout(q.deadline(), a.clock.Now().Location()) },
		Check: func(at time.Time) string {
			if !q.untimed && !at.After(a.clock.Now()) {
				return i18n.T("compose.send_later.past")
			}
			return ""
		},
	}, func() { a.setReplyDate(q.thread, q.deadline()) })
}

// replyDateReadout is the dialog's reading of d: 周五（11月27日）前回复, with its time if any.
func replyDateReadout(d core.Deadline, loc *time.Location) string {
	day, _ := time.ParseInLocation(time.DateOnly, d.Date, loc)
	if d.Time != "" {
		return i18n.Tf("reader.deadline.readout_at", weekdayDay(day), d.Time)
	}
	return i18n.Tf("reader.deadline.readout", weekdayDay(day))
}

// arrivalNote is the date dialog's note about 收到就总结: under it, that it
// finds new mail's dates; chosen before, that it no longer does; else, with
// a model and the level offered, where to choose it. "": none.
func arrivalNote(a *App) string {
	switch {
	case !core.SummaryOnArrival:
		return ""
	case a.settings.SummaryMode == core.SummaryArrive:
		return i18n.T("reader.deadline.by_arrival")
	case a.arrival.Service != "":
		return i18n.T("reader.deadline.arrival_stopped")
	case a.settings.DefaultModel != "":
		return i18n.Tf("reader.deadline.on_arrival", i18n.T("settings.ai"), i18n.T("settings.summary.arrive"))
	}
	return ""
}

// setReplyDate sets (d.Date "": removes) the conversation's 回复日期 as the
// user's, with a toast whose 撤销 puts back what was there.
func (a *App) setReplyDate(threadID int64, d core.Deadline) {
	var before core.RemovedDeadline
	a.do(func(ctx context.Context) (err error) {
		before, err = a.svc.SetThreadDeadline(ctx, threadID, d)
		return err
	}, func(err error) {
		if err != nil {
			a.toastErr(err)
			return
		}
		title := i18n.T("reader.deadline.removed")
		if d.Date != "" {
			title = i18n.T("reader.deadline.set_done")
		}
		now := a.clock.Now()
		a.toasts = append(a.toasts, ui.Toast{ID: fmt.Sprintf("reply-date-%d", now.UnixNano()), Title: title, Action: i18n.T("compose.undo"),
			Timeout: viewUndoWindow, Data: kit.ToastData{Deadline: now.Add(viewUndoWindow)}, OnAction: func() {
				a.do(func(ctx context.Context) error { return a.svc.UndoSetThreadDeadline(ctx, before) }, func(err error) {
					if err != nil {
						a.toastErr(err)
					}
				})
			}})
	})
}
