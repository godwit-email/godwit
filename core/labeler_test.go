package core

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/require"
)

// fakeJudge answers by question name: p (missing is 0), refused, or err for
// the whole call. during runs inside the first call, before it answers.
type fakeJudge struct {
	mu      sync.Mutex
	p       map[string]float64
	refused map[string]bool
	err     error
	during  func()
	calls   int
	states  []string
	qs      [][]ai.Question
}

func (j *fakeJudge) Ask(_ context.Context, state string, qs []ai.Question) ([]ai.Answer, error) {
	j.mu.Lock()
	j.calls++
	first := j.calls == 1
	j.states = append(j.states, state)
	j.qs = append(j.qs, qs)
	during, err := j.during, j.err
	j.mu.Unlock()
	if first && during != nil {
		during()
	}
	if err != nil {
		return nil, err
	}
	out := make([]ai.Answer, len(qs))
	for i, q := range qs {
		out[i] = ai.Answer{Name: q.Name, Probability: j.p[q.Name], Refused: j.refused[q.Name]}
	}
	return out, nil
}

func (j *fakeJudge) count() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.calls
}

func useLabelJudge(s *Service, j *fakeJudge) {
	s.labelJudge = func(context.Context) (ai.Judge, error) { return j, nil }
}

func labeledAt(t *testing.T, s *Service, id int64) any {
	t.Helper()
	rows, err := s.db.Query(t.Context(), "SELECT labeled_at FROM messages WHERE id = ?", id)
	require.NoError(t, err)
	return rows.Values[0][0]
}

func setLabeledAt(t *testing.T, s *Service, id int64, v any) {
	t.Helper()
	_, err := s.db.Execute(t.Context(), "UPDATE messages SET labeled_at = ? WHERE id = ?", v, id)
	require.NoError(t, err)
}

func addSent(t *testing.T, s *Service) {
	t.Helper()
	_, err := s.db.Execute(t.Context(), "INSERT OR IGNORE INTO folders(id, account_id, name, role) VALUES (2, 1, 'Sent', 'sent')")
	require.NoError(t, err)
}

func qNames(qs []ai.Question) []string {
	var out []string
	for _, q := range qs {
		out = append(out, q.Name)
	}
	return out
}

func TestClassifyAppliesAuto(t *testing.T) {
	s := newLabelService(t, "", "zh-CN")
	l := labelIDs(t, s)
	j := &fakeJudge{p: map[string]float64{"otp": 0.5, "bill": 0.4999}}
	useLabelJudge(s, j)
	a := addMessage(t, s, "登录验证码", "Code Host <noreply@x>", "验证码 482913", "", 1)
	n, err := s.classifyBatch(t.Context())
	require.NoError(t, err)
	require.Equal(t, 1, n)
	got, err := s.MessageLabels(t.Context(), []int64{a})
	require.NoError(t, err)
	require.Equal(t, []AppliedLabel{{LabelID: l["otp"], Auto: true}}, got[a], "0.5 is yes, just under is no")
	require.Greater(t, store.Int(labeledAt(t, s, a)), int64(0))
	require.Equal(t, []string{"otp", "bill", "order", "travel", "calendar", "notice", "newsletter"}, qNames(j.qs[0]))
	require.Equal(t, ai.YesNo, j.qs[0][0].Kind)
	require.Contains(t, j.qs[0][0].Instructions, "验证码")
	require.Contains(t, j.qs[0][0].Instructions, "一次性验证码")
	require.Contains(t, j.states[0], "<untrusted_email")
}

func TestClassifyDropsStaleAuto(t *testing.T) {
	s := newLabelService(t, "", "zh-CN")
	l := labelIDs(t, s)
	useLabelJudge(s, &fakeJudge{p: map[string]float64{"bill": 0.1}})
	a := addMessage(t, s, "a", "a@x", "", "", 1)
	setRow(t, s, a, l["bill"], "auto", 1)
	setLabeledAt(t, s, a, 0)
	_, err := s.classifyBatch(t.Context())
	require.NoError(t, err)
	_, on := labelRow(t, s, a, l["bill"])
	require.Equal(t, -1, on)
}

func TestClassifySkipsManual(t *testing.T) {
	s := newLabelService(t, "", "zh-CN")
	l := labelIDs(t, s)
	j := &fakeJudge{p: map[string]float64{"otp": 0.9, "bill": 0.1}}
	useLabelJudge(s, j)
	a := addMessage(t, s, "a", "a@x", "", "", 1)
	setRow(t, s, a, l["otp"], "manual", 0)
	setRow(t, s, a, l["bill"], "manual", 1)
	_, err := s.classifyBatch(t.Context())
	require.NoError(t, err)
	require.NotContains(t, qNames(j.qs[0]), "otp")
	require.NotContains(t, qNames(j.qs[0]), "bill")
	src, on := labelRow(t, s, a, l["otp"])
	require.Equal(t, "manual", src)
	require.Equal(t, 0, on)
	src, on = labelRow(t, s, a, l["bill"])
	require.Equal(t, "manual", src)
	require.Equal(t, 1, on)
}

func TestClassifyOnlyInbox(t *testing.T) {
	s := newLabelService(t, "", "zh-CN")
	j := &fakeJudge{}
	useLabelJudge(s, j)
	addSent(t, s)
	a := addMessage(t, s, "a", "a@x", "", "", 1)
	_, err := s.db.Execute(t.Context(), "UPDATE messages SET folder_id = 2 WHERE id = ?", a)
	require.NoError(t, err)
	n, err := s.classifyBatch(t.Context())
	require.NoError(t, err)
	require.Equal(t, 0, n)
	require.Equal(t, 0, j.count())
}

func TestClassifyRefusedIsNo(t *testing.T) {
	s := newLabelService(t, "", "zh-CN")
	l := labelIDs(t, s)
	useLabelJudge(s, &fakeJudge{p: map[string]float64{"otp": 0.9, "bill": 0.8}, refused: map[string]bool{"otp": true}})
	a := addMessage(t, s, "a", "a@x", "", "", 1)
	_, err := s.classifyBatch(t.Context())
	require.NoError(t, err)
	got, err := s.MessageLabels(t.Context(), []int64{a})
	require.NoError(t, err)
	require.Equal(t, []AppliedLabel{{LabelID: l["bill"], Auto: true}}, got[a])
	require.Greater(t, store.Int(labeledAt(t, s, a)), int64(0))
}

func TestClassifyErrorKeepsPending(t *testing.T) {
	s := newLabelService(t, "", "zh-CN")
	useLabelJudge(s, &fakeJudge{err: errors.New("boom"), p: map[string]float64{"otp": 1}})
	a := addMessage(t, s, "a", "a@x", "", "", 1)
	_, err := s.classifyBatch(t.Context())
	require.Error(t, err)
	require.Nil(t, labeledAt(t, s, a))
	got, err := s.MessageLabels(t.Context(), []int64{a})
	require.NoError(t, err)
	require.Empty(t, got[a])
}

func TestClassifyNoModel(t *testing.T) {
	s := newLabelService(t, "", "zh-CN")
	a := addMessage(t, s, "a", "a@x", "", "", 1)
	_, err := s.classifyBatch(t.Context())
	require.ErrorIs(t, err, errNoLabelModel)
	require.Nil(t, labeledAt(t, s, a))
}

func TestClassifyAutoOff(t *testing.T) {
	s := newLabelService(t, "", "zh-CN")
	j := &fakeJudge{}
	useLabelJudge(s, j)
	st, err := s.GetSettings(t.Context())
	require.NoError(t, err)
	require.True(t, st.AutoLabel, "on by default")
	st.AutoLabel = false
	_, err = s.SetSettings(t.Context(), st)
	require.NoError(t, err)
	a := addMessage(t, s, "a", "a@x", "", "", 1)
	b := addMessage(t, s, "b", "b@x", "", "", 2)
	setLabeledAt(t, s, b, 0)
	n, err := s.classifyBatch(t.Context())
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Equal(t, 1, j.count())
	require.Nil(t, labeledAt(t, s, a), "new mail waits while auto-labeling is off")
	require.Greater(t, store.Int(labeledAt(t, s, b)), int64(0), "a rescan target runs anyway")
}

func TestClassifyVanishedRows(t *testing.T) {
	s := newLabelService(t, "", "zh-CN")
	l := labelIDs(t, s)
	a := addMessage(t, s, "a", "a@x", "", "", 1)
	b := addMessage(t, s, "b", "b@x", "", "", 2) // newer: asked first
	j := &fakeJudge{p: map[string]float64{"otp": 0.9, "bill": 0.9}}
	j.during = func() {
		require.NoError(t, s.DeleteLabel(context.Background(), l["otp"]))
		_, err := s.db.Execute(context.Background(), "DELETE FROM messages WHERE id = ?", b)
		require.NoError(t, err)
	}
	useLabelJudge(s, j)
	_, err := s.classifyBatch(t.Context())
	require.NoError(t, err)
	got, err := s.MessageLabels(t.Context(), []int64{a})
	require.NoError(t, err)
	require.Equal(t, []AppliedLabel{{LabelID: l["bill"], Auto: true}}, got[a])
}

func TestClassifyKeepsConcurrentManual(t *testing.T) {
	s := newLabelService(t, "", "zh-CN")
	otp := labelIDs(t, s)["otp"]
	a := addMessage(t, s, "a", "a@x", "", "", 1)
	j := &fakeJudge{p: map[string]float64{"otp": 0.9}}
	j.during = func() { require.NoError(t, s.SetLabel(context.Background(), []int64{a}, otp, false)) }
	useLabelJudge(s, j)
	_, err := s.classifyBatch(t.Context())
	require.NoError(t, err)
	src, on := labelRow(t, s, a, otp)
	require.Equal(t, "manual", src)
	require.Equal(t, 0, on)
}

func TestClassifyNoLabels(t *testing.T) {
	s := newLabelService(t, "", "zh-CN")
	j := &fakeJudge{}
	useLabelJudge(s, j)
	_, err := s.db.Execute(t.Context(), "DELETE FROM labels")
	require.NoError(t, err)
	a := addMessage(t, s, "a", "a@x", "", "", 1)
	_, err = s.classifyBatch(t.Context())
	require.NoError(t, err)
	require.Equal(t, 0, j.count())
	require.Greater(t, store.Int(labeledAt(t, s, a)), int64(0))
}

func TestClassifyBodyText(t *testing.T) {
	s := newLabelService(t, "", "zh-CN")
	j := &fakeJudge{}
	useLabelJudge(s, j)
	addMessage(t, s, "a", "a@x", "", "<p>你的验证码是 123456</p>", 2)
	addMessage(t, s, "b", "b@x", strings.Repeat("字", 5000), "", 1)
	_, err := s.classifyBatch(t.Context())
	require.NoError(t, err)
	require.Len(t, j.states, 2)
	require.Contains(t, j.states[0], "你的验证码是 123456", "an HTML-only body is read as text")
	require.Equal(t, 4000, strings.Count(j.states[1], "字"))
}

func TestClassifyDiscardsStale(t *testing.T) {
	for name, c := range map[string]struct {
		start  any
		change string
		want   func(t *testing.T, v any)
	}{
		"stopped":   {0, "UPDATE messages SET labeled_at = unixepoch() WHERE id = ?", func(t *testing.T, v any) { require.Greater(t, store.Int(v), int64(0)) }},
		"moved":     {nil, "UPDATE messages SET folder_id = 2 WHERE id = ?", func(t *testing.T, v any) { require.Nil(t, v) }},
		"rescanned": {nil, "UPDATE messages SET labeled_at = 5 WHERE id = ?", func(t *testing.T, v any) { require.EqualValues(t, 5, v) }},
	} {
		t.Run(name, func(t *testing.T) {
			s := newLabelService(t, "", "zh-CN")
			addSent(t, s)
			otp := labelIDs(t, s)["otp"]
			a := addMessage(t, s, "a", "a@x", "", "", 1)
			setLabeledAt(t, s, a, c.start)
			j := &fakeJudge{p: map[string]float64{"otp": 0.9}}
			j.during = func() {
				_, err := s.db.Execute(context.Background(), c.change, a)
				require.NoError(t, err)
			}
			useLabelJudge(s, j)
			_, err := s.classifyBatch(t.Context())
			require.NoError(t, err)
			_, on := labelRow(t, s, a, otp)
			require.Equal(t, -1, on, "the stale answer is dropped")
			c.want(t, labeledAt(t, s, a))
		})
	}
}

// timerClock holds every timer until fire is called with its duration.
type timerClock struct {
	mu   sync.Mutex
	held map[time.Duration][]func()
}

func (c *timerClock) AfterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.held == nil {
		c.held = map[time.Duration][]func(){}
	}
	c.held[d] = append(c.held[d], f)
	return func() bool { return false }
}

func (c *timerClock) fire(d time.Duration) {
	c.mu.Lock()
	fs := c.held[d]
	delete(c.held, d)
	c.mu.Unlock()
	for _, f := range fs {
		f()
	}
}

func (c *timerClock) waiting(d time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.held[d]) > 0
}

func TestLabelerBackoffIgnoresWake(t *testing.T) {
	s := newLabelService(t, "", "zh-CN")
	clock := &timerClock{}
	s.hookClock = clock
	j := &fakeJudge{err: errors.New("boom")}
	useLabelJudge(s, j)
	addMessage(t, s, "a", "a@x", "", "", 1)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { s.runLabeler(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	require.Eventually(t, func() bool { return clock.waiting(hookBackoff(1)) }, 5*time.Second, 5*time.Millisecond)
	for range 3 {
		s.wakeLabeler()
	}
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, 1, j.count(), "a sync's wake-up does not cut the backoff short")
	clock.fire(hookBackoff(1))
	require.Eventually(t, func() bool { return j.count() == 2 }, 5*time.Second, 5*time.Millisecond)
}
