package core

import (
	"bytes"
	"log/slog"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ageCache makes every cached output last used daysAgo days ago.
func (e skillEnv) ageCache(t *testing.T, daysAgo int) {
	t.Helper()
	_, err := e.s.db.Execute(t.Context(), "UPDATE ai_cache SET created_at = ?", time.Now().AddDate(0, 0, -daysAgo).Unix())
	require.NoError(t, err)
}

// passDays moves every cached output's last use days further into the past.
func (e skillEnv) passDays(t *testing.T, days int) {
	t.Helper()
	_, err := e.s.db.Execute(t.Context(), "UPDATE ai_cache SET created_at = created_at - ?", days*24*60*60)
	require.NoError(t, err)
}

func TestAICacheExpires(t *testing.T) {
	cases := []struct {
		name   string
		answer func(n int) string // the model's n-th answer
		get    func(t *testing.T, e skillEnv) any
	}{
		{"summarize", func(n int) string { return []string{"old", "new"}[n] }, func(t *testing.T, e skillEnv) any {
			return texts(e.run(t, "summarize", Target{Kind: "message", ID: e.msg}))
		}},
		{"quick replies", func(n int) string { return `{"replies":[{"html":"<p>` + []string{"old", "new"}[n] + `</p>"}]}` }, func(t *testing.T, e skillEnv) any {
			rs, err := e.s.QuickReplies(t.Context(), e.msg)
			require.NoError(t, err)
			return rs
		}},
		{"whole-body translation", func(n int) string { return `{"texts":["` + []string{"旧", "新"}[n] + `"],"source":"en"}` }, func(t *testing.T, e skillEnv) any {
			return e.run(t, "translate", Target{Kind: "message", ID: e.msg, PreserveHTML: true})[1].Translations
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newSkillEnv(t)
			e.srv.Script(testllm.Turn{Text: []string{c.answer(0)}}, testllm.Turn{Text: []string{c.answer(1)}})
			first := c.get(t, e)
			require.Equal(t, 1, e.cacheRows(t))

			e.ageCache(t, 29)
			require.NoError(t, e.s.cleanAICache(t.Context()))
			assert.Equal(t, first, c.get(t, e), "29 days unused is still cached")
			assert.Len(t, e.srv.Requests(), 1)

			e.ageCache(t, 31)
			require.NoError(t, e.s.cleanAICache(t.Context()))
			assert.Zero(t, e.cacheRows(t))
			assert.NotEqual(t, first, c.get(t, e), "generated again")
			assert.Len(t, e.srv.Requests(), 2)
		})
	}
}

func TestAICacheHitKeepsIt(t *testing.T) {
	e := newSkillEnv(t)
	tg := Target{Kind: "message", ID: e.msg}
	e.srv.Script(testllm.Turn{Text: []string{"sum"}})
	e.run(t, "summarize", tg)
	e.ageCache(t, 29)
	assert.Equal(t, []string{"sum"}, texts(e.run(t, "summarize", tg)))
	e.passDays(t, 2)
	require.NoError(t, e.s.cleanAICache(t.Context()))
	assert.Equal(t, []string{"sum"}, texts(e.run(t, "summarize", tg)), "the hit counted as a use")
	assert.Len(t, e.srv.Requests(), 1)
}

func TestAICacheRefreshFailureStillHits(t *testing.T) {
	e := newSkillEnv(t)
	tg := Target{Kind: "message", ID: e.msg}
	e.srv.Script(testllm.Turn{Text: []string{"secret summary"}})
	e.run(t, "summarize", tg)
	_, err := e.s.db.Execute(t.Context(), `CREATE TRIGGER nope BEFORE UPDATE ON ai_cache BEGIN SELECT RAISE(ABORT, 'boom'); END`)
	require.NoError(t, err)
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	assert.Equal(t, []string{"secret summary"}, texts(e.run(t, "summarize", tg)))
	assert.Len(t, e.srv.Requests(), 1)
	assert.Contains(t, buf.String(), "ai cache refresh failed")
	assert.NotContains(t, buf.String(), "secret summary")
}
