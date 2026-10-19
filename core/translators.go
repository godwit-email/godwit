package core

// Translation channels (docs/superpowers/specs/2026-10-14-native-plugins-design.md
// 2.7): the built-in model translation (RunSkill's streaming path) and any
// running plugin that declares the translate capability, native or script,
// behind one small interface.

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/gateway"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/plugins"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"golang.org/x/text/language"
)

// translatorPrefix marks a plugin in Settings.Translator and in a run's model slot.
const translatorPrefix = "plugin/"

// TranslatorInfo is a translation channel: ID "" is the model, else
// "plugin/<name>" for an installed plugin declaring translate or a built-in
// one that translates, whose Title is its display name.
type TranslatorInfo struct {
	ID, Name                 string
	Title                    plugins.Text
	Native, Running, Builtin bool
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

// ListTranslators lists the model, the built-in translating plugins, and the
// installed plugins declaring translate, by name.
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
	out := []TranslatorInfo{{}}
	for i := range builtinPlugins {
		if b := &builtinPlugins[i]; b.translates && s.builtinAvailable(b) {
			on, err := s.builtinOn(ctx, b.name)
			if err != nil {
				return nil, err
			}
			out = append(out, TranslatorInfo{ID: translatorPrefix + b.name, Name: b.name, Title: b.title, Running: on, Builtin: true})
		}
	}
	return append(out, plug...), nil
}

// boundTranslator is a translation channel ready to call: version is part of
// its cache keys, what may change its translations.
type boundTranslator struct {
	version []any
	call    func(ctx context.Context, req translateRequest) (translateResult, error)
}

// translatorFor is the plugin name as a translator: the system's for the
// built-in system-translation when it is on, else a running plugin.
func (s *Service) translatorFor(ctx context.Context, name string) (boundTranslator, error) {
	if b := builtin(name); b != nil {
		if !b.translates || !s.builtinAvailable(b) {
			return boundTranslator{}, ErrTranslatorUnavailable
		}
		if on, err := s.builtinOn(ctx, name); err != nil || !on {
			return boundTranslator{}, cmp.Or(err, ErrTranslatorUnavailable)
		}
		sys := s.opts.SystemTranslator
		return boundTranslator{version: []any{sys.Version()}, call: func(ctx context.Context, req translateRequest) (translateResult, error) {
			texts, src, err := sys.Translate(ctx, req.Texts, req.Source, req.Target)
			if err == nil && len(texts) != len(req.Texts) {
				err = fmt.Errorf("%w: %d texts for %d", errTranslatorAnswer, len(texts), len(req.Texts))
			}
			if err != nil {
				return translateResult{}, fmt.Errorf("system translation: %w", err)
			}
			return translateResult{Texts: texts, Source: src}, nil
		}}, nil
	}
	tr, rp, err := s.pluginTranslator(name)
	if err != nil {
		return boundTranslator{}, err
	}
	_, cfgVer, err := s.pluginConfig(ctx, name) // its settings may change its translations
	if err != nil {
		return boundTranslator{}, err
	}
	return boundTranslator{version: []any{rp.hash, cfgVer}, call: func(ctx context.Context, req translateRequest) (translateResult, error) {
		return s.callTranslator(ctx, name, tr, rp, req)
	}}, nil
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

// sameLanguage reports whether two language tags name one language in one
// script: "en-US" is "en", "zh-CN" is "zh-Hans", but "zh-Hant" is not "zh-Hans".
func sameLanguage(a, b string) bool {
	ta, errA := language.Parse(a)
	tb, errB := language.Parse(b)
	if errA != nil || errB != nil {
		return false
	}
	ba, _ := ta.Base()
	bb, _ := tb.Base()
	sa, _ := ta.Script()
	sb, _ := tb.Script()
	return ba == bb && sa == sb
}

// needsNoTranslation reports whether src, a translator's detected language,
// is the target or one of the languages kept as they are.
func needsNoTranslation(src, target string, keep []string) bool {
	return src != "" && (sameLanguage(src, target) || slices.ContainsFunc(keep, func(k string) bool { return sameLanguage(k, src) }))
}

// runPluginTranslate is RunSkill("translate") on the plugin the settings
// name: the same events as the model path (context, at most one text, done),
// and the same caching; errors come back from RunSkill itself. Whatever
// happens it does not fall back to the model.
func (s *Service) runPluginTranslate(ctx context.Context, end func(), plugin string, t Target, st Settings) (<-chan ai.Event, error) {
	tr, err := s.translatorFor(ctx, plugin)
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
		key = cacheKey("translate-plugin", plugin, tr.version, t.Kind, t.ID, members, lang, keep)
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
	res, err := tr.call(ctx, translateRequest{Texts: paras, Target: lang})
	if err != nil {
		return nil, err
	}
	out := strings.Join(res.Texts, "\n\n")
	if needsNoTranslation(res.Source, lang, keep) {
		out = "" // cached as "needs no translation", as the model path's NO_TRANSLATION
	}
	if key != "" {
		s.cachePut(ctx, key, account, out, "translate")
	}
	return reply(out)
}

// callTranslator runs one translation and keeps the plugin's failure count:
// a script plugin's error counts as an action's does, a native one's error
// envelope is its answer (plugins.IsNativeFailure), and a wrong number of
// texts always counts.
func (s *Service) callTranslator(ctx context.Context, plugin string, tr translator, rp *runningPlugin, req translateRequest) (translateResult, error) {
	res, err := tr.translate(ctx, req)
	if err == nil && len(res.Texts) != len(req.Texts) {
		err = fmt.Errorf("%w: %d texts for %d", errTranslatorAnswer, len(res.Texts), len(req.Texts))
	}
	if ctx.Err() != nil || errors.Is(err, plugins.ErrClosed) {
		return translateResult{}, errors.Join(ctx.Err(), err) // cancelled or stopped: not the plugin's failure
	}
	if err != nil {
		var failed error
		if rp.native == nil || plugins.IsNativeFailure(err) || errors.Is(err, errTranslatorAnswer) {
			failed = s.pluginFailed(ctx, plugin, rp.gen)
		}
		return translateResult{}, errors.Join(fmt.Errorf("translation plugin %s: %w", plugin, s.cleanError(err)), failed)
	}
	return res, s.pluginSucceeded(ctx, plugin, rp.gen)
}

// pluginDraftRun is a run that translates the draft HTML src (and optional subject) with plugin and
// answers it as the model would, for assistSkill's proposal.
func (s *Service) pluginDraftRun(plugin, src, target, subject string, translateSubject bool) runFunc {
	return func(ctx context.Context, _ ai.Request) (<-chan ai.Event, func() ai.Result) {
		events, done := make(chan ai.Event), make(chan struct{})
		var res ai.Result
		go func() {
			defer close(done)
			defer close(events)
			outHTML, err := s.translateDraftHTML(ctx, plugin, src, target)
			if err != nil {
				res = ai.Result{Err: &ai.Error{Code: ai.CodeInternal, Msg: err.Error()}}
				return
			}
			out := outHTML
			if translateSubject && strings.TrimSpace(subject) != "" {
				subjRes, err := s.translateDraftText(ctx, plugin, subject, target)
				if err == nil && strings.TrimSpace(subjRes) != "" {
					out = "<subject>" + strings.TrimSpace(subjRes) + "</subject><body>" + outHTML + "</body>"
				}
			}
			res = ai.Result{Messages: []ai.Message{{Role: "model", Text: out}}}
		}()
		return events, func() ai.Result { <-done; return res }
	}
}

func (s *Service) translateDraftText(ctx context.Context, plugin, text, target string) (string, error) {
	tr, err := s.translatorFor(ctx, plugin)
	if err != nil {
		return "", err
	}
	res, err := tr.call(ctx, translateRequest{Texts: []string{text}, Target: target})
	if err != nil {
		return "", err
	}
	if len(res.Texts) > 0 {
		return res.Texts[0], nil
	}
	return text, nil
}

var (
	// blockTags hold one translatable text each when no other block is inside them.
	blockTags = map[atom.Atom]bool{atom.P: true, atom.H1: true, atom.H2: true, atom.H3: true, atom.H4: true, atom.H5: true, atom.H6: true,
		atom.Li: true, atom.Td: true, atom.Th: true, atom.Blockquote: true, atom.Div: true, atom.Dd: true, atom.Dt: true}
	// styleTags may wrap a block's whole text and stay around its translation.
	styleTags = map[atom.Atom]bool{atom.A: true, atom.B: true, atom.Strong: true, atom.I: true, atom.Em: true, atom.U: true,
		atom.S: true, atom.Del: true, atom.Span: true, atom.Font: true}
)

// translateDraftHTML translates draft HTML block by block: a paragraph,
// heading, list item, quote or table cell (and text beside blocks) is one
// text, sent in batches within the ABI's request limit; pre and code stay as
// they are. A block's text between images and code is one segment, its
// translation put in place (setText) so they stay where they were in the
// sentence, as does a link or style around the whole segment; a segment in
// several styles comes back in the first one's, which the user sees in the proposal.
// shortcut: a sentence around inline code translates in pieces; placeholders would keep it whole, if plugins kept them.
func (s *Service) translateDraftHTML(ctx context.Context, plugin, src, target string) (string, error) {
	tr, err := s.translatorFor(ctx, plugin)
	if err != nil {
		return "", err
	}
	body := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := html.ParseFragment(strings.NewReader(src), body)
	if err != nil {
		return "", err
	}
	for _, n := range nodes {
		body.AppendChild(n)
	}
	var leaves, runs []*html.Node
	var segs [][]*html.Node
	var texts, leads, trails []string
	leaf := func(n *html.Node) {
		leaves = append(leaves, n)
		for _, seg := range segments(n) {
			t := segText(seg)
			if core := strings.TrimSpace(t); core != "" {
				lead := t[:len(t)-len(strings.TrimLeftFunc(t, unicode.IsSpace))]
				segs, texts = append(segs, seg), append(texts, core)
				leads, trails = append(leads, lead), append(trails, t[len(lead)+len(core):])
			}
		}
	}
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.DataAtom == atom.Pre {
			return
		}
		if (n == body || blockTags[n.DataAtom]) && !hasBlock(n) {
			leaf(n)
			return
		}
		// Text beside blocks, as a list item's own text before its nested
		// list, is a text of its own: wrap each run of it while translating.
		for c := n.FirstChild; c != nil; {
			if isBlockNode(c) {
				walk(c)
				c = c.NextSibling
				continue
			}
			run := &html.Node{Type: html.ElementNode, Data: "span", DataAtom: atom.Span}
			n.InsertBefore(run, c)
			for c != nil && !isBlockNode(c) {
				next := c.NextSibling
				n.RemoveChild(c)
				run.AppendChild(c)
				c = next
			}
			runs = append(runs, run)
			leaf(run)
		}
	}
	walk(body)
	if len(texts) == 0 {
		return src, nil
	}
	// Long blocks go in pieces, and pieces in batches, each request well within the ABI's limit.
	var pieces []draftPiece
	for i, t := range texts {
		for _, p := range splitText(t, draftPieceBytes) {
			p.leaf = i
			pieces = append(pieces, p)
		}
	}
	out := make([]string, len(texts))
	for start := 0; start < len(pieces); {
		end, size := start, 0
		var batch []string
		for end < len(pieces) && (end == start || size+jsonLen(pieces[end].text) <= draftBatch) {
			size += jsonLen(pieces[end].text)
			batch = append(batch, pieces[end].text)
			end++
		}
		res, err := tr.call(ctx, translateRequest{Texts: batch, Target: target})
		if err != nil {
			return "", err
		}
		for k, tx := range res.Texts {
			p := pieces[start+k]
			out[p.leaf] += tx + p.sep
		}
		start = end
	}
	for i, seg := range segs {
		setText(seg, leads[i]+out[i]+trails[i])
	}
	for _, n := range leaves {
		pruneEmpty(n)
	}
	for _, r := range runs { // unwrap the runs before rendering
		for r.FirstChild != nil {
			c := r.FirstChild
			r.RemoveChild(c)
			r.Parent.InsertBefore(c, r)
		}
		r.Parent.RemoveChild(r)
	}
	var b strings.Builder
	for c := body.FirstChild; c != nil; c = c.NextSibling {
		if err := html.Render(&b, c); err != nil {
			return "", err
		}
	}
	return b.String(), nil
}

// isBlockNode reports whether c is, or holds, a block: not part of a run of text.
func isBlockNode(c *html.Node) bool {
	return c.Type == html.ElementNode && (blockTags[c.DataAtom] || c.DataAtom == atom.Pre || c.DataAtom == atom.Ul || c.DataAtom == atom.Ol ||
		c.DataAtom == atom.Table || c.DataAtom == atom.Tbody || c.DataAtom == atom.Thead || c.DataAtom == atom.Tfoot || c.DataAtom == atom.Tr ||
		c.DataAtom == atom.Hr || hasBlock(c))
}

// draftBatch caps the JSON-encoded text of one translate request, well under
// the ABI's 1 MiB; draftPieceBytes caps a piece of a long block (JSON may
// grow it sixfold: "<" is \u003c).
const (
	draftBatch      = 256 << 10
	draftPieceBytes = 32 << 10
)

// draftPiece is a block's text, or a piece of it, and what joins it to the next piece.
type draftPiece struct {
	text, sep string
	leaf      int
}

// splitText cuts s into pieces of at most max bytes: at a space or line
// break in the second half of each piece (the separator kept as sep), else at
// a rune boundary.
func splitText(s string, max int) []draftPiece {
	var out []draftPiece
	for len(s) > max {
		cut := max
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		if i := strings.LastIndexAny(s[:cut], " \n"); i > max/2 {
			out = append(out, draftPiece{text: s[:i], sep: s[i : i+1]})
			s = s[i+1:]
			continue
		}
		out = append(out, draftPiece{text: s[:cut]})
		s = s[cut:]
	}
	return append(out, draftPiece{text: s})
}

func jsonLen(s string) int {
	b, _ := json.Marshal(s)
	return len(b)
}

// hasBlock reports whether a block or pre element is inside n.
func hasBlock(n *html.Node) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if isBlockNode(c) {
			return true
		}
	}
	return false
}

// segments splits n's text at the elements that stay as they are (code,
// images): each segment is its text nodes and <br>s, in order.
func segments(n *html.Node) [][]*html.Node {
	segs := [][]*html.Node{nil}
	var walk func(*html.Node)
	walk = func(c *html.Node) {
		switch {
		case c.Type == html.TextNode || c.DataAtom == atom.Br:
			segs[len(segs)-1] = append(segs[len(segs)-1], c)
			return
		case c != n && c.Type == html.ElementNode && !styleTags[c.DataAtom]:
			segs = append(segs, nil) // commands and snippets are not translated
			return
		}
		for d := c.FirstChild; d != nil; d = d.NextSibling {
			walk(d)
		}
	}
	walk(n)
	return segs
}

// segText is seg's text, a line break for each <br>.
func segText(seg []*html.Node) string {
	var b strings.Builder
	for _, c := range seg {
		if c.Type == html.TextNode {
			b.WriteString(c.Data)
		} else {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// setText puts text in place of seg's text: in its first text node that is
// not blank, in that node's styles; its other text and its <br>s go, the
// translation's line breaks becoming <br>s. pruneEmpty then drops the style
// elements left empty.
func setText(seg []*html.Node, text string) {
	var first *html.Node
	for _, c := range seg {
		switch {
		case c.Type != html.TextNode:
			c.Parent.RemoveChild(c)
		case first == nil && strings.TrimSpace(c.Data) != "":
			first = c
		default:
			c.Data = ""
		}
	}
	lines := strings.Split(text, "\n")
	first.Data = lines[0]
	at := first
	for _, line := range lines[1:] {
		br := &html.Node{Type: html.ElementNode, Data: "br", DataAtom: atom.Br}
		t := &html.Node{Type: html.TextNode, Data: line}
		at.Parent.InsertBefore(br, at.NextSibling)
		at.Parent.InsertBefore(t, br.NextSibling)
		at = t
	}
}

// pruneEmpty removes the style elements under n that hold no text and no other element.
func pruneEmpty(n *html.Node) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		if c.Type == html.ElementNode && c.DataAtom != atom.Code {
			pruneEmpty(c)
			if styleTags[c.DataAtom] && empty(c) {
				n.RemoveChild(c)
			}
		} else if c.Type == html.TextNode && c.Data == "" {
			n.RemoveChild(c)
		}
		c = next
	}
}

func empty(n *html.Node) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode || c.Type == html.TextNode && c.Data != "" {
			return false
		}
	}
	return true
}
