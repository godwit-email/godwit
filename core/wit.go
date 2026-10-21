package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	netmail "net/mail"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/godwit-email/godwit/core/store"
)

// askTools are the built-in tools of an Ask run, which also offers every
// running plugin's tools and the enabled MCP servers' tools. None of them sends mail.
var askTools = []string{"search_mail", "get_message", "get_thread", "read_attachment", "classify_mail", "list_skills", "use_skill", "read_skill_file", "create_draft"}

const (
	askSystem = `You are Wit, the assistant of the Godwit mail app. Use the tools to search and read the user's mail and to use skills. ` +
		`You cannot send mail: create_draft only proposes a draft, which the user approves and sends themselves. ` +
		`Each mail a tool returns has a "ref" number, the same for that mail through the whole conversation. ` +
		`When your answer draws on a mail, cite it right after the claim as [ref], for example [2]. Cite only refs the tools returned.`
	titleRunes      = 40
	maxSearchResult = 20
	languageClues   = 3 // latest subjects from each recipient
)

// Ask runs input in the "ask" conversation with the mailbox tools. Mail the
// tools show the model comes back as Citations on their tool_result events;
// create_draft calls become approvals. The first input titles the
// conversation. The channel ends with done, error or interrupted (see startRun).
func (s *Service) Ask(ctx context.Context, conversationID int64, input string) (<-chan WitEvent, error) {
	return s.AskWith(ctx, conversationID, input, nil)
}

// AskWith is Ask with the mail the user quoted (message IDs, at most
// maxQuoted after dedupe): the run reads it through mailContext, in its scope,
// and the model gets it before the question, cited like the tools' mail (a
// "context" event first). An ID that does not exist fails AskWith.
func (s *Service) AskWith(ctx context.Context, conversationID int64, input string, quoted []int64) (<-chan WitEvent, error) {
	return s.askAt(ctx, conversationID, input, quoted, -1)
}

// AskAgain is AskWith asked in place of the question at Messages[at]: the
// conversation is rewound to before it (RewindConversation) only once
// everything else that can refuse the question checked out (launch), so a
// refused question leaves the conversation as it was.
func (s *Service) AskAgain(ctx context.Context, conversationID int64, at int, input string, quoted []int64) (<-chan WitEvent, error) {
	if at < 0 {
		return nil, fmt.Errorf("no question at %d", at)
	}
	return s.askAt(ctx, conversationID, input, quoted, at)
}

// askAt is AskWith, rewinding to at first unless it is -1.
func (s *Service) askAt(ctx context.Context, conversationID int64, input string, quoted []int64, at int) (<-chan WitEvent, error) {
	if strings.TrimSpace(input) == "" {
		return nil, errors.New("the question is empty")
	}
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	quoted, err = s.quotedIDs(ctx, quoted)
	var conv Conversation
	if err == nil {
		conv, _, err = s.conversation(ctx, conversationID)
	}
	if err == nil && conv.Kind != "ask" {
		err = fmt.Errorf("conversation %d is not an ask conversation", conversationID)
	}
	title := input
	if r := []rune(title); len(r) > titleRunes {
		title = string(r[:titleRunes])
	}
	// The title is saved once the run is claimed and before the run takes
	// over end (with the rewind, if any), so Close waits for both and a
	// failure fails Ask.
	setTitle := sqlite.Statement{SQL: "UPDATE conversations SET title = ? WHERE id = ? AND title = ''", Args: []any{title, conversationID}}
	var w *witStream
	if err == nil {
		w, err = s.launch(ctx, end, conv, input, slices.Concat(s.registered(askTools...), s.pluginTools(true), s.mcpTools(ctx, true)), quoted, at, setTitle)
	}
	if err != nil {
		end()
		return nil, err
	}
	return w.ch, nil
}

// maxQuoted caps the messages a question quotes.
const maxQuoted = 10

// quotedIDs is ids without repeats, each an existing message.
func (s *Service) quotedIDs(ctx context.Context, ids []int64) ([]int64, error) {
	var out []int64
	for _, id := range ids {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	if len(out) > maxQuoted {
		return nil, fmt.Errorf("quote at most %d emails", maxQuoted)
	}
	for _, id := range out {
		rows, err := s.db.Query(ctx, "SELECT 1 FROM messages WHERE id = ?", id)
		if err != nil {
			return nil, err
		}
		if len(rows.Values) == 0 {
			return nil, fmt.Errorf("message %d %w", id, errMailNotFound)
		}
	}
	return out, nil
}

// quote is the model's context of the quoted mail ids for job: read in its
// scope and numbered as the context's citations (job.quoted).
func (s *Service) quote(ctx context.Context, job *runJob, ids []int64) (string, error) {
	items, err := s.mailContext(ctx, job.scope, ids, defaultMailBudget)
	if err != nil {
		return "", err
	}
	job.quoted = s.number(ctx, job, "", citations(items))
	b, err := json.Marshal(blocks(items, job.quoted))
	if err != nil {
		return "", err
	}
	return "The user quoted these emails for the question below; cite them by their ref:\n" + string(b), nil
}

// witSystem is the system prompt of a run's requests, started or resumed: for
// ask the same for every run of the conversation; for assist built from the
// draft as it is (assistSystemPrompt).
func (s *Service) witSystem(ctx context.Context, job *runJob, tools []toolDesc) (string, error) {
	switch job.conv.Kind {
	case "assist":
		return s.assistSystemPrompt(ctx, job, tools)
	case "ask":
	default:
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

// cite numbers and records the mail a read tool showed the model, for its
// tool_result event; the citations line up with items.
func (s *Service) cite(ctx context.Context, items []mailItem) []Citation {
	job, call := runOf(ctx), ai.CallID(ctx)
	cs := s.number(ctx, job, call, citations(items))
	job.cites.Store(call, cs)
	return cs
}

func citations(items []mailItem) []Citation {
	cs := make([]Citation, 0, len(items))
	for _, it := range items {
		cs = append(cs, Citation{MessageID: it.MessageID, Subject: it.Subject, From: it.From, Date: it.Date, Suspect: it.Suspect, Quote: it.Quote})
	}
	return cs
}

// blocks is what the model sees of items; ref is the citation number (cs[i].N) it cites them by.
func blocks(items []mailItem, cs []Citation) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for i, it := range items {
		out = append(out, map[string]any{"message_id": it.MessageID, "ref": cs[i].N, "mail": it.Block})
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
		s.mustRegister(toolDesc{Spec: ai.ToolSpec{Name: name, Description: desc, Schema: schema, Run: run}})
	}
	read("search_mail", "Search the user's mail. Returns subject, sender, date and a snippet of each match, newest first.",
		object(map[string]any{"query": stringProp, "account_id": intProp,
			"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": maxSearchResult}}, "query"),
		s.searchMail)
	read("get_message", "Read one message by its message_id.", object(map[string]any{"id": intProp}, "id"), s.getMessage)
	read("read_attachment", "Read the text of one attachment by its attachment id (listed with the mail): PDF, Word, Excel, PowerPoint, CSV, HTML or plain text. Images and archives are not readable.",
		object(map[string]any{"id": intProp}, "id"), s.readAttachment)
	read("get_thread", "Read every message of a thread by its thread_id.", object(map[string]any{"id": intProp}, "id"), s.getThread)
	// No Egress: the judge is a model service, so its traffic is model traffic,
	// as for the run itself (review #17).
	read("classify_mail", "Ask typed questions about many messages at once: a predicate (the probability it holds), one choice of a list, or a score on levels. "+
		"Use it for bulk questions over messages you found (is it a newsletter, which project, how urgent) instead of reading each one. "+
		"Returns each message's answers and ref; it does not return the mail text.",
		object(map[string]any{
			"ids": map[string]any{"type": "array", "items": intProp, "minItems": 1, "maxItems": maxClassifyIDs},
			"questions": map[string]any{"type": "array", "minItems": 1, "maxItems": plugins.MaxQuestions, "items": object(map[string]any{
				"name":         map[string]any{"type": "string", "pattern": "^[a-z][a-z0-9_]{0,40}$"},
				"type":         map[string]any{"type": "string", "enum": []string{"predicate", "choice", "score"}},
				"instructions": stringProp,
				"choices":      choicesProp, "levels": levelsProp,
			}, "name", "type", "instructions")},
		}, "ids", "questions"),
		s.classifyMail)
	read("list_skills", "List the skills you can use, with what each is for.", object(map[string]any{}), s.listSkills)
	read("use_skill", "Load a skill's instructions and the list of its reference files.",
		object(map[string]any{"name": stringProp}, "name"), s.useSkill)
	read("read_skill_file", "Read a file of a skill, under references/ or assets/.",
		object(map[string]any{"name": stringProp, "path": stringProp}, "name", "path"), s.readSkillFile)
	s.mustRegister(toolDesc{Spec: ai.ToolSpec{Name: "create_draft",
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
	res := blocks(items, s.cite(ctx, items))
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
	return blocks(items, s.cite(ctx, items))[0], nil
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
	return map[string]any{"thread_id": in.ID, "messages": blocks(items, s.cite(ctx, items))}, nil
}

const (
	maxClassifyIDs    = 50
	maxClassifyGenIDs = 10 // when the default model judges: one generation per message
	classifyRunes     = 8000
	classifyQuestions = 16 << 10 // question bytes: with a message's state, within a judge's request limit
	classifyParallel  = 4
)

// classifyDeadline bounds a classify_mail call; tests shorten it.
var classifyDeadline = 2 * time.Minute

var (
	choicesProp = map[string]any{"type": "array", "items": object(map[string]any{"value": stringProp, "description": stringProp}, "value")}
	levelsProp  = map[string]any{"type": "array", "items": object(map[string]any{"label": stringProp, "description": stringProp}, "label")}
)

// classifyMail asks the judge the questions about each message alone, with
// its own body budget so none is starved. Questions come only from the
// arguments; the judge sees mail only as untrusted state, and only typed
// answers come back.
func (s *Service) classifyMail(ctx context.Context, args json.RawMessage) (any, error) {
	var in struct {
		IDs       []int64            `json:"ids"`
		Questions []plugins.Question `json:"questions"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	if len(in.IDs) == 0 || len(in.Questions) == 0 {
		return nil, errors.New("give at least one id and one question")
	}
	if len(in.Questions) > plugins.MaxQuestions {
		return nil, fmt.Errorf("at most %d questions", plugins.MaxQuestions)
	}
	if len(in.IDs) > maxClassifyIDs {
		return nil, fmt.Errorf("at most %d messages per call", maxClassifyIDs)
	}
	if plugins.QuestionBytes(in.Questions) > classifyQuestions {
		return nil, fmt.Errorf("the questions are too long: at most %d KB", classifyQuestions>>10)
	}
	qs := in.Questions
	if err := ai.ValidateQuestions(qs); err != nil {
		return nil, err
	}
	j, _, gen, err := s.resolveJudge(ctx)
	if err != nil {
		return nil, err
	}
	if gen && len(in.IDs) > maxClassifyGenIDs {
		return nil, fmt.Errorf("at most %d messages per call without a judge service", maxClassifyGenIDs)
	}
	res := make([]map[string]any, len(in.IDs))
	var items []mailItem
	var at []int // items[k] answers in.IDs[at[k]]
	for i, id := range in.IDs {
		it, err := s.mailContext(ctx, runOf(ctx).scope, []int64{id}, mailBudget{PerMessage: classifyRunes, Total: classifyRunes})
		if errors.Is(err, errMailNotFound) { // one gone message doesn't fail the others
			res[i] = map[string]any{"message_id": id, "error": "not found"}
			continue
		}
		if err != nil {
			return nil, err
		}
		items, at = append(items, it[0]), append(at, i)
	}
	cs := s.cite(ctx, items)
	ctx, cancel := context.WithTimeout(ctx, classifyDeadline)
	defer cancel()
	var wg sync.WaitGroup
	sem := make(chan struct{}, classifyParallel)
	for k, it := range items {
		i := at[k]
		res[i] = map[string]any{"message_id": it.MessageID, "ref": cs[k].N}
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			if as, err := j.Ask(ctx, it.Block, qs); err != nil {
				res[i]["error"] = err.Error()
			} else {
				res[i]["answers"] = as
			}
		})
	}
	wg.Wait()
	return map[string]any{"results": res}, nil
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
	if err := s.languageClues(ctx, in); err != nil {
		return prepared{}, err
	}
	// The approval records the final body, as NewDraft(new) would make it.
	d, err := s.buildDraft(ctx, in.AccountID, "new", 0)
	if err != nil {
		return prepared{}, err
	}
	if in.HTML, err = s.sanitizeDraft(ctx, mail.StripMarkers(in.HTML)+d.HTML); err != nil {
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
		ms, err := s.mailWith(ctx, in.AccountID, k, languageClues, []string{"from_addr"},
			func(m MessageSummary) bool { return mail.AddrKey(m.From) == k })
		if err != nil {
			return err
		}
		from = append(from, ms...)
	}
	if len(from) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("Not created yet. Write the draft in the language of the recipient's past emails to the user. " +
		"Subjects of their latest emails, as language clues:\n")
	items := s.subjectContext(job.scope, from)
	s.cite(ctx, items)
	for _, it := range items {
		b.WriteString(it.Block + "\n")
	}
	b.WriteString("Call create_draft again with the body in that language.")
	return errors.New(b.String())
}

func likePattern(s string) string {
	return "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(s) + "%"
}

// mailWith returns the account's latest n messages, newest first, that match
// says involve addr. A LIKE on cols only narrows the candidates; match is
// checked here, page by page, until n match or none are left.
// ponytail: LIKE scan over the account's mail; index addresses if it gets slow
func (s *Service) mailWith(ctx context.Context, accountID int64, addr string, n int, cols []string, match func(MessageSummary) bool) ([]MessageSummary, error) {
	var conds []string
	args := []any{accountID}
	for _, c := range cols {
		conds = append(conds, "m."+c+` LIKE ? ESCAPE '\'`)
		args = append(args, likePattern(addr))
	}
	q := "SELECT " + msgCols + " FROM messages m WHERE m.account_id = ? AND (" + strings.Join(conds, " OR ") +
		") ORDER BY m.date DESC, m.id DESC LIMIT ? OFFSET ?"
	const page = 50
	var out []MessageSummary
	for off := 0; len(out) < n; off += page {
		rows, err := s.db.Query(ctx, q, append(args, page, off)...)
		if err != nil {
			return nil, err
		}
		for _, r := range rows.Values {
			if m := scanSummary(r); len(out) < n && match(m) {
				out = append(out, m)
			}
		}
		if len(rows.Values) < page {
			break
		}
	}
	return out, nil
}

// applyDraft inserts the approved snapshot as it is, linked to its approval.
func (s *Service) applyDraft(_ context.Context, a Approval) ([]sqlite.Statement, any, any, error) {
	var in draftArgs
	if err := json.Unmarshal(a.Args, &in); err != nil {
		return nil, nil, nil, err
	}
	return []sqlite.Statement{{SQL: `INSERT INTO drafts(account_id, mode, to_addrs, cc_addrs, bcc_addrs, reply_to, subject, body_html,
		updated_at, ai_approval_id) VALUES (?, 'new', ?, ?, '[]', '', ?, ?, ?, ?)`,
			Args: []any{in.AccountID, encodeList(in.To), encodeList(in.Cc), in.Subject, in.HTML, time.Now().Unix(), a.ID}}},
		map[string]any{"created": true, "subject": in.Subject}, nil, nil
}
