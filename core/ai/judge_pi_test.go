package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// judgeBoth asks the same questions of a Genkit-path and a pi judge whose
// service replies status and body, and returns each one's request body,
// answers and error.
func judgeBoth(t *testing.T, f JudgeFormat, key string, status int, body string) (reqs [2]string, got [2][]Answer, errs [2]error) {
	t.Helper()
	for i, newJ := range []func(*http.Client, JudgeFormat, string, string, string) (Judge, error){NewJudge, NewPiJudge} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			var v any
			json.Unmarshal(b, &v)
			b, _ = json.Marshal(v)
			reqs[i] = r.Header.Get("Authorization") + " " + r.URL.Path + " " + string(b)
			w.WriteHeader(status)
			io.WriteString(w, body)
		}))
		j, err := newJ(nil, f, srv.URL+"/v1", key, "m")
		if err != nil {
			t.Fatal(err)
		}
		got[i], errs[i] = j.Ask(context.Background(), "the mail", testQs)
		srv.Close()
	}
	return
}

// The pi judge sends what the Genkit path sends and reads every reply, good
// or bad, into the same answers and probabilities, or the same kind of error.
func TestPiJudgeMatchesGenkit(t *testing.T) {
	scored := func(f JudgeFormat) string {
		if f == FormatDecisions {
			return `{"answers":[{"type":"predicate","name":"urgent","probability":0.31},` +
				`{"type":"choice","name":"topic","choice":"support","confidence":0.8,"probabilities":[{"value":"support","probability":0.7},{"value":"billing","probability":0.3}]},` +
				`{"type":"score","name":"tone","score":1.4,"confidence":0.6,"probabilities":[{"value":2,"label":"furious","probability":0.2},{"value":0,"label":"calm","probability":0.4},{"value":1,"label":"annoyed","probability":0.4}]}]}`
		}
		return `{"answers":{"urgent":{"type":"noul","noul":0.31},` +
			`"topic":{"type":"choice","choice":"support","confidence":0.8,"probabilities":{"billing":0.3,"support":0.7}},` +
			`"tone":{"type":"score","score":1.4,"confidence":0.6,"probabilities":{"2":0.2,"0":0.4,"1":0.4}}}}`
	}
	for _, f := range []JudgeFormat{FormatDecisions, FormatSystemOne} {
		body := map[JudgeFormat]func([]wa) string{FormatDecisions: decisionsBody, FormatSystemOne: systemOneBody}[f]
		cases := map[string]string{
			"valid":               body(okAnswers),
			"probabilities":       scored(f),
			"refusal":             body(with(1, wa{"topic", "refusal", nil})),
			"label out of set":    body(with(1, wa{"topic", "choice", "sales"})),
			"probability above 1": body(with(0, wa{"urgent", "yes", 1.2})),
			"score a hair above":  body(with(2, wa{"tone", "score", 2.0000000001})),
			"score past slack":    body(with(2, wa{"tone", "score", 2.00001})),
			"missing answer":      body(okAnswers[:2]),
			"extra name":          body(append(append([]wa(nil), okAnswers...), wa{"other", "yes", 0.5})),
			"wrong type":          body(with(0, wa{"urgent", "choice", "billing"})),
			"truncated":           `{"answers":[{"name":"urgent",`,
			"oversize":            strings.Repeat(" ", maxJudgeBody) + body(okAnswers),
		}
		if f == FormatDecisions {
			cases["missing probability"] = strings.Replace(scored(f), `"probability":0.3}`, `}`, 1)
			cases["bool choice"] = strings.Replace(body(okAnswers), `"choice":"billing"`, `"choice":true`, 1)
		}
		for name, b := range cases {
			reqs, got, errs := judgeBoth(t, f, "sk-k", http.StatusOK, b)
			if reqs[0] != reqs[1] {
				t.Errorf("%s/%s: requests differ\ngenkit %s\npi     %s", f, name, reqs[0], reqs[1])
			}
			g, _ := json.Marshal(got[0])
			p, _ := json.Marshal(got[1])
			if string(g) != string(p) || (errs[0] == nil) != (errs[1] == nil) || errors.Is(errs[0], ErrJudgeAnswer) != errors.Is(errs[1], ErrJudgeAnswer) {
				t.Errorf("%s/%s:\ngenkit %s %v\npi     %s %v", f, name, g, errs[0], p, errs[1])
			}
		}
		// A keyless judge (a plugin's) sends no Authorization on either.
		if reqs, _, _ := judgeBoth(t, f, "", http.StatusOK, body(okAnswers)); reqs[0] != reqs[1] || !strings.HasPrefix(reqs[1], " /v1/") {
			t.Errorf("%s keyless: %q, %q", f, reqs[0], reqs[1])
		}
		for _, st := range []int{401, 403, 422} {
			_, _, errs := judgeBoth(t, f, "sk-secret", st, `{"error":"body-secret"}`)
			if errs[0] == nil || errs[0].Error() != errs[1].Error() {
				t.Errorf("%s %d: genkit %v, pi %v", f, st, errs[0], errs[1])
			}
		}
	}
}

// A description-only level keeps its empty label on pi, as the SDK sends it.
func TestPiDecisionsSendsEmptyLevelLabels(t *testing.T) {
	qs := []Question{{Name: "tone", Instructions: "How angry?", Type: Score, Levels: []Level{{"", "calm"}, {"", "furious"}}}}
	var reqs [2]string
	for i, newJ := range []func(*http.Client, JudgeFormat, string, string, string) (Judge, error){NewJudge, NewPiJudge} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			var v any
			json.Unmarshal(b, &v)
			b, _ = json.Marshal(v)
			reqs[i] = string(b)
			io.WriteString(w, decisionsBody([]wa{{"tone", "score", 1.0}}))
		}))
		j, _ := newJ(nil, FormatDecisions, srv.URL, "k", "m")
		if _, err := j.Ask(context.Background(), "mail", qs); err != nil {
			t.Fatal(err)
		}
		srv.Close()
	}
	if !strings.Contains(reqs[1], `"levels":[{"description":"calm","label":""},{"description":"furious","label":""}]`) || reqs[0] != reqs[1] {
		t.Errorf("genkit %s\npi     %s", reqs[0], reqs[1])
	}
}

func TestPiJudgeTransportFailures(t *testing.T) {
	j, _ := NewPiJudge(nil, FormatSystemOne, "http://127.0.0.1:1", "k", "m")
	if _, err := j.Ask(context.Background(), "mail", testQs); !errors.Is(err, errUnreachable) {
		t.Errorf("unreachable: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := j.Ask(ctx, "mail", testQs); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
}

// pi-ai retries what the Genkit path retries, a long Retry-After cut to its 2s.
func TestPiJudgeRetries(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits++; hits == 1 {
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(429)
			return
		}
		io.WriteString(w, decisionsBody(okAnswers))
	}))
	defer srv.Close()
	j, _ := NewPiJudge(nil, FormatDecisions, srv.URL, "k", "m")
	start := time.Now()
	if _, err := j.Ask(context.Background(), "mail", testQs); err != nil || hits != 2 {
		t.Fatalf("hits %d: %v", hits, err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("waited %v", d)
	}
	for _, st := range []int{529, 503} {
		hits = 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; w.WriteHeader(st) }))
		j, _ := NewPiJudge(nil, FormatSystemOne, srv.URL, "k", "m")
		_, err := j.Ask(context.Background(), "mail", testQs)
		var he *HTTPError
		if hits != judgeAttempts || !errors.As(err, &he) || he.Status != st {
			t.Errorf("%d: hits %d, %v", st, hits, err)
		}
		srv.Close()
	}
}
