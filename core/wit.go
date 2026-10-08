package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	netmail "net/mail"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/store"
)

// askTools are the tools of an Ask run. None of them sends mail.
var askTools = []string{"search_mail", "get_message", "get_thread", "list_skills", "use_skill", "read_skill_file", "create_draft"}

const (
	askSystem = `You are Wit, the assistant of the Godwit mail app. Use the tools to search and read the user's mail and to use skills. ` +
		`You cannot send mail: create_draft only proposes a draft, which the user approves and sends themselves.`
	titleRunes      = 40
	maxSearchResult = 20
	languageClues   = 3 // latest subjects from each recipient
)

// Ask runs input in the "ask" conversation with the mailbox tools. Mail the
// tools show the model comes back as Citations on their tool_result events;
// create_draft calls become approvals. The first input titles the
// conversation. The channel ends with done, error or interrupted (see startRun).
func (s *Service) Ask(ctx context.Context, conversationID int64, input string) (<-chan WitEvent, error) {
	if strings.TrimSpace(input) == "" {
		return nil, errors.New("the question is empty")
	}
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	conv, _, err := s.conversation(ctx, conversationID)
	if err == nil && conv.Kind != "ask" {
		err = fmt.Errorf("conversation %d is not an ask conversation", conversationID)
	}
	var w *witStream
	if err == nil {
		w, err = s.launch(ctx, end, conv, input, s.registered(askTools...))
	}
	if err != nil {
		end()
		return nil, err
	}
	title := strings.Join(strings.Fields(input), " ")
	if r := []rune(title); len(r) > titleRunes {
		title = string(r[:titleRunes])
	}
	if _, err := s.db.Execute(context.WithoutCancel(ctx), "UPDATE conversations SET title = ? WHERE id = ? AND title = ''", title, conv.ID); err != nil {
		// The run is under way; a missing title only shows as untitled.
		slog.Default().Warn("could not title a conversation", "conversation", conv.ID, "err", err)
	}
	return w.ch, nil
}

// witSystem is a conversation's system prompt, the same for every run of it,
// started or resumed.
func (s *Service) witSystem(ctx context.Context, conv Conversation) (string, error) {
	if conv.Kind != "ask" {
		return ai.UntrustedSystemNote, nil
	}
	st, err := s.loadSettings(ctx)
	if err != nil {
		return "", err
	}
	lang := s.readingLanguage(ctx)
	drafts := "Write drafts in " + lang + "."
	if st.ReplyLanguage == "match" {
		drafts = "Write a new email in the language of the recipient's past emails to the user; if there are none, use " + lang + "."
	}
	return askSystem + "\n\n" + ai.UntrustedSystemNote + "\n\nAnswer the user in " + lang + ". " + drafts, nil
}

// cite records the mail a read tool showed the model, for its tool_result event.
func cite(ctx context.Context, items []mailItem) {
	cs := make([]Citation, 0, len(items))
	for _, it := range items {
		cs = append(cs, Citation{MessageID: it.MessageID, Subject: it.Subject, From: it.From, Suspect: it.Suspect})
	}
	runOf(ctx).cites.Store(ai.CallID(ctx), cs)
}

func blocks(items []mailItem) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, map[string]any{"message_id": it.MessageID, "mail": it.Block})
	}
	return out
}

func object(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

var (
	intProp    = map[string]any{"type": "integer"}
	stringProp = map[string]any{"type": "string"}
	listProp   = map[string]any{"type": "array", "items": stringProp}
)

// registerAskTools offers Ask's tools to Wit runs.
func (s *Service) registerAskTools() {
	read := func(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (any, error)) {
		s.registerTool(registeredTool{Spec: ai.ToolSpec{Name: name, Description: desc, Schema: schema, Run: run}})
	}
	read("search_mail", "Search the user's mail. Returns subject, sender, date and a snippet of each match, newest first.",
		// No minimum/maximum on limit: arguments that fail the schema end the
		// whole run, so an out-of-range limit is clamped instead.
		object(map[string]any{"query": stringProp, "account_id": intProp,
			"limit": map[string]any{"type": "integer", "description": "at most 20"}}, "query"),
		s.searchMail)
	read("get_message", "Read one message by its message_id.", object(map[string]any{"id": intProp}, "id"), s.getMessage)
	read("get_thread", "Read every message of a thread by its thread_id.", object(map[string]any{"id": intProp}, "id"), s.getThread)
	read("list_skills", "List the skills you can use, with what each is for.", object(map[string]any{}), s.listSkills)
	read("use_skill", "Load a skill's instructions and the list of its reference files.",
		object(map[string]any{"name": stringProp}, "name"), s.useSkill)
	read("read_skill_file", "Read a file of a skill, under references/ or assets/.",
		object(map[string]any{"name": stringProp, "path": stringProp}, "name", "path"), s.readSkillFile)
	s.registerTool(registeredTool{Spec: ai.ToolSpec{Name: "create_draft",
		Description: "Propose a new draft. The user approves it before it is saved, and sends it themselves. html is the body; the signature is added.",
		Schema: object(map[string]any{"account_id": intProp, "to": listProp, "cc": listProp, "subject": stringProp, "html": stringProp},
			"to", "subject", "html")},
		Write: true, Exec: &executor{Prepare: s.prepareDraft, Apply: s.applyDraft}})
}

func (s *Service) searchMail(ctx context.Context, args json.RawMessage) (any, error) {
	var in struct {
		Query     string `json:"query"`
		AccountID int64  `json:"account_id"`
		Limit     int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	if in.Limit <= 0 || in.Limit > maxSearchResult {
		in.Limit = maxSearchResult
	}
	list, err := s.Search(ctx, in.AccountID, in.Query, in.Limit)
	if err != nil {
		return nil, err
	}
	items := s.summaryContext(runOf(ctx).scope, list)
	cite(ctx, items)
	res := blocks(items)
	for i, m := range list {
		res[i]["thread_id"] = m.ThreadID
	}
	return map[string]any{"results": res}, nil
}

type idArgs struct {
	ID int64 `json:"id"`
}

func (s *Service) getMessage(ctx context.Context, args json.RawMessage) (any, error) {
	var in idArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	items, err := s.mailContext(ctx, runOf(ctx).scope, []int64{in.ID}, defaultMailBudget)
	if err != nil {
		return nil, err
	}
	cite(ctx, items)
	return blocks(items)[0], nil
}

func (s *Service) getThread(ctx context.Context, args json.RawMessage) (any, error) {
	var in idArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	_, ids, _, err := s.mailTarget(ctx, "thread", in.ID)
	if err != nil {
		return nil, err
	}
	items, err := s.mailContext(ctx, runOf(ctx).scope, ids, defaultMailBudget)
	if err != nil {
		return nil, err
	}
	cite(ctx, items)
	return map[string]any{"thread_id": in.ID, "messages": blocks(items)}, nil
}

// listSkills lists the enabled skills without a UI binding, by name.
func (s *Service) listSkills(ctx context.Context, _ json.RawMessage) (any, error) {
	ok, _, err := s.loadSkills()
	if err != nil {
		return nil, err
	}
	off, err := s.disabledSkills(ctx)
	if err != nil {
		return nil, err
	}
	out := []map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(ok)) {
		if sk := ok[name]; len(sk.On) == 0 && !slices.Contains(off, name) {
			out = append(out, map[string]string{"name": name, "description": sk.Description})
		}
	}
	return map[string]any{"skills": out}, nil
}

func (s *Service) useSkill(ctx context.Context, args json.RawMessage) (any, error) {
	var in struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	sk, err := s.skill(ctx, in.Name)
	if err != nil {
		return nil, err
	}
	refs, err := sk.References()
	if err != nil {
		return nil, err
	}
	return map[string]any{"name": sk.Name, "instructions": sk.Body, "references": refs}, nil
}

func (s *Service) readSkillFile(ctx context.Context, args json.RawMessage) (any, error) {
	var in struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	sk, err := s.skill(ctx, in.Name)
	if err != nil {
		return nil, err
	}
	b, err := sk.ReadFile(in.Path)
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(b) {
		return nil, fmt.Errorf("%s is not a text file", in.Path)
	}
	return map[string]any{"path": in.Path, "content": string(b)}, nil
}

// draftArgs are create_draft's arguments; prepared, they are the final ones.
type draftArgs struct {
	AccountID int64    `json:"account_id"`
	To        []string `json:"to"`
	Cc        []string `json:"cc"`
	Subject   string   `json:"subject"`
	HTML      string   `json:"html"`
}

// prepareDraft resolves the account (the default one if none is given),
// checks the recipients and sanitizes the HTML. With ReplyLanguage "match",
// the first call to recipients the user has mail from fails with the
// subjects of their latest mail as language clues, and the model calls again.
func (s *Service) prepareDraft(ctx context.Context, _ Conversation, args json.RawMessage) (prepared, error) {
	var in draftArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return prepared{}, err
	}
	if in.AccountID == 0 {
		rows, err := s.db.Query(ctx, "SELECT id FROM accounts ORDER BY is_default DESC, id LIMIT 1")
		if err != nil {
			return prepared{}, err
		}
		if len(rows.Values) == 0 {
			return prepared{}, errors.New("there is no mail account")
		}
		in.AccountID = store.Int(rows.Values[0][0])
	} else if _, err := s.account(ctx, in.AccountID); err != nil {
		return prepared{}, err
	}
	if len(in.To) == 0 {
		return prepared{}, errors.New("to needs at least one recipient")
	}
	for _, a := range slices.Concat(in.To, in.Cc) {
		if _, err := netmail.ParseAddress(a); err != nil {
			return prepared{}, fmt.Errorf("invalid address %q", a)
		}
	}
	in.HTML = mail.SanitizeCompose(in.HTML)
	if err := s.languageClues(ctx, in); err != nil {
		return prepared{}, err
	}
	b, err := json.Marshal(in)
	if err != nil {
		return prepared{}, err
	}
	return prepared{Args: b, Summary: "New draft to " + strings.Join(in.To, ", ") + ": " + in.Subject,
		TargetKind: "account", TargetID: in.AccountID}, nil
}

// languageClues returns an error carrying the clues the first time a run
// drafts to in.To under ReplyLanguage "match", if the user has mail from them.
func (s *Service) languageClues(ctx context.Context, in draftArgs) error {
	st, err := s.loadSettings(ctx)
	if err != nil || st.ReplyLanguage != "match" {
		return err
	}
	keys := make([]string, 0, len(in.To))
	for _, a := range in.To {
		keys = append(keys, mail.AddrKey(a))
	}
	slices.Sort(keys)
	job := runOf(ctx)
	if _, shown := job.clued.LoadOrStore(strings.Join(keys, ","), true); shown {
		return nil
	}
	var from []MessageSummary
	for _, k := range slices.Compact(keys) {
		like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(k) + "%"
		// ponytail: LIKE scan over the account's mail; index from addresses if it gets slow
		rows, err := s.db.Query(ctx, "SELECT "+msgCols+` FROM messages m WHERE m.account_id = ? AND m.from_addr LIKE ? ESCAPE '\'
			ORDER BY m.date DESC, m.id DESC LIMIT 50`, in.AccountID, like)
		if err != nil {
			return err
		}
		n := 0
		for _, r := range rows.Values {
			if m := scanSummary(r); n < languageClues && mail.AddrKey(m.From) == k {
				from = append(from, m)
				n++
			}
		}
	}
	if len(from) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("Not created yet. Write the draft in the language of the recipient's past emails to the user. " +
		"Subjects of their latest emails, as language clues:\n")
	for _, it := range s.subjectContext(job.scope, from) {
		b.WriteString(it.Block + "\n")
	}
	b.WriteString("Call create_draft again with the body in that language.")
	return errors.New(b.String())
}

// applyDraft inserts the approved draft like NewDraft(new) would, with the
// signature, linked to its approval.
func (s *Service) applyDraft(ctx context.Context, a Approval) ([]sqlite.Statement, any, any, error) {
	var in draftArgs
	if err := json.Unmarshal(a.Args, &in); err != nil {
		return nil, nil, nil, err
	}
	d, err := s.buildDraft(ctx, in.AccountID, "new", 0)
	if err != nil {
		return nil, nil, nil, err
	}
	html := mail.SanitizeCompose(in.HTML + d.HTML)
	return []sqlite.Statement{{SQL: `INSERT INTO drafts(account_id, mode, to_addrs, cc_addrs, bcc_addrs, reply_to, subject, body_html,
		updated_at, ai_approval_id) VALUES (?, 'new', ?, ?, '[]', '', ?, ?, ?, ?)`,
			Args: []any{in.AccountID, encodeList(in.To), encodeList(in.Cc), in.Subject, html, time.Now().Unix(), a.ID}}},
		map[string]any{"created": true, "subject": in.Subject}, nil, nil
}
