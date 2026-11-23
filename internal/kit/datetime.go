package kit

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/theme"
)

// DateTimeOpts are a DateTimePicker's: Min, the first day that can be
// chosen (zero for any); the accessible names of the calendar (Date), the
// clock (Time) and the calendar's month buttons; and, for a time that may be
// left out, Untimed with AnyTime, the label of the switch that leaves it out.
// Locale is the interface language the month, the weekdays and the readout
// are written in, "" for the window's (Context.Locale); the first day of the
// week stays the region's.
type DateTimeOpts struct {
	Min                              time.Time
	Date, Time, PrevMonth, NextMonth string
	Untimed                          *bool
	AnyTime                          string
	Locale                           string
}

// locale is o's Locale, or the window's.
func (o DateTimeOpts) locale(c *ui.Context) string {
	if o.Locale != "" {
		return o.Locale
	}
	return c.Locale()
}

// pickerKey keys a calendar's month shown and the day it last followed.
type pickerKey struct{}

type pickerMonth struct{ shown, followed time.Time }

// DateTimePicker chooses *at: a month's calendar beside a clock, the time
// typed in a field over a face whose hands both drag. A day is a large
// button (SizeLg) and the calendar seven of them wide, the face six tall;
// too narrow for both, the face goes under.
func DateTimePicker(c *ui.Context, at *time.Time, o DateTimeOpts) ui.Element {
	th := c.Theme()
	return ui.Row(c).Wrap().AlignItems(ui.Start).Gap(th.Space(4)).Children(func() {
		calendar(c, at, o)
		clockFace(c, at, o)
	})
}

// DateTimeDialogOpts are a DateTimeDialog's: its picker's, its title, the
// note under the picker ("" for none), and its buttons' labels. Readout
// writes the time chosen under the title (nil: Readout's). Check says why a
// time cannot be confirmed, "" when it can (nil: any can).
type DateTimeDialogOpts struct {
	DateTimeOpts
	Title, Note, Confirm, Cancel, Close string
	Readout                             func(at time.Time) string
	Check                               func(at time.Time) string
}

// refusal is the reason Check gave at the time it refused.
type refusal struct {
	why string
	at  time.Time
}

// DateTimeDialog is a Dialog choosing *at in a DateTimePicker, the time
// chosen read out under its title. Confirm, or ⌘↩ wherever the focus is,
// closes it and calls onConfirm, unless Check refuses the time: the reason
// is then read out in Danger until the time changes. Cancel, Escape and the
// close button close it, *at as chosen.
func DateTimeDialog(c *ui.Context, open *bool, at *time.Time, o DateTimeDialogOpts, onConfirm func()) {
	t, th := theme.Of(c), c.Theme()
	refused := ui.Local(c.Root(), open, func() refusal { return refusal{} })
	if !refused.at.Equal(*at) {
		refused.why = ""
	}
	confirm := func() {
		if o.Check != nil {
			if why := o.Check(*at); why != "" {
				*refused = refusal{why, *at}
				return
			}
		}
		*open = false
		onConfirm()
	}
	desc := Readout(o.locale(c), *at, c.Now())
	if o.Readout != nil {
		desc = o.Readout(*at)
	}
	if refused.why != "" {
		desc = refused.why
	}
	style := func(d ui.Element) {
		d.FontFeatures("tnum").Role(ui.RoleStatus) // read out as it changes
		if refused.why != "" {
			d.TextColor(t.Danger)
		}
	}
	Dialog(c, open, o.Title, DialogOpts{Desc: desc, DescStyle: style, Close: o.Close}, func() {
		if c.Shortcut(ui.Cmd, ui.KeyEnter) {
			confirm()
		}
		DateTimePicker(c, at, o.DateTimeOpts)
		if o.Note != "" {
			ui.Row(c).Gap(th.Space(1.5)).AlignItems(ui.Center).Children(func() {
				InputIcon(c, "info").Size(theme.IconSm, theme.IconSm).TextColor(t.Muted)
				Text(c, o.Note, t.Text.Xs).TextColor(t.Muted).Shrink(1)
			})
		}
	}, func() {
		if Button(c, o.Cancel, ButtonOpts{Variant: Outline}).Clicked() {
			*open = false
		}
		if Button(c, o.Confirm, ButtonOpts{}).Clicked() {
			confirm()
		}
	})
}

// Readout is at as a DateTimeDialog reads it out in locale at now: the
// month, the day, the weekday and the time, "Today · " or "Tomorrow · "
// before it for those days, as Intl writes them.
func Readout(locale string, at, now time.Time) string {
	when := i18n.FormatDate(locale, at, i18n.DateOptions{Skeleton: "MMMMEdjmm"})
	days := int(math.Round(day0(at).Sub(day0(now.In(at.Location()))).Hours() / 24))
	if days != 0 && days != 1 {
		return when
	}
	rel := []rune(i18n.FormatRelativeTime(locale, float64(days), i18n.RelativeDay, i18n.RelativeTimeOptions{Auto: true}))
	return strings.ToUpper(string(rel[:1])) + string(rel[1:]) + " · " + when
}

// The face's drawing in hundredths of its width from its centre: where the
// ticks run, the numerals' circle, the hands' lengths and widths (thicker
// while dragged) and the hub. These are a clock's proportions, not the
// layout's; the face is six days tall.
const (
	faceTickOut, faceTickMajor, faceTickMinor = 47, 41, 44
	faceNumerals                              = 34
	faceMinute, faceHour                      = 40, 25
	faceMinuteW, faceMinuteDragged            = 1.6, 2.2
	faceHourW, faceHourDragged                = 2.6, 3.2
	faceHub                                   = 2.4
)

// clockKey keys the hand a press on the face holds: 'h', 'm' or none.
type clockKey struct{}

// clockFace is DateTimePicker's time of day: a field to type it in, level with
// the calendar's month, over a face whose hands both drag. Time runs on
// through 12: the minute hand past 12 carries the hour, the hour hand past
// 12 turns the morning into the afternoon. The minute hand snaps to 5
// minutes; on the face ←→ step 5 minutes, ↑↓ an hour.
func clockFace(c *ui.Context, at *time.Time, o DateTimeOpts) {
	t, th := theme.Of(c), c.Theme()
	cell := th.Space(9)
	off := o.Untimed != nil && *o.Untimed
	h, m := at.Hour(), at.Minute()
	mins := h*60 + m
	put := func(v int) {
		v = (v%1440 + 1440) % 1440
		y, mo, d := at.Date()
		*at = time.Date(y, mo, d, v/60, v%60, 0, 0, at.Location())
	}
	ui.Column(c).AlignItems(ui.Center).Gap(th.Space(4)).Shrink(0).Children(func() {
		ui.Row(c).Height(cell).AlignItems(ui.Center).Children(func() {
			hh := th.Space(8)
			ui.TimeInput(c, at).Label(o.Time).Height(hh).Radius(controlRadius(c, hh)).Background(t.Background).Border(theme.Stroke, t.Input).Disabled(off)
		})
		size := cell * 6
		face := ui.Box(c).Size(size, size).Shrink(0).Focusable().Disabled(off).Role(ui.RoleSlider).Label(o.Time).
			Range(0, 1439, float64(mins)).Value(fmt.Sprintf("%d:%02d", h, m))
		grab := ui.Local(face, clockKey{}, func() byte { return 0 })
		switch {
		case face.Shortcut(0, ui.KeyRight):
			put(mins + 5)
		case face.Shortcut(0, ui.KeyLeft):
			put(mins - 5)
		case face.Shortcut(0, ui.KeyUp):
			put(mins + 60)
		case face.Shortcut(0, ui.KeyDown):
			put(mins - 60)
		}
		if x, y, _ := face.PointerPosition(); face.Pressed() {
			// The pointer in hundredths of the face, and its turn round the centre.
			x, y = x/size*100, y/size*100
			a := math.Atan2(float64(x-50), float64(50-y))
			turn := math.Mod(a/(2*math.Pi)+1, 1)
			if *grab == 0 { // the hand whose tip is nearer; a press elsewhere moves the minutes
				mx, my := facePoint(float64(m)/60, faceMinute)
				hx, hy := facePoint((float64(h%12)+float64(m)/60)/12, faceHour)
				*grab = 'm'
				if math.Hypot(float64(x)-hx, float64(y)-hy) < math.Hypot(float64(x)-mx, float64(y)-my) {
					*grab = 'h'
				}
			}
			// The nearest way round from the hand's place to the pointer, so the time runs on through 12.
			wrap := func(d, n int) int { return ((d+n/2)%n+n)%n - n/2 }
			if *grab == 'm' {
				put(mins + wrap(int(math.Round(turn*12))%12*5-m, 60))
			} else {
				put(mins + wrap(int(math.Round(turn*12-float64(m)/60))-h%12, 12)*60)
			}
			h, m = at.Hour(), at.Minute()
		} else {
			*grab = 0
		}
		face.Cursor(ui.CursorGrab)
		if *grab != 0 {
			face.Cursor(ui.CursorGrabbing)
		}
		pm, held := h >= 12, *grab
		face.Draw(func(p *ui.Painter, r ui.Rect) {
			s := r.W / 100
			at := func(turn, rr float64) [2]float32 {
				x, y := facePoint(turn, rr)
				return [2]float32{r.X + float32(x)*s, r.Y + float32(y)*s}
			}
			p.Fill(r, t.Raised, r.W/2)
			if pm {
				p.Fill(r, tint(t, 1), r.W/2)
			}
			p.Stroke(r, t.Border, r.W/2, theme.Stroke)
			for i := range 60 {
				w, end, col := float32(1), float64(faceTickMinor), t.Muted
				if i%5 == 0 {
					w, end, col = 2, faceTickMajor, t.Foreground
				}
				p.StrokePolyline([][2]float32{at(float64(i)/60, faceTickOut), at(float64(i)/60, end)}, w, col)
			}
			hw, mw := float32(faceHourW), float32(faceMinuteW)
			if held == 'h' {
				hw = faceHourDragged
			}
			if held == 'm' {
				mw = faceMinuteDragged
			}
			centre := at(0, 0)
			p.StrokePolyline([][2]float32{centre, at((float64(h%12)+float64(m)/60)/12, faceHour)}, hw*s, t.Foreground)
			p.StrokePolyline([][2]float32{centre, at(float64(m)/60, faceMinute)}, mw*s, t.Foreground)
			hub := float32(faceHub) * s
			p.Fill(ui.Rect{X: centre[0] - hub, Y: centre[1] - hub, W: 2 * hub, H: 2 * hub}, t.Foreground, hub)
		})
		face.Children(func() {
			// The numerals in the interface's text, placed in percent of the face.
			for _, n := range []int{12, 3, 6, 9} {
				x, y := facePoint(float64(n)/12, faceNumerals)
				Text(c, strconv.Itoa(n), t.Text.Xs).TextColor(t.Muted).FontFeatures("tnum").Absolute().
					LeftPercent(float32(x)).TopPercent(float32(y)).TranslatePercent(-50, -50).PassThrough().AccessHidden(true)
			}
		})
		if o.Untimed != nil {
			ui.Row(c).AlignItems(ui.Center).Gap(th.Space(2)).Children(func() {
				Switch(c, o.Untimed).Label(o.AnyTime)
				Text(c, o.AnyTime, t.Text.Sm).Role(ui.RoleNone)
			})
		}
	})
}

// facePoint is the point of the face at turn (0 at 12, clockwise) and r from
// the centre, in hundredths of its width from its top left.
func facePoint(turn, r float64) (x, y float64) {
	return 50 + r*math.Sin(turn*2*math.Pi), 50 - r*math.Cos(turn*2*math.Pi)
}

// day0 is t's day, at midnight.
func day0(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// onDay is at on day d, at at's time of day.
func onDay(at, d time.Time) time.Time {
	y, m, dd := d.Date()
	return time.Date(y, m, dd, at.Hour(), at.Minute(), at.Second(), at.Nanosecond(), at.Location())
}

// calendar is DateTimePicker's month: the title between buttons to the
// months around, the weekdays, and six weeks of days. The arrows move the
// day chosen while it has the focus.
func calendar(c *ui.Context, at *time.Time, o DateTimeOpts) {
	t, th := theme.Of(c), c.Theme()
	cell := th.Space(9)
	// The words in the interface language; the week from the region, or the
	// language's when the region is unknown, as mygo's own calendar does.
	locale, region := o.locale(c), c.Region()
	if region.HourCycle != 12 && region.HourCycle != 24 {
		region = i18n.RegionFor(locale)
	}
	first := region.FirstWeekday
	min := time.Time{}
	if !o.Min.IsZero() {
		min = day0(o.Min.In(at.Location()))
	}
	choose := func(d time.Time) {
		if d = day0(d); !d.Before(min) {
			*at = onDay(*at, d)
		}
	}
	grid := ui.Column(c).Width(th.Space(7 * 9)).Shrink(0).Focusable().Role(ui.RoleTable).Label(o.Date) // seven days wide
	st := ui.Local(grid, pickerKey{}, func() pickerMonth { return pickerMonth{} })
	if !st.followed.Equal(day0(*at)) { // the month follows the day the app or the keys set
		st.followed = day0(*at)
		st.shown = time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, at.Location())
	}
	month := st.shown
	switch {
	case grid.Shortcut(0, ui.KeyLeft):
		choose(at.AddDate(0, 0, -1))
	case grid.Shortcut(0, ui.KeyRight):
		choose(at.AddDate(0, 0, 1))
	case grid.Shortcut(0, ui.KeyUp):
		choose(at.AddDate(0, 0, -7))
	case grid.Shortcut(0, ui.KeyDown):
		choose(at.AddDate(0, 0, 7))
	}
	title := i18n.FormatDate(locale, month, i18n.DateOptions{Skeleton: "yMMMM"})
	grid.Children(func() {
		ui.Row(c).Height(cell).AlignItems(ui.Center).Children(func() {
			if IconButton(c, "chevron-left", o.PrevMonth, ButtonOpts{Variant: Ghost, Size: SizeLg}).Clicked() {
				st.shown = month.AddDate(0, -1, 0)
			}
			Text(c, title, t.Text.Sm).FontWeight(500).Grow(1).TextAlign(ui.Center)
			if IconButton(c, "chevron-right", o.NextMonth, ButtonOpts{Variant: Ghost, Size: SizeLg}).Clicked() {
				st.shown = month.AddDate(0, 1, 0)
			}
		})
		names := i18n.Weekdays(locale, i18n.Short, true)
		ui.Row(c).Height(th.Space(7)).Children(func() {
			for i := range 7 {
				Text(c, names[(int(first)+i)%7], t.Text.Xs).Width(cell).TextAlign(ui.Center).TextColor(t.Muted).Padding(th.Space(1), 0, 0, 0).Role(ui.RoleColumnHeader)
			}
		})
		start := month.AddDate(0, 0, -((int(month.Weekday()) - int(first) + 7) % 7))
		today := day0(c.Now().In(at.Location()))
		for w := range 6 {
			ui.Row(c).Role(ui.RoleRow).Children(func() {
				for d := range 7 {
					day := start.AddDate(0, 0, w*7+d)
					dayCell(c, day, dayLook{
						chosen: day.Equal(day0(*at)), today: day.Equal(today),
						outside: day.Month() != month.Month(), off: day.Before(min),
					}, locale, func() { choose(day) })
				}
			})
		}
	})
}

type dayLook struct{ chosen, today, outside, off bool }

// dayCell is a day of the calendar: a ghost button, the day chosen filled
// with the primary, today in the hover tint, the days of the months around
// muted and those that cannot be chosen faded.
func dayCell(c *ui.Context, day time.Time, l dayLook, locale string, onClick func()) {
	t := theme.Of(c)
	b := button(c, ButtonOpts{Variant: Ghost, Size: SizeLg, Disabled: l.off}, true).FontWeight(400).
		Label(i18n.FormatDate(locale, day, i18n.DateOptions{Skeleton: "yMMMMd"})).Checked(l.chosen)
	fg := t.Foreground
	switch {
	case l.chosen:
		b.Background(t.Primary)
		fg = t.PrimaryText
	case l.today:
		b.Background(tint(t, 1))
	}
	if l.outside && !l.chosen {
		fg = t.Muted
	}
	if l.off {
		fg = t.Muted.Alpha(0.5)
	}
	b.Children(func() {
		Text(c, i18n.FormatNumber(locale, day.Day(), i18n.NumberOptions{}), t.Text.Sm).TextColor(fg).FontFeatures("tnum").Role(ui.RoleNone)
	})
	if b.Clicked() {
		onClick()
	}
}
