package core

import (
	"strings"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/internal/testclock"
	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ctxs(c string) []string { return []string{c} }

func TestTimeNote(t *testing.T) {
	zone := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 10, 21, 22, 20, 0, 0, zone)
	note := func(at time.Time) string { return timeNote(nil, at) }

	first := note(now)
	assert.True(t, strings.HasPrefix(first, timeNotePrefix), first)
	assert.Contains(t, first, "Wednesday")

	cases := []struct {
		name  string
		state []string
		at    time.Time
		fresh bool
	}{
		{"recent note holds", ctxs(note(now)), now.Add(10 * time.Minute), false},
		{"old note", ctxs(note(now)), now.Add(timeNoteGap), true},
		{"next day", ctxs(note(now.Add(90 * time.Minute))), now.Add(105 * time.Minute), true},
		{"after quoted mail", ctxs(note(now) + "\n\nThe user quoted these emails"), now.Add(time.Minute), false},
		{"offset changed", ctxs(note(now)), now.Add(10 * time.Minute).In(time.FixedZone("JST", 9*3600)), true},
		{"clock set back", ctxs(note(now)), now.Add(-time.Hour), true},
		{"quote only", ctxs("The user quoted these emails"), now, true},
	}
	for _, c := range cases {
		assert.Equal(t, c.fresh, timeNote(c.state, c.at) != "", c.name)
	}
}

// TestAskTimeNote: the first question carries the time, hidden from the
// conversation; a question right after it does not repeat it.
func TestAskTimeNote(t *testing.T) {
	e := newSkillEnv(t)
	conv := e.askConv(t)
	e.srv.Script(testllm.Turn{Text: []string{"Hi."}}, testllm.Turn{Text: []string{"Again."}})
	e.ask(t, conv.ID, "first")
	e.ask(t, conv.ID, "second")

	reqs := e.srv.Requests()
	require.Len(t, reqs, 2)
	assert.Contains(t, string(reqs[0].System), "Mail dates are in UTC")
	assert.Equal(t, 1, strings.Count(promptOf(t, reqs[0]), timeNotePrefix))
	assert.Equal(t, 1, strings.Count(promptOf(t, reqs[1]), timeNotePrefix), "the first note still holds")

	c, err := e.s.GetConversation(t.Context(), conv.ID)
	require.NoError(t, err)
	assert.Equal(t, "first", c.Turns[0].Question, "the note never shows")
}

// TestAskTimeNoteZone: the note gives the time in the zone the UI sent, not
// in time.Local; an unknown zone falls back to time.Local.
func TestAskTimeNoteZone(t *testing.T) {
	e := newSkillEnv(t)
	e.s.clock = testclock.New(time.Date(2026, 10, 21, 14, 20, 0, 0, time.UTC))
	ask := func(zone string) string {
		conv := e.askConv(t)
		e.srv.Script(testllm.Turn{Text: []string{"Hi."}})
		ch, err := e.s.Ask(WithTimeZone(t.Context(), zone), conv.ID, "now?")
		require.NoError(t, err)
		drainWit(t, ch)
		reqs := e.srv.Requests()
		return promptOf(t, reqs[len(reqs)-1])
	}
	assert.Contains(t, ask("Asia/Tokyo"), timeNotePrefix+"2026-10-21 23:20 +09:00 (Wednesday, JST)")
	assert.Contains(t, ask("America/New_York"), timeNotePrefix+"2026-10-21 10:20 -04:00 (Wednesday, EDT)")
	local := time.Date(2026, 10, 21, 14, 20, 0, 0, time.UTC).In(time.Local).Format(timeNoteLayout)
	assert.Contains(t, ask("Not/AZone"), timeNotePrefix+local)
}
