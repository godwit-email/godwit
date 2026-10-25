package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/gateway"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/skills"
)

type bodyTranslation struct {
	Text         string
	Translations map[string]string
}

// runBodyTranslation keeps the reader's HTML local and translates only its text.
// The ordinary RunSkill text contract remains available to selections and API callers.
func (s *Service) runBodyTranslation(ctx context.Context, end func(), sk skills.Skill, t Target, st Settings) (<-chan ai.Event, error) {
	account, _, members, err := s.mailTarget(ctx, "message", t.ID)
	if err != nil {
		return nil, err
	}
	if err := s.bind(ctx, account); err != nil {
		return nil, err
	}
	m, err := s.mailForModel(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	var originals []string
	seen := map[string]bool{}
	collect := func(text string) string {
		if strings.TrimSpace(text) != "" && !seen[text] {
			seen[text] = true
			originals = append(originals, text)
		}
		return text
	}
	if m.HTML != "" {
		mail.MapHTMLText(m.HTML, collect)
	} else {
		collect(m.Text)
	}
	lang := s.readingLanguage(ctx)
	keep := slices.Sorted(slices.Values(st.NoTranslate))
	var ref string
	var version any
	var call func(context.Context, translateRequest) (translateResult, error)
	mask := false // a model reads long links as references, as in mailContext
	entry := gateway.Entry{Source: gateway.SourceSkill, Tool: "skill:translate", TargetKind: "message", TargetID: t.ID, Decision: "run"}
	if plugin, ok := strings.CutPrefix(st.Translator, translatorPrefix); ok {
		tr, err := s.translatorFor(ctx, plugin)
		if err != nil {
			return nil, err
		}
		ref, version, call = st.Translator, tr.version, tr.call
		entry.Source, entry.Extension, entry.Tool = gateway.SourcePlugin, plugin, "plugin:"+plugin+"/translate"
	} else {
		model, modelRef, err := s.resolveTask(ctx, sk.Model, sk.Tier == "small")
		if err != nil {
			return nil, err
		}
		e, err := s.aiEngine()
		if err != nil {
			return nil, err
		}
		ref, version, mask = modelRef, []any{model.ServiceID, model.Version}, true
		system := sk.Body + "\n\n" + ai.UntrustedSystemNote + "\nTarget language: " + lang +
			". Languages needing no translation: " + strings.Join(append([]string{lang}, keep...), ", ") +
			`. For this request return ONLY a JSON object {"texts":[...],"source":"language-tag"}. Translate each input string in order, with exactly one nonempty output per input. Preserve strings needing no translation unchanged instead of NO_TRANSLATION. Do not include headers, notes, Markdown, or the untrusted_email wrapper.`
		call = func(ctx context.Context, req translateRequest) (translateResult, error) {
			b, _ := json.Marshal(req.Texts)
			prompt := ai.Untrusted(fmt.Sprintf("message:%d", t.ID), string(b), ai.InjectionQuote(strings.Join(req.Texts, "\n")) != "")
			text, err := s.runText(ctx, e, ai.Request{Model: model, System: system, Prompt: prompt, MaxTurns: 1}, 128<<10)
			if err != nil {
				return translateResult{}, err
			}
			var res translateResult
			if err := json.Unmarshal([]byte(text), &res); err != nil {
				return res, fmt.Errorf("translation returned invalid JSON: %w", err)
			}
			return res, nil
		}
	}
	key := cacheKey("body-translate-v1", sk.Version, members, ref, version, lang, keep)
	cached, hit, err := s.cacheGet(ctx, key)
	if err != nil {
		return nil, err
	}
	var result bodyTranslation
	if hit {
		if err := json.Unmarshal([]byte(cached), &result); err != nil {
			return nil, err
		}
	} else if err := s.gateway().Record(ctx, entry); err != nil {
		return nil, err
	}
	b, _ := json.Marshal([]mailSource{{MessageID: t.ID, Suspect: ai.InjectionQuote(strings.Join(originals, "\n")) != ""}})
	out := make(chan ai.Event, 3)
	go func() {
		defer end()
		defer close(out)
		out <- ai.Event{Kind: "context", Text: string(b), Model: ref}
		if !hit {
			if mask {
				result, err = translateMasked(ctx, originals, t.ID, lang, keep, call)
			} else {
				result, err = translateBodyTexts(ctx, originals, lang, keep, call)
			}
			if err == nil {
				cached, _ := json.Marshal(result)
				s.cachePut(ctx, key, account, string(cached), "translate")
			}
		}
		if err != nil {
			var ae *ai.Error
			if !errors.As(err, &ae) {
				code := ai.CodeInternal
				if errors.Is(err, context.Canceled) {
					code = ai.CodeCanceled
				}
				ae = &ai.Error{Code: code, Msg: s.cleanError(err).Error()}
			}
			out <- ai.Event{Kind: "error", Err: ae}
			return
		}
		if len(result.Translations) > 0 {
			out <- ai.Event{Kind: "text", Text: result.Text, Translations: result.Translations}
		}
		out <- ai.Event{Kind: "done"}
	}()
	return out, nil
}

// translateMasked is translateBodyTexts with the long links of message id
// masked before the texts are cut into pieces, and put back after.
func translateMasked(ctx context.Context, originals []string, id int64, target string, keep []string, call func(context.Context, translateRequest) (translateResult, error)) (bodyTranslation, error) {
	masked, links := maskLinks(originals, id)
	res, err := translateBodyTexts(ctx, masked, target, keep, call)
	if err != nil {
		return res, err
	}
	out := bodyTranslation{Text: unmask(res.Text, strings.Join(masked, "\n"), links), Translations: map[string]string{}}
	for i, text := range originals {
		if tr, ok := res.Translations[masked[i]]; ok {
			if tr = unmask(tr, masked[i], links); tr != text {
				out.Translations[text] = tr
			}
		}
	}
	return out, nil
}

func translateBodyTexts(ctx context.Context, originals []string, target string, keep []string, call func(context.Context, translateRequest) (translateResult, error)) (bodyTranslation, error) {
	var pieces []draftPiece
	for i, text := range originals {
		for _, p := range splitText(strings.TrimSpace(text), 8<<10) {
			p.leaf = i
			pieces = append(pieces, p)
		}
	}
	translated := make([]string, len(originals))
	for start := 0; start < len(pieces); {
		if err := ctx.Err(); err != nil {
			return bodyTranslation{}, err
		}
		end, size := start, 0
		var batch []string
		for end < len(pieces) && (end == start || size+jsonLen(pieces[end].text) <= 16<<10) {
			size += jsonLen(pieces[end].text)
			batch = append(batch, pieces[end].text)
			end++
		}
		res, err := call(ctx, translateRequest{Texts: batch, Target: target})
		if err != nil {
			return bodyTranslation{}, err
		}
		if err := ctx.Err(); err != nil {
			return bodyTranslation{}, err
		}
		if len(res.Texts) != len(batch) {
			return bodyTranslation{}, fmt.Errorf("translation returned %d texts for %d", len(res.Texts), len(batch))
		}
		if needsNoTranslation(res.Source, target, keep) {
			res.Texts = batch
		}
		for i, text := range res.Texts {
			if strings.TrimSpace(text) == "" {
				return bodyTranslation{}, errors.New("translation returned empty text")
			}
			p := pieces[start+i]
			translated[p.leaf] += text + p.sep
		}
		start = end
	}
	result := bodyTranslation{Translations: map[string]string{}}
	for i, text := range originals {
		core := strings.TrimSpace(text)
		lead := text[:len(text)-len(strings.TrimLeftFunc(text, unicode.IsSpace))]
		replacement := lead + translated[i] + text[len(lead)+len(core):]
		if replacement != text {
			result.Translations[text] = replacement
		}
	}
	result.Text = strings.Join(translated, "\n\n")
	return result, nil
}
