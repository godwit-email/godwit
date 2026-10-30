package core

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/ai/mask"
	"github.com/godwit-email/godwit/core/gateway"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/store"
)

// ErrUseAssist: skills on drafts run through AssistSkill, which makes proposals.
var ErrUseAssist = errors.New("draft skills run through AssistSkill")

// mailSource is one entry of RunSkill's leading context event.
type mailSource struct {
	MessageID int64 `json:"message_id"`
	Suspect   bool  `json:"suspect"`
}

// Target is what a skill runs on: message, thread, selection (ID is the
// message the text was selected in) or draft.
type Target struct {
	Kind string
	ID   int64
	Text string
	// PreserveHTML translates message text nodes for the reader, without rewriting markup.
	PreserveHTML bool
}

const (
	noTranslation  = "NO_TRANSLATION"
	sentinelBuffer = 32 // runes a translate run holds back before forwarding text
)

// RunSkill runs an enabled skill on t with no tools. The first event is
// "context": Text is a JSON list of {message_id, suspect} for the mail the
// model sees, so the UI can flag suspected prompt injection, and Model is the
// "slug/model" the run uses (skill godwit-model, else SmallModel for the small
// tier, else DefaultModel). message and
// thread runs are cached by everything that changes their output; a hit
// replays context, one text event and done without asking the model. The
// channel ends with exactly one of done or error and is closed after it;
// cancel ctx to stop early.
func (s *Service) RunSkill(ctx context.Context, name string, t Target) (<-chan ai.Event, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	ch, err := s.runSkill(ctx, end, name, t)
	if err != nil {
		end()
	}
	return ch, err
}

func (s *Service) runSkill(ctx context.Context, end func(), name string, t Target) (<-chan ai.Event, error) {
	ctx = mask.Tag(ctx, cmp.Or(map[string]string{"translate": featureTranslate}[name], featureSkill), nil)
	sk, err := s.skill(ctx, name)
	if err != nil {
		return nil, err
	}
	if t.Kind == "draft" || sk.Output == "replace-draft" {
		return nil, ErrUseAssist
	}
	if !slices.Contains(sk.On, t.Kind) {
		return nil, fmt.Errorf("skill %s does not run on a %s", name, t.Kind)
	}
	if name == "translate" { // before any model: a plugin translates without one
		st, err := s.loadSettings(ctx)
		if err != nil {
			return nil, err
		}
		if t.PreserveHTML && t.Kind == "message" {
			return s.runBodyTranslation(ctx, end, sk, t, st)
		}
		if plugin, ok := strings.CutPrefix(st.Translator, translatorPrefix); ok {
			return s.runPluginTranslate(ctx, end, plugin, t, st)
		}
	}
	model, ref, err := s.resolveTask(ctx, sk.Model, sk.Tier == "small")
	if err != nil {
		return nil, err
	}

	if t.Kind == "selection" && strings.TrimSpace(t.Text) == "" {
		return nil, errors.New("selection is empty")
	}
	kind := t.Kind
	if kind == "selection" {
		kind = "message"
	}
	account, ids, members, err := s.mailTarget(ctx, kind, t.ID)
	if err != nil {
		return nil, err
	}
	if err := s.bind(ctx, account); err != nil {
		return nil, err
	}

	scope := &gateway.Scope{}
	var items []mailItem
	if t.Kind == "selection" {
		items = []mailItem{s.selectionContext(scope, t.ID, t.Text, defaultMailBudget)}
	} else if items, err = s.mailContext(ctx, scope, ids, defaultMailBudget); err != nil {
		return nil, err
	}
	var blocks []string
	var srcs []mailSource
	for _, it := range items {
		blocks = append(blocks, it.Block)
		srcs = append(srcs, mailSource{it.MessageID, it.Suspect})
	}
	b, _ := json.Marshal(srcs)
	ctxEvent := ai.Event{Kind: "context", Text: string(b), Model: ref}

	st, err := s.loadSettings(ctx)
	if err != nil {
		return nil, err
	}
	lang := s.readingLanguage(ctx)
	system := sk.Body + "\n\n" + ai.UntrustedSystemNote + "\n\nAnswer in " + lang + "."
	translate := name == "translate"
	var keep []string // translate's own setting, part of its cache key
	if translate {
		keep = slices.Sorted(slices.Values(st.NoTranslate))
		system += "\n\nTarget language: " + lang + ". Languages that need no translation: " +
			strings.Join(append([]string{lang}, keep...), ", ") + ". If the text is in one of them, reply exactly " + noTranslation + " and nothing else."
	}
	digest := name == "summarize" && t.Kind == "thread" // ParseThreadDigest reads it
	if digest {
		system += "\n\n" + threadDigestFormat
	}

	key := ""
	if t.Kind == "message" || t.Kind == "thread" {
		parts := []any{sk.Name, sk.Version, t.Kind, t.ID, members, model.ServiceID, model.Version, model.Model, lang, keep}
		if digest { // apart from summaries cached before the format
			parts = append(parts, threadDigestFormat)
		}
		key = cacheKey(parts...)
		o, hit, err := s.cacheGet(ctx, key)
		if err != nil {
			return nil, err
		}
		if hit {
			out := make(chan ai.Event, 3)
			out <- ctxEvent
			if o != "" {
				out <- ai.Event{Kind: "text", Text: o}
			}
			out <- ai.Event{Kind: "done"}
			close(out)
			end()
			return out, nil
		}
	}

	// Skills get no tools; the run itself is audited once.
	if err := s.gateway().Record(ctx, gateway.Entry{Source: gateway.SourceSkill, Tool: "skill:" + name, TargetKind: t.Kind, TargetID: t.ID, Decision: "run"}); err != nil {
		return nil, err
	}
	e, err := s.aiEngine()
	if err != nil {
		return nil, err
	}
	events, wait := e.Run(ctx, ai.Request{Model: model, System: system, Prompt: strings.Join(blocks, "\n\n"), MaxTurns: 1})
	events = s.expandEvents(ctx, events)

	out := make(chan ai.Event, 1)
	go func() {
		defer end()
		send := func(ev ai.Event) {
			select {
			case out <- ev:
			case <-ctx.Done():
			}
		}
		send(ctxEvent)
		var text strings.Builder
		holding := translate
		for ev := range events {
			if ev.Kind == "text" {
				text.WriteString(ev.Text)
				if !holding {
					send(ev)
				} else if utf8.RuneCountInString(text.String()) >= sentinelBuffer {
					holding = false
					send(ai.Event{Kind: "text", Text: text.String()})
				}
				continue
			}
			// No tools, so anything else is the terminal event.
			s.runRejected(model, wait().Err)
			if ev.Kind == "done" {
				o := text.String()
				if translate && strings.TrimSpace(o) == noTranslation {
					o = "" // cached as "needs no translation"
				} else if holding && o != "" {
					send(ai.Event{Kind: "text", Text: o})
				}
				if key != "" {
					s.cachePut(ctx, key, account, o, name)
				}
			}
			// The terminal event always fits: once ctx is done the reader may be
			// gone, so drop a queued event to make room.
			select {
			case out <- ev:
			case <-ctx.Done():
				select {
				case <-out:
				default:
				}
				out <- ev
			}
		}
		close(out)
	}()
	return out, nil
}

// contentMember is one message of a cached target's content version.
type contentMember struct {
	ID   int64
	Hash string // sha256 of subject and bodies
}

// mailTarget loads a message or thread: its account, member IDs by date, and
// its content version (members sorted by ID). It also proves the target exists.
func (s *Service) mailTarget(ctx context.Context, kind string, id int64) (int64, []int64, []contentMember, error) {
	where := "id = ?"
	if kind == "thread" {
		where = "thread_id = ?"
	}
	rows, err := s.db.Query(ctx, "SELECT id, account_id, subject, body_text, body_html FROM messages WHERE "+where+" ORDER BY date, id", id)
	if err != nil {
		return 0, nil, nil, err
	}
	if len(rows.Values) == 0 {
		return 0, nil, nil, fmt.Errorf("%s %d not found", kind, id)
	}
	var ids []int64
	var members []contentMember
	for _, r := range rows.Values {
		ids = append(ids, store.Int(r[0]))
		sum := sha256.Sum256([]byte(store.Str(r[2]) + "\x00" + store.Str(r[3]) + "\x00" + store.Str(r[4])))
		members = append(members, contentMember{store.Int(r[0]), hex.EncodeToString(sum[:])})
	}
	slices.SortFunc(members, func(a, b contentMember) int { return cmp.Compare(a.ID, b.ID) })
	return store.Int(rows.Values[0][1]), ids, members, nil
}

// cacheKey hashes everything that changes a cached output.
func cacheKey(parts ...any) string {
	b, _ := json.Marshal(parts)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (s *Service) cacheGet(ctx context.Context, key string) (string, bool, error) {
	rows, err := s.db.Query(ctx, "SELECT output FROM ai_cache WHERE key = ?", key)
	if err != nil || len(rows.Values) == 0 {
		return "", false, err
	}
	// created_at is the last use (cleanAICache); like cachePut's write, a
	// failed refresh only costs an earlier miss.
	if _, err := s.db.Execute(ctx, "UPDATE ai_cache SET created_at = ? WHERE key = ?", time.Now().Unix(), key); err != nil {
		slog.Default().Warn("ai cache refresh failed", "err", err)
	}
	return store.Str(rows.Values[0][0]), true, nil
}

// cachePut stores output. The user already has it: a failed write only costs
// a later miss, so it is logged (without content), not returned.
func (s *Service) cachePut(ctx context.Context, key string, account int64, output, what string) {
	if _, err := s.db.Execute(ctx, "INSERT OR REPLACE INTO ai_cache(key, account_id, output, created_at) VALUES (?, ?, ?, ?)",
		key, account, output, time.Now().Unix()); err != nil {
		slog.Default().Warn("ai cache write failed", "skill", what, "err", err)
	}
}

// aiCacheKeepFor is how long an output stays cached after its last use: its
// key cannot be traced back to a message, so outputs of deleted mail and of
// changed keys go by age alone.
const aiCacheKeepFor = 30 * 24 * time.Hour

// cleanAICache deletes outputs unused for aiCacheKeepFor.
func (s *Service) cleanAICache(ctx context.Context) error {
	_, err := s.db.Execute(ctx, "DELETE FROM ai_cache WHERE created_at < ?", time.Now().Add(-aiCacheKeepFor).Unix())
	return err
}

// QuickReply is one suggested reply: sanitized HTML, a plain-text preview,
// and a short label naming its intent ("" from answers cached before it).
type QuickReply struct{ Label, Preview, HTML string }

const (
	maxQuickReplies    = 3
	quickPreviewRunes  = 80
	QuickLabelMaxRunes = 32
	quickLabelMinRunes = 6
	quickRepliesSystem = `Suggest up to three short, distinct replies the user could send to the email you are given, written as the user. ` +
		`Answer with JSON only, exactly in this shape: {"replies":[{"label":"...","html":"<p>...</p>"}]}. Use simple HTML (paragraphs, line breaks). ` +
		`Each label names the reply's intent in one to three words, capitalized, in the replies' language (at most 25 characters; at most eight characters in Chinese or Japanese). ` +
		`No greeting placeholders, no signature, no quoted text.`
)

// QuickReplies suggests up to three replies to a message with the default
// model. It returns none, without asking the model, when Settings.QuickReplies
// is off or the message is from the account itself. Results are cached like
// RunSkill's; an answer that is not the requested JSON is an error.
func (s *Service) QuickReplies(ctx context.Context, messageID int64) ([]QuickReply, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	defer end()
	st, err := s.loadSettings(ctx)
	if err != nil || !st.QuickReplies {
		return nil, err
	}
	account, ids, members, err := s.mailTarget(ctx, "message", messageID)
	if err != nil {
		return nil, err
	}
	if err := s.bind(ctx, account); err != nil {
		return nil, err
	}
	acc, err := s.account(ctx, account)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, "SELECT from_addr FROM messages WHERE id = ?", messageID)
	if err != nil {
		return nil, err
	}
	if len(rows.Values) > 0 && mail.AddrKey(store.Str(rows.Values[0][0])) == mail.AddrKey(acc.Email) {
		return nil, nil
	}
	model, _, err := s.resolveTask(ctx, "", true)
	if err != nil {
		return nil, err
	}

	system := quickRepliesSystem + "\n\n" + ai.UntrustedSystemNote + "\n\n"
	if st.ReplyLanguage == "reading" {
		system += "Write the replies in " + s.readingLanguage(ctx) + "."
	} else {
		system += "Write the replies in the language of the email."
	}
	key := cacheKey("quick-replies", messageID, members, model.ServiceID, model.Version, model.Model, st.ReplyLanguage, system)
	if o, hit, err := s.cacheGet(ctx, key); err != nil || hit {
		var rs []QuickReply
		if hit {
			err = json.Unmarshal([]byte(o), &rs)
			if err == nil {
				changed := false
				for i := range rs {
					if rs[i].Label == "" || len([]rune(rs[i].Label)) > QuickLabelMaxRunes || rs[i].Label == rs[i].Preview {
						rs[i].Label = ExtractQuickLabel(rs[i].Preview)
						changed = true
					}
				}
				if changed {
					if b, mErr := json.Marshal(rs); mErr == nil {
						s.cachePut(ctx, key, account, string(b), "quick_replies")
					}
				}
			}
		}
		return rs, err
	}

	items, err := s.mailContext(ctx, &gateway.Scope{}, ids, defaultMailBudget)
	if err != nil {
		return nil, err
	}
	var blocks []string
	for _, it := range items {
		blocks = append(blocks, it.Block)
	}
	if err := s.gateway().Record(ctx, gateway.Entry{Source: gateway.SourceSkill, Tool: "quick_replies", TargetKind: "message", TargetID: messageID, Decision: "run"}); err != nil {
		return nil, err
	}
	r, err := s.complete(ctx, featureReplies, ai.Request{Model: model, System: system, Prompt: strings.Join(blocks, "\n\n"), Raw: true}, 0)
	if err != nil {
		return nil, err
	}

	x := s.linkExpander()
	rs, err := parseQuickReplies(r.Text, func(h string) string { return x.text(ctx, r.RestoreHTML(h)) })
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(rs)
	s.cachePut(ctx, key, account, string(b), "quick_replies")
	return rs, nil
}

// parseQuickReplies reads {"replies":[{"label":...,"html":...}]}, optionally inside a
// markdown code fence, keeping the first three non-empty replies, their link
// references expanded and sanitized.
func parseQuickReplies(out string, expand func(string) string) ([]QuickReply, error) {
	out = strings.TrimSpace(out)
	if rest, ok := strings.CutPrefix(out, "```"); ok {
		rest = strings.TrimPrefix(rest, "json")
		out = strings.TrimSuffix(strings.TrimSpace(rest), "```")
	}
	// Pointers tell a missing or null field from an empty one: only a real
	// array of objects with string html is an answer (an empty array is one).
	var v struct {
		Replies *[]*struct {
			Label string  `json:"label"`
			HTML  *string `json:"html"`
		} `json:"replies"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		return nil, fmt.Errorf("quick replies: the model did not answer with the requested JSON: %w", err)
	}
	if v.Replies == nil {
		return nil, errors.New("quick replies: the model's answer has no replies array")
	}
	for _, r := range *v.Replies {
		if r == nil || r.HTML == nil {
			return nil, errors.New("quick replies: a reply in the model's answer has no html string")
		}
	}
	rs := []QuickReply{}
	for _, r := range *v.Replies {
		h := cleanHTML(expand(*r.HTML)) // before the sanitizer drops a godwit:// href
		p := strings.Join(strings.Fields(mail.HTMLToText(h)), " ")
		if p == "" {
			continue
		}
		if runes := []rune(p); len(runes) > quickPreviewRunes {
			p = string(runes[:quickPreviewRunes])
		}
		l := strings.Join(strings.Fields(mail.HTMLToText(expand(html.EscapeString(r.Label)))), " ") // expand restores HTML
		if l != "" && l != p && len([]rune(l)) <= QuickLabelMaxRunes {
			l = FormatQuickLabel(l)
		} else {
			l = ExtractQuickLabel(p)
		}
		if rs = append(rs, QuickReply{Label: l, Preview: p, HTML: h}); len(rs) == maxQuickReplies {
			break
		}
	}
	return rs, nil
}

// ExtractQuickLabel extracts a concise intent clause directly from a reply's preview text
// (Superhuman style, 0 latency). It isolates the core action/question using universal
// punctuation delimiters (?, !, ., —, comma) and word boundaries, completely language-agnostic.
func ExtractQuickLabel(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	// If the text starts with a short greeting line ending in newline or comma/colon/exclamation,
	// followed by the actual body, skip that leading line.
	if nl := strings.IndexByte(text, '\n'); nl > 0 && nl <= 35 {
		firstLine := strings.TrimSpace(text[:nl])
		rest := strings.TrimSpace(text[nl+1:])
		if rest != "" && (strings.HasSuffix(firstLine, ",") || strings.HasSuffix(firstLine, "!") || strings.HasSuffix(firstLine, ":")) {
			text = rest
		}
	}

	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) == 0 {
		return ""
	}

	// Look for natural clause (sentence end, dash, or comma break within QuickLabelMaxRunes)
	if clause, ok := findNaturalClause(text, QuickLabelMaxRunes); ok {
		return cleanAndCapitalizeLabel(clause)
	}

	if len(runes) <= QuickLabelMaxRunes {
		return cleanAndCapitalizeLabel(text)
	}

	return truncateAtWordBoundary(text, QuickLabelMaxRunes)
}

// FormatQuickLabel cleans, capitalizes and bounds an explicit label provided by the model.
func FormatQuickLabel(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"'“„”[](){}.:;,-_`)
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return ""
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	s = string(r)
	if len(r) > QuickLabelMaxRunes {
		s = truncateAtWordBoundary(s, QuickLabelMaxRunes)
	}
	return s
}

func cleanAndCapitalizeLabel(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"'“„”[](){}`)
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return ""
	}

	endsInQuestion := strings.HasSuffix(s, "?") || strings.HasSuffix(s, "？")
	endsInExclamation := strings.HasSuffix(s, "!") || strings.HasSuffix(s, "！")
	s = strings.TrimRight(s, ",-—:;.")
	s = strings.TrimSpace(s)

	if endsInQuestion && !strings.HasSuffix(s, "?") && !strings.HasSuffix(s, "？") {
		s += "?"
	} else if endsInExclamation && !strings.HasSuffix(s, "!") && !strings.HasSuffix(s, "！") {
		s += "!"
	}

	r := []rune(s)
	if len(r) > 0 {
		r[0] = unicode.ToUpper(r[0])
	}
	return string(r)
}

func truncateAtWordBoundary(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return cleanAndCapitalizeLabel(s)
	}

	target := maxRunes - 1
	sub := string(runes[:target])
	if lastSpace := strings.LastIndex(sub, " "); lastSpace >= quickLabelMinRunes {
		cut := strings.TrimRight(sub[:lastSpace], ",-—:;.?!…")
		return cleanAndCapitalizeLabel(cut) + "…"
	}

	return cleanAndCapitalizeLabel(string(runes[:target])) + "…"
}

func findNaturalClause(text string, maxRunes int) (string, bool) {
	runes := []rune(text)
	// 1. Sentence ends: '?', '!', '\n', '？', '！', '.', '。'
	for i, r := range runes {
		if i+1 > maxRunes {
			break
		}
		if r == '?' || r == '!' || r == '\n' || r == '？' || r == '！' {
			clause := strings.TrimSpace(string(runes[:i+1]))
			if len([]rune(clause)) >= quickLabelMinRunes {
				return clause, true
			}
		}
		if (r == '.' || r == '。') && (i+1 == len(runes) || unicode.IsSpace(runes[i+1]) || runes[i+1] == '"' || runes[i+1] == '”' || r == '。') {
			clause := strings.TrimSpace(string(runes[:i]))
			if len([]rune(clause)) >= quickLabelMinRunes {
				return clause, true
			}
		}
	}

	// 2. Strong punctuation breaks: '—', '--', ' - ', ';', ':'
	for i, r := range runes {
		if i+1 > maxRunes {
			break
		}
		if r == ';' || r == ':' || r == '—' {
			clause := strings.TrimSpace(string(runes[:i]))
			if len([]rune(clause)) >= quickLabelMinRunes {
				return clause, true
			}
		}
	}
	for _, dash := range []string{" -- ", " - "} {
		if idx := strings.Index(text, dash); idx > 0 {
			clause := strings.TrimSpace(text[:idx])
			if len([]rune(clause)) >= quickLabelMinRunes && len([]rune(clause)) <= maxRunes {
				return clause, true
			}
		}
	}

	// 3. Clause comma breaks: ', ', '，', '、'
	commaSeparators := []string{", ", "，", "、"}
	earliestIdx := -1
	for _, sep := range commaSeparators {
		idx := strings.Index(text, sep)
		if idx > 0 {
			clause := strings.TrimSpace(text[:idx])
			cRunes := len([]rune(clause))
			if cRunes >= quickLabelMinRunes && cRunes <= maxRunes {
				if earliestIdx == -1 || idx < earliestIdx {
					earliestIdx = idx
				}
			}
		}
	}
	if earliestIdx != -1 {
		clause := strings.TrimSpace(text[:earliestIdx])
		return clause, true
	}

	return "", false
}

// DraftFromQuickReply starts a reply draft to messageID with r's HTML,
// sanitized again, at the start of the editable part, above the quote and
// signature.
func (s *Service) DraftFromQuickReply(ctx context.Context, messageID int64, r QuickReply) (Draft, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return Draft{}, err
	}
	defer end()
	rows, err := s.db.Query(ctx, "SELECT account_id FROM messages WHERE id = ?", messageID)
	if err != nil {
		return Draft{}, err
	}
	if len(rows.Values) == 0 {
		return Draft{}, fmt.Errorf("message %d not found", messageID)
	}
	account := store.Int(rows.Values[0][0])
	if err := s.bind(ctx, account); err != nil {
		return Draft{}, err
	}
	d, err := s.NewDraft(ctx, account, "reply", messageID)
	if err != nil {
		return Draft{}, err
	}
	// NewDraft opens a reply with an empty paragraph for the user's text.
	d.HTML = cleanHTML(r.HTML) + strings.TrimPrefix(d.HTML, "<p></p>")
	return s.SaveDraft(ctx, d)
}
