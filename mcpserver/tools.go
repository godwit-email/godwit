package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/rpc"
)

// maxBody caps a message body returned by get_message.
const maxBody = 64 << 10

var tools = []tool{
	{"list_accounts", "List the mail accounts: ID, address, name and which one is the default.", true,
		`{"type":"object","additionalProperties":false}`, listAccounts},
	{"list_folders", "List an account's folders with their IDs, roles and unread counts.", true,
		`{"type":"object","properties":{"account_id":{"type":"integer","minimum":1}},"required":["account_id"],"additionalProperties":false}`, listFolders},
	{"list_threads", "List conversations, newest first, in one folder (folder_id) or across every account's inbox or starred mail (unified); give exactly one. Pass next_cursor back as cursor for the next page.", true,
		`{"type":"object","properties":{"folder_id":{"type":"integer","minimum":1},"unified":{"type":"string","enum":["inbox","starred"]},"cursor":{"type":"string"},"limit":{"type":"integer","minimum":1,"maximum":50}},"additionalProperties":false}`, listThreads},
	{"get_thread", "Get a conversation's messages, oldest first, without their bodies (use get_message).", true,
		`{"type":"object","properties":{"thread_id":{"type":"integer","minimum":1}},"required":["thread_id"],"additionalProperties":false}`, getThread},
	{"get_message", "Get a message's plain-text body (up to 64 KB) and metadata; attachments are listed by name, type and size, never fetched.", true,
		`{"type":"object","properties":{"message_id":{"type":"integer","minimum":1}},"required":["message_id"],"additionalProperties":false}`, getMessage},
	{"search", "Search synced mail, in one account or (without account_id) in all of them.", true,
		`{"type":"object","properties":{"query":{"type":"string","minLength":1},"account_id":{"type":"integer","minimum":1},"limit":{"type":"integer","minimum":1,"maximum":50}},"required":["query"],"additionalProperties":false}`, search},
	{"list_drafts", "List an account's drafts, most recently saved first.", true,
		`{"type":"object","properties":{"account_id":{"type":"integer","minimum":1}},"required":["account_id"],"additionalProperties":false}`, listDrafts},
	{"get_draft", "Get a draft: recipients, subject, its editable body, the quoted mail and the signature, and its revision (update_draft needs it).", true,
		`{"type":"object","properties":{"draft_id":{"type":"integer","minimum":1}},"required":["draft_id"],"additionalProperties":false}`, getDraft},
	{"create_draft", "Create a draft for the user to review and send; it is not sent. For a reply give reply_to_message_id (and reply_all to answer everyone): the draft is in that message's account, addressed and quoted for you, and to, cc and subject override only if given. A new draft needs to and subject and uses account_id or the default account. body is plain text.", false,
		`{"type":"object","properties":{"to":{"type":"array","items":{"type":"string"}},"cc":{"type":"array","items":{"type":"string"}},"subject":{"type":"string"},"body":{"type":"string"},"account_id":{"type":"integer","minimum":1},"reply_to_message_id":{"type":"integer","minimum":1},"reply_all":{"type":"boolean"}},"required":["body"],"additionalProperties":false}`, createDraft},
	{"update_draft", "Change a draft's subject, to, cc or plain-text body; fields left out stay as they are, and the quote and signature are kept. revision is the draft's from get_draft: if the draft changed since, nothing is saved; read it again.", false,
		`{"type":"object","properties":{"draft_id":{"type":"integer","minimum":1},"revision":{"type":"integer","minimum":1},"subject":{"type":"string"},"to":{"type":"array","items":{"type":"string"}},"cc":{"type":"array","items":{"type":"string"}},"body":{"type":"string"}},"required":["draft_id","revision"],"additionalProperties":false}`, updateDraft},
	{"send_draft", "Ask Godwit to send a draft as it is at revision (from get_draft). The user's Send through MCP setting decides: off fails; confirm (the default) returns at once while Godwit asks the user, who may decline; direct queues it in the outbox. If the draft changed since revision, nothing is sent; read it again.", false,
		`{"type":"object","properties":{"draft_id":{"type":"integer","minimum":1},"revision":{"type":"integer","minimum":1}},"required":["draft_id","revision"],"additionalProperties":false}`, sendDraft},
}

func listAccounts(ctx context.Context, c *rpc.Client, _ json.RawMessage) (string, error) {
	var accs []core.Account
	if err := c.Call(ctx, "ListAccounts", &accs); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, a := range accs {
		fmt.Fprintf(&b, "account %d · %s · %s", a.ID, a.Email, a.Name)
		if a.IsDefault {
			b.WriteString(" · default")
		}
		b.WriteByte('\n')
	}
	return orNone(b.String(), "no accounts"), nil
}

func listFolders(ctx context.Context, c *rpc.Client, args json.RawMessage) (string, error) {
	var a struct {
		AccountID int64 `json:"account_id"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	var fs []core.Folder
	if err := c.Call(ctx, "ListFolders", &fs, a.AccountID); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, f := range fs {
		fmt.Fprintf(&b, "folder %d · %q", f.ID, f.Name)
		if f.Role != "" {
			fmt.Fprintf(&b, " · role %s", f.Role)
		}
		fmt.Fprintf(&b, " · %d unread\n", f.Unread)
	}
	return orNone(b.String(), "no folders"), nil
}

func listThreads(ctx context.Context, c *rpc.Client, args json.RawMessage) (string, error) {
	var a struct {
		FolderID int64  `json:"folder_id"`
		Unified  string `json:"unified"`
		Cursor   string `json:"cursor"`
		Limit    int    `json:"limit"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if (a.FolderID == 0) == (a.Unified == "") {
		return "", errors.New("give exactly one of folder_id and unified")
	}
	if a.Limit == 0 {
		a.Limit = 50
	}
	var page core.ThreadPage
	if err := c.Call(ctx, "ListThreads", &page, core.View{FolderID: a.FolderID, Unified: a.Unified}, a.Cursor, a.Limit); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, t := range page.Items {
		fmt.Fprintf(&b, "thread %d · %d messages, %d unread · last %s", t.ID, t.Count, t.Unread, stamp(t.LastDate))
		if t.HasAttachments {
			b.WriteString(" · has attachments")
		}
		b.WriteByte('\n')
		id := fmt.Sprintf("thread-%d", t.ID)
		untrusted(&b, id, "subject", t.Subject)
		untrusted(&b, id, "participants", strings.Join(t.Participants, ", "))
		untrusted(&b, id, "snippet", t.Snippet)
		b.WriteByte('\n')
	}
	if page.NextCursor != "" {
		fmt.Fprintf(&b, "next_cursor: %s\n", page.NextCursor)
	}
	return orNone(b.String(), "no threads"), nil
}

func getThread(ctx context.Context, c *rpc.Client, args json.RawMessage) (string, error) {
	var a struct {
		ThreadID int64 `json:"thread_id"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	var t core.Thread
	if err := c.Call(ctx, "GetThread", &t, a.ThreadID); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "thread %d · %d messages\n", t.ID, len(t.Messages))
	untrusted(&b, fmt.Sprintf("thread-%d", t.ID), "subject", t.Subject)
	for _, m := range t.Messages {
		b.WriteByte('\n')
		summary(&b, m)
	}
	return b.String(), nil
}

func getMessage(ctx context.Context, c *rpc.Client, args json.RawMessage) (string, error) {
	var a struct {
		MessageID int64 `json:"message_id"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	var m core.MessageText
	if err := c.Call(ctx, "GetMessageText", &m, a.MessageID); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "account %d\n", m.AccountID)
	summary(&b, m.MessageSummary)
	id := fmt.Sprintf("message-%d", m.ID)
	untrusted(&b, id, "reply_to", m.ReplyTo)
	for _, at := range m.Attachments {
		fmt.Fprintf(&b, "attachment %d · %d bytes", at.ID, at.Size)
		if at.Inline {
			b.WriteString(" · inline")
		}
		b.WriteByte('\n')
		untrusted(&b, id, "attachment_name", at.Filename)
		untrusted(&b, id, "attachment_type", at.ContentType)
	}
	body, cut := truncate(m.Text, maxBody)
	untrusted(&b, id, "body", body)
	if cut || m.Truncated {
		b.WriteString("(body truncated)\n")
	}
	return b.String(), nil
}

func search(ctx context.Context, c *rpc.Client, args json.RawMessage) (string, error) {
	var a struct {
		Query     string `json:"query"`
		AccountID int64  `json:"account_id"`
		Limit     int    `json:"limit"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if a.Limit == 0 {
		a.Limit = 50
	}
	var ms []core.MessageSummary
	if err := c.Call(ctx, "Search", &ms, a.AccountID, a.Query, a.Limit); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, m := range ms {
		summary(&b, m)
		b.WriteByte('\n')
	}
	return orNone(b.String(), "no messages found"), nil
}

func listDrafts(ctx context.Context, c *rpc.Client, args json.RawMessage) (string, error) {
	var a struct {
		AccountID int64 `json:"account_id"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	var ds []core.Draft
	if err := c.Call(ctx, "ListDrafts", &ds, a.AccountID); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, d := range ds {
		draft(&b, d, false)
		b.WriteByte('\n')
	}
	return orNone(b.String(), "no drafts"), nil
}

func getDraft(ctx context.Context, c *rpc.Client, args json.RawMessage) (string, error) {
	var a struct {
		DraftID int64 `json:"draft_id"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	var d core.Draft
	if err := c.Call(ctx, "GetDraft", &d, a.DraftID); err != nil {
		return "", err
	}
	var b strings.Builder
	draft(&b, d, true)
	return b.String(), nil
}

// edit is the draft fields create_draft and update_draft change; nil leaves
// one as it is.
type edit struct {
	To      *[]string `json:"to"`
	Cc      *[]string `json:"cc"`
	Subject *string   `json:"subject"`
	Body    *string   `json:"body"`
}

// apply merges e into d. The body replaces the editable part only; the quote
// and signature stay.
func (e edit) apply(d *core.Draft) {
	if e.To != nil {
		d.To = *e.To
	}
	if e.Cc != nil {
		d.Cc = *e.Cc
	}
	if e.Subject != nil {
		d.Subject = *e.Subject
	}
	if e.Body != nil {
		_, quote, sig := mail.SplitDraft(d.HTML)
		d.HTML = mail.JoinDraft(paragraphs(*e.Body), quote, sig)
	}
}

func createDraft(ctx context.Context, c *rpc.Client, args json.RawMessage) (string, error) {
	var a struct {
		edit
		AccountID int64 `json:"account_id"`
		ReplyTo   int64 `json:"reply_to_message_id"`
		ReplyAll  bool  `json:"reply_all"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	mode, account := "new", a.AccountID
	switch {
	case a.ReplyTo != 0:
		mode = "reply"
		if a.ReplyAll {
			mode = "reply_all"
		}
		var src core.MessageText
		if err := c.Call(ctx, "GetMessageText", &src, a.ReplyTo); err != nil {
			return "", err
		}
		if account != 0 && account != src.AccountID {
			return "", fmt.Errorf("message %d is in account %d, not account %d; leave account_id out to reply from the message's account", a.ReplyTo, src.AccountID, account)
		}
		account = src.AccountID
	case a.ReplyAll:
		return "", errors.New("reply_all needs reply_to_message_id")
	case a.To == nil || a.Subject == nil:
		return "", errors.New("a new draft needs to and subject")
	case account == 0:
		var accs []core.Account
		if err := c.Call(ctx, "ListAccounts", &accs); err != nil {
			return "", err
		}
		for _, acc := range accs {
			if acc.IsDefault {
				account = acc.ID
			}
		}
		if account == 0 {
			return "", errors.New("no default account; give account_id")
		}
	}
	var d core.Draft
	if err := c.Call(ctx, "NewDraft", &d, account, mode, a.ReplyTo); err != nil {
		return "", err
	}
	return save(ctx, c, d.ID, 0, a.edit)
}

func updateDraft(ctx context.Context, c *rpc.Client, args json.RawMessage) (string, error) {
	var a struct {
		edit
		DraftID  int64 `json:"draft_id"`
		Revision int64 `json:"revision"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if a.Revision <= 0 { // save treats 0 as "no check"
		return "", errors.New("invalid argument: revision (expected integer ≥ 1)")
	}
	return save(ctx, c, a.DraftID, a.Revision, a.edit)
}

// sendDraft relays the request; Godwit's core decides by the setting.
func sendDraft(ctx context.Context, c *rpc.Client, args json.RawMessage) (string, error) {
	var a struct {
		DraftID  int64 `json:"draft_id"`
		Revision int64 `json:"revision"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	var sent bool
	if err := c.Call(ctx, "RequestMCPSend", &sent, a.DraftID, a.Revision); err != nil {
		return "", err
	}
	if sent {
		return "sent: the draft is in Godwit's outbox", nil
	}
	return "waiting for the user to confirm in Godwit; it is not sent yet", nil
}

// save reads the draft back, merges e and saves it against the revision read
// (which must be revision, unless that is 0).
func save(ctx context.Context, c *rpc.Client, id, revision int64, e edit) (string, error) {
	var d core.Draft
	if err := c.Call(ctx, "GetDraft", &d, id); err != nil {
		return "", err
	}
	if revision != 0 && d.Revision != revision {
		return "", core.ErrStaleDraft
	}
	e.apply(&d)
	if err := c.Call(ctx, "SaveDraft", &d, d); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("saved; it is not sent\n")
	draft(&b, d, true)
	return b.String(), nil
}

// decode decodes checked arguments into v; it fails only if they do not fit
// v, and then says so without repeating them.
func decode(args json.RawMessage, v any) error {
	if json.Unmarshal(args, v) != nil {
		return errors.New("invalid arguments")
	}
	return nil
}

// systemFlags are IMAP's system flags (matched without case) as summary
// names them; any other flag is a server's keyword, so mail-derived.
var systemFlags = map[string]string{`\seen`: `\Seen`, `\answered`: `\Answered`, `\flagged`: `\Flagged`,
	`\draft`: `\Draft`, `\deleted`: `\Deleted`}

// summary writes a message as listed.
func summary(b *strings.Builder, m core.MessageSummary) {
	fmt.Fprintf(b, "message %d · thread %d · folder %d · %s", m.ID, m.ThreadID, m.FolderID, stamp(m.Date))
	var flags, keywords []string
	for _, f := range m.Flags {
		if name, ok := systemFlags[strings.ToLower(f)]; ok {
			flags = append(flags, name)
		} else {
			keywords = append(keywords, f)
		}
	}
	if len(flags) > 0 {
		fmt.Fprintf(b, " · flags %s", strings.Join(flags, " "))
	}
	if m.HasAttachments {
		b.WriteString(" · has attachments")
	}
	b.WriteByte('\n')
	id := fmt.Sprintf("message-%d", m.ID)
	untrusted(b, id, "from", m.From)
	untrusted(b, id, "to", strings.Join(m.To, ", "))
	untrusted(b, id, "cc", strings.Join(m.Cc, ", "))
	untrusted(b, id, "subject", m.Subject)
	untrusted(b, id, "snippet", m.Snippet)
	untrusted(b, id, "keywords", strings.Join(keywords, " "))
}

// draft writes d; full adds its body, quote, signature and attachments. All
// of a draft's text is marked: replies take addresses, subject and quote
// from mail.
func draft(b *strings.Builder, d core.Draft, full bool) {
	fmt.Fprintf(b, "draft %d · account %d · %s", d.ID, d.AccountID, d.Mode)
	if d.SourceMessageID != 0 {
		fmt.Fprintf(b, " to message %d", d.SourceMessageID)
	}
	fmt.Fprintf(b, " · revision %d · saved %s\n", d.Revision, stamp(d.UpdatedAt))
	id := fmt.Sprintf("draft-%d", d.ID)
	untrusted(b, id, "to", strings.Join(d.To, ", "))
	untrusted(b, id, "cc", strings.Join(d.Cc, ", "))
	untrusted(b, id, "bcc", strings.Join(d.Bcc, ", "))
	untrusted(b, id, "subject", d.Subject)
	if !full {
		return
	}
	ed, quote, sig := mail.SplitDraft(d.HTML)
	untrusted(b, id, "body", mail.HTMLToText(ed))
	untrusted(b, id, "quote", mail.HTMLToText(quote))
	untrusted(b, id, "signature", mail.HTMLToText(sig))
	for _, at := range d.Attachments {
		fmt.Fprintf(b, "attachment %d · %d bytes\n", at.ID, at.Size)
		untrusted(b, id, "attachment_name", at.Filename)
		untrusted(b, id, "attachment_type", at.ContentType)
	}
}

// paragraphs is plain text as HTML: blank lines separate paragraphs, line
// breaks stay.
func paragraphs(text string) string {
	var b strings.Builder
	for p := range strings.SplitSeq(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n") {
		if p = strings.Trim(p, "\n"); p != "" {
			b.WriteString("<p>" + strings.ReplaceAll(html.EscapeString(p), "\n", "<br>") + "</p>")
		}
	}
	return b.String()
}

// truncate cuts s to at most n bytes on a rune boundary.
func truncate(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n], true
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func orNone(s, none string) string {
	if s == "" {
		return none
	}
	return s
}
