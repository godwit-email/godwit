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
}

const (
	noTranslation  = "NO_TRANSLATION"
	sentinelBuffer = 32 // runes a translate run holds back before forwarding text
)

// RunSkill runs an enabled skill on t with no tools. The first event is
// "context": Text is a JSON list of {message_id, suspect} for the mail the
// model sees, so the UI can flag suspected prompt injection. message and
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
	model, err := s.resolveModel(ctx, sk.Model)
	if err != nil {
		return nil, err
	}

	// Target: its account, members, and content version (which also proves it still exists).
	where, arg := "id = ?", t.ID
	if t.Kind == "thread" {
		where = "thread_id = ?"
	}
	rows, err := s.db.Query(ctx, "SELECT id, account_id, subject, body_text, body_html FROM messages WHERE "+where+" ORDER BY date, id", arg)
	if err != nil {
		return nil, err
	}
	if len(rows.Values) == 0 {
		return nil, fmt.Errorf("%s %d not found", t.Kind, t.ID)
	}
	if t.Kind == "selection" && strings.TrimSpace(t.Text) == "" {
		return nil, errors.New("selection is empty")
	}
	account := store.Int(rows.Values[0][1])
	var ids []int64
	type member struct {
		ID   int64
		Hash string // sha256 of subject and bodies
	}
	var members []member // sorted by ID
	for _, r := range rows.Values {
		ids = append(ids, store.Int(r[0]))
		sum := sha256.Sum256([]byte(store.Str(r[2]) + "\x00" + store.Str(r[3]) + "\x00" + store.Str(r[4])))
		members = append(members, member{store.Int(r[0]), hex.EncodeToString(sum[:])})
	}
	slices.SortFunc(members, func(a, b member) int { return cmp.Compare(a.ID, b.ID) })
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
	ctxEvent := ai.Event{Kind: "context", Text: string(b)}

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
		b, _ := json.Marshal([]any{sk.Name, sk.Version, t.Kind, t.ID, members,
			model.ServiceID, model.Version, model.Model, lang, keep})
		sum := sha256.Sum256(b)
		key = hex.EncodeToString(sum[:])
		rows, err := s.db.Query(ctx, "SELECT output FROM ai_cache WHERE key = ?", key)
		if err != nil {
			return nil, err
		}
		if len(rows.Values) > 0 {
			out := make(chan ai.Event, 3)
			out <- ctxEvent
			if o := store.Str(rows.Values[0][0]); o != "" {
				out <- ai.Event{Kind: "text", Text: o}
			}
			out <- ai.Event{Kind: "done"}
			close(out)
			end()
			return out, nil
		}
	}

	// Skills get no tools; the run itself is audited once.
	if err := gateway.New(s.db).Record(ctx, gateway.SourceSkill, "skill:"+name, t.Kind, t.ID, "run"); err != nil {
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
			wait()
			if ev.Kind == "done" {
				o := text.String()
				if translate && strings.TrimSpace(o) == noTranslation {
					o = "" // cached as "needs no translation"
				} else if holding && o != "" {
					send(ai.Event{Kind: "text", Text: o})
				}
				if key != "" {
					// The user has the output: a failed write only costs a later miss.
					if _, err := s.db.Execute(ctx, "INSERT OR REPLACE INTO ai_cache(key, account_id, output, created_at) VALUES (?, ?, ?, ?)",
						key, account, o, time.Now().Unix()); err != nil {
						slog.Default().Warn("ai cache write failed", "skill", name, "err", err)
					}
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
