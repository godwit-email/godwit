package core

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// usePluginJudge installs the fake native plugin with a judge model "j" (its
// assets, if any) and the library spec extra, and makes it the judge.
func usePluginJudge(t *testing.T, s *Service, assets map[string]string, extra map[string]any) string {
	t.Helper()
	j := map[string]any{"id": "j", "name": "J", "transport": "plugin", "operation": "judge"}
	if assets != nil {
		j["assets"] = assets
	}
	lib := map[string]any{"models": map[string]any{"models": []any{j}}}
	for k, v := range extra {
		lib[k] = v
	}
	record := useNativeModels(t, s, nil, lib)
	st, err := s.GetSettings(t.Context())
	require.NoError(t, err)
	st.JudgeService = "plugin/nm/j"
	_, err = s.SetSettings(t.Context(), st)
	require.NoError(t, err)
	return record
}

// recorded are the requests the fake library got for method, one JSON line each.
func recorded(t *testing.T, record, method string) []string {
	t.Helper()
	b, err := os.ReadFile(record + "." + method)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

var twoQuestions = []ai.Question{{Name: "a", Type: ai.Predicate, Instructions: "a?"}, {Name: "b", Type: ai.Predicate, Instructions: "b?"}}

func TestPluginJudgeAnswers(t *testing.T) {
	s := newMailContextService(t)
	record := usePluginJudge(t, s, nil, nil)
	j, label, gen, err := s.resolveJudge(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "plugin/nm/j", label)
	assert.False(t, gen)
	got, err := j.Ask(t.Context(), "mail", twoQuestions)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "b", got[1].Name)
	assert.InDelta(t, 0.9, *got[1].Probability, 1e-9)
	calls := recorded(t, record, "models.judge")
	require.Len(t, calls, 2, "one question per call")
	assert.JSONEq(t, `{"modelId":"j","input":"mail","questions":[{"name":"a","type":"predicate","instructions":"a?"}]}`, calls[0])
	assert.Nil(t, recorded(t, record, "models.load"), "a model without assets is not loaded")
}

func TestPluginJudgeQueuedCancel(t *testing.T) {
	s := newMailContextService(t)
	record := usePluginJudge(t, s, nil, nil)
	j, _, _, err := s.resolveJudge(t.Context())
	require.NoError(t, err)
	rp := j.(pluginModelJudge).rp
	rp.judging <- struct{}{} // another call holds the slot
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err = j.Ask(ctx, "mail", twoQuestions)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Nil(t, recorded(t, record, "models.judge"), "a queued call that gave up is never dispatched")
	<-rp.judging
	_, err = j.Ask(t.Context(), "mail", twoQuestions)
	assert.NoError(t, err)
}

func TestPluginJudgeWrongAnswerCounts(t *testing.T) {
	s := newMailContextService(t)
	usePluginJudge(t, s, nil, map[string]any{"judgeWrong": true})
	j, _, _, err := s.resolveJudge(t.Context())
	require.NoError(t, err)
	_, err = j.Ask(t.Context(), "mail", twoQuestions)
	assert.ErrorIs(t, err, ai.ErrJudgePlugin)
	assert.False(t, unanswerable(err), "a broken plugin stops the round")
	assert.Equal(t, int64(1), pluginFailures(t, s, "nm"))
}

func TestPluginJudgeInClassifier(t *testing.T) {
	for code, moveOn := range map[string]bool{"model_not_ready": false, "invalid_request": true} {
		t.Run(code, func(t *testing.T) {
			s := newLabelService(t, "", "en")
			usePluginJudge(t, s, nil, map[string]any{"judgeError": code})
			a := addMessage(t, s, "a", "a@x", "hello", "", 1)
			n, err := s.classifyBatch(t.Context())
			if moveOn {
				require.NoError(t, err)
				assert.Equal(t, 1, n)
				assert.NotNil(t, labeledAt(t, s, a), "done with the labels it has")
			} else {
				assert.ErrorIs(t, err, ai.ErrJudgeUnavailable)
				assert.Nil(t, labeledAt(t, s, a), "still pending")
			}
			assert.Equal(t, int64(0), pluginFailures(t, s, "nm"), "the plugin's own answer is no failure")
		})
	}
}

// fakeAssets verifies every model's files as dirs, counting leases.
type fakeAssets struct {
	noModelAssets
	verified, released int
}

func (f *fakeAssets) Verify(context.Context, string, string, map[string]string) (map[string]string, func(), error) {
	f.verified++
	return map[string]string{"weights": "/models/4b"}, func() { f.released++ }, nil
}

func TestPluginJudgeAssets(t *testing.T) {
	s := newMailContextService(t)
	record := usePluginJudge(t, s, map[string]string{"weights": "4b"}, nil)
	j, _, _, err := s.resolveJudge(t.Context())
	require.NoError(t, err)
	_, err = j.Ask(t.Context(), "mail", twoQuestions)
	assert.ErrorIs(t, err, ai.ErrJudgeUnavailable, "noModelAssets: update_required")
	assert.Nil(t, recorded(t, record, "models.judge"))

	fa := &fakeAssets{}
	s.assets = fa
	_, err = j.Ask(t.Context(), "mail", twoQuestions)
	require.NoError(t, err)
	assert.Equal(t, 1, fa.verified, "verified once per process")
	loads := recorded(t, record, "models.load")
	require.Len(t, loads, 1)
	assert.JSONEq(t, `{"modelId":"j","assets":{"weights":"/models/4b"}}`, loads[0])

	require.NoError(t, s.SetPluginEnabled(t.Context(), "nm", false))
	assert.Equal(t, 1, fa.released, "stopping the plugin releases its files")
}

func TestPluginJudgeCancelKeepsSlot(t *testing.T) {
	s := newMailContextService(t)
	record := usePluginJudge(t, s, nil, map[string]any{"judgeSleepMs": 300})
	j, _, _, err := s.resolveJudge(t.Context())
	require.NoError(t, err)
	_, err = j.Ask(t.Context(), "mail", twoQuestions[:1]) // the process is up
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err = j.Ask(ctx, "mail", twoQuestions[:1])
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	rp := j.(pluginModelJudge).rp
	select {
	case rp.judging <- struct{}{}:
		t.Fatal("the slot was free while the dispatched call still ran")
	default:
	}
	assert.Eventually(t, func() bool { return len(recorded(t, record, "models.judge")) == 2 }, 2*time.Second, 10*time.Millisecond)
	_, err = j.Ask(t.Context(), "mail", twoQuestions[:1])
	assert.NoError(t, err, "the slot comes back once the call ends")
}

func TestPluginJudgeReloadsAfterRestart(t *testing.T) {
	s := newMailContextService(t)
	record := usePluginJudge(t, s, map[string]string{"weights": "4b"}, nil)
	fa := &fakeAssets{}
	s.assets = fa
	j, _, _, err := s.resolveJudge(t.Context())
	require.NoError(t, err)
	_, err = j.Ask(t.Context(), "mail", twoQuestions[:1])
	require.NoError(t, err)
	rp := j.(pluginModelJudge).rp
	_, err = rp.native.Call(t.Context(), "probe.crash", struct{}{})
	require.Error(t, err)
	_, err = j.Ask(t.Context(), "mail", twoQuestions[:1])
	require.NoError(t, err)
	assert.Len(t, recorded(t, record, "models.load"), 2, "the new process loads the model again")
	assert.Equal(t, 2, fa.verified)
	assert.Equal(t, 1, fa.released, "the old process's lease is released")
}

func TestPluginJudgeReloadsOtherAssets(t *testing.T) {
	s := newMailContextService(t)
	record := usePluginJudge(t, s, map[string]string{"weights": "4b"}, nil)
	fa := &fakeAssets{}
	s.assets = fa
	j, _, _, err := s.resolveJudge(t.Context())
	require.NoError(t, err)
	_, err = j.Ask(t.Context(), "mail", twoQuestions[:1])
	require.NoError(t, err)
	rp := j.(pluginModelJudge).rp
	m := j.(pluginModelJudge).model
	m.Assets = map[string]string{"weights": "8b"} // as new settings may list it
	rp.models.Store(&modelCatalog{rp.cfgKey(), []plugins.ProviderModel{m}})
	j, _, _, err = s.resolveJudge(t.Context())
	require.NoError(t, err)
	_, err = j.Ask(t.Context(), "mail", twoQuestions[:1])
	require.NoError(t, err)
	assert.Len(t, recorded(t, record, "models.load"), 2, "other files are loaded again")
	assert.Equal(t, 1, fa.released, "the old files' lease goes")
}

func TestPluginJudgeLoadRejected(t *testing.T) {
	s := newMailContextService(t)
	usePluginJudge(t, s, map[string]string{"weights": "4b"}, map[string]any{"loadError": "invalid_request"})
	s.assets = &fakeAssets{}
	j, _, _, err := s.resolveJudge(t.Context())
	require.NoError(t, err)
	_, err = j.Ask(t.Context(), "mail", twoQuestions[:1])
	assert.ErrorIs(t, err, ai.ErrJudgeUnavailable)
	assert.False(t, unanswerable(err), "a model that will not load is not about this message")
}
