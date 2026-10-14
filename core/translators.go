package core

// Translation channels (docs/superpowers/specs/2026-10-14-native-plugins-design.md
// 2.7): the built-in model translation (RunSkill's streaming path) and any
// running plugin that declares the translate capability, native or script,
// behind one small interface.

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/gateway"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/plugins"
)

// translatorPrefix marks a plugin in Settings.Translator and in a run's model slot.
const translatorPrefix = "plugin/"

// TranslatorInfo is a translation channel: ID "" is the model, else
// "plugin/<name>" for an installed plugin declaring translate.
type TranslatorInfo struct {
	ID, Name        string
	Native, Running bool
}

type translateRequest struct {
	Texts  []string `json:"texts"`
	Source string   `json:"source"`
	Target string   `json:"target"`
}

type translateResult struct {
	Texts  []string `json:"texts"`
	Source string   `json:"source"`
}

// translator is a plugin that translates.
type translator interface {
	translate(ctx context.Context, req translateRequest) (translateResult, error)
}

type nativeTranslator struct{ n *plugins.NativeProcess }

func (t nativeTranslator) translate(ctx context.Context, req translateRequest) (translateResult, error) {
	raw, err := t.n.Call(ctx, "translate", req)
	if err != nil {
		return translateResult{}, err
	}
	var res translateResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return translateResult{}, fmt.Errorf("%w: the translate result is not {texts, source}", errTranslatorAnswer)
	}
	return res, nil
}

// errTranslatorAnswer: a translator broke the contract (a failure, unlike an error it reports).
var errTranslatorAnswer = errors.New("the translation plugin answered wrongly")

// ListTranslators lists the model and the installed plugins declaring translate, by name.
func (s *Service) ListTranslators(ctx context.Context) ([]TranslatorInfo, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	defer end()
	s.pluginsMu.RLock()
	defer s.pluginsMu.RUnlock()
	rows, err := s.pluginRows(ctx, "")
	if err != nil {
		return nil, err
	}
	var plug []TranslatorInfo
	for name, r := range rows {
		if slices.Contains(r.m.Capabilities, "translate") {
			plug = append(plug, TranslatorInfo{ID: translatorPrefix + name, Name: name, Native: r.m.IsNative(), Running: s.running[name] != nil})
		}
	}
	sort.Slice(plug, func(i, j int) bool { return plug[i].Name < plug[j].Name })
	return append([]TranslatorInfo{{}}, plug...), nil
}

// pluginTranslator is the running plugin name as a translator.
func (s *Service) pluginTranslator(name string) (translator, *runningPlugin, error) {
	s.pluginsMu.RLock()
	defer s.pluginsMu.RUnlock()
	rp := s.running[name]
	if rp == nil || !slices.Contains(rp.row.m.Capabilities, "translate") {
		return nil, nil, ErrTranslatorUnavailable
	}
	if rp.native != nil {
		return nativeTranslator{rp.native}, rp, nil
	}
	return jsTranslator{s, name, rp}, rp, nil
}

// jsTranslator is a script plugin's ctx.translators.register.
type jsTranslator struct {
	s    *Service
	name string
	rp   *runningPlugin
}

func (t jsTranslator) translate(ctx context.Context, req translateRequest) (translateResult, error) {
	r := t.rp.row
	hosts, _ := plugins.EgressFor(plugins.KindTranslate, r.m, r.granted == r.permHash, true, true)
	if err := t.s.markHoldsMail(ctx, t.name); err != nil { // mail content, before the plugin has it
		return translateResult{}, err
	}
	inv := plugins.Invocation{ID: "translate:" + rand.Text(), Kind: plugins.KindTranslate, EgressHosts: hosts, Config: t.rp.host.snapshot()}
	v, err := t.rp.p.Translate(ctx, inv, req)
	if err != nil {
		return translateResult{}, err
	}
	b, _ := json.Marshal(v)
	var res translateResult
	if err := json.Unmarshal(b, &res); err != nil {
		return translateResult{}, fmt.Errorf("%w: the translate result is not {texts, source}", errTranslatorAnswer)
	}
	return res, nil
}

var paragraphRE = regexp.MustCompile(`\n[ \t]*\n`)

// paragraphs splits text at blank lines, without the empty ones.
func paragraphs(text string) []string {
	var out []string
	for _, p := range paragraphRE.Split(strings.ReplaceAll(text, "\r\n", "\n"), -1) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// baseLang is a language tag's language, lower case: "en" for "en-US".
func baseLang(tag string) string {
	b, _, _ := strings.Cut(tag, "-")
	return strings.ToLower(b)
}

// runPluginTranslate is RunSkill("translate") on the plugin the settings
// name: the same events as the model path (context, at most one text, done),
// and the same caching; errors come back from RunSkill itself. Whatever
// happens it does not fall back to the model.
func (s *Service) runPluginTranslate(ctx context.Context, end func(), plugin string, t Target, st Settings) (<-chan ai.Event, error) {
	tr, rp, err := s.pluginTranslator(plugin)
	if err != nil {
		return nil, err
	}
	kind := t.Kind
	if kind == "selection" {
		if strings.TrimSpace(t.Text) == "" {
			return nil, errors.New("selection is empty")
		}
		kind = "message"
	}
	account, ids, members, err := s.mailTarget(ctx, kind, t.ID)
	if err != nil {
		return nil, err
	}
	if err := s.bind(ctx, account); err != nil {
		return nil, err
	}
	text := t.Text
	if t.Kind != "selection" {
		m, err := s.mailForModel(ctx, ids[len(ids)-1])
		if err != nil {
			return nil, err
		}
		if text = m.Text; strings.TrimSpace(text) == "" {
			text = mail.HTMLToText(m.HTML)
		}
	}
	if r := []rune(text); len(r) > defaultMailBudget.PerMessage {
		text = string(r[:defaultMailBudget.PerMessage])
	}
	lang := s.readingLanguage(ctx)
	keep := slices.Sorted(slices.Values(st.NoTranslate))
	b, _ := json.Marshal([]mailSource{{MessageID: t.ID}})
	ctxEvent := ai.Event{Kind: "context", Text: string(b), Model: translatorPrefix + plugin}
	reply := func(out string) (<-chan ai.Event, error) {
		ch := make(chan ai.Event, 3)
		ch <- ctxEvent
		if out != "" {
			ch <- ai.Event{Kind: "text", Text: out}
		}
		ch <- ai.Event{Kind: "done"}
		close(ch)
		end()
		return ch, nil
	}

	key := ""
	if t.Kind == "message" || t.Kind == "thread" {
		key = cacheKey("translate-plugin", plugin, rp.hash, t.Kind, t.ID, members, lang, keep)
		if o, hit, err := s.cacheGet(ctx, key); err != nil {
			return nil, err
		} else if hit {
			return reply(o)
		}
	}
	paras := paragraphs(text)
	if len(paras) == 0 {
		return reply("")
	}
	if err := s.gateway().Record(ctx, gateway.Entry{Source: gateway.SourcePlugin, Extension: plugin, Tool: "plugin:" + plugin + "/translate",
		TargetKind: t.Kind, TargetID: t.ID, Decision: "run"}); err != nil {
		return nil, err
	}
	res, err := tr.translate(ctx, translateRequest{Texts: paras, Target: lang})
	if err == nil && len(res.Texts) != len(paras) {
		err = fmt.Errorf("%w: %d texts for %d", errTranslatorAnswer, len(res.Texts), len(paras))
	}
	if ctx.Err() != nil || errors.Is(err, plugins.ErrClosed) {
		return nil, errors.Join(ctx.Err(), err) // cancelled or stopped: not the plugin's failure
	}
	if err != nil {
		var failed error
		// A script plugin's error counts as an action's does; a native one's
		// error envelope is its answer (plugins.IsNativeFailure).
		if rp.native == nil || plugins.IsNativeFailure(err) || errors.Is(err, errTranslatorAnswer) {
			failed = s.pluginFailed(ctx, plugin, rp.gen)
		}
		return nil, errors.Join(fmt.Errorf("translation plugin %s: %w", plugin, s.cleanError(err)), failed)
	}
	if err := s.pluginSucceeded(ctx, plugin, rp.gen); err != nil {
		return nil, err
	}
	out := strings.Join(res.Texts, "\n\n")
	if src := baseLang(res.Source); src != "" && (src == baseLang(lang) || slices.ContainsFunc(keep, func(k string) bool { return baseLang(k) == src })) {
		out = "" // cached as "needs no translation", as the model path's NO_TRANSLATION
	}
	if key != "" {
		s.cachePut(ctx, key, account, out, "translate")
	}
	return reply(out)
}
