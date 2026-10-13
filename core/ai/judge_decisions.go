package ai

import "context"

// decisions asks the OpenAI Decisions API (research §1): the questions and
// answers are this package's own shape, so they go and come back as they are.
func (h judgeHTTP) decisions(ctx context.Context, input string, qs []Question) ([]Answer, error) {
	var resp struct{ Answers []Answer }
	if err := h.post(ctx, map[string]any{"model": h.model, "input": input, "questions": qs}, &resp); err != nil {
		return nil, err
	}
	return resp.Answers, nil
}
