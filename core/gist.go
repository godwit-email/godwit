package core

import (
	"context"
	"strings"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/gateway"
)

const (
	// gistRunes caps a gist: the folded row it fits shows less.
	gistRunes  = 60
	gistSystem = `Write one line saying what the email you are given says or asks, in its sender's own stance, ` +
		`as a note for its reader: at most 30 characters in Chinese or Japanese, about 12 words otherwise. ` +
		`No prefix such as "This email" or "The sender", no quotes, no trailing period. Answer with the line only.`
)

// MessageGist is a message's one-line gist, which its folded row in a thread
// shows in place of the start of its text: by the small model (resolveTask),
// in the reading language, cached by the message's content and the model as
// RunSkill's outputs are. "" when the model wrote nothing.
func (s *Service) MessageGist(ctx context.Context, messageID int64) (string, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return "", err
	}
	defer end()
	account, ids, members, err := s.mailTarget(ctx, "message", messageID)
	if err != nil {
		return "", err
	}
	if err := s.bind(ctx, account); err != nil {
		return "", err
	}
	model, _, err := s.resolveTask(ctx, "", true)
	if err != nil {
		return "", err
	}
	system := gistSystem + "\n\n" + ai.UntrustedSystemNote + "\n\nWrite it in " + s.readingLanguage(ctx) + "."
	key := cacheKey("gist", messageID, members, model.ServiceID, model.Version, model.Model, system)
	if o, hit, err := s.cacheGet(ctx, key); err != nil || hit {
		return o, err
	}

	items, err := s.mailContext(ctx, &gateway.Scope{}, ids, defaultMailBudget)
	if err != nil {
		return "", err
	}
	var blocks []string
	for _, it := range items {
		blocks = append(blocks, it.Block)
	}
	if err := s.gateway().Record(ctx, gateway.Entry{Source: gateway.SourceSkill, Tool: "gist", TargetKind: "message", TargetID: messageID, Decision: "run"}); err != nil {
		return "", err
	}
	r, err := s.complete(ctx, featureSkill, ai.Request{Model: model, System: system, Prompt: strings.Join(blocks, "\n\n")}, 4<<10)
	if err != nil {
		return "", err
	}
	// Its link references expanded here: the line is cut after, never a reference.
	g := gistLine(s.expandLinks(ctx, r.Text))
	if g != "" {
		s.cachePut(ctx, key, account, g, "gist")
	}
	return g, nil
}

// gistLine is a model's gist as one line: its first non-empty one, spaces
// folded, quotes and a closing full stop dropped, at most gistRunes.
func gistLine(out string) string {
	var line string
	for l := range strings.Lines(out) {
		if line = strings.Join(strings.Fields(l), " "); line != "" {
			break
		}
	}
	line = strings.Trim(line, `"'“”‘’「」`)
	line = strings.TrimRight(line, ".。")
	if r := []rune(line); len(r) > gistRunes {
		line = string(r[:gistRunes-1]) + "…"
	}
	return line
}
