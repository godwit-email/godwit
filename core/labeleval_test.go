package core

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"github.com/godwit-email/godwit/core/ai"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestLabelEvalLive scores the auto-label judges against the cases in
// GODWIT_LIVE_LABEL_CASES (default testdata/labeleval, whose README has the
// format). Each judge runs only with its key set, and keys are never logged:
//
//	GODWIT_LIVE_OPENAI_KEY=… GODWIT_LIVE_TYPESAFE_KEY=… GODWIT_LIVE_GEN_MODEL=… \
//	  go test ./core -run TestLabelEvalLive -v -timeout 1h
func TestLabelEvalLive(t *testing.T) {
	openai, typesafe, model := os.Getenv("GODWIT_LIVE_OPENAI_KEY"), os.Getenv("GODWIT_LIVE_TYPESAFE_KEY"), os.Getenv("GODWIT_LIVE_GEN_MODEL")
	var ways []evalWay
	var skipped []string
	judge := func(name, env, key, provider, base, format, judgeModel string) {
		if key == "" {
			skipped = append(skipped, name+": "+env+" not set")
			return
		}
		ways = append(ways, evalWay{name: name, setup: func(t *testing.T, s *Service) {
			ms, err := s.AddModelService(bg, ModelServiceInput{Name: name, Provider: provider, BaseURL: base, APIKey: key, JudgeFormat: format, JudgeModel: judgeModel})
			require.NoError(t, err)
			setEvalSettings(t, s, func(st *Settings) { st.JudgeService = ms.Slug })
			requireEvalJudge(t, s, false)
		}})
	}
	judge("openai-decisions", "GODWIT_LIVE_OPENAI_KEY", openai, ProviderOpenAI, "https://api.openai.com/v1", string(ai.FormatDecisions), "gpt-6-luna")
	judge("typesafe-systemone", "GODWIT_LIVE_TYPESAFE_KEY", typesafe, ProviderTypeSafe, "https://api.typesafe.ai/v1", string(ai.FormatSystemOne), "jev-latest")
	if openai == "" || model == "" {
		skipped = append(skipped, "generation model: GODWIT_LIVE_OPENAI_KEY and GODWIT_LIVE_GEN_MODEL not both set")
	} else {
		ways = append(ways, evalWay{name: "gen-" + model, gen: true, setup: func(t *testing.T, s *Service) {
			ms, err := s.AddModelService(bg, ModelServiceInput{Name: "OpenAI", Provider: ProviderOpenAI, Kind: ai.KindOpenAI,
				BaseURL: "https://api.openai.com/v1", APIKey: openai, Models: []string{model}})
			require.NoError(t, err)
			// No judge service: the default model answers (the small one, once the classifier asks it).
			setEvalSettings(t, s, func(st *Settings) { st.DefaultModel, st.SmallModel = ms.Slug+"/"+model, ms.Slug+"/"+model })
			requireEvalJudge(t, s, true)
		}})
	}
	if len(ways) == 0 {
		t.Skip("no GODWIT_LIVE_* key set")
	}
	suite, err := loadEvalSuite(cmp.Or(os.Getenv("GODWIT_LIVE_LABEL_CASES"), "testdata/labeleval"))
	require.NoError(t, err, "no model was called")
	t.Log("\n" + evalReport(runLabelEval(t, suite, ways), skipped))
}

func setEvalSettings(t *testing.T, s *Service, set func(*Settings)) {
	t.Helper()
	st, err := s.GetSettings(bg)
	require.NoError(t, err)
	set(&st)
	_, err = s.SetSettings(bg, st)
	require.NoError(t, err)
}

// requireEvalJudge checks the settings pick the way's kind of judge.
func requireEvalJudge(t *testing.T, s *Service, gen bool) {
	t.Helper()
	_, _, got, err := s.resolveJudge(bg)
	require.NoError(t, err)
	require.Equal(t, gen, got, "a generation model answers")
}

func TestLabelEvalSamples(t *testing.T) {
	suite, err := loadEvalSuite("testdata/labeleval")
	require.NoError(t, err)
	langs := map[string]bool{}
	seen := map[string]map[string]bool{}
	negation := false
	for _, c := range suite.cases {
		langs[c.Lang] = true
		negation = negation || c.Set == "negation"
		for slug, e := range c.Expect {
			if seen[slug] == nil {
				seen[slug] = map[string]bool{}
			}
			seen[slug][e] = true
		}
	}
	require.Equal(t, map[string]bool{"zh": true, "en": true, "ja": true}, langs)
	for _, p := range presetLabels {
		require.True(t, seen[p.slug]["yes"], "a case where %s is yes", p.slug)
		require.True(t, seen[p.slug]["no"], "a case where %s is no", p.slug)
	}
	require.True(t, negation, "cases for the negation set")
}

// evalExpect is a full expectation with the given labels changed.
func evalExpect(set map[string]string) map[string]string {
	e := map[string]string{}
	for _, p := range presetLabels {
		e[p.slug] = "no"
	}
	maps.Copy(e, set)
	return e
}

func writeEvalCases(t *testing.T, dir, file string, cases ...any) {
	t.Helper()
	var b strings.Builder
	for _, c := range cases {
		line, err := json.Marshal(c)
		require.NoError(t, err)
		b.Write(line)
		b.WriteByte('\n')
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, file), []byte(b.String()), 0o600))
}

func TestLabelEvalRejectsBadCases(t *testing.T) {
	dir := t.TempDir()
	m := map[string]any{"from": "a@example.com", "subject": "s", "text": "t"}
	missing := evalExpect(nil)
	delete(missing, "bill")
	writeEvalCases(t, dir, "a.jsonl",
		map[string]any{"id": "ok", "lang": "en", "mail": m, "expect": evalExpect(nil)},
		map[string]any{"id": "missing", "lang": "en", "mail": m, "expect": missing},
		map[string]any{"id": "unknown", "lang": "en", "mail": m, "expect": evalExpect(map[string]string{"recipts": "yes"})},
		map[string]any{"id": "value", "lang": "en", "mail": m, "expect": evalExpect(map[string]string{"otp": "maybe"})},
	)
	writeEvalCases(t, dir, "b.jsonl",
		map[string]any{"id": "ok", "lang": "en", "mail": m, "expect": evalExpect(nil)},
		map[string]any{"id": "lang", "lang": "fr", "mail": m, "expect": evalExpect(nil)},
		map[string]any{"id": "set", "lang": "en", "set": "nope", "mail": m, "expect": evalExpect(nil)},
		map[string]any{"id": "both", "lang": "en", "mail": m, "raw": "Subject: x\n\nx", "expect": evalExpect(nil)},
		map[string]any{"id": "typo", "lang": "en", "mial": m, "expect": evalExpect(nil)},
	)
	_, err := loadEvalSuite(dir)
	require.Error(t, err)
	for _, want := range []string{
		"a.jsonl:2 missing: no expectation for bill",
		`a.jsonl:3 unknown: unknown label "recipts"`,
		`a.jsonl:4 value: otp is "maybe"`,
		"b.jsonl:1 ok: id also used at a.jsonl:1 ok",
		`b.jsonl:2 lang: lang "fr"`,
		`b.jsonl:3 set: unknown label set "nope"`,
		"b.jsonl:4 both: give one of mail and raw",
		`b.jsonl:5: json: unknown field "mial"`,
	} {
		require.ErrorContains(t, err, want)
	}

	// Label sets UpdateLabel would refuse, mid-run, are refused up front.
	dir = t.TempDir()
	writeEvalCases(t, dir, "a.jsonl", map[string]any{"id": "ok", "lang": "en", "mail": m, "expect": evalExpect(nil)})
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bad.labelset.json"), []byte(`{"labels": {
		"en": {"otp": {"name": " ", "description": "x"}, "bill": {"name": "orders", "description": "x"}, "nope": {"name": "Nope"}},
		"fr": {}}}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "empty.labelset.json"), []byte(`{"labels": {}}`), 0o600))
	_, err = loadEvalSuite(dir)
	for _, want := range []string{
		"bad.labelset.json: en: otp: a name of 1 to 32 characters",
		`bad.labelset.json: en: bill: name "orders" is also order's`,
		"bad.labelset.json: en: nope: unknown label",
		`bad.labelset.json: unknown preset language "fr"`,
		"empty.labelset.json: no preset language",
	} {
		require.ErrorContains(t, err, want)
	}
}

// judgeFunc is an ai.Judge answering with f, safe for parallel runs.
type judgeFunc struct {
	mu     sync.Mutex
	f      func(state string, qs []ai.Question) ([]ai.Answer, error)
	calls  int
	states []string
	qs     [][]ai.Question
}

func (j *judgeFunc) Ask(_ context.Context, state string, qs []ai.Question) ([]ai.Answer, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.calls++
	j.states = append(j.states, state)
	j.qs = append(j.qs, qs)
	return j.f(state, qs)
}

func fakeEvalWay(name string, gen bool, j ai.Judge) evalWay {
	return evalWay{name: name, gen: gen, setup: func(t *testing.T, s *Service) {
		s.labelJudge = func(context.Context) (ai.Judge, error) { return j, nil }
	}}
}

// byCase answers by the case named in the mail's subject ("case-<name>"):
// probabilities by slug (missing is 0), "refused" for a refusal, or a nil map
// for no valid answer to the whole mail.
func byCase(answers map[string]map[string]any) func(string, []ai.Question) ([]ai.Answer, error) {
	return func(state string, qs []ai.Question) ([]ai.Answer, error) {
		var ps map[string]any
		for name, a := range answers {
			if strings.Contains(state, "case-"+name+"\n") {
				ps = a
				if a == nil {
					return nil, fmt.Errorf("%w: no JSON", ai.ErrJudgeAnswer)
				}
			}
		}
		out := make([]ai.Answer, len(qs))
		for i, q := range qs {
			if ps[q.Name] == "refused" {
				out[i] = ai.Answer{Type: ai.Refusal, Name: q.Name}
				continue
			}
			p, _ := ps[q.Name].(float64)
			out[i] = ai.Answer{Type: ai.Predicate, Name: q.Name, Probability: &p}
		}
		return out, nil
	}
}

func evalMailCase(id string, expect map[string]string) map[string]any {
	return map[string]any{"id": id, "lang": "en", "expect": evalExpect(expect),
		"mail": map[string]any{"from": "Example <a@example.com>", "to": []string{"me@example.com"}, "subject": "case-" + id, "text": "body of " + id}}
}

func findRun(t *testing.T, runs []evalRun, way, preset, set string) evalRun {
	t.Helper()
	for _, r := range runs {
		if r.way.name == way && r.preset == preset && r.set == set {
			require.NoError(t, r.err)
			return r
		}
	}
	t.Fatalf("no run %s/%s/%s", way, preset, set)
	return evalRun{}
}

func findLine(t *testing.T, lines []evalLine, lang string, thr float64, label string) evalLine {
	t.Helper()
	for _, l := range lines {
		if l.lang == lang && l.threshold == thr && l.label == label {
			return l
		}
	}
	t.Fatalf("no line %q %v %q", lang, thr, label)
	return evalLine{}
}

func TestLabelEvalScores(t *testing.T) {
	dir := t.TempDir()
	writeEvalCases(t, dir, "a.jsonl",
		evalMailCase("hit", map[string]string{"otp": "yes"}),
		evalMailCase("either", map[string]string{"otp": "either"}),
		evalMailCase("refuse", map[string]string{"bill": "yes"}),
		evalMailCase("unans", map[string]string{"order": "yes"}),
	)
	suite, err := loadEvalSuite(dir)
	require.NoError(t, err)
	answers := byCase(map[string]map[string]any{
		"hit":    {"otp": 0.8, "bill": 0.7999},
		"either": {"otp": 0.9},
		"refuse": {"bill": "refused"},
		"unans":  nil,
	})
	j := &judgeFunc{f: answers}
	runs := runLabelEval(t, suite, []evalWay{fakeEvalWay("api", false, j), fakeEvalWay("gen", true, &judgeFunc{f: answers})})
	require.Len(t, runs, 6, "two ways, three preset languages")
	require.Equal(t, 12, j.calls, "one call a mail")

	r := findRun(t, runs, "api", "en", "")
	require.Equal(t, 4, r.calls)
	require.Equal(t, 1, r.refused)
	require.Equal(t, 1, r.unanswered)
	require.Equal(t, 1, r.either)
	lines := r.score()
	require.Len(t, lines, 10*(1+len(presetLabels))+10*1, "0.50 to 0.95, each label and all; all again by mail language")

	otp := findLine(t, lines, "", 0.8, "otp")
	require.Equal(t, evalTally{tp: 1, tn: 2}, otp.evalTally, "at the threshold is yes; either is not scored")
	require.Equal(t, 3, otp.n)
	require.Nil(t, otp.ci, "too few cases for an interval")
	require.Equal(t, evalTally{fn: 1, tn: 2}, findLine(t, lines, "", 0.85, "otp").evalTally)
	require.Equal(t, evalTally{fn: 1, tn: 3}, findLine(t, lines, "", 0.8, "bill").evalTally, "just under is no; a refusal is no")
	require.Equal(t, evalTally{fn: 1, tn: 3}, findLine(t, lines, "", 0.8, "order").evalTally, "no answer to the mail is no")
	require.Equal(t, evalTally{tp: 1, fn: 2, tn: 24}, findLine(t, lines, "", 0.8, "").evalTally)
	require.Equal(t, evalTally{tp: 1, fn: 2, tn: 24}, findLine(t, lines, "en", 0.8, "").evalTally)

	gen := findRun(t, runs, "gen", "en", "").score()
	require.Len(t, gen, 1+len(presetLabels)+1, "a generation model's answer is 0 or 1: one point")
	require.Equal(t, 0.5, gen[0].threshold)
}

func TestLabelEvalSets(t *testing.T) {
	dir := t.TempDir()
	negated := evalMailCase("negated", nil)
	negated["set"] = "negation"
	writeEvalCases(t, dir, "a.jsonl", evalMailCase("plain", nil), negated)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "reworded.labelset.json"), []byte(`{"paraphrase": true,
		"labels": {"en": {"otp": {"name": "Codes", "description": "Reworded codes"}}}}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "negation.labelset.json"), []byte(`{
		"labels": {"en": {"bill": {"name": "Bills", "description": "Bills, but not renewals"}}}}`), 0o600))
	suite, err := loadEvalSuite(dir)
	require.NoError(t, err)
	j := &judgeFunc{f: byCase(nil)}
	runs := runLabelEval(t, suite, []evalWay{fakeEvalWay("api", false, j)})

	var got []string
	for _, r := range runs {
		require.NoError(t, r.err)
		var ids []string
		for _, c := range r.cases {
			ids = append(ids, c.ID)
		}
		got = append(got, r.preset+"/"+r.set+": "+strings.Join(ids, " "))
	}
	require.Equal(t, []string{"zh/: plain", "en/: plain", "en/negation: negated", "en/reworded: plain", "ja/: plain"}, got,
		"a paraphrase runs the cases with no set, another set only its own; a set runs where it has the language")

	asked := func(id, slug, text string) bool {
		for i, st := range j.states {
			for _, q := range j.qs[i] {
				if strings.Contains(st, "case-"+id+"\n") && q.Name == slug && strings.Contains(q.Instructions, text) {
					return true
				}
			}
		}
		return false
	}
	require.True(t, asked("plain", "otp", "Reworded codes"), "the slug stays, the question has the set's description")
	require.True(t, asked("plain", "otp", "One-time codes"), "and the presets' under no set")
	require.True(t, asked("negated", "bill", "Bills, but not renewals"))
	require.False(t, asked("negated", "bill", "receipts"), "a set's own cases run only under it")
}

func TestLabelEvalReport(t *testing.T) {
	suite, err := loadEvalSuite("testdata/labeleval")
	require.NoError(t, err)
	// Yes to every label, so every no case is a false yes and is listed.
	j := &judgeFunc{f: func(_ string, qs []ai.Question) ([]ai.Answer, error) {
		out := make([]ai.Answer, len(qs))
		for i, q := range qs {
			p := 0.9
			out[i] = ai.Answer{Type: ai.Predicate, Name: q.Name, Probability: &p}
		}
		return out, nil
	}}
	runs := runLabelEval(t, suite, []evalWay{fakeEvalWay("api", false, j), fakeEvalWay("gen", true, j)})
	report := evalReport(runs, []string{"typesafe-systemone: GODWIT_LIVE_TYPESAFE_KEY not set"})
	t.Log(report)

	for _, c := range suite.cases {
		if c.Mail != nil {
			require.NotContains(t, report, c.Mail.Subject, "no mail content")
		}
	}
	require.NotContains(t, report, "Your sign-in code")
	require.NotContains(t, report, "482913")
	require.Contains(t, report, "typesafe-systemone: GODWIT_LIVE_TYPESAFE_KEY not set")
	require.Contains(t, report, "too few cases")
	require.Contains(t, report, "en-personal-dinner 0.90", "a false yes by case and probability")
	require.Contains(t, report, "one point")
}

func TestLabelEvalInterval(t *testing.T) {
	r := evalRun{way: evalWay{name: "api"}, got: map[string]evalGot{}}
	for i := range 100 {
		id := fmt.Sprint(i)
		r.cases = append(r.cases, evalCase{ID: id, Lang: "en", Expect: evalExpect(map[string]string{"otp": "yes"})})
		r.got[id] = evalGot{p: map[string]float64{"otp": float64(i%4) / 4}} // 0.75 a quarter of the time
	}
	l := findLine(t, r.score(), "", 0.75, "otp")
	require.Equal(t, evalTally{tp: 25, fn: 75}, l.evalTally)
	require.Len(t, l.ci, 4)
	require.Equal(t, []float64{1, 1}, l.ci[:2], "every yes is right")
	require.Less(t, l.ci[2], 0.25)
	require.Greater(t, l.ci[3], 0.25)
	require.Greater(t, l.ci[2], 0.1)
	require.Less(t, l.ci[3], 0.4)
}
