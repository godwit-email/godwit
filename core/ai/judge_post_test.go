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

// judgeOnce asks testQs of a judge whose service replies status and body,
// and returns its request (Authorization, path, body) and outcome.
func judgeOnce(t *testing.T, f JudgeFormat, key string, status int, body string) (req string, got []Answer, err error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var v any
		json.Unmarshal(b, &v)
		b, _ = json.Marshal(v)
		req = r.Header.Get("Authorization") + " " + r.URL.Path + " " + string(b)
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	defer srv.Close()
	j, err := NewJudge(nil, nil, f, srv.URL+"/v1", key, "m")
	if err != nil {
		t.Fatal(err)
	}
	got, err = j.Ask(context.Background(), "the mail", testQs)
	return req, got, err
}

// The judge sends what the Genkit path's sent (its SDK, for Decisions) and
// reads every reply, good or bad, into the answers and probabilities, or the
// kind of error, it read them into (recorded from it before it was removed, #236).
func TestJudgeMatchesGenkit(t *testing.T) {
	sent := map[JudgeFormat]string{
		FormatDecisions: `/v1/decisions {"input":"the mail","model":"m","questions":[{"instructions":"Is it urgent?","name":"urgent","type":"predicate"},` +
			`{"choices":[{"description":"money","value":"billing"},{"description":"help","value":"support"}],"instructions":"Topic?","name":"topic","type":"choice"},` +
			`{"instructions":"How angry?","levels":[{"label":"calm"},{"label":"annoyed"},{"label":"furious"}],"name":"tone","type":"score"}]}`,
		FormatSystemOne: `/v1/systemone {"model":"m","questions":{"tone":{"criteria":["calm","annoyed","furious"],"instructions":"How angry?","type":"score"},` +
			`"topic":{"criteria":{"billing":"money","support":"help"},"instructions":"Topic?","type":"choice"},"urgent":{"instructions":"Is it urgent?","type":"noul"}},"state":"the mail"}`,
	}
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
	const okRead = `[{"type":"predicate","name":"urgent","probability":1},{"type":"choice","name":"topic","choice":"billing","probabilities":[{"value":"billing","probability":0.9}]},{"type":"score","name":"tone","score":2}]`
	scoredRead := map[JudgeFormat]string{
		FormatDecisions: `[{"type":"predicate","name":"urgent","probability":0.31},{"type":"choice","name":"topic","choice":"support","probabilities":[{"value":"support","probability":0.7},{"value":"billing","probability":0.3}],"confidence":0.8},` +
			`{"type":"score","name":"tone","score":1.4,"probabilities":[{"value":0,"label":"calm","probability":0.4},{"value":1,"label":"annoyed","probability":0.4},{"value":2,"label":"furious","probability":0.2}],"confidence":0.6}]`,
		FormatSystemOne: `[{"type":"predicate","name":"urgent","probability":0.31},{"type":"choice","name":"topic","choice":"support","probabilities":[{"value":"billing","probability":0.3},{"value":"support","probability":0.7}],"confidence":0.8},` +
			`{"type":"score","name":"tone","score":1.4,"probabilities":[{"value":0,"label":"calm","probability":0.4},{"value":1,"label":"annoyed","probability":0.4},{"value":2,"label":"furious","probability":0.2}],"confidence":0.6}]`,
	}
	for _, f := range []JudgeFormat{FormatDecisions, FormatSystemOne} {
		body := map[JudgeFormat]func([]wa) string{FormatDecisions: decisionsBody, FormatSystemOne: systemOneBody}[f]
		cases := map[string]struct{ body, read string }{ // read "": an ErrJudgeAnswer
			"valid":               {body(okAnswers), okRead},
			"probabilities":       {scored(f), scoredRead[f]},
			"refusal":             {body(with(1, wa{"topic", "refusal", nil})), `[{"type":"predicate","name":"urgent","probability":1},{"type":"refusal","name":"topic"},{"type":"score","name":"tone","score":2}]`},
			"label out of set":    {body(with(1, wa{"topic", "choice", "sales"})), ""},
			"probability above 1": {body(with(0, wa{"urgent", "yes", 1.2})), ""},
			"score a hair above":  {body(with(2, wa{"tone", "score", 2.0000000001})), okRead},
			"score past slack":    {body(with(2, wa{"tone", "score", 2.00001})), ""},
			"missing answer":      {body(okAnswers[:2]), ""},
			"extra name":          {body(append(append([]wa(nil), okAnswers...), wa{"other", "yes", 0.5})), ""},
			"wrong type":          {body(with(0, wa{"urgent", "choice", "billing"})), ""},
			"truncated":           {`{"answers":[{"name":"urgent",`, ""},
			"oversize":            {strings.Repeat(" ", maxJudgeBody) + body(okAnswers), ""},
		}
		if f == FormatDecisions {
			cases["missing probability"] = struct{ body, read string }{strings.Replace(scored(f), `"probability":0.3}`, `}`, 1), ""}
			cases["bool choice"] = struct{ body, read string }{strings.Replace(body(okAnswers), `"choice":"billing"`, `"choice":true`, 1), ""}
		}
		for name, c := range cases {
			req, got, err := judgeOnce(t, f, "sk-k", http.StatusOK, c.body)
			if req != "Bearer sk-k "+sent[f] {
				t.Errorf("%s/%s: sent %s", f, name, req)
			}
			g, _ := json.Marshal(got)
			switch {
			case c.read == "" && !errors.Is(err, ErrJudgeAnswer):
				t.Errorf("%s/%s: %s %v, want an ErrJudgeAnswer", f, name, g, err)
			case c.read != "" && (err != nil || string(g) != c.read):
				t.Errorf("%s/%s: %s %v\nwant %s", f, name, g, err, c.read)
			}
		}
		// A keyless judge (a plugin's) sends no Authorization.
		if req, _, _ := judgeOnce(t, f, "", http.StatusOK, body(okAnswers)); req != " "+sent[f] {
			t.Errorf("%s keyless: %q", f, req)
		}
		for st, want := range map[int]string{401: "the model service rejected the key", 403: "the model service refused this key for that API", 422: "judge: HTTP 422"} {
			if _, _, err := judgeOnce(t, f, "sk-secret", st, `{"error":"body-secret"}`); err == nil || err.Error() != want {
				t.Errorf("%s %d: %v", f, st, err)
			}
		}
	}
}

// A description-only level keeps its empty label, as the SDK sent it.
func TestDecisionsSendsEmptyLevelLabels(t *testing.T) {
	qs := []Question{{Name: "tone", Instructions: "How angry?", Type: Score, Levels: []Level{{"", "calm"}, {"", "furious"}}}}
	var req string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var v any
		json.Unmarshal(b, &v)
		b, _ = json.Marshal(v)
		req = string(b)
		io.WriteString(w, decisionsBody([]wa{{"tone", "score", 1.0}}))
	}))
	defer srv.Close()
	j, _ := NewJudge(nil, nil, FormatDecisions, srv.URL, "k", "m")
	if _, err := j.Ask(context.Background(), "mail", qs); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(req, `"levels":[{"description":"calm","label":""},{"description":"furious","label":""}]`) {
		t.Errorf("sent %s", req)
	}
}

func TestJudgeTransportFailures(t *testing.T) {
	j, _ := NewJudge(nil, nil, FormatSystemOne, "http://127.0.0.1:1", "k", "m")
	if _, err := j.Ask(context.Background(), "mail", testQs); !errors.Is(err, errUnreachable) {
		t.Errorf("unreachable: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := j.Ask(ctx, "mail", testQs); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
}

// pi-ai retries 429, 529 and 5xx, a long Retry-After cut to its 2s.
func TestJudgeRetries(t *testing.T) {
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
	j, _ := NewJudge(nil, nil, FormatDecisions, srv.URL, "k", "m")
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
		j, _ := NewJudge(nil, nil, FormatSystemOne, srv.URL, "k", "m")
		_, err := j.Ask(context.Background(), "mail", testQs)
		var he *HTTPError
		if hits != 3 || !errors.As(err, &he) || he.Status != st {
			t.Errorf("%d: hits %d, %v", st, hits, err)
		}
		srv.Close()
	}
}
