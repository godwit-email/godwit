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
		r.dateButtons(c)
	})
}

// replyDate is the 等你处理 line's 回复日期: the date, or, on a conversation
// waiting on the user with none, its 回复状态 (unless todo, the summary's
// to-do, says what waits) and 设回复日期.
func (r *reader) replyDate(c *ui.Context, todo bool) {
	if r.deadline != nil {
		r.deadlineLine(c)
		return
	}
	if !r.unreplied {
		return
	}
	if !todo {
		ui.Text(c, r.unrepliedText()).LineHeight(theme.LineReading).TextColor(theme.Of(c).Foreground).Shrink(1)
	}
	r.dateButtons(c)
}

// dateButtons are 改日期 and 去掉 under a 回复日期, 设回复日期 with none.
func (r *reader) dateButtons(c *ui.Context) {
	a, tid, d := r.p.a, r.key[0], r.deadline
	ui.Row(c).Gap(c.Theme().Space(3)).AlignItems(ui.Center).Children(func() {
		label := i18n.T("reader.deadline.set")
		if d != nil {
			label = i18n.T("reader.deadline.change")
		}
		b := kit.Button(c, label, kit.ButtonOpts{Variant: kit.Link, Size: kit.SizeXs}).OnClick(func() { r.datePick.start(d) })
		a.replyDatePopover(c, b, &r.datePick, tid)
		if d != nil && kit.Button(c, i18n.T("reader.deadline.remove"), kit.ButtonOpts{Variant: kit.Link, Size: kit.SizeXs}).Clicked() {
			a.setReplyDate(tid, core.Deadline{})
		}
	})
}

// deadlineCard is the 等你处理 line where no summary shows: the summary's
// card with only that line.
func (r *reader) deadlineCard(c *ui.Context) {
	if r.deadline == nil && !r.unreplied {
		return
	}
	t, th := theme.Of(c), c.Theme()
	kit.Block(c, ui.Column(c), kit.ToneCard).Radius(t.Radius.Xl).FillWidth().Padding(th.Space(3), th.Space(4)).Children(func() {
		r.todoRow(c, false, func() { r.replyDate(c, false) })
	})
}

// dateSetter is 改日期's popover: whether it is open, and the date and time typed.
type dateSetter struct {
	open       bool
	date, time string
}

// start opens the popover on d's date and time (none: empty).
func (ds *dateSetter) start(d *core.Deadline) {
	*ds = dateSetter{open: true}
	if d != nil {
		ds.date, ds.time = d.Date, d.Time
	}
}

// replyDatePreset is one of the popover's days.
type replyDatePreset struct {
	label string
	day   time.Time
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

// replyDatePopover is 改日期's popover on anchor while ds is open: the
// presets with their dates, a date and optional time to type with 设定, and,
// with a model set, the way to have summaries find reply dates.
func (a *App) replyDatePopover(c *ui.Context, anchor ui.Element, ds *dateSetter, threadID int64) {
	if !ds.open {
		return
	}
	set := func(d core.Deadline) {
		ds.open = false
		a.setReplyDate(threadID, d)
	}
	kit.PopoverAt(c, anchor, &ds.open, ui.AnchorBottomLeft, ui.AnchorTopLeft, theme.PopupOffset, func() {
		t, th := theme.Of(c), c.Theme()
		title := i18n.T("reader.deadline.title")
		ui.Column(c).Width(theme.SyncMenuWidth).Gap(th.Space(2)).Label(title).Children(func() {
			kit.Text(c, title, t.Text.Xs).FontWeight(500).TextColor(t.Muted)
			ui.Column(c).FillWidth().Children(func() {
				for i, p := range replyDatePresets(a.clock.Now()) {
					kit.MenuItem(c.Key(i), false).FillWidth().Label(p.label + " " + monthDay(p.day)).OnClick(func() { set(core.Deadline{Date: p.day.Format(time.DateOnly)}) }).Children(func() {
						kit.Text(c, p.label, t.Text.Sm).Grow(1).Shrink(1)
						kit.Text(c, monthDay(p.day), t.Text.Sm).TextColor(t.Muted).Shrink(0)
					})
				}
			})
			kit.Separator(c, false)
			_, dateErr := time.Parse(time.DateOnly, ds.date)
			_, timeErr := time.Parse("15:04", ds.time)
			ui.Row(c).FillWidth().Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
				kit.Input(c, &ds.date, kit.InputOpts{Placeholder: "YYYY-MM-DD", Size: kit.SizeSm, Invalid: ds.date != "" && dateErr != nil}).
					Label(i18n.T("reader.deadline.date_field")).Grow(1).Shrink(1).MinWidth(0)
				kit.Input(c, &ds.time, kit.InputOpts{Placeholder: "HH:MM", Size: kit.SizeSm, Invalid: ds.time != "" && timeErr != nil}).
					Label(i18n.T("reader.deadline.time_field")).Width(th.Space(16)).Shrink(0)
				ok := dateErr == nil && (ds.time == "" || timeErr == nil) // no time: the end of the day
				if kit.Button(c, i18n.T("reader.deadline.apply"), kit.ButtonOpts{Size: kit.SizeSm, Disabled: !ok}).Shrink(0).Clicked() && ok {
					set(core.Deadline{Date: ds.date, Time: ds.time})
				}
			})
			if note, link := arrivalFooter(a); link != "" { // 收到就总结, on AI 功能
				kit.Separator(c, false)
				ui.Row(c).FillWidth().Gap(th.Space(1)).AlignItems(ui.Center).Children(func() {
					if note != "" {
						kit.Text(c, note, t.Text.Xs).TextColor(t.Muted).Shrink(1)
					}
					if kit.Button(c, link, kit.ButtonOpts{Variant: kit.Link, Size: kit.SizeXs}).Shrink(0).Clicked() {
						ds.open = false
						a.navigate(Route{Page: PageSettings, Settings: "features"})
					}
				})
			}
		})
	})
}

// arrivalFooter is the date popover's last line about 收到就总结: under it,
// that it finds new mail's dates; chosen before, that it no longer does;
// else, with a model and the level offered, the way to it. link "": none.
func arrivalFooter(a *App) (note, link string) {
	switch {
	case !core.SummaryOnArrival:
		return "", ""
	case a.settings.SummaryMode == core.SummaryArrive:
		return i18n.T("reader.deadline.by_arrival"), i18n.T("reader.deadline.settings")
	case a.arrival.Service != "":
		return i18n.T("reader.deadline.arrival_stopped"), i18n.T("reader.deadline.settings")
	case a.settings.DefaultModel != "":
		return "", i18n.T("reader.deadline.on_arrival")
	}
	return "", ""
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
