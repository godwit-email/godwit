package kit

import (
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
)

var pickerLabels = DateTimeOpts{Date: "Date", Time: "Time", PrevMonth: "Previous month", NextMonth: "Next month"}

// picker draws a DateTimePicker of *at with o, in English.
func picker(at *time.Time, o DateTimeOpts) *ui.Tester {
	tt := ui.NewTester(themed(func(c *ui.Context) {
		ui.Column(c).Fill().Padding(16).Children(func() { DateTimePicker(c, at, o) })
	}), 640, 480)
	tt.SetLocale("en")
	tt.Frame()
	return tt
}

// The calendar shows the month of the time chosen; its buttons go to the
// months around, a click on a day chooses it keeping the time of day, and the
// days before Min cannot be chosen.
func TestDateTimeCalendar(t *testing.T) {
	at := time.Date(2026, 11, 15, 16, 0, 0, 0, time.Local)
	o := pickerLabels
	o.Min = time.Date(2026, 11, 15, 9, 30, 0, 0, time.Local)
	tt := picker(&at, o)
	if !tt.HasText("November 2026") {
		t.Fatalf("no month title in %q", tt.Texts())
	}
	if d, ok := tt.Access("November 14, 2026"); !ok || !d.Disabled {
		t.Errorf("the day before Min: %+v, want it disabled", d)
	}
	_ = tt.Click("November 14, 2026")
	if want := time.Date(2026, 11, 15, 16, 0, 0, 0, time.Local); !at.Equal(want) {
		t.Errorf("a click on a disabled day chose %v", at)
	}
	if err := tt.Click("November 20, 2026"); err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 11, 20, 16, 0, 0, 0, time.Local); !at.Equal(want) {
		t.Errorf("chose %v, want %v", at, want)
	}
	if d, _ := tt.Access("November 20, 2026"); !d.Checked {
		t.Errorf("the day chosen is not checked: %+v", d)
	}
	if err := tt.Click("Next month"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("December 2026") {
		t.Fatalf("next month shows %q", tt.Texts())
	}
	if err := tt.Click("December 3, 2026"); err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 12, 3, 16, 0, 0, 0, time.Local); !at.Equal(want) {
		t.Errorf("chose %v, want %v", at, want)
	}
	_ = tt.Click("Previous month")
	_ = tt.Click("Previous month")
	if !tt.HasText("October 2026") {
		t.Errorf("two months back shows %q", tt.Texts())
	}
}

// picker24 is picker in a region of 24-hour times.
func picker24(at *time.Time, o DateTimeOpts) *ui.Tester {
	tt := picker(at, o)
	tt.SetRegion(i18n.Region{Locale: "en-GB", FirstWeekday: time.Monday, HourCycle: 24})
	tt.Frame()
	return tt
}

func center(r ui.Rect) (x, y float32) { return r.X + r.W/2, r.Y + r.H/2 }

// A time typed in the field sets the time of *at, keeping its day, and the
// face reads it.
func TestDateTimeTypeTime(t *testing.T) {
	at := time.Date(2026, 11, 15, 16, 0, 0, 0, time.Local)
	tt := picker24(&at, pickerLabels)
	var hours ui.AccessInfo
	for _, a := range tt.AccessAll() {
		if a.Role == ui.RoleStepper {
			hours = a
			break
		}
	}
	if hours.Role != ui.RoleStepper {
		t.Fatal("no time field")
	}
	tt.ClickAt(center(hours.Bounds))
	for _, k := range []ui.Key{ui.Key0, ui.Key9, ui.Key3, ui.Key0} {
		tt.Key(0, k)
	}
	if want := time.Date(2026, 11, 15, 9, 30, 0, 0, time.Local); !at.Equal(want) {
		t.Errorf("typed 09 30, got %v, want %v", at, want)
	}
	if face, _ := tt.AccessByRole(ui.RoleSlider, "Time"); face.Value != "9:30" {
		t.Errorf("the face reads %q, want 9:30", face.Value)
	}
}

// faceAt is the point of face at turn (0 at 12, clockwise) and r, in
// hundredths of the face's width, from its centre.
func faceAt(face ui.Rect, turn, r float64) (x, y float32) {
	cx, cy := center(face)
	s := float64(face.W) / 100
	return cx + float32(r*s*math.Sin(turn*2*math.Pi)), cy - float32(r*s*math.Cos(turn*2*math.Pi))
}

// drag presses the face at turn, r and moves round through turns.
func drag(tt *ui.Tester, face ui.Rect, r float64, turns ...float64) {
	x, y := faceAt(face, turns[0], r)
	tt.Press(x, y)
	tt.Frame()
	for _, turn := range turns[1:] {
		x, y = faceAt(face, turn, r)
		tt.Move(x, y)
		tt.Frame()
	}
	tt.Release(x, y)
	tt.Frame()
}

// Both hands drag, and the time runs on through 12: the minute hand past 12
// carries the hour, the hour hand back past 12 turns the afternoon into the
// morning.
func TestDateTimeDragHands(t *testing.T) {
	at := time.Date(2026, 11, 15, 16, 0, 0, 0, time.Local)
	tt := picker24(&at, pickerLabels)
	face, ok := tt.AccessByRole(ui.RoleSlider, "Time")
	if !ok {
		t.Fatal("no clock face")
	}
	drag(tt, face.Bounds, faceMinute, 0, 0.25)
	if at.Hour() != 16 || at.Minute() != 15 {
		t.Errorf("the minute hand to 3: %v, want 16:15", at.Format("15:04"))
	}
	drag(tt, face.Bounds, faceMinute, 0.25, 0.5, 0.75, 0, 0.05)
	if at.Hour() != 17 || at.Minute() != 5 {
		t.Errorf("the minute hand on round past 12: %v, want 17:05", at.Format("15:04"))
	}
	at = time.Date(2026, 11, 15, 16, 0, 0, 0, time.Local)
	tt.Frame()
	drag(tt, face.Bounds, faceHour, 4.0/12, 2.0/12, 0, 10.0/12)
	if at.Hour() != 10 || at.Minute() != 0 || at.Day() != 15 {
		t.Errorf("the hour hand back past 12 to 10: %v, want 15th 10:00", at.Format("2 15:04"))
	}
	// ←→ step 5 minutes, ↑↓ an hour, round the day.
	tt.Key(0, ui.KeyUp)
	tt.Key(0, ui.KeyRight)
	if at.Hour() != 11 || at.Minute() != 5 {
		t.Errorf("↑ → on the face: %v, want 11:05", at.Format("15:04"))
	}
}

// With the calendar focused the arrows move the day chosen, a week up and down.
func TestDateTimeCalendarKeys(t *testing.T) {
	at := time.Date(2026, 11, 15, 16, 0, 0, 0, time.Local)
	tt := picker(&at, pickerLabels)
	if err := tt.Click("November 15, 2026"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyRight)
	tt.Key(0, ui.KeyDown)
	if want := time.Date(2026, 11, 23, 16, 0, 0, 0, time.Local); !at.Equal(want) {
		t.Errorf("→ then ↓ chose %v, want %v", at, want)
	}
}

// The readout writes the day and time in the user's language, today and
// tomorrow named before them.
func TestReadout(t *testing.T) {
	now := time.Date(2026, 11, 15, 15, 30, 0, 0, time.Local)
	for _, k := range []struct {
		locale string
		at     time.Time
		want   string
	}{
		{"zh-Hans", time.Date(2026, 11, 15, 16, 0, 0, 0, time.Local), "今天 · 11月15日周日 16:00"},
		{"zh-Hans", time.Date(2026, 11, 16, 17, 5, 0, 0, time.Local), "明天 · 11月16日周一 17:05"},
		{"zh-Hans", time.Date(2026, 11, 17, 8, 17, 0, 0, time.Local), "11月17日周二 8:17"},
		{"en", time.Date(2026, 11, 16, 17, 5, 0, 0, time.Local), "Tomorrow · Mon, November 16 at 5:05 PM"},
	} {
		if got := Readout(k.locale, k.at, now); got != k.want {
			t.Errorf("%s %v: %q, want %q", k.locale, k.at, got, k.want)
		}
	}
}

// dialog draws a DateTimeDialog of *at, open, which refuses a time not after
// now, in English.
func dialog(at *time.Time, open *bool, now time.Time, confirmed *int) *ui.Tester {
	o := DateTimeDialogOpts{
		DateTimeOpts: pickerLabels, Title: "Send later", Note: "Only a running Godwit sends.",
		Confirm: "Schedule", Cancel: "Cancel", Close: "Close",
		Check: func(at time.Time) string {
			if !at.After(now) {
				return "Pick a time in the future."
			}
			return ""
		},
	}
	tt := ui.NewTester(themed(func(c *ui.Context) {
		DateTimeDialog(c, open, at, o, func() { *confirmed++ })
	}), 800, 640)
	tt.SetLocale("en")
	tt.SetRegion(i18n.Region{Locale: "en-GB", FirstWeekday: time.Monday, HourCycle: 24})
	tt.Advance(time.Second)
	return tt
}

// The dialog reads the time chosen out under its title; a time Check
// refuses is not confirmed, the reason read out in its place until the
// time changes; Schedule then confirms it and closes the dialog.
func TestDateTimeDialogConfirms(t *testing.T) {
	now := time.Date(2026, 11, 15, 15, 30, 0, 0, time.Local)
	at, open, confirmed := time.Date(2026, 11, 15, 15, 0, 0, 0, time.Local), true, 0
	tt := dialog(&at, &open, now, &confirmed)
	if !tt.HasText("Send later") || !tt.HasText("Only a running Godwit sends.") {
		t.Fatalf("texts %q", tt.Texts())
	}
	if err := tt.Click("Schedule"); err != nil {
		t.Fatal(err)
	}
	if confirmed != 0 || !open || !tt.HasText("Pick a time in the future.") {
		t.Fatalf("a past time: confirmed %d, open %v, texts %q", confirmed, open, tt.Texts())
	}
	face, _ := tt.AccessByRole(ui.RoleSlider, "Time")
	tt.ClickAt(faceAt(face.Bounds, 0, faceMinute)) // on the minute hand, where it is
	tt.Key(0, ui.KeyUp)
	if tt.HasText("Pick a time in the future.") {
		t.Error("the refusal stays after the time changed")
	}
	if err := tt.Click("Schedule"); err != nil {
		t.Fatal(err)
	}
	if confirmed != 1 || open {
		t.Errorf("confirmed %d, open %v at %v, want 1 and closed; texts %q", confirmed, open, at, tt.Texts())
	}
}

// ⌘↩ confirms wherever the focus is; Escape closes without confirming.
func TestDateTimeDialogKeys(t *testing.T) {
	now := time.Date(2026, 11, 15, 15, 30, 0, 0, time.Local)
	at, open, confirmed := time.Date(2026, 11, 16, 9, 0, 0, 0, time.Local), true, 0
	tt := dialog(&at, &open, now, &confirmed)
	if err := tt.Click("November 17, 2026"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyEnter)
	if confirmed != 1 || open || at.Day() != 17 {
		t.Errorf("⌘↩: confirmed %d, open %v, at %v", confirmed, open, at)
	}
	open = true
	tt.Advance(time.Second)
	tt.Key(0, ui.KeyEscape)
	if confirmed != 1 || open {
		t.Errorf("Escape: confirmed %d, open %v, want closed unconfirmed", confirmed, open)
	}
}

// A time that may be left out: the switch leaves it out, and the clock is
// then disabled.
func TestDateTimeUntimed(t *testing.T) {
	at := time.Date(2026, 11, 15, 16, 0, 0, 0, time.Local)
	untimed := false
	o := pickerLabels
	o.Untimed, o.AnyTime = &untimed, "Any time"
	tt := picker24(&at, o)
	if face, _ := tt.AccessByRole(ui.RoleSlider, "Time"); face.Disabled {
		t.Fatal("the clock is disabled with a time")
	}
	if err := tt.Click("Any time"); err != nil {
		t.Fatal(err)
	}
	if !untimed {
		t.Fatal("the switch left no time out")
	}
	if face, _ := tt.AccessByRole(ui.RoleSlider, "Time"); !face.Disabled || face.Value != "" {
		t.Errorf("the clock without a time: %+v", face)
	}
	// the field shows no time, only where one goes
	if _, hours := tt.AccessByRole(ui.RoleStepper, ""); !tt.HasText("--:--") || hours {
		t.Errorf("the field without a time: %q", tt.Texts())
	}
	if err := tt.Click("Any time"); err != nil {
		t.Fatal(err)
	}
	if face, _ := tt.AccessByRole(ui.RoleSlider, "Time"); untimed || face.Disabled || face.Value != "16:00" || tt.HasText("--:--") {
		t.Errorf("the time not back: %+v, %q", face, tt.Texts())
	}
}
