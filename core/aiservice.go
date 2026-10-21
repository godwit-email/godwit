package core

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/godwit-email/godwit/core/ai"
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

	key := ""
	if t.Kind == "message" || t.Kind == "thread" {
		key = cacheKey(sk.Name, sk.Version, t.Kind, t.ID, members, model.ServiceID, model.Version, model.Model, lang, keep)
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

// QuickReply is one suggested reply: sanitized HTML and a plain-text preview.
type QuickReply struct{ Preview, HTML string }

const (
	maxQuickReplies    = 3
	quickPreviewRunes  = 80
	quickRepliesSystem = `Suggest up to three short, distinct replies the user could send to the email you are given, written as the user. ` +
		`Answer with JSON only, exactly in this shape: {"replies":[{"html":"<p>...</p>"}]}. Use simple HTML (paragraphs, line breaks). ` +
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
		}
		return rs, err
	}

	items, err := s.mailContext(ctx, &gateway.Scope{}, ids, mailBudget{PerMessage: defaultMailBudget.PerMessage, Total: defaultMailBudget.Total, ShortURLs: true})
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
	e, err := s.aiEngine()
	if err != nil {
		return nil, err
	}
	events, wait := e.Run(ctx, ai.Request{Model: model, System: system, Prompt: strings.Join(blocks, "\n\n"), MaxTurns: 1})
	var text strings.Builder
	for ev := range events {
		switch ev.Kind {
		case "text":
			text.WriteString(ev.Text)
			continue
		case "error":
			s.runRejected(model, wait().Err)
			return nil, ev.Err
		}
		wait() // no tools: anything else is done
	}

	rs, err := parseQuickReplies(text.String())
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(rs)
	s.cachePut(ctx, key, account, string(b), "quick_replies")
	return rs, nil
}

// parseQuickReplies reads {"replies":[{"html":...}]}, optionally inside a
// markdown code fence, keeping the first three non-empty replies, sanitized.
func parseQuickReplies(out string) ([]QuickReply, error) {
	out = strings.TrimSpace(out)
	if rest, ok := strings.CutPrefix(out, "```"); ok {
		rest = strings.TrimPrefix(rest, "json")
		out = strings.TrimSuffix(strings.TrimSpace(rest), "```")
	}
	// Pointers tell a missing or null field from an empty one: only a real
	// array of objects with string html is an answer (an empty array is one).
	var v struct {
		Replies *[]*struct {
			HTML *string `json:"html"`
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
		h := cleanHTML(*r.HTML)
		p := strings.Join(strings.Fields(mail.HTMLToText(h)), " ")
		if p == "" {
			continue
		}
		if runes := []rune(p); len(runes) > quickPreviewRunes {
			p = string(runes[:quickPreviewRunes])
		}
		if rs = append(rs, QuickReply{Preview: p, HTML: h}); len(rs) == maxQuickReplies {
			break
		}
	}
	return rs, nil
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
