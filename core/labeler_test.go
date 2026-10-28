package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/ai/mask"
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
		p := j.p[q.Name]
		out[i] = ai.Answer{Type: ai.Predicate, Name: q.Name, Probability: &p}
		if j.refused[q.Name] {
			out[i] = ai.Answer{Type: ai.Refusal, Name: q.Name}
		}
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
	j := &fakeJudge{p: map[string]float64{"otp": 0.8, "bill": 0.7999}}
	useLabelJudge(s, j)
	a := addMessage(t, s, "登录验证码", "Code Host <noreply@x>", "验证码 482913", "", 1)
	n, err := s.classifyBatch(t.Context())
	require.NoError(t, err)
	require.Equal(t, 1, n)
	got, err := s.MessageLabels(t.Context(), []int64{a})
	require.NoError(t, err)
	require.Equal(t, []AppliedLabel{{LabelID: l["otp"], Auto: true}}, got[a], "0.8 is yes, just under is no")
	require.Greater(t, store.Int(labeledAt(t, s, a)), int64(0))
	require.Equal(t, []string{"otp", "bill", "order", "travel", "calendar", "notice", "newsletter"}, qNames(j.qs[0]))
	require.Equal(t, ai.Predicate, j.qs[0][0].Type)
	require.Contains(t, j.qs[0][0].Instructions, "验证码")
	require.Contains(t, j.qs[0][0].Instructions, "一次性验证码")
	require.Contains(t, j.states[0], "<untrusted_email")
}

func TestClassifyUsesJudgeThreshold(t *testing.T) {
	for _, p := range []float64{0.5, 0.8, 0.95} {
		t.Run(fmt.Sprint(p), func(t *testing.T) {
			s := newLabelService(t, "", "zh-CN")
			st, err := s.GetSettings(t.Context())
			require.NoError(t, err)
			st.JudgeThreshold = p
			_, err = s.SetSettings(t.Context(), st)
			require.NoError(t, err)
			l := labelIDs(t, s)
			useLabelJudge(s, &fakeJudge{p: map[string]float64{"otp": p, "bill": max(0, p-0.0001)}, refused: map[string]bool{"order": true}})
			id := addMessage(t, s, "code", "a@x", "482913", "", 1)
			n, err := s.classifyBatch(t.Context())
			require.NoError(t, err)
			require.Equal(t, 1, n)
			got, err := s.MessageLabels(t.Context(), []int64{id})
			require.NoError(t, err)
			require.Equal(t, []AppliedLabel{{LabelID: l["otp"], Auto: true}}, got[id], "at the threshold is yes, just under is no")
		})
	}
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

// A mail 脱敏 blocks (拦截) is skipped, its labels as they are, and not tried again.
func TestClassifySkipsABlockedMail(t *testing.T) {
	s := newLabelService(t, "", "zh-CN")
	useLabelJudge(s, &fakeJudge{err: fmt.Errorf("judge: %w", &mask.BlockedError{Categories: []string{mask.IDs}})})
	a := addMessage(t, s, "a", "a@x", "", "", 1)
	_, err := s.classifyBatch(t.Context())
	require.NoError(t, err)
	require.Greater(t, store.Int(labeledAt(t, s, a)), int64(0))
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
	clock := &timerClock{}
	testHookClock = clock // before New: the janitor reads it from the start
	t.Cleanup(func() { testHookClock = nil })
	s := newLabelService(t, "", "zh-CN")
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

func TestRescanMissing(t *testing.T) {
	s := newLabelService(t, "", "zh-CN")
	useLabelJudge(s, &fakeJudge{})
	otp := labelIDs(t, s)["otp"]
	now := time.Now().Unix()
	a := addMessage(t, s, "a", "a@x", "", "", now)
	b := addMessage(t, s, "b", "b@x", "", "", now)
	c := addMessage(t, s, "c", "c@x", "", "", now)
	for _, id := range []int64{a, b, c} {
		setLabeledAt(t, s, id, 100)
	}
	setRow(t, s, a, otp, "auto", 1)
	setRow(t, s, b, otp, "manual", 0)
	n, err := s.RescanLabels(t.Context(), 30, false)
	require.NoError(t, err)
	require.Equal(t, 2, n)
	require.EqualValues(t, 100, labeledAt(t, s, a))
	require.EqualValues(t, 0, labeledAt(t, s, b))
	require.EqualValues(t, 0, labeledAt(t, s, c))
}

func TestRescanAll(t *testing.T) {
	s := newLabelService(t, "", "zh-CN")
	useLabelJudge(s, &fakeJudge{})
	l := labelIDs(t, s)
	now := time.Now().Unix()
	a := addMessage(t, s, "a", "a@x", "", "", now)
	addMessage(t, s, "b", "b@x", "", "", now)
	addMessage(t, s, "c", "c@x", "", "", now)
	setRow(t, s, a, l["otp"], "auto", 1)
	setRow(t, s, a, l["bill"], "manual", 1)
	n, err := s.RescanLabels(t.Context(), 30, true)
	require.NoError(t, err)
	require.Equal(t, 3, n)
	_, on := labelRow(t, s, a, l["otp"])
	require.Equal(t, 1, on, "a model label stays until the judge answers again")
	p, err := s.PendingLabels(t.Context())
	require.NoError(t, err)
	require.Equal(t, 3, p)
	_, err = s.classifyBatch(t.Context())
	require.NoError(t, err)
	_, on = labelRow(t, s, a, l["otp"])
	require.Equal(t, -1, on, "the model's labels are judged again")
	src, on := labelRow(t, s, a, l["bill"])
	require.Equal(t, "manual", src)
	require.Equal(t, 1, on)
}

// TestRescanJudgeFailureKeepsLabels: a judge that fails during a rescan, or a
// stop before it gets to a message, leaves that message's model labels on.
func TestRescanJudgeFailureKeepsLabels(t *testing.T) {
	s := newLabelService(t, "", "zh-CN")
	otp := labelIDs(t, s)["otp"]
	now := time.Now().Unix()
	a := addMessage(t, s, "a", "a@x", "", "", now)
	b := addMessage(t, s, "b", "b@x", "", "", now)
	setRow(t, s, a, otp, "auto", 1)
	setRow(t, s, b, otp, "auto", 1)
	useLabelJudge(s, &fakeJudge{err: errors.New("judge down")})
	_, err := s.RescanLabels(t.Context(), 30, true)
	require.NoError(t, err)
	_, err = s.classifyBatch(t.Context())
	require.Error(t, err)
	_, on := labelRow(t, s, a, otp)
	require.Equal(t, 1, on)
	require.NoError(t, s.StopRescan(t.Context()))
	_, on = labelRow(t, s, b, otp)
	require.Equal(t, 1, on)
}

func TestRescanRange(t *testing.T) {
	s := newLabelService(t, "", "zh-CN")
	useLabelJudge(s, &fakeJudge{})
	old := addMessage(t, s, "old", "a@x", "", "", time.Now().AddDate(0, 0, -40).Unix())
	setLabeledAt(t, s, old, 100)
	n, err := s.RescanLabels(t.Context(), 30, true)
	require.NoError(t, err)
	require.Equal(t, 0, n)
	require.EqualValues(t, 100, labeledAt(t, s, old))
	n, err = s.RescanLabels(t.Context(), 0, true)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.EqualValues(t, 0, labeledAt(t, s, old))
}

func TestStopRescan(t *testing.T) {
	s := newLabelService(t, "", "zh-CN")
	useLabelJudge(s, &fakeJudge{})
	now := time.Now().Unix()
	a := addMessage(t, s, "a", "a@x", "", "", now)
	setLabeledAt(t, s, a, 100)
	old := addMessage(t, s, "old", "o@x", "", "", time.Now().AddDate(0, 0, -40).Unix())
	_, err := s.RescanLabels(t.Context(), 30, true)
	require.NoError(t, err)
	fresh := addMessage(t, s, "new", "n@x", "", "", now) // arrives during the rescan
	require.NoError(t, s.StopRescan(t.Context()))
	p, err := s.PendingLabels(t.Context())
	require.NoError(t, err)
	require.Equal(t, 0, p)
	require.Greater(t, store.Int(labeledAt(t, s, a)), int64(100))
	require.Nil(t, labeledAt(t, s, old), "outside the range: untouched")
	require.Nil(t, labeledAt(t, s, fresh), "new mail waits for the classifier, not the stop")
}

// TestClassifyUnanswerableKeepsLabels: a message the model cannot answer for (a reply
// with no valid answer, a request the service rejects) is done with the labels
// it has, so it does not stop every later message.
func TestClassifyUnanswerableKeepsLabels(t *testing.T) {
	for name, e := range map[string]error{"bad answer": ai.ErrJudgeAnswer, "rejected": &ai.HTTPError{Status: 400}} {
		t.Run(name, func(t *testing.T) {
			s := newLabelService(t, "", "zh-CN")
			useLabelJudge(s, &fakeJudge{err: e})
			otp := labelIDs(t, s)["otp"]
			a := addMessage(t, s, "a", "a@x", "", "", 1)
			setRow(t, s, a, otp, "auto", 1) // a rescan target's earlier answer
			n, err := s.classifyBatch(t.Context())
			require.NoError(t, err)
			require.Equal(t, 1, n)
			require.Greater(t, store.Int(labeledAt(t, s, a)), int64(0))
			_, on := labelRow(t, s, a, otp)
			require.Equal(t, 1, on, "no answer is no reason to drop it")
		})
	}
}

// TestRescanNeedsModel: with no model to classify with, a rescan is refused
// before it drops any model labels.
func TestRescanNeedsModel(t *testing.T) {
	s := newLabelService(t, "", "zh-CN")
	otp := labelIDs(t, s)["otp"]
	a := addMessage(t, s, "a", "a@x", "", "", time.Now().Unix())
	setRow(t, s, a, otp, "auto", 1)
	_, err := s.RescanLabels(t.Context(), 30, true)
	require.ErrorIs(t, err, ErrAINotConfigured)
	_, on := labelRow(t, s, a, otp)
	require.Equal(t, 1, on)
	require.False(t, s.LabelModelReady(t.Context()))
	useLabelJudge(s, &fakeJudge{})
	require.True(t, s.LabelModelReady(t.Context()))
}

// TestClassifyUnanswerableAfterLabelEdit: a label edited while an unanswerable
// request is out still sends the message back to be asked again.
func TestClassifyUnanswerableAfterLabelEdit(t *testing.T) {
	s := newLabelService(t, "", "zh-CN")
	otp := labelIDs(t, s)["otp"]
	a := addMessage(t, s, "a", "a@x", "", "", 1)
	setLabeledAt(t, s, a, 0)
	j := &fakeJudge{err: ai.ErrJudgeAnswer}
	j.during = func() {
		_, err := s.db.Execute(context.Background(), "UPDATE labels SET description = 'edited' WHERE id = ?", otp)
		require.NoError(t, err)
	}
	useLabelJudge(s, j)
	_, err := s.classifyBatch(t.Context())
	require.NoError(t, err)
	require.EqualValues(t, 0, labeledAt(t, s, a))
}

// TestClassifyWaitsForPresets: before the presets are seeded (a headless first
// start) nothing is classified, so the seeded labels later apply to that mail.
func TestClassifyWaitsForPresets(t *testing.T) {
	s := newLabelService(t, "", "")
	j := &fakeJudge{}
	useLabelJudge(s, j)
	a := addMessage(t, s, "a", "a@x", "", "", 1)
	n, err := s.classifyBatch(t.Context())
	require.NoError(t, err)
	require.Equal(t, 0, n)
	require.Nil(t, labeledAt(t, s, a))
}

// TestClassifyDropsEditedLabel: an answer to a label the user edited while the
// model answered is dropped, and the message asked again.
func TestClassifyDropsEditedLabel(t *testing.T) {
	s := newLabelService(t, "", "zh-CN")
	otp := labelIDs(t, s)["otp"]
	a := addMessage(t, s, "a", "a@x", "", "", 1)
	j := &fakeJudge{p: map[string]float64{"otp": 0.9}}
	j.during = func() {
		_, err := s.UpdateLabel(context.Background(), otp, LabelInput{Name: "验证码", Description: "只算短信验证码", Color: "red", Icon: "shield-keyhole"})
		require.NoError(t, err)
	}
	useLabelJudge(s, j)
	_, err := s.classifyBatch(t.Context())
	require.NoError(t, err)
	_, on := labelRow(t, s, a, otp)
	require.Equal(t, -1, on)
	require.Nil(t, labeledAt(t, s, a), "asked again with the new description")
}

// TestClassifyDropsWhenLabelAdded: a label created while the model answered
// was never asked about, so the message stays pending.
func TestClassifyDropsWhenLabelAdded(t *testing.T) {
	s := newLabelService(t, "", "zh-CN")
	otp := labelIDs(t, s)["otp"]
	a := addMessage(t, s, "a", "a@x", "", "", 1)
	j := &fakeJudge{p: map[string]float64{"otp": 0.9}}
	j.during = func() {
		_, err := s.CreateLabel(context.Background(), LabelInput{Name: "家人", Color: "pink", Icon: "heart-3"})
		require.NoError(t, err)
	}
	useLabelJudge(s, j)
	_, err := s.classifyBatch(t.Context())
	require.NoError(t, err)
	_, on := labelRow(t, s, a, otp)
	require.Equal(t, -1, on)
	require.Nil(t, labeledAt(t, s, a), "asked again with the new label")
}

// TestClassifyStopsWhenAutoOff: turning auto-labeling off mid-batch stops
// the new mail still queued in it.
func TestClassifyStopsWhenAutoOff(t *testing.T) {
	s := newLabelService(t, "", "zh-CN")
	a := addMessage(t, s, "a", "a@x", "", "", 1)
	b := addMessage(t, s, "b", "b@x", "", "", 2) // newer: asked first
	j := &fakeJudge{}
	j.during = func() {
		st, err := s.GetSettings(context.Background())
		require.NoError(t, err)
		st.AutoLabel = false
		_, err = s.SetSettings(context.Background(), st)
		require.NoError(t, err)
	}
	useLabelJudge(s, j)
	_, err := s.classifyBatch(t.Context())
	require.NoError(t, err)
	require.Equal(t, 1, j.count())
	require.Greater(t, store.Int(labeledAt(t, s, b)), int64(0))
	require.Nil(t, labeledAt(t, s, a))
}

// TestRescanBeforePresets: a rescan asked for before the presets are seeded
// (headless, through the Local API) runs against the labels there are; only
// new mail waits for the presets.
func TestRescanBeforePresets(t *testing.T) {
	s := newLabelService(t, "", "")
	l, err := s.CreateLabel(t.Context(), LabelInput{Name: "Family", Color: "pink", Icon: "heart-3"})
	require.NoError(t, err)
	useLabelJudge(s, &fakeJudge{p: map[string]float64{l.Slug: 0.9}})
	a := addMessage(t, s, "a", "a@x", "", "", time.Now().Unix())
	n, err := s.RescanLabels(t.Context(), 30, false)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	fresh := addMessage(t, s, "new", "n@x", "", "", time.Now().Unix())
	_, err = s.classifyBatch(t.Context())
	require.NoError(t, err)
	p, err := s.PendingLabels(t.Context())
	require.NoError(t, err)
	require.Zero(t, p, "the rescan finished")
	got, err := s.MessageLabels(t.Context(), []int64{a})
	require.NoError(t, err)
	require.Equal(t, []AppliedLabel{{LabelID: l.ID, Auto: true}}, got[a])
	require.Nil(t, labeledAt(t, s, fresh), "new mail still waits for the presets")
}
