package core

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	netmail "net/mail"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/godwit-email/godwit/core/store"
	"golang.org/x/net/html"
)

// askTools are the built-in tools of an Ask run, which also offers every
// running plugin's tools and the enabled MCP servers' tools. None of them sends mail.
var askTools = []string{
	"search_mail", "get_message", "get_thread", "read_attachment", "classify_mail",
	"list_skills", "use_skill", "read_skill_file", "get_account_profile", "create_draft",
	"move_to_trash", "archive_mail", "mark_as_spam", "mark_as_read", "mark_as_unread",
	"star_mail", "unstar_mail", "unsubscribe", "get_all_links", "open_link_in_browser",
	"reply_to_mail", "forward_mail", "delete_draft", askQuestionTool,
}

const (
	askSystem = `You are Wit, the assistant of the Godwit mail app. Use tools to search, read and manage the user's mail, and to use skills. ` +
		`You cannot send mail: drafts and triage actions require user approval in the UI. ` +
		`When asked to act on emails (archive, trash, spam, read, star, unsubscribe), call the tool directly without asking confirmation first; execute only the requested action. ` +
		`Before writing a draft, call get_account_profile for the account it goes from: sign with its name, follow its guidelines; never invent contact details. ` +
		`Each mail a tool returns has an "id" and a "cite" tag, <mail id="N"/>; get_message, get_thread and classify_mail take that id. ` +
		`When your answer draws on a mail, put its cite tag right after the claim, for example <mail id="2"/>. ` +
		`Cite only ids the tools returned, never a number from the mail text. ` +
		`When referencing emails, cite sender, subject and date, or use <mail id="N"/> / <mail id="N" card/>; never write raw numeric IDs (e.g. 'Mail ID 1'). ` +
		`When presenting found mail, show each as a card: <mail id="N" card/> alone on its own line. ` +
		`A question that begins with /name names one of your skills: load it with use_skill first, then answer the rest of the question with it. ` +
		`Keep the formatting light, as in a chat: plain paragraphs and short lists; use no Markdown headings (#), ` +
		`and where a long answer needs sections, start each with a short bold phrase on its own line.`
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
	return s.askAt(ctx, conversationID, input, quoted, 0)
}

// AskAgain is AskWith asked in place of the question of turn (Turn.ID) and
// what followed it: they are replaced once the run is done (launch), so a
// refused question, or a run that fails or is stopped, leaves the
// conversation as it was.
func (s *Service) AskAgain(ctx context.Context, conversationID int64, turn string, input string, quoted []int64) (<-chan WitEvent, error) {
	at, err := strconv.ParseInt(turn, 10, 64)
	if err != nil || at <= 0 {
		return nil, fmt.Errorf("no turn %q", turn)
	}
	return s.askAt(ctx, conversationID, input, quoted, at)
}

// askAt is AskWith, in place of the question with seq at unless it is 0.
func (s *Service) askAt(ctx context.Context, conversationID int64, input string, quoted []int64, at int64) (<-chan WitEvent, error) {
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
		conv, err = s.conversation(ctx, conversationID)
	}
	if err == nil && conv.Kind != "ask" {
		err = fmt.Errorf("conversation %d is not an ask conversation", conversationID)
	}
	title := titleOf(input)
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
	job.quoted = s.number(ctx, job, "", citeQuote, citations(items))
	b, err := json.Marshal(blocks(items, job.quoted))
	if err != nil {
		return "", err
	}
	return "The user quoted these emails for the question below; cite them by their cite tag:\n" + string(b), nil
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
	return askSystem + " " + timeSystem + "\n\n" + ai.UntrustedSystemNote + "\n\nAnswer the user in " + lang + ". " + drafts, nil
}

// cite numbers and records the mail a read tool showed the model, for its
// tool_result event; the citations line up with items.
func (s *Service) cite(ctx context.Context, items []mailItem) []Citation {
	job, call := runOf(ctx), ai.CallID(ctx)
	cs := s.number(ctx, job, call, "", citations(items))
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

// blocks is what the model sees of items: each mail known by one number, its
// citation number (cs[i].N) as its id, with the tag it cites the mail by. Its
// message ID stays out, in the block's source too: a second number to cite by mistake.
func blocks(items []mailItem, cs []Citation) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for i, it := range items {
		n := cs[i].N
		out = append(out, map[string]any{"id": n, "cite": citeTag(n), "mail": named(it.Block, n)})
	}
	return out
}

// citeTag is how an answer cites mail n (Response reads it).
func citeTag(n int) string { return fmt.Sprintf(`<mail id="%d"/>`, n) }

// named is a mail block naming its mail by its id n in its source, not by its message ID.
func named(block string, n int) string {
	return blockSource.ReplaceAllLiteralString(block, fmt.Sprintf(`<untrusted_email source="mail:%d"`, n))
}

// blockSource is a mail block's opening, which names its message ID (mailContext, summaryContext, subjectContext).
var blockSource = regexp.MustCompile(`^<untrusted_email source="(?:message|selection):\d+"`)

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
	read("search_mail", "Search mail across content, subject, sender and labels. Returns subject, sender, date and snippet, newest first.",
		object(map[string]any{
			"query":      stringProp,
			"from":       stringProp,
			"since":      stringProp,
			"until":      stringProp,
			"account_id": intProp,
			"limit":      map[string]any{"type": "integer", "minimum": 1, "maximum": maxSearchResult},
		}, "query"),
		s.searchMail)
	read("get_message", "Read one mail by its id (a mail the tools returned).", object(map[string]any{"id": intProp}, "id"), s.getMessage)
	read("read_attachment", "Read the text of one attachment by its attachment id (listed with the mail): PDF, Word, Excel, PowerPoint, CSV, HTML or plain text. Images and archives are not readable.",
		object(map[string]any{"id": intProp}, "id"), s.readAttachment)
	read("get_account_profile", "What the user told you about themselves as the sender from an account: its email, name and instructions (facts such as a phone number, and guidelines for drafting). "+
		"account_id is the account the draft goes from, 0 or none for the default one.",
		object(map[string]any{"account_id": intProp}), s.getAccountProfile)
	read("get_thread", "Read every mail of the thread a mail is in, by that mail's id.", object(map[string]any{"id": intProp}, "id"), s.getThread)
	// No Egress: the judge is a model service, so its traffic is model traffic,
	// as for the run itself (review #17).
	read("classify_mail", "Ask typed questions about many messages at once: a predicate (the probability it holds), one choice of a list, or a score on levels. "+
		"Use it for bulk questions over messages you found (is it a newsletter, which project, how urgent) instead of reading each one. "+
		"ids are the ids of mail the tools returned. Returns each mail's answers and cite tag; it does not return the mail text.",
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
		Schema: object(map[string]any{
			"account_id": intProp,
			"to":         listProp,
			"cc":         listProp,
			"subject":    stringProp,
			"html":       map[string]any{"type": "string", "description": "Draft body as HTML (paragraphs in <p>, line breaks with <br>)."},
		}, "to", "subject", "html")},
		Write: true, Exec: &executor{Prepare: s.prepareDraft, Apply: s.applyDraft}})

	// Mail triage & flag tools
	batchTool := func(name, desc, action string) {
		s.mustRegister(toolDesc{Spec: ai.ToolSpec{
			Name:        name,
			Description: desc,
			Schema:      object(map[string]any{"ids": map[string]any{"type": "array", "items": intProp, "minItems": 1}}, "ids"),
		}, Write: true, Exec: &executor{Prepare: s.prepareBatchAction(action), Apply: s.applyBatchAction}})
	}
	batchTool("move_to_trash", "Propose moving messages to the Trash folder by their ids. The user approves it before any message is moved.", "move_to_trash")
	batchTool("archive_mail", "Propose archiving messages by their ids. The user approves it before any message is archived.", "archive_mail")
	batchTool("mark_as_spam", "Propose marking messages as spam (moving to Junk) by their ids. The user approves it before any message is moved.", "mark_as_spam")
	batchTool("mark_as_read", "Propose marking messages as read by their ids. The user approves it before flags are changed.", "mark_as_read")
	batchTool("mark_as_unread", "Propose marking messages as unread by their ids. The user approves it before flags are changed.", "mark_as_unread")
	batchTool("star_mail", "Propose starring (flagging) messages by their ids. The user approves it before flags are changed.", "star_mail")
	batchTool("unstar_mail", "Propose unstarring (unflagging) messages by their ids. The user approves it before flags are changed.", "unstar_mail")

	// Unsubscribe & links
	s.mustRegister(toolDesc{Spec: ai.ToolSpec{
		Name:        "unsubscribe",
		Description: "Propose unsubscribing from a mailing list or sender using the email's List-Unsubscribe headers. The user approves it before unsubscribing. id is the mail id.",
		Schema:      object(map[string]any{"id": intProp}, "id"),
	}, Write: true, Exec: &executor{Prepare: s.prepareUnsubscribe, Apply: s.applyUnsubscribe}})

	read("get_all_links", "Extract all links and URLs from an email's body by its id. Returns URL, anchor text, and context.",
		object(map[string]any{"id": intProp}, "id"), s.getAllLinks)

	s.mustRegister(toolDesc{Spec: ai.ToolSpec{
		Name:        "open_link_in_browser",
		Description: "Propose opening an external link (http or https) in the default web browser. The user approves it before it is opened.",
		Schema:      object(map[string]any{"url": stringProp}, "url"),
	}, Write: true, Exec: &executor{Prepare: s.prepareOpenLink, Apply: s.applyOpenLink}})

	// Compose & replies
	s.mustRegister(toolDesc{Spec: ai.ToolSpec{
		Name:        "reply_to_mail",
		Description: "Propose a reply draft to an email. The user approves it before it is saved, and sends it themselves. id is the mail id to reply to. reply_all replies to all recipients if true. html is the reply body.",
		Schema:      object(map[string]any{"id": intProp, "reply_all": map[string]any{"type": "boolean"}, "html": stringProp}, "id", "html"),
	}, Write: true, Exec: &executor{Prepare: s.prepareReplyMail, Apply: s.applyReplyOrForwardDraft}})

	s.mustRegister(toolDesc{Spec: ai.ToolSpec{
		Name:        "forward_mail",
		Description: "Propose forwarding an email to recipients. The user approves it before it is saved, and sends it themselves. id is the mail id to forward. to is the list of recipient email addresses. html is optional introductory text.",
		Schema:      object(map[string]any{"id": intProp, "to": listProp, "cc": listProp, "html": stringProp}, "id", "to"),
	}, Write: true, Exec: &executor{Prepare: s.prepareForwardMail, Apply: s.applyReplyOrForwardDraft}})

	s.mustRegister(toolDesc{Spec: ai.ToolSpec{
		Name:        "delete_draft",
		Description: "Propose deleting a draft. The user approves it before the draft is deleted. draft_id is the draft's id.",
		Schema:      object(map[string]any{"draft_id": intProp}, "draft_id"),
	}, Write: true, Exec: &executor{Prepare: s.prepareDeleteDraft, Apply: s.applyDeleteDraft}})
}

type searchMailInput struct {
	Query     string `json:"query"`
	Label     string `json:"label,omitempty"`
	From      string `json:"from,omitempty"`
	Since     string `json:"since,omitempty"`
	Until     string `json:"until,omitempty"`
	AccountID int64  `json:"account_id,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

func parseDateOrDuration(s string) (time.Time, bool) {
	s = strings.TrimSpace(strings.Trim(s, `"'`))
	if s == "" {
		return time.Time{}, false
	}
	if len(s) >= 2 {
		unit := s[len(s)-1]
		val, err := strconv.Atoi(s[:len(s)-1])
		if err == nil && val > 0 {
			switch unit {
			case 'd', 'D':
				return time.Now().AddDate(0, 0, -val), true
			case 'w', 'W':
				return time.Now().AddDate(0, 0, -val*7), true
			case 'm', 'M':
				return time.Now().AddDate(0, -val, 0), true
			case 'y', 'Y':
				return time.Now().AddDate(-val, 0, 0), true
			case 'h', 'H':
				return time.Now().Add(-time.Duration(val) * time.Hour), true
			}
		}
	}
	for _, layout := range []string{
		"2006-01-02",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
		time.RFC3339,
		"02.01.2006",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

var (
	filterLabelRegex = regexp.MustCompile(`(?i)\blabel:\s*("([^"]+)"|'([^']+)'|([^\s,]+))`)
	filterFromRegex  = regexp.MustCompile(`(?i)\bfrom:\s*("([^"]+)"|'([^']+)'|([^\s,]+))`)
	filterSinceRegex = regexp.MustCompile(`(?i)\bsince:\s*("([^"]+)"|'([^']+)'|([^\s,]+))`)
	filterUntilRegex = regexp.MustCompile(`(?i)\buntil:\s*("([^"]+)"|'([^']+)'|([^\s,]+))`)
	filterIsRegex    = regexp.MustCompile(`(?i)\bis:\s*("([^"]+)"|'([^']+)'|([^\s,]+))`)
)

func extractFilterValue(m []string) string {
	for i := 2; i < len(m); i++ {
		if m[i] != "" {
			return m[i]
		}
	}
	if len(m) > 1 {
		return m[1]
	}
	return ""
}

func (s *Service) parseSearchQuery(ctx context.Context, in searchMailInput) (SearchQuery, error) {
	raw := in.Query
	label := in.Label
	from := in.From
	sinceStr := in.Since
	untilStr := in.Until
	var unread, starred *bool

	if m := filterLabelRegex.FindStringSubmatch(raw); len(m) > 0 {
		if label == "" {
			label = extractFilterValue(m)
		}
		raw = filterLabelRegex.ReplaceAllString(raw, "")
	}
	if m := filterFromRegex.FindStringSubmatch(raw); len(m) > 0 {
		if from == "" {
			from = extractFilterValue(m)
		}
		raw = filterFromRegex.ReplaceAllString(raw, "")
	}
	if m := filterSinceRegex.FindStringSubmatch(raw); len(m) > 0 {
		if sinceStr == "" {
			sinceStr = extractFilterValue(m)
		}
		raw = filterSinceRegex.ReplaceAllString(raw, "")
	}
	if m := filterUntilRegex.FindStringSubmatch(raw); len(m) > 0 {
		if untilStr == "" {
			untilStr = extractFilterValue(m)
		}
		raw = filterUntilRegex.ReplaceAllString(raw, "")
	}
	if m := filterIsRegex.FindStringSubmatch(raw); len(m) > 0 {
		v := strings.ToLower(extractFilterValue(m))
		switch v {
		case "unread":
			t := true
			unread = &t
		case "read":
			f := false
			unread = &f
		case "starred", "flagged":
			t := true
			starred = &t
		case "unstarred":
			f := false
			starred = &f
		}
		raw = filterIsRegex.ReplaceAllString(raw, "")
	}

	raw = strings.TrimSpace(raw)
	if raw == "" && strings.TrimSpace(label) != "" {
		raw = strings.TrimSpace(label)
	} else if strings.TrimSpace(label) != "" && !strings.Contains(strings.ToLower(raw), strings.ToLower(strings.TrimSpace(label))) {
		raw = raw + " " + strings.TrimSpace(label)
	}

	sinceTime, _ := parseDateOrDuration(sinceStr)
	untilTime, _ := parseDateOrDuration(untilStr)

	return SearchQuery{
		AccountID: in.AccountID,
		Text:      raw,
		From:      strings.TrimSpace(from),
		Since:     sinceTime,
		Until:     untilTime,
		Unread:    unread,
		Starred:   starred,
	}, nil
}

func (s *Service) searchMail(ctx context.Context, args json.RawMessage) (any, error) {
	var in searchMailInput
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	if in.Limit <= 0 || in.Limit > maxSearchResult {
		in.Limit = maxSearchResult
	}
	q, err := s.parseSearchQuery(ctx, in)
	if err != nil {
		return nil, err
	}
	list, err := s.SearchWith(ctx, q, in.Limit)
	if err != nil {
		return nil, err
	}
	items := s.summaryContext(runOf(ctx).scope, list)
	return map[string]any{"results": blocks(items, s.cite(ctx, items))}, nil
}

type idArgs struct {
	ID int64 `json:"id"`
}

func (s *Service) getMessage(ctx context.Context, args json.RawMessage) (any, error) {
	var in idArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	id, err := s.mailOf(ctx, runOf(ctx), int(in.ID))
	if err != nil {
		return nil, err
	}
	items, err := s.mailContext(ctx, runOf(ctx).scope, []int64{id}, defaultMailBudget)
	if err != nil {
		return nil, err
	}
	return blocks(items, s.cite(ctx, items))[0], nil
}

// draftAccount is the account a draft is sent from: id, or for 0 the default one.
func (s *Service) draftAccount(ctx context.Context, id int64) (Account, error) {
	if id == 0 {
		rows, err := s.db.Query(ctx, "SELECT id FROM accounts ORDER BY is_default DESC, position, id LIMIT 1")
		if err != nil {
			return Account{}, err
		}
		if len(rows.Values) == 0 {
			return Account{}, errors.New("there is no mail account")
		}
		id = store.Int(rows.Values[0][0])
	}
	return s.account(ctx, id)
}

// getAccountProfile is what the user told Wit about themselves as the sender from an
// account; the model asks for it only to write a draft (the ask prompt stays
// the same for every run, and holds no personal facts).
func (s *Service) getAccountProfile(ctx context.Context, args json.RawMessage) (any, error) {
	var in struct {
		AccountID int64 `json:"account_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	acc, err := s.draftAccount(ctx, in.AccountID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"account_id": acc.ID, "email": acc.Email, "name": acc.Name, "instructions": acc.AIInstructions}, nil
}

func (s *Service) getThread(ctx context.Context, args json.RawMessage) (any, error) {
	var in idArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	id, err := s.mailOf(ctx, runOf(ctx), int(in.ID))
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, "SELECT thread_id FROM messages WHERE id = ?", id)
	if err != nil {
		return nil, err
	}
	if len(rows.Values) == 0 {
		return nil, fmt.Errorf("mail %d %w", in.ID, errMailNotFound)
	}
	_, ids, _, err := s.mailTarget(ctx, "thread", store.Int(rows.Values[0][0]))
	if err != nil {
		return nil, err
	}
	items, err := s.mailContext(ctx, runOf(ctx).scope, ids, defaultMailBudget)
	if err != nil {
		return nil, err
	}
	return map[string]any{"messages": blocks(items, s.cite(ctx, items))}, nil
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
	for i, n := range in.IDs {
		id, err := s.mailOf(ctx, runOf(ctx), int(n))
		if err == nil {
			var it []mailItem
			it, err = s.mailContext(ctx, runOf(ctx).scope, []int64{id}, mailBudget{PerMessage: classifyRunes, Total: classifyRunes})
			if err == nil {
				items, at = append(items, it[0]), append(at, i)
				continue
			}
		}
		if errors.Is(err, errMailNotFound) { // one unknown or gone mail doesn't fail the others
			res[i] = map[string]any{"id": n, "error": "not found"}
			continue
		}
		return nil, err
	}
	cs := s.cite(ctx, items)
	ctx, cancel := context.WithTimeout(ctx, classifyDeadline)
	defer cancel()
	var wg sync.WaitGroup
	sem := make(chan struct{}, classifyParallel)
	for k, it := range items {
		i := at[k]
		res[i] = map[string]any{"id": cs[k].N, "cite": citeTag(cs[k].N)}
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
	acc, err := s.draftAccount(ctx, in.AccountID)
	if err != nil {
		return prepared{}, err
	}
	in.AccountID = acc.ID
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
	if in.HTML, err = s.sanitizeDraft(ctx, mail.StripMarkers(ensureHTML(in.HTML))+d.HTML); err != nil {
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
	cs := s.cite(ctx, items)
	for i, it := range items {
		b.WriteString(named(it.Block, cs[i].N) + "\n")
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

type batchItem struct {
	ID        int    `json:"id"`
	MsgID     int64  `json:"msg_id"`
	AccountID int64  `json:"account_id"`
	Subject   string `json:"subject"`
	From      string `json:"from"`
	Date      int64  `json:"date"`
}

type batchArgs struct {
	Action      string      `json:"action"`
	IDs         []int       `json:"ids"`
	Items       []batchItem `json:"items"`
	SelectedIDs []int       `json:"selected_ids"`
}

func (s *Service) prepareBatchAction(defaultAction string) func(context.Context, Conversation, json.RawMessage) (prepared, error) {
	return func(ctx context.Context, _ Conversation, raw json.RawMessage) (prepared, error) {
		var in struct {
			IDs    []int  `json:"ids"`
			Action string `json:"action"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return prepared{}, err
		}
		in.Action = defaultAction // the tool, not an argument, names what is done
		if len(in.IDs) == 0 {
			return prepared{}, errors.New("ids cannot be empty")
		}
		items := make([]batchItem, 0, len(in.IDs))
		for _, citeID := range in.IDs {
			msgID, err := s.mailOf(ctx, runOf(ctx), citeID)
			if err != nil {
				return prepared{}, err
			}
			rows, err := s.db.Query(ctx, "SELECT account_id, subject, from_addr, date FROM messages WHERE id = ?", msgID)
			if err != nil {
				return prepared{}, err
			}
			if len(rows.Values) == 0 {
				return prepared{}, fmt.Errorf("mail %d %w", citeID, errMailNotFound)
			}
			r := rows.Values[0]
			items = append(items, batchItem{
				ID:        citeID,
				MsgID:     msgID,
				AccountID: store.Int(r[0]),
				Subject:   store.Str(r[1]),
				From:      store.Str(r[2]),
				Date:      store.Int(r[3]),
			})
		}
		var summary string
		n := len(items)
		switch in.Action {
		case "move_to_trash":
			summary = fmt.Sprintf("Move %d messages to Trash", n)
		case "archive_mail":
			summary = fmt.Sprintf("Archive %d messages", n)
		case "mark_as_spam":
			summary = fmt.Sprintf("Mark %d messages as spam", n)
		case "mark_as_read":
			summary = fmt.Sprintf("Mark %d messages as read", n)
		case "mark_as_unread":
			summary = fmt.Sprintf("Mark %d messages as unread", n)
		case "star_mail":
			summary = fmt.Sprintf("Star %d messages", n)
		case "unstar_mail":
			summary = fmt.Sprintf("Unstar %d messages", n)
		default:
			summary = fmt.Sprintf("%s on %d messages", in.Action, n)
		}
		ba := batchArgs{
			Action:      in.Action,
			IDs:         in.IDs,
			Items:       items,
			SelectedIDs: in.IDs,
		}
		b, err := json.Marshal(ba)
		if err != nil {
			return prepared{}, err
		}
		return prepared{
			Args:       b,
			Summary:    summary,
			TargetKind: "messages",
		}, nil
	}
}

func (s *Service) applyBatchAction(ctx context.Context, a Approval) ([]sqlite.Statement, any, any, error) {
	var in batchArgs
	if err := json.Unmarshal(a.Args, &in); err != nil {
		return nil, nil, nil, err
	}
	action := a.Tool
	// The card sends the ids left checked; none checked applies to none.
	selectedSet := make(map[int]bool)
	if in.SelectedIDs != nil {
		for _, id := range in.SelectedIDs {
			selectedSet[id] = true
		}
	} else {
		for _, id := range in.IDs {
			selectedSet[id] = true
		}
	}

	byAccount := make(map[int64][]int64)
	var processedCount int
	for _, it := range in.Items {
		if !selectedSet[it.ID] {
			continue
		}
		byAccount[it.AccountID] = append(byAccount[it.AccountID], it.MsgID)
		processedCount++
	}
	if len(in.Items) == 0 && len(in.IDs) > 0 {
		for _, citeID := range in.IDs {
			if !selectedSet[citeID] {
				continue
			}
			msgID, err := s.mailOf(ctx, runOf(ctx), citeID)
			if err != nil {
				return nil, nil, nil, err
			}
			rows, err := s.db.Query(ctx, "SELECT account_id FROM messages WHERE id = ?", msgID)
			if err != nil || len(rows.Values) == 0 {
				continue
			}
			accID := store.Int(rows.Values[0][0])
			byAccount[accID] = append(byAccount[accID], msgID)
			processedCount++
		}
	}

	if processedCount == 0 {
		return nil, map[string]any{"applied": 0, "action": action}, nil, nil
	}

	// Accounts are applied one by one: one failing does not undo the others, so
	// the result says what was applied and what failed, never "nothing" after some.
	applied := 0
	var errs []error
	for _, msgIDs := range byAccount {
		if err := s.batchApply(ctx, action, msgIDs); err != nil {
			errs = append(errs, err)
			continue
		}
		applied += len(msgIDs)
	}
	if applied == 0 && len(errs) > 0 {
		return nil, nil, nil, errors.Join(errs...)
	}
	res := map[string]any{"applied": applied, "action": action}
	if len(errs) > 0 {
		res["failed"], res["error"] = processedCount-applied, errors.Join(errs...).Error()
	}
	return nil, res, nil, nil
}

// batchApply applies a batch action to msgIDs, all of one account.
func (s *Service) batchApply(ctx context.Context, action string, msgIDs []int64) error {
	switch action {
	case "move_to_trash":
		return s.Delete(ctx, msgIDs)
	case "archive_mail":
		return s.MoveToRole(ctx, msgIDs, "archive")
	case "mark_as_spam":
		return s.MoveToRole(ctx, msgIDs, "junk")
	case "mark_as_read":
		return s.SetFlags(ctx, msgIDs, []string{`\Seen`}, nil)
	case "mark_as_unread":
		return s.SetFlags(ctx, msgIDs, nil, []string{`\Seen`})
	case "star_mail":
		return s.SetFlags(ctx, msgIDs, []string{`\Flagged`}, nil)
	case "unstar_mail":
		return s.SetFlags(ctx, msgIDs, nil, []string{`\Flagged`})
	}
	return fmt.Errorf("unknown batch action %q", action)
}

type unsubArgs struct {
	ID     int    `json:"id"`
	MsgID  int64  `json:"msg_id"`
	Method string `json:"method"`
	Host   string `json:"host"`
	URL    string `json:"url"`
	List   string `json:"list"`
}

func (s *Service) prepareUnsubscribe(ctx context.Context, _ Conversation, raw json.RawMessage) (prepared, error) {
	var in struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return prepared{}, err
	}
	msgID, err := s.mailOf(ctx, runOf(ctx), in.ID)
	if err != nil {
		return prepared{}, err
	}
	u, err := s.UnsubscribeMethod(ctx, msgID)
	if err != nil {
		return prepared{}, err
	}
	if u.Method == "" {
		return prepared{}, errors.New("this email does not provide an unsubscribe header")
	}
	summary := "Unsubscribe from " + cmp.Or(u.List, u.Host, "mailing list")
	if u.Host != "" && u.List != "" && u.List != u.Host { // the card names where it goes (ADR 0004), not only the list
		summary += " (" + u.Host + ")"
	}
	var dests []string
	if u.Method == UnsubscribeOneClick { // the only method that sends anything from Godwit
		dests = []string{u.Host}
	}
	arg := unsubArgs{
		ID:     in.ID,
		MsgID:  msgID,
		Method: u.Method,
		Host:   u.Host,
		URL:    u.URL,
		List:   u.List,
	}
	b, err := json.Marshal(arg)
	if err != nil {
		return prepared{}, err
	}
	return prepared{
		Args:         b,
		Summary:      summary,
		TargetKind:   "message",
		TargetID:     msgID,
		Destinations: dests,
	}, nil
}

func (s *Service) applyUnsubscribe(ctx context.Context, a Approval) ([]sqlite.Statement, any, any, error) {
	var in unsubArgs
	if err := json.Unmarshal(a.Args, &in); err != nil {
		return nil, nil, nil, err
	}
	approved := a.Destinations
	if approved == nil {
		approved = []string{} // the approval audited it: never again as the user's own click
	}
	res, err := s.unsubscribe(ctx, in.MsgID, approved)
	if errors.Is(err, ErrUnsubscribeWeb) {
		return nil, map[string]any{"unsubscribed": false, "method": "web", "url": in.URL, "message": "Please open the unsubscribe link in your browser"}, nil, nil
	}
	if err != nil {
		return nil, nil, nil, err
	}
	return nil, map[string]any{
		"unsubscribed": res.Failure == "",
		"method":       res.Method,
		"host":         res.Host,
		"failure":      res.Failure,
		"draft_id":     res.DraftID,
	}, nil, nil
}

type mailLink struct {
	URL  string `json:"url"`
	Text string `json:"text"`
}

func (s *Service) getAllLinks(ctx context.Context, raw json.RawMessage) (any, error) {
	var in idArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	msgID, err := s.mailOf(ctx, runOf(ctx), int(in.ID))
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, "SELECT body_html, body_text FROM messages WHERE id = ?", msgID)
	if err != nil {
		return nil, err
	}
	if len(rows.Values) == 0 {
		return nil, fmt.Errorf("mail %d %w", in.ID, errMailNotFound)
	}
	bodyHTML := store.Str(rows.Values[0][0])
	bodyText := store.Str(rows.Values[0][1])

	seen := make(map[string]bool)
	var links []mailLink

	if bodyHTML != "" {
		doc, err := html.Parse(strings.NewReader(bodyHTML))
		if err == nil {
			var extract func(*html.Node)
			extract = func(n *html.Node) {
				if n.Type == html.ElementNode && strings.EqualFold(n.Data, "a") {
					var href string
					for _, attr := range n.Attr {
						if strings.EqualFold(attr.Key, "href") {
							href = strings.TrimSpace(attr.Val)
							break
						}
					}
					if href != "" && !seen[href] && (strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") || strings.HasPrefix(href, "mailto:")) {
						seen[href] = true
						var text strings.Builder
						for c := n.FirstChild; c != nil; c = c.NextSibling {
							if c.Type == html.TextNode {
								text.WriteString(c.Data)
							}
						}
						links = append(links, mailLink{
							URL:  href,
							Text: strings.TrimSpace(text.String()),
						})
					}
				}
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					extract(c)
				}
			}
			extract(doc)
		}
	}

	if len(links) == 0 && bodyText != "" {
		re := regexp.MustCompile(`https?://[^\s<>"]+`)
		matches := re.FindAllString(bodyText, -1)
		for _, m := range matches {
			if !seen[m] {
				seen[m] = true
				links = append(links, mailLink{URL: m, Text: m})
			}
		}
	}

	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(links)
	text := strings.TrimSpace(b.String())
	// Anchor texts are the sender's words: inside the untrusted block, as every mail field is.
	return map[string]any{"mail_id": in.ID, "links": ai.Untrusted(fmt.Sprintf("message:%d", in.ID), text, ai.SuspectInjection(text))}, nil
}

type openLinkArgs struct {
	URL string `json:"url"`
}

// prepareOpenLink checks the link; the card shows it whole, since a link from
// a mail can carry data out in its query.
func (s *Service) prepareOpenLink(_ context.Context, _ Conversation, raw json.RawMessage) (prepared, error) {
	var in openLinkArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return prepared{}, err
	}
	u, err := url.Parse(strings.TrimSpace(in.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return prepared{}, errors.New("url must start with http:// or https://")
	}
	if s.opts.OpenURL == nil {
		return prepared{}, errors.New("this app cannot open links in a browser")
	}
	b, err := json.Marshal(openLinkArgs{URL: u.String()})
	if err != nil {
		return prepared{}, err
	}
	return prepared{Args: b, Summary: "Open " + u.String(), TargetKind: "url", Destinations: []string{u.Host}}, nil
}

func (s *Service) applyOpenLink(_ context.Context, a Approval) ([]sqlite.Statement, any, any, error) {
	var in openLinkArgs
	if err := json.Unmarshal(a.Args, &in); err != nil {
		return nil, nil, nil, err
	}
	u, err := url.Parse(in.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || s.opts.OpenURL == nil {
		return nil, nil, nil, errors.New("url must start with http:// or https://")
	}
	if err := s.opts.OpenURL(u.String()); err != nil {
		return nil, nil, nil, err
	}
	return nil, map[string]any{"opened": true, "url": u.String()}, nil, nil
}

type replyForwardArgs struct {
	ID              int      `json:"id,omitempty"`
	ReplyAll        bool     `json:"reply_all,omitempty"`
	AccountID       int64    `json:"account_id"`
	Mode            string   `json:"mode"`
	SourceMessageID int64    `json:"source_message_id"`
	To              []string `json:"to"`
	Cc              []string `json:"cc"`
	Subject         string   `json:"subject"`
	HTML            string   `json:"html"`
}

func (s *Service) prepareReplyMail(ctx context.Context, _ Conversation, raw json.RawMessage) (prepared, error) {
	var in struct {
		ID       int    `json:"id"`
		ReplyAll bool   `json:"reply_all"`
		HTML     string `json:"html"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return prepared{}, err
	}
	msgID, err := s.mailOf(ctx, runOf(ctx), in.ID)
	if err != nil {
		return prepared{}, err
	}
	rows, err := s.db.Query(ctx, "SELECT account_id FROM messages WHERE id = ?", msgID)
	if err != nil {
		return prepared{}, err
	}
	if len(rows.Values) == 0 {
		return prepared{}, fmt.Errorf("mail %d %w", in.ID, errMailNotFound)
	}
	accID := store.Int(rows.Values[0][0])
	mode := "reply"
	if in.ReplyAll {
		mode = "reply_all"
	}
	d, err := s.buildDraft(ctx, accID, mode, msgID)
	if err != nil {
		return prepared{}, err
	}
	bodyHTML := mail.StripMarkers(ensureHTML(in.HTML)) + d.HTML
	sanitized, err := s.sanitizeDraft(ctx, bodyHTML)
	if err != nil {
		return prepared{}, err
	}
	args := replyForwardArgs{
		ID:              in.ID,
		ReplyAll:        in.ReplyAll,
		AccountID:       accID,
		Mode:            mode,
		SourceMessageID: msgID,
		To:              d.To,
		Cc:              d.Cc,
		Subject:         d.Subject,
		HTML:            sanitized,
	}
	b, err := json.Marshal(args)
	if err != nil {
		return prepared{}, err
	}
	return prepared{
		Args:       b,
		Summary:    "Reply to " + strings.Join(d.To, ", ") + ": " + d.Subject,
		TargetKind: "account",
		TargetID:   accID,
	}, nil
}

func (s *Service) prepareForwardMail(ctx context.Context, _ Conversation, raw json.RawMessage) (prepared, error) {
	var in struct {
		ID   int      `json:"id"`
		To   []string `json:"to"`
		Cc   []string `json:"cc"`
		HTML string   `json:"html"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
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
	msgID, err := s.mailOf(ctx, runOf(ctx), in.ID)
	if err != nil {
		return prepared{}, err
	}
	rows, err := s.db.Query(ctx, "SELECT account_id FROM messages WHERE id = ?", msgID)
	if err != nil {
		return prepared{}, err
	}
	if len(rows.Values) == 0 {
		return prepared{}, fmt.Errorf("mail %d %w", in.ID, errMailNotFound)
	}
	accID := store.Int(rows.Values[0][0])
	d, err := s.buildDraft(ctx, accID, "forward", msgID)
	if err != nil {
		return prepared{}, err
	}
	bodyHTML := mail.StripMarkers(ensureHTML(in.HTML)) + d.HTML
	sanitized, err := s.sanitizeDraft(ctx, bodyHTML)
	if err != nil {
		return prepared{}, err
	}
	args := replyForwardArgs{
		ID:              in.ID,
		AccountID:       accID,
		Mode:            "forward",
		SourceMessageID: msgID,
		To:              in.To,
		Cc:              in.Cc,
		Subject:         d.Subject,
		HTML:            sanitized,
	}
	b, err := json.Marshal(args)
	if err != nil {
		return prepared{}, err
	}
	return prepared{
		Args:       b,
		Summary:    "Forward to " + strings.Join(in.To, ", ") + ": " + d.Subject,
		TargetKind: "account",
		TargetID:   accID,
	}, nil
}

func (s *Service) applyReplyOrForwardDraft(ctx context.Context, a Approval) ([]sqlite.Statement, any, any, error) {
	var in replyForwardArgs
	if err := json.Unmarshal(a.Args, &in); err != nil {
		return nil, nil, nil, err
	}
	res, err := s.db.Execute(ctx, `INSERT INTO drafts(account_id, mode, source_message_id, to_addrs, cc_addrs, bcc_addrs, reply_to, subject, body_html, updated_at, ai_approval_id) VALUES (?, ?, ?, ?, ?, '[]', '', ?, ?, ?, ?)`,
		in.AccountID, in.Mode, in.SourceMessageID, encodeList(in.To), encodeList(in.Cc), in.Subject, in.HTML, time.Now().Unix(), a.ID)
	if err != nil {
		return nil, nil, nil, err
	}
	draftID := res.LastInsertID
	if in.Mode == "forward" && in.SourceMessageID > 0 {
		_ = s.copyAttachments(ctx, draftID, in.SourceMessageID)
	}
	return nil, map[string]any{"created": true, "draft_id": draftID, "subject": in.Subject}, nil, nil
}

type deleteDraftArgs struct {
	DraftID int64  `json:"draft_id"`
	Subject string `json:"subject"`
}

func (s *Service) prepareDeleteDraft(ctx context.Context, _ Conversation, raw json.RawMessage) (prepared, error) {
	var in struct {
		DraftID int64 `json:"draft_id"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return prepared{}, err
	}
	rows, err := s.db.Query(ctx, "SELECT id, subject FROM drafts WHERE id = ?", in.DraftID)
	if err != nil {
		return prepared{}, err
	}
	if len(rows.Values) == 0 {
		return prepared{}, fmt.Errorf("draft %d not found", in.DraftID)
	}
	subject := store.Str(rows.Values[0][1])
	summary := "Delete draft: " + cmp.Or(subject, "(no subject)")
	b, err := json.Marshal(deleteDraftArgs{DraftID: in.DraftID, Subject: subject})
	if err != nil {
		return prepared{}, err
	}
	return prepared{
		Args:       b,
		Summary:    summary,
		TargetKind: "draft",
		TargetID:   in.DraftID,
	}, nil
}

func (s *Service) applyDeleteDraft(ctx context.Context, a Approval) ([]sqlite.Statement, any, any, error) {
	var in deleteDraftArgs
	if err := json.Unmarshal(a.Args, &in); err != nil {
		return nil, nil, nil, err
	}
	if err := s.DeleteDraft(ctx, in.DraftID); err != nil {
		return nil, nil, nil, err
	}
	return nil, map[string]any{"deleted": true, "draft_id": in.DraftID}, nil, nil
}
