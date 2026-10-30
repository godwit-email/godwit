package core

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/gateway"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/store"
	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// assistTools are the tools of an Assist run: read mail, ask the user, and
// propose a new editable part for this draft, plus the running plugins' read tools (ADR 0003;
// the compose panel changes only its draft). None of them sends mail.
var assistTools = []string{"search_mail", "get_message", "read_attachment", "update_draft", askQuestionTool}

const (
	assistSystem = `You are Wit, the writing assistant beside the user's draft in the Godwit compose window. ` +
		`update_draft replaces the editable part of the draft with your HTML (paragraphs, line breaks, bold, italic, links, lists). ` +
		`Give update_draft a concise subject that fits the draft when it has none or only a placeholder, and keep the subject in step with ` +
		`the draft when the user asks to change its tone, length, dates, topic or intent. A reply's or forward's subject stays as it is ` +
		`unless the user explicitly asks to change the subject. When the user asks for a change, actually make it. ` +
		`When the request needs no change to the draft (it already does what is asked, or lacks the text the user refers to), do not call update_draft: ` +
		`say in one plain sentence why nothing changed. ` +
		`Call update_draft at most once per turn. Once update_draft has been executed, do not call update_draft again: your task is complete. ` +
		`If update_draft reports no_change, say in one plain sentence why the draft stays as it is, without further tool calls. ` +
		`When the user declines your last update_draft and then says what to change, revise that proposal accordingly. ` +
		`The user reviews every change except the first text of an empty draft. The quoted email and the signature stay as they are: ` +
		`never write them. You cannot send mail. ` +
		`<sender> is the user writing from the draft's account: sign with its name, take facts such as a phone number from its instructions and follow their guidelines; ` +
		`never invent a name or contact details it does not give.`
	selectionSystem = `You are Wit, rewriting a part of the user's draft they selected. Call update_selection once with the HTML ` +
		`that replaces the selection, and nothing before or after it: the text around it is read-only context. ` +
		`Keep the language of the selection unless the instruction asks for another.`

	// What update_draft's Summary says about a change, for the GUI.
	kindInitial   = "initial"   // the editable part was empty: applied at once
	kindPartial   = "partial"   // shown as a word diff
	kindRewrite   = "rewrite"   // more than half the words changed: shown side by side
	kindTranslate = "translate" // translated: shown as translation
	kindNoChange  = "no_change" // the same draft: nothing to review, settled at once

	selectionContext = 500 // runes of read-only context on each side of a selection
	recentMail       = 5   // latest mail with the recipients in an Assist run
)

// Selection is a byte range [Start, End) of the draft's editable HTML (the
// first value mail.SplitDraft returns) at Revision. Neither end may fall
// inside a tag or a character reference.
type Selection struct {
	Revision   int64
	Start, End int
}

// updateArgs are update_draft's final arguments: the new and the old editable
// HTML (the GUI diffs them) at the draft's bound revision, and optional subject.
type updateArgs struct {
	DraftID    int64   `json:"draft_id"`
	Revision   int64   `json:"revision"`
	Old        string  `json:"old"`
	HTML       string  `json:"html"`
	OldSubject string  `json:"old_subject,omitempty"`
	Subject    *string `json:"subject,omitempty"`
}

// draftUndo is an applied update_draft's undo: the editable part it replaced,
// the revision it wrote, and the previous subject if changed.
type draftUndo struct {
	Editable   string  `json:"editable"`
	Revision   int64   `json:"revision"`
	OldSubject *string `json:"old_subject,omitempty"`
}

func isDraftEdit(tool string) bool { return tool == "update_draft" || tool == "update_selection" }

var (
	// Any tag, inline ones included: a one-line reply with only a link is still HTML.
	htmlTag   = regexp.MustCompile(`(?i)</?[a-z][a-z0-9]*\b[^>]*>`)
	paraBreak = regexp.MustCompile(`\n[ \t]*\n\s*`)
)

// ensureHTML converts plain text (with single or double line breaks) into
// HTML paragraphs (<p>) and breaks (<br>) when it contains no tags at all.
func ensureHTML(h string) string {
	if h == "" || htmlTag.MatchString(h) {
		return h
	}
	return textHTML(h)
}

// textHTML is plain text as HTML paragraphs (<p>) and breaks (<br>), escaped.
func textHTML(h string) string {
	h = strings.ReplaceAll(h, "\r\n", "\n")
	paras := paraBreak.Split(strings.TrimSpace(h), -1)
	var b strings.Builder
	for _, p := range paras {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		lines := strings.Split(p, "\n")
		b.WriteString("<p>")
		for i, line := range lines {
			if i > 0 {
				b.WriteString("<br>")
			}
			b.WriteString(html.EscapeString(line))
		}
		b.WriteString("</p>")
	}
	return b.String()
}

// cleanHTML is model (or GUI) HTML made safe for a draft: forged quote and
// signature markers become plain content, then it is sanitized.
func cleanHTML(h string) string { return mail.SanitizeCompose(mail.StripMarkers(h)) }

var selectionSpec = ai.ToolSpec{Name: "update_selection",
	Description: "Replace the selected part of the draft with html. The user reviews the change.",
	Schema:      object(map[string]any{"html": map[string]any{"type": "string", "description": "Replacement HTML (paragraphs in <p>, line breaks with <br>)."}}, "html")}

// registerAssistTools offers the draft tools to Wit runs. update_selection
// is prepared only by the run RewriteSelection starts, which knows the selection.
func (s *Service) registerAssistTools() {
	s.mustRegister(toolDesc{Spec: ai.ToolSpec{Name: "update_draft",
		Description: "Replace the editable part of the draft (everything but the quoted email and the signature) with html, and set or update the subject.",
		Schema: object(map[string]any{
			"html":    map[string]any{"type": "string", "description": "The draft body as HTML (paragraphs in <p>, line breaks with <br>)."},
			"subject": map[string]any{"type": "string", "description": "Subject for the draft. Always provide this when drafting or when changes affect the subject; keep it consistent with the content."},
		}, "html")},
		Write: true, Exec: &executor{Prepare: s.prepareUpdate, Apply: s.applyUpdate}})
	s.mustRegister(toolDesc{Spec: selectionSpec, Write: true, Exec: &executor{
		Prepare: func(context.Context, Conversation, json.RawMessage) (prepared, error) {
			return prepared{}, errors.New("there is no selection to replace any more")
		}, Apply: s.applyUpdate}})
}

// Assist runs input in the draft's assist conversation. The model sees the
// draft's editable part, the email it replies to or forwards, the names of its
// attachments and the latest mail with its recipients; it can read mail and
// propose a new editable part (update_draft). A proposal for an empty draft
// is applied at once (see applyInitial); the others wait for the user. The
// channel ends with done, error or interrupted, as Ask's.
func (s *Service) Assist(ctx context.Context, draftID int64, input string) (<-chan WitEvent, error) {
	return s.AssistWith(ctx, draftID, input, nil)
}

// AssistWith is Assist with the mail the user quoted, as AskWith.
func (s *Service) AssistWith(ctx context.Context, draftID int64, input string, quoted []int64) (<-chan WitEvent, error) {
	if strings.TrimSpace(input) == "" {
		return nil, errors.New("the request is empty")
	}
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	quoted, err = s.quotedIDs(ctx, quoted)
	var conv Conversation
	if err == nil {
		conv, err = s.assistConversation(ctx, draftID)
	}
	var w *witStream
	if err == nil {
		w, err = s.launch(ctx, end, conv, input, slices.Concat(s.registered(assistTools...), s.pluginTools(false), s.mcpTools(ctx, false)), quoted, 0)
	}
	if err != nil {
		end()
		return nil, err
	}
	return w.ch, nil
}

// assistDraft loads the draft and ties the call begun with ctx to its account.
// One in the outbox is ErrStaleDraft.
func (s *Service) assistDraft(ctx context.Context, draftID int64) (Draft, error) {
	d, err := s.GetDraft(ctx, draftID)
	if err == nil {
		err = s.checkWitDraft(ctx, draftID)
	}
	if err != nil {
		return Draft{}, err
	}
	return d, s.bind(ctx, d.AccountID)
}

// AssistConversation is the ID of the draft's assist conversation, the one
// Assist and AssistSkill run in; with create, one is made if it has none,
// else it is 0.
func (s *Service) AssistConversation(ctx context.Context, draftID int64, create bool) (int64, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return 0, err
	}
	defer end()
	if !create {
		id, err := s.findAssistConversation(ctx, draftID)
		return id, err
	}
	c, err := s.assistConversation(ctx, draftID)
	return c.ID, err
}

// findAssistConversation is the draft's assist conversation, 0 for none.
// Conversations RewriteSelection made (their runs offer update_selection) are not it.
func (s *Service) findAssistConversation(ctx context.Context, draftID int64) (int64, error) {
	rows, err := s.db.Query(ctx, `SELECT c.id FROM conversations c WHERE c.kind = 'assist' AND c.draft_id = ? AND NOT EXISTS
		(SELECT 1 FROM runs r WHERE r.conversation_id = c.id AND r.tools LIKE '%"update_selection"%') ORDER BY c.id LIMIT 1`, draftID)
	if err != nil || len(rows.Values) == 0 {
		return 0, err
	}
	return store.Int(rows.Values[0][0]), nil
}

// assistConversation returns the draft's assist conversation, creating it.
// Conversations RewriteSelection made (their runs offer update_selection) are not it.
func (s *Service) assistConversation(ctx context.Context, draftID int64) (Conversation, error) {
	if _, err := s.assistDraft(ctx, draftID); err != nil {
		return Conversation{}, err
	}
	// ponytail: one lock for every draft's find-or-create; per draft if it ever contends
	s.assistMu.Lock()
	defer s.assistMu.Unlock()
	id, err := s.findAssistConversation(ctx, draftID)
	if err != nil {
		return Conversation{}, err
	}
	if id != 0 {
		return s.conversation(ctx, id)
	}
	return s.newAssistConversation(ctx, draftID, "")
}

// SetDraftModel sets the model of draftID's assist conversation, keeping
// its effort, as picked before its next question asks on it.
func (s *Service) SetDraftModel(ctx context.Context, draftID int64, model string) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	conv, err := s.assistConversation(ctx, draftID)
	if err != nil {
		return err
	}
	return s.SetConversationModel(ctx, conv.ID, model, conv.Effort)
}

func (s *Service) newAssistConversation(ctx context.Context, draftID int64, title string) (Conversation, error) {
	now := time.Now().Unix()
	r, err := s.db.Execute(ctx, "INSERT INTO conversations(kind, draft_id, title, model, created_at, updated_at) VALUES ('assist', ?, ?, '', ?, ?)",
		draftID, title, now, now)
	if err != nil {
		return Conversation{}, err
	}
	return s.conversation(ctx, r.LastInsertID)
}

// assistSystemPrompt is an assist conversation's system prompt, built for
// every request of a run, started or resumed, from the draft as it is then.
// A selection run gets no draft context: its prompt carries the selection.
func (s *Service) assistSystemPrompt(ctx context.Context, job *runJob, tools []toolDesc) (string, error) {
	d, err := s.GetDraft(ctx, job.conv.DraftID)
	if err != nil {
		return "", err
	}
	answer := "Answer the user in " + s.readingLanguage(ctx) + ". "
	if slices.ContainsFunc(tools, func(t toolDesc) bool { return t.Spec.Name == "update_selection" }) {
		return selectionSystem + "\n\n" + answer, nil
	}
	target, err := s.draftLanguage(ctx, d)
	if err != nil {
		return "", err
	}
	c, cited, err := s.assistContext(ctx, job.scope, d)
	if err != nil {
		return "", err
	}
	job.cited = s.contextCites(ctx, job, cited)
	// the context names each mail by its id, as the read tools do (blocks): one number per mail
	for _, ct := range job.cited {
		c = strings.ReplaceAll(c, fmt.Sprintf(`source="message:%d"`, ct.MessageID), fmt.Sprintf(`source="mail:%d"`, ct.N))
	}
	if len(job.cited) > 0 {
		c += "\n\nEach email above is named by its id (mail:N in its source), which get_message takes."
	}
	return assistSystem + " " + timeSystem + "\n\n" + ai.UntrustedSystemNote + "\n\n" + answer + "Write the draft in " + target + ".\n\n" + c, nil
}

// senderBlock is what the user told Wit about themselves as the sender from
// acc, escaped so that it stays one block.
func senderBlock(acc Account) string {
	b := "The user sending this draft:\n<sender>\n<email>" + html.EscapeString(acc.Email) + "</email>"
	if acc.Name != "" {
		b += "\n<name>" + html.EscapeString(acc.Name) + "</name>"
	}
	if acc.AIInstructions != "" {
		b += "\n<instructions>\n" + html.EscapeString(acc.AIInstructions) + "\n</instructions>"
	}
	return b + "\n</sender>"
}

// draftLanguage is the language a draft is written in (the reply language setting):
// the reading language, or under "match" the language of the email a reply
// answers, else of the recipients' past mail (the context shows it).
func (s *Service) draftLanguage(ctx context.Context, d Draft) (string, error) {
	st, err := s.loadSettings(ctx)
	if err != nil {
		return "", err
	}
	lang := s.readingLanguage(ctx)
	switch {
	case st.ReplyLanguage != "match":
		return lang, nil
	case d.Mode == "reply" || d.Mode == "reply_all":
		return "the language of the email this draft replies to", nil
	}
	return "the language of the recipients' past emails to the user; if there are none, use " + lang, nil
}

// assistContext is what an Assist run knows about the draft. Mail-derived
// fields go through the mail context builders; attachment names, which a
// forward copies from mail, are wrapped the same way.
func (s *Service) assistContext(ctx context.Context, scope *gateway.Scope, d Draft) (string, []Citation, error) {
	var cited []mailItem
	editable, _, _ := mail.SplitDraft(d.HTML)
	var b strings.Builder
	acc, err := s.account(ctx, d.AccountID)
	if err != nil {
		return "", nil, err
	}
	b.WriteString(senderBlock(acc) + "\n\n")
	if d.SourceMessageID == 0 {
		b.WriteString("The draft's subject:\n<subject>\n" + d.Subject + "\n</subject>\n\n")
	} else {
		// A reply's subject comes from the mail it answers; changing it splits the thread at the recipient.
		scope.MarkReadMail()
		b.WriteString("The draft's subject, taken from the email it answers (keep it unless the user explicitly asks to change the subject):\n" +
			ai.Untrusted(fmt.Sprintf("draft:%d:subject", d.ID), d.Subject, ai.SuspectInjection(d.Subject)) + "\n\n")
	}
	b.WriteString("The editable part of the draft, as HTML (the quoted email and the signature are not shown and stay as they are):\n<draft>\n" +
		editable + "\n</draft>")
	if d.SourceMessageID != 0 {
		items, err := s.mailContext(ctx, scope, []int64{d.SourceMessageID}, defaultMailBudget)
		if err != nil {
			return "", nil, err
		}
		cited = append(cited, items...)
		how := "replies to"
		if d.Mode == "forward" {
			how = "forwards"
		}
		b.WriteString("\n\nThe email this draft " + how + ":\n" + items[0].Block)
	}
	if len(d.Attachments) > 0 {
		var names []string
		for _, a := range d.Attachments {
			names = append(names, fmt.Sprintf("%s (%s, %d bytes)", a.Filename, a.ContentType, a.Size))
		}
		list := strings.Join(names, "\n")
		suspect := ai.SuspectInjection(list)
		if d.SourceMessageID != 0 { // a forward's copied names: their links are the source message's
			list = linkRefs(list, d.SourceMessageID)
		}
		scope.MarkReadMail()
		b.WriteString("\n\nFiles attached to the draft (names only; their content is not available):\n" +
			ai.Untrusted(fmt.Sprintf("draft:%d:attachments", d.ID), list, suspect))
	}
	recent, err := s.correspondence(ctx, d.AccountID, slices.Concat(d.To, d.Cc), recentMail)
	if err != nil {
		return "", nil, err
	}
	if len(recent) > 0 {
		b.WriteString("\n\nThe latest mail with the recipients, newest first:")
		for _, it := range s.summaryContext(scope, recent) {
			b.WriteString("\n" + it.Block)
			if !slices.ContainsFunc(cited, func(c mailItem) bool { return c.MessageID == it.MessageID }) {
				cited = append(cited, it)
			}
		}
	}
	return b.String(), citations(cited), nil
}

// correspondence is the latest n messages of the account from or to any of addrs.
func (s *Service) correspondence(ctx context.Context, accountID int64, addrs []string, n int) ([]MessageSummary, error) {
	seen := map[int64]bool{}
	var out []MessageSummary
	for _, a := range addrs {
		k := mail.AddrKey(a)
		is := func(x string) bool { return mail.AddrKey(x) == k }
		ms, err := s.mailWith(ctx, accountID, k, n, []string{"from_addr", "to_addrs", "cc_addrs"}, func(m MessageSummary) bool {
			return is(m.From) || slices.ContainsFunc(m.To, is) || slices.ContainsFunc(m.Cc, is)
		})
		if err != nil {
			return nil, err
		}
		for _, m := range ms {
			if !seen[m.ID] {
				seen[m.ID] = true
				out = append(out, m)
			}
		}
	}
	slices.SortFunc(out, func(a, b MessageSummary) int {
		return cmp.Or(b.Date.Compare(a.Date), cmp.Compare(b.ID, a.ID))
	})
	return out[:min(n, len(out))], nil
}

// proposeUpdate prepares an update of the draft's editable part: edit gets the
// editable part and revision now and returns the new part; the proposal binds
// that revision.
func (s *Service) proposeUpdate(ctx context.Context, draftID int64, edit func(old string, revision int64) (string, error)) (prepared, error) {
	return s.proposeUpdateWithSubject(ctx, draftID, nil, edit)
}

func (s *Service) proposeUpdateWithSubject(ctx context.Context, draftID int64, subject *string, edit func(old string, revision int64) (string, error)) (prepared, error) {
	d, err := s.GetDraft(ctx, draftID)
	if err != nil {
		return prepared{}, err
	}
	old, _, _ := mail.SplitDraft(d.HTML)
	h, err := edit(old, d.Revision)
	if err != nil {
		return prepared{}, err
	}
	newSubject := subject != nil && strings.TrimSpace(*subject) != "" && *subject != d.Subject // a blank subject would erase it unseen
	if !mail.Blank(old) && !newSubject && sameHTML(old, h) {
		b, err := json.Marshal(updateArgs{DraftID: d.ID, Revision: d.Revision, Old: old, HTML: h})
		if err != nil {
			return prepared{}, err
		}
		return prepared{Args: b, Summary: kindNoChange, TargetKind: "draft", TargetID: d.ID, TargetVersion: d.Revision}, nil
	}
	kind, err := changeKind(ctx, old, h)
	if err != nil {
		return prepared{}, err
	}
	args := updateArgs{DraftID: d.ID, Revision: d.Revision, Old: old, HTML: h}
	if newSubject {
		args.Subject = subject
		args.OldSubject = d.Subject
		if kind == kindInitial && !mail.Blank(old) {
			kind = kindPartial
		}
	}
	b, err := json.Marshal(args)
	if err != nil {
		return prepared{}, err
	}
	return prepared{Args: b, Summary: kind, TargetKind: "draft", TargetID: d.ID, TargetVersion: d.Revision}, nil
}

type updateDraftIn struct {
	HTML    string  `json:"html"`
	Subject *string `json:"subject"`
}

func htmlArg(args json.RawMessage) (string, error) {
	var in updateDraftIn
	err := json.Unmarshal(args, &in)
	return in.HTML, err
}

// prepareUpdate is update_draft's Prepare: the conversation's draft, at its
// current revision, with the model's HTML cleaned and optional subject.
func (s *Service) prepareUpdate(ctx context.Context, conv Conversation, args json.RawMessage) (prepared, error) {
	if conv.DraftID == 0 {
		return prepared{}, errors.New("update_draft only edits the draft of a compose window")
	}
	var in updateDraftIn
	if err := json.Unmarshal(args, &in); err != nil {
		return prepared{}, err
	}
	return s.proposeUpdateWithSubject(ctx, conv.DraftID, in.Subject, func(string, int64) (string, error) { return cleanHTML(ensureHTML(in.HTML)), nil })
}

// applyUpdate writes the approved editable part between the draft's own quote
// and signature, and updates subject if provided. If the draft changed since, execute's revision check fails
// the write, so the parts read here are those of the bound revision.
func (s *Service) applyUpdate(ctx context.Context, a Approval) ([]sqlite.Statement, any, any, error) {
	if a.Summary == kindNoChange {
		return nil, map[string]any{"updated": false, "summary": kindNoChange}, nil, nil
	}
	var in updateArgs
	if err := json.Unmarshal(a.Args, &in); err != nil {
		return nil, nil, nil, err
	}
	d, err := s.GetDraft(ctx, in.DraftID)
	if err != nil {
		return nil, nil, nil, err
	}
	_, q, sig := mail.SplitDraft(d.HTML)
	newSubject := d.Subject
	var undoSubject *string
	if in.Subject != nil {
		newSubject = *in.Subject
		undoSubject = &d.Subject
	}
	return []sqlite.Statement{{SQL: "UPDATE drafts SET subject = ?, body_html = ?, updated_at = ?, revision = revision + 1 WHERE id = ?",
			Args: []any{newSubject, mail.JoinDraft(in.HTML, q, sig), time.Now().Unix(), d.ID}}},
		map[string]any{"updated": true}, draftUndo{Editable: in.Old, Revision: in.Revision + 1, OldSubject: undoSubject}, nil
}

// applyInitial approves, through the normal path, the pending proposals that
// write an empty draft (the first generation is written directly), those that
// change nothing (audited no_change), and those a session grant covers (audited auto:session). It returns the
// approvals as they are now, and whether any was decided; the caller then
// resumes the run (runLedger.resume).
func (s *Service) applyInitial(ctx context.Context, as []Approval) ([]Approval, bool) {
	applied := false
	for i, a := range as {
		if a.State != "pending" {
			continue
		}
		decision := "executed"
		switch {
		case isDraftEdit(a.Tool) && a.Summary == kindInitial:
		case isDraftEdit(a.Tool) && a.Summary == kindNoChange:
			decision = kindNoChange
		case s.granted(ctx, a.ID):
			decision = decisionSession
		default:
			continue
		}
		if err := s.decideAs(ctx, a.ID, true, nil, false, decision); err != nil {
			slog.Default().Warn("could not apply an approval at once", "approval", a.ID, "err", err)
		}
		applied = true
		if got, err := s.getApproval(ctx, a.ID); err == nil {
			as[i] = got
		}
	}
	return as, applied
}

// ApplyEditedProposal accepts a pending draft proposal with html, the editable
// part the user settled on in the GUI, in place of the proposed one: markers
// removed, sanitized, then applied like ResolveApproval (same revision check:
// a changed draft makes it stale, ErrStaleDraft).
func (s *Service) ApplyEditedProposal(ctx context.Context, approvalID int64, edited string) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	a, err := s.approvalRow(ctx, approvalID)
	if err != nil {
		return err
	}
	if !isDraftEdit(a.Tool) {
		return fmt.Errorf("approval %d does not edit a draft", approvalID)
	}
	var in updateArgs
	if err := json.Unmarshal(a.Args, &in); err != nil {
		return err
	}
	in.HTML = cleanHTML(ensureHTML(edited))
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return s.decide(ctx, approvalID, true, b, true)
}

// UndoLastAssist puts back the editable part the draft's latest applied
// proposal (by approval ID, not by clock) replaced. ErrInvalidState: there is none, or the draft changed
// since it was written (an undo counts as a change).
func (s *Service) UndoLastAssist(ctx context.Context, draftID int64) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	rows, err := s.db.Query(ctx, `SELECT undo FROM approvals WHERE target_kind = 'draft' AND target_id = ? AND state = 'done'
		AND tool IN ('update_draft', 'update_selection') ORDER BY id DESC LIMIT 1`, draftID)
	if err != nil {
		return err
	}
	if len(rows.Values) == 0 {
		return ErrInvalidState
	}
	var u draftUndo
	if err := json.Unmarshal([]byte(store.Str(rows.Values[0][0])), &u); err != nil {
		return err
	}
	s.editMu.Lock()
	defer s.editMu.Unlock()
	d, err := s.GetDraft(ctx, draftID)
	if err != nil {
		return err
	}
	if err := s.checkWritable(ctx, draftID); err != nil {
		return err
	}
	if d.Revision != u.Revision {
		return ErrInvalidState
	}
	_, q, sig := mail.SplitDraft(d.HTML)
	newSubject := d.Subject
	if u.OldSubject != nil {
		newSubject = *u.OldSubject
	}
	r, err := s.db.Execute(ctx, "UPDATE drafts SET subject = ?, body_html = ?, updated_at = ?, revision = revision + 1 WHERE id = ? AND revision = ?",
		newSubject, mail.JoinDraft(u.Editable, q, sig), time.Now().Unix(), draftID, u.Revision)
	if err == nil && r.Changes == 0 {
		err = ErrInvalidState
	}
	return err
}

// checkSelection validates sel against the editable part it was made on.
func checkSelection(editable string, sel Selection) error {
	if sel.Start < 0 || sel.Start >= sel.End || sel.End > len(editable) {
		return errors.New("the selection is empty or outside the draft")
	}
	for _, p := range []int{sel.Start, sel.End} {
		if !boundary(editable, p) {
			return errors.New("the selection starts or ends inside a tag or a character reference")
		}
	}
	return nil
}

// boundary reports whether byte offset p of sanitized HTML is between runes,
// outside any tag and any character reference (raw < > & only occur as those).
func boundary(h string, p int) bool {
	if p < len(h) && !utf8.RuneStart(h[p]) {
		return false
	}
	pre := h[:p]
	if strings.LastIndexByte(pre, '<') > strings.LastIndexByte(pre, '>') {
		return false
	}
	i := strings.LastIndexByte(pre, '&')
	return i < 0 || strings.ContainsAny(pre[i:], ";<>")
}

// spliceSelection replaces sel in editable with the model's fragment. Only
// the range changes: the fragment goes in as HTML if the result is already
// clean as it is, else as plain text, else the call fails.
func spliceSelection(editable string, sel Selection, fragment string) (string, error) {
	h := cleanHTML(fragment)
	text := cleanHTML(strings.ReplaceAll(html.EscapeString(mail.HTMLToText(h)), "\n", "<br>"))
	for _, c := range []string{h, text} {
		if out := editable[:sel.Start] + c + editable[sel.End:]; mail.SanitizeCompose(out) == out {
			return out, nil
		}
	}
	return "", errors.New("the replacement does not fit in the selection")
}

// RewriteSelection rewrites sel of the draft's editable part as instruction
// says, in a new assist conversation of its own. The model gets only the
// selected HTML and up to 500 characters of read-only context on each side,
// and answers with update_selection; the proposal splices its HTML into the
// range, so nothing outside it can change. A sel made on another revision is
// ErrStaleDraft.
func (s *Service) RewriteSelection(ctx context.Context, draftID int64, sel Selection, instruction string) (<-chan WitEvent, error) {
	if strings.TrimSpace(instruction) == "" {
		return nil, errors.New("the instruction is empty")
	}
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	w, err := s.rewriteSelection(ctx, end, draftID, sel, instruction)
	if err != nil {
		end()
		return nil, err
	}
	return w.ch, nil
}

func (s *Service) rewriteSelection(ctx context.Context, end func(), draftID int64, sel Selection, instruction string) (*witStream, error) {
	d, err := s.assistDraft(ctx, draftID)
	if err != nil {
		return nil, err
	}
	editable, _, _ := mail.SplitDraft(d.HTML)
	if d.Revision != sel.Revision {
		return nil, ErrStaleDraft
	}
	if err := checkSelection(editable, sel); err != nil {
		return nil, err
	}
	conv, err := s.newAssistConversation(ctx, draftID, titleOf(instruction))
	if err != nil {
		return nil, err
	}
	if parent, perr := s.assistConversation(ctx, draftID); perr == nil && parent.Model != "" {
		_ = s.SetConversationModel(ctx, conv.ID, parent.Model, parent.Effort)
	}
	tool := toolDesc{Spec: selectionSpec, Write: true, Exec: &executor{Apply: s.applyUpdate,
		Prepare: func(ctx context.Context, conv Conversation, args json.RawMessage) (prepared, error) {
			h, err := htmlArg(args)
			if err != nil {
				return prepared{}, err
			}
			return s.proposeUpdate(ctx, conv.DraftID, func(old string, rev int64) (string, error) {
				if rev != sel.Revision {
					return "", ErrStaleDraft
				}
				return spliceSelection(old, sel, h)
			})
		}}}
	before, after := []rune(editable[:sel.Start]), []rune(editable[sel.End:])
	before, after = before[max(0, len(before)-selectionContext):], after[:min(len(after), selectionContext)]
	prompt := "Instruction: " + instruction +
		"\n\nRead-only context before the selection:\n<before>\n" + string(before) + "\n</before>" +
		"\n\nThe selection to replace, as HTML:\n<selection>\n" + editable[sel.Start:sel.End] + "\n</selection>" +
		"\n\nRead-only context after the selection:\n<after>\n" + string(after) + "\n</after>"
	w, err := s.launch(ctx, end, conv, prompt, []toolDesc{tool}, nil, 0)
	if err != nil { // a run that never started leaves no conversation for findAssistConversation to take
		_, derr := s.db.Execute(context.WithoutCancel(ctx), "DELETE FROM conversations WHERE id = ? AND NOT EXISTS (SELECT 1 FROM runs WHERE conversation_id = ?)", conv.ID, conv.ID)
		return nil, errors.Join(err, derr)
	}
	return w, nil
}

// AssistSkill runs a replace-draft skill (tone, length, proofreading,
// translate-draft) on the draft's editable part, or on opts.Selection of it if
// the skill runs on selections; the model sees that and nothing else. The skill has no tools, as every skill: the host turns its
// output into an update_draft proposal in the draft's assist conversation,
// which the user decides like any other (an empty draft's is applied at
// once). The conversation's history is left as it is. The channel streams
// the skill's text and ends with interrupted (the proposal) or error.
func (s *Service) AssistSkill(ctx context.Context, draftID int64, skill string, opts SkillOptions) (<-chan WitEvent, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	w, err := s.assistSkill(ctx, end, draftID, skill, opts)
	if err != nil {
		end()
		return nil, err
	}
	return w.ch, nil
}

// SkillOptions are AssistSkill's options. Selection, if set, limits the skill
// to that range; TargetLanguage is translate-draft's target (empty: the
// reading language).
type SkillOptions struct {
	Selection      *Selection
	TargetLanguage string
}

func (s *Service) assistSkill(ctx context.Context, end func(), draftID int64, name string, opts SkillOptions) (*witStream, error) {
	sel := opts.Selection
	if sel != nil { // the caller keeps its struct: work on a copy
		c := *sel
		sel = &c
	}
	sk, err := s.skill(ctx, name)
	if err != nil {
		return nil, err
	}
	on := "draft"
	if sel != nil {
		on = "selection"
	}
	if sk.Output != "replace-draft" || !slices.Contains(sk.On, on) {
		return nil, fmt.Errorf("skill %s does not rewrite a %s", name, on)
	}
	conv, err := s.assistConversation(ctx, draftID)
	if err != nil {
		return nil, err
	}
	d, err := s.GetDraft(ctx, draftID)
	if err != nil {
		return nil, err
	}
	editable, _, _ := mail.SplitDraft(d.HTML)
	// A reply's or forward's subject is kept: changing it splits the thread at the recipient.
	withSubject := sel == nil && d.SourceMessageID == 0
	if sel != nil {
		if d.Revision != sel.Revision {
			return nil, ErrStaleDraft
		}
		if err := checkSelection(editable, *sel); err != nil {
			return nil, err
		}
	}
	st, err := s.loadSettings(ctx)
	if err != nil {
		return nil, err
	}
	target := cmp.Or(strings.TrimSpace(opts.TargetLanguage), s.readingLanguage(ctx))
	ref := sk.Model
	var run runFunc
	var req ai.Request
	plugin, byPlugin := strings.CutPrefix(st.Translator, translatorPrefix)
	if byPlugin = byPlugin && name == "translate-draft"; byPlugin {
		// The plugin translates instead of a model; the proposal, its decision and undo stay as they are.
		if _, err := s.translatorFor(ctx, plugin); err != nil {
			return nil, err
		}
		src := editable
		if sel != nil {
			src = editable[sel.Start:sel.End]
		}
		ref, run = translatorPrefix+plugin, s.pluginDraftRun(plugin, src, target, d.Subject, withSubject)
	} else {
		if ref = cmp.Or(ref, conv.Model, st.DefaultModel); ref == "" {
			return nil, ErrAINotConfigured
		}
		model, err := s.resolveModel(ctx, ref)
		if err != nil {
			return nil, err
		}
		e, err := s.aiEngine()
		if err != nil {
			return nil, err
		}
		system := sk.Body + "\n\n" + ai.UntrustedSystemNote
		if name == "translate-draft" { // never "the original's language": the skill does not see it
			system += "\n\nTarget language: " + target + "."
		}
		// Raw: the values go back into the draft's HTML escaped, once it is parsed (host)
		run, req = e.Run, ai.Request{Model: model, System: system, MaxTurns: 1, Reasoning: true, Effort: conv.Effort, Raw: true}
	}
	if err := s.gateway().Record(ctx, gateway.Entry{Source: gateway.SourceSkill, Tool: "skill:" + name, TargetKind: "draft", TargetID: d.ID, Decision: "run"}); err != nil {
		return nil, err
	}
	if byPlugin { // the plugin gets the draft: the audit log says so (not ref: a plugin model is "plugin/…" too)
		if err := s.gateway().Record(ctx, gateway.Entry{Source: gateway.SourcePlugin, Extension: plugin, Tool: "plugin:" + plugin + "/translate",
			TargetKind: "draft", TargetID: d.ID, Decision: "run"}); err != nil {
			return nil, err
		}
	}

	var job *runJob
	var h *ai.History
	err = s.ledger.claim(ctx, conv, ref, []string{}, func() error { return s.checkWitDraft(ctx, conv.DraftID) }, func(j *runJob) (err error) {
		job = j
		h, err = s.claimed(ctx, j)
		return err
	})
	if err != nil {
		return nil, err
	}
	job.base, job.command = h, ai.Command{Skill: name}
	if name == "translate-draft" {
		job.command.Lang = target
	}
	// A skill reads only its target (ADR 0009): no mail, not even the one replied to.
	prompt := "The editable part of the draft, as HTML:\n<draft>\n" + editable + "\n</draft>"
	if withSubject && d.Subject != "" {
		prompt = "The draft's subject:\n<subject>\n" + d.Subject + "\n</subject>\n\n" + prompt
	}
	if sel != nil {
		prompt = "<selection>\n" + editable[sel.Start:sel.End] + "\n</selection>"
	}
	job.host = func(ctx context.Context, res ai.Result) ai.Result {
		out := ""
		for _, m := range res.Messages {
			if m.Role == "model" {
				out = m.Text
			}
		}
		m, _ := s.masker() // without it there is nothing to restore: a plugin's text already is
		if out = unfence(m.DropCut(out)); out == "" {
			return ai.Result{Err: &ai.Error{Code: ai.CodeBadResponse, Msg: "the skill returned no text"}}
		}
		outText := out
		var newSubj *string
		if sel == nil {
			subj, body, ok := splitSubject(out)
			if ok && withSubject {
				newSubj = new(m.Restore(subj))
			}
			outText = body
		}
		outText = m.RestoreHTML(outText)
		p, err := s.proposeUpdateWithSubject(ctx, d.ID, newSubj, func(old string, rev int64) (string, error) {
			if sel == nil {
				return cleanHTML(outText), nil
			}
			if rev != sel.Revision {
				return "", ErrStaleDraft
			}
			return spliceSelection(old, *sel, outText)
		})
		if err == nil && name == "translate-draft" {
			p.Summary = kindTranslate
		}
		if err == nil {
			err = s.gateway().Record(ctx, gateway.Entry{Source: gateway.SourceWit, Tool: "update_draft", TargetKind: "draft", TargetID: d.ID, Decision: "approve"})
		}
		if err != nil {
			return ai.Result{Err: &ai.Error{Code: ai.CodeInternal, Msg: err.Error()}}
		}
		job.prepared.Store("skill", p)
		return ai.Result{Interrupts: []ai.ToolCall{{ID: "skill", Name: "update_draft"}}}
	}
	out := s.newStream()
	req.Prompt = prompt
	go s.drive(job, end, run, req, out)
	return out, nil
}

// unfence drops a markdown code fence around a model's answer.
// splitSubject parses a skill's answer as HTML and takes out its first
// <subject> element: ok, its text and the rest, else out as it is. A <body>
// wrapper is dropped by the parser; children of an unclosed <subject> stay
// in the body.
func splitSubject(out string) (subject, body string, ok bool) {
	ctx := &xhtml.Node{Type: xhtml.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := xhtml.ParseFragment(strings.NewReader(out), ctx)
	if err != nil {
		return "", out, false
	}
	root := &xhtml.Node{Type: xhtml.ElementNode, Data: "div", DataAtom: atom.Div}
	for _, n := range nodes {
		root.AppendChild(n)
	}
	var subj *xhtml.Node
	for n := range root.Descendants() {
		if n.Type == xhtml.ElementNode && n.Data == "subject" {
			subj = n
			break
		}
	}
	if subj == nil {
		return "", out, false
	}
	var text strings.Builder
	for c := subj.FirstChild; c != nil; {
		next := c.NextSibling
		subj.RemoveChild(c)
		if c.Type == xhtml.TextNode {
			text.WriteString(c.Data)
		} else {
			subj.Parent.InsertBefore(c, subj)
		}
		c = next
	}
	subj.Parent.RemoveChild(subj)
	var b strings.Builder
	for c := range root.ChildNodes() {
		if err := xhtml.Render(&b, c); err != nil {
			return "", out, false
		}
	}
	return strings.Join(strings.Fields(text.String()), " "), strings.TrimSpace(b.String()), true
}

func unfence(s string) string {
	s = strings.TrimSpace(s)
	if rest, ok := strings.CutPrefix(s, "```"); ok {
		if i := strings.IndexByte(rest, '\n'); i >= 0 {
			rest = rest[i+1:] // the fence's language tag
		}
		s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), "```"))
	}
	return s
}

// Bounds of changeKind's word comparison; past them a change is a rewrite.
const (
	maxDiffWords = 4000
	maxDiffCells = 4_000_000
)

// changeKind classifies a new editable part against the old (sanitized) one:
// initial if the old one is blank; rewrite if more than half of its words
// changed (replaced, removed or added, counted on the longer side), or if the
// texts are too long to compare; else partial. Han and kana characters count
// as one word each. A cancelled ctx stops the comparison.
func changeKind(ctx context.Context, old, new string) (string, error) {
	if mail.Blank(old) {
		return kindInitial, nil
	}
	a, b := words(old), words(new)
	if len(a) > maxDiffWords || len(b) > maxDiffWords || len(a)*len(b) > maxDiffCells {
		return kindRewrite, nil
	}
	prev, cur := make([]int, len(b)+1), make([]int, len(b)+1)
	for i := range a {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		for j := range b {
			if a[i] == b[j] {
				cur[j+1] = prev[j] + 1
			} else {
				cur[j+1] = max(prev[j+1], cur[j])
			}
		}
		prev, cur = cur, prev
	}
	same := prev[len(b)]
	if 2*max(len(a)-same, len(b)-same) > len(a) {
		return kindRewrite, nil
	}
	return kindPartial, nil
}

// sameHTML reports whether two editable parts are the same sanitized HTML: a
// change of formatting alone (bold, a link) is a change, and so is whitespace,
// which <pre> and white-space styles show.
func sameHTML(a, b string) bool { return cleanHTML(a) == cleanHTML(b) }

func words(h string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range mail.HTMLToText(h) {
		switch {
		case unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana):
			flush()
			out = append(out, string(r))
		case unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) || r == '\'':
			cur.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return out
}
