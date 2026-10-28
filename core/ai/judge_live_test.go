package ai

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestJudgeLive asks each real judge service a yes/no and a choice question.
// It runs only with keys in the environment and never logs them:
//
//	GODWIT_LIVE_OPENAI_KEY=… GODWIT_LIVE_TYPESAFE_KEY=… go test ./core/ai -run TestJudgeLive -v
func TestJudgeLive(t *testing.T) {
	for _, tc := range []struct {
		name, env, base, model string
		format                 JudgeFormat
	}{
		{"openai-decisions", "GODWIT_LIVE_OPENAI_KEY", "https://api.openai.com/v1", "gpt-6-luna", FormatDecisions},
		{"typesafe-systemone", "GODWIT_LIVE_TYPESAFE_KEY", "https://api.typesafe.ai/v1", "jev-latest", FormatSystemOne},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := os.Getenv(tc.env)
			if key == "" {
				t.Skip(tc.env + " not set")
			}
			j, err := NewJudge(&http.Client{Timeout: 30 * time.Second}, nil, tc.format, tc.base, key, tc.model)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			start := time.Now()
			ans, err := j.Ask(ctx, "Your one-time login code is 482913. It expires in 10 minutes.", []Question{
				{Name: "is_otp", Instructions: "Does this message contain a one-time login code?", Type: Predicate},
				{Name: "kind", Instructions: "What kind of email is this?", Type: Choice, Choices: []ChoiceOption{
					{Value: "otp", Description: "a login or verification code"},
					{Value: "receipt", Description: "a payment receipt"},
					{Value: "other", Description: "anything else"},
				}},
			})
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			t.Logf("%s answered in %v: %+v", tc.name, time.Since(start).Round(time.Millisecond), ans)
			byName := map[string]Answer{}
			for _, a := range ans {
				byName[a.Name] = a
			}
			if a := byName["is_otp"]; a.Type == Refusal || a.Probability == nil || *a.Probability < 0.5 {
				t.Errorf("is_otp = %+v, want yes", a)
			}
			if a := byName["kind"]; a.Type == Refusal || a.Choice != "otp" {
				t.Errorf("kind = %+v, want otp", a)
			}
		})
	}
}
