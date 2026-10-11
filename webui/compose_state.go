package webui

// Account is a sender the page can pick.
type Account struct {
	// ID and Email identify the account.
	ID    int64  `json:"id"`
	Email string `json:"email"`
}

// Attachment is a file attached to the draft.
type Attachment struct {
	// ID names it to RemoveAttachment; Size is in bytes.
	ID       int64  `json:"id"`
	Filename string `json:"filename"`
	Size     int    `json:"size"`
}

// ComposeState is what a compose page shows when it opens.
type ComposeState struct {
	// DraftID is the window's draft; Accounts are the senders to pick from.
	DraftID  int64     `json:"draftId"`
	Accounts []Account `json:"accounts"`
	// From is the sending account's ID.
	From int64 `json:"from"`
	// To, Cc, Bcc, ReplyTo and Subject are the header fields as stored.
	To      []string `json:"to"`
	Cc      []string `json:"cc"`
	Bcc     []string `json:"bcc"`
	ReplyTo string   `json:"replyTo"`
	Subject string   `json:"subject"`
	// Body is the editable part as sanitized HTML.
	Body string `json:"body"`
	// Quote and Signature are read-only plain text; SignatureImages are the
	// signature's images as data: URLs, shown after its text.
	Quote           string   `json:"quote"`
	Signature       string   `json:"signature"`
	SignatureImages []string `json:"signatureImages"`
	// Attachments are the draft's files.
	Attachments []Attachment `json:"attachments"`
	// Revision is what the next save must pass; Theme styles the page.
	Revision int64 `json:"revision"`
	Theme    Theme `json:"theme"`
	// UpdatedAt is when the draft was last stored, in Unix milliseconds.
	UpdatedAt int64 `json:"updatedAt"`
	// Text is the page's copy: i18n key → text in the UI language.
	Text map[string]string `json:"text"`
	// Wit is what the Wit panel offers, as WitInfo.
	Wit WitInfo `json:"wit"`
}

// ComposeEdit is what the page saves: the fields it edits, and the revision
// they were made against.
type ComposeEdit struct {
	// To, Cc, Bcc, ReplyTo, Subject and Body (HTML) are the edited fields, as
	// in ComposeState; Revision is the one they were made against.
	To       []string `json:"to"`
	Cc       []string `json:"cc"`
	Bcc      []string `json:"bcc"`
	ReplyTo  string   `json:"replyTo"`
	Subject  string   `json:"subject"`
	Body     string   `json:"body"`
	Revision int64    `json:"revision"`
}

// SaveResult is the outcome of a save.
type SaveResult struct {
	// Revision is the draft's new revision, to pass to the next save.
	Revision int64 `json:"revision"`
	// Conflict means the draft changed elsewhere: nothing was saved.
	Conflict bool `json:"conflict"`
}

// Change is the outcome of an account switch or an attachment change.
type Change struct {
	// Revision and Attachments are the draft's after the change.
	Revision    int64        `json:"revision"`
	Attachments []Attachment `json:"attachments"`
	// Conflict means the draft changed elsewhere: nothing was done.
	Conflict bool `json:"conflict"`
	// Signature is the draft's signature as plain text, SignatureImages its
	// images as data: URLs.
	Signature       string   `json:"signature"`
	SignatureImages []string `json:"signatureImages"`
	// UpdatedAt is when the draft was last stored, in Unix milliseconds.
	UpdatedAt int64 `json:"updatedAt"`
}

// AttachResult is the outcome of adding files: a partial success is no error.
type AttachResult struct {
	Change
	// TooLarge are the files over the size limit; Failed those that failed
	// for another reason (logged).
	TooLarge []string `json:"tooLarge"`
	Failed   []string `json:"failed"`
}

// SendWarning is a plugin's word about the draft.
type SendWarning struct {
	// Plugin is its name; Message what it said.
	Plugin  string `json:"plugin"`
	Message string `json:"message"`
}

// SendResult is the outcome of a send.
type SendResult struct {
	// Sent means it is in the outbox and the window closes.
	Sent bool `json:"sent"`
	// Conflict means the draft is not at the revision sent: nothing was sent.
	Conflict bool `json:"conflict"`
	// Warnings, with Digest, need the user's go-ahead: Send again with Digest.
	Warnings []SendWarning `json:"warnings"`
	Digest   string        `json:"digest"`
	// Blocked is the plugin that refused the draft, and its message.
	Blocked *SendWarning `json:"blocked"`
}
