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
	"github.com/godwit-email/godwit/core/ai/mask"
	"github.com/godwit-email/godwit/core/gateway"
	"github.com/godwit-email/godwit/core/skills"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
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
	read := readBody(m)
	var originals []string
	seen := map[string]bool{}
	for _, n := range read.nodes {
		if text := read.whole[n[0]:n[1]]; !seen[text] {
			seen[text] = true
			originals = append(originals, text)
		}
	}
	lang := s.readingLanguage(ctx)
	keep := slices.Sorted(slices.Values(st.NoTranslate))
	var ref string
	var version any
	var call func(context.Context, translateRequest) (translateResult, error)
	byModel := false        // a model reads long links as references, as in mailContext
	var masker *mask.Masker // masks the texts before they are cut, as the model's requests will
	var bound boundTranslator
	entry := gateway.Entry{Source: gateway.SourceSkill, Tool: "skill:translate", TargetKind: "message", TargetID: t.ID, Decision: "run"}
	if plugin, ok := strings.CutPrefix(st.Translator, translatorPrefix); ok {
		tr, err := s.translatorFor(ctx, plugin)
		if err != nil {
			return nil, err
		}
		ref, version, bound = st.Translator, tr.version, tr
		call = func(ctx context.Context, req translateRequest) (translateResult, error) {
			req.masked = true // before the texts were cut
			return tr.call(ctx, req)
		}
		entry.Source, entry.Extension, entry.Tool = gateway.SourcePlugin, plugin, "plugin:"+plugin+"/translate"
	} else {
		model, modelRef, err := s.resolveTask(ctx, sk.Model, sk.Tier == "small")
		if err != nil {
			return nil, err
		}
		m, err := s.masker()
		if err != nil {
			return nil, err
		}
		ref, version, byModel = modelRef, []any{model.ServiceID, model.Version}, true
		if !model.OnDevice && m.On() {
			masker = m
		}
		system := sk.Body + "\n\n" + ai.UntrustedSystemNote + "\nTarget language: " + lang +
			". Languages needing no translation: " + strings.Join(append([]string{lang}, keep...), ", ") +
			`. For this request return ONLY a JSON object {"texts":[...],"source":"language-tag"}. Translate each input string in order, with exactly one nonempty output per input. Preserve strings needing no translation unchanged instead of NO_TRANSLATION. Do not include headers, notes, Markdown, or the untrusted_email wrapper.`
		call = func(ctx context.Context, req translateRequest) (translateResult, error) {
			b, _ := json.Marshal(req.Texts)
			prompt := ai.Untrusted(fmt.Sprintf("message:%d", t.ID), string(b), ai.InjectionQuote(strings.Join(req.Texts, "\n")) != "")
			r, err := s.complete(ctx, featureTranslate, ai.Request{Model: model, System: system, Prompt: prompt, Raw: true}, 128<<10)
			if err != nil {
				return translateResult{}, err
			}
			var res translateResult
			if err := json.Unmarshal([]byte(r.Text), &res); err != nil {
				return res, fmt.Errorf("translation returned invalid JSON: %w", err)
			}
			for i, t := range res.Texts {
				res.Texts[i] = r.Restore(t)
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
			if byModel {
				result, err = translateMasked(ctx, originals, read, t.ID, lang, keep, masker, call)
			} else {
				var masked []string
				if masked, err = bound.maskWhole(ctx, read, originals); err == nil {
					result, err = translateRestored(ctx, originals, masked, func(tr, _ string) string { return bound.masker.Restore(tr) }, lang, keep, call)
				}
			}
			if err == nil {
				cached, _ := json.Marshal(result)
				s.cachePut(ctx, key, account, string(cached), "translate")
			}
		}
		if err != nil {
			ae := ai.Blocked(err)
			if ae == nil && !errors.As(err, &ae) {
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
// masked before the texts are cut into pieces, and put back after. With m,
// the texts are cut as m masks them and go so: no value is cut in two, nor
// from the label it was found by.
func translateMasked(ctx context.Context, originals []string, read bodyRead, id int64, target string, keep []string, m *mask.Masker, call func(context.Context, translateRequest) (translateResult, error)) (bodyTranslation, error) {
	masked, links := maskLinks(originals, id)
	if m != nil {
		cut, err := m.RequestWhole(ctx, read.whole, read.nodes, masked) // recorded here: what goes out is already masked
		if err != nil {
			return bodyTranslation{}, err
		}
		send := call
		call = func(ctx context.Context, req translateRequest) (translateResult, error) {
			return send(mask.Premasked(ctx), req)
		}
		masked = cut
	}
	return translateRestored(ctx, originals, masked, func(tr, src string) string { return unmask(m.Restore(tr), src, links) }, target, keep, call)
}

// bodyRead is a message's body as masking reads it: whole is its subject's
// line, then the text nodes translation sends, in order (nodes: where each
// stands in whole), read as the reader shows them, so a code is found by a
// label in another node: nodes in one block run on, blocks go on a line each.
type bodyRead struct {
	whole string
	nodes [][2]int
}

// readBody is m's bodyRead; its HTML's nodes are those MapHTMLText replaces.
// shortcut: a code set one digit per cell reads "1\n2\n…", which mail.SignInCode does not find either; join cells of one character if such codes are seen to leak.
func readBody(m Message) bodyRead {
	var b strings.Builder
	var nodes [][2]int
	b.WriteString("Subject: " + m.Subject + "\n")
	line := false // a block began or ended since the last text
	add := func(text string) {
		if line {
			b.WriteByte('\n')
			line = false
		}
		if strings.TrimSpace(text) != "" {
			nodes = append(nodes, [2]int{b.Len(), b.Len() + len(text)})
		}
		b.WriteString(text)
	}
	if m.HTML == "" {
		add(m.Text)
		return bodyRead{b.String(), nodes}
	}
	ctx := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	top, err := html.ParseFragment(strings.NewReader(m.HTML), ctx)
	if err != nil {
		return bodyRead{b.String(), nil} // MapHTMLText translates nothing either
	}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		switch n.DataAtom {
		case atom.Style, atom.Script, atom.Code, atom.Pre:
			line = line || n.DataAtom == atom.Pre
			return
		}
		block := isBlockNode(n) || n.DataAtom == atom.Br
		line = line || block
		if n.Type == html.TextNode {
			add(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		line = line || block
	}
	for _, n := range top {
		walk(n)
	}
	return bodyRead{b.String(), nodes}
}

// translateRestored is translateBodyTexts on masked, originals with values
// replaced before they are cut into pieces; restore puts them back in tr, a
// translation of the masked text src.
func translateRestored(ctx context.Context, originals, masked []string, restore func(tr, src string) string, target string, keep []string, call func(context.Context, translateRequest) (translateResult, error)) (bodyTranslation, error) {
	res, err := translateBodyTexts(ctx, masked, target, keep, call)
	if err != nil {
		return res, err
	}
	out := bodyTranslation{Text: restore(res.Text, strings.Join(masked, "\n")), Translations: map[string]string{}}
	for i, text := range originals {
		if tr, ok := res.Translations[masked[i]]; ok {
			if tr = restore(tr, masked[i]); tr != text {
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
