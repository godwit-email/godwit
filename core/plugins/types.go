// Package plugins runs third-party TypeScript plugins (spec 5.3–5.5): manifest
// validation, the sandbox and the ctx API. It does not import core; the host
// side is injected.
package plugins

import (
	"errors"
	"slices"
	"time"
)

// MailMessage is a message as a plugin sees it. Text and HTML are cut to the
// host's limit, with Truncated set.
type MailMessage struct {
	ID          int64            `json:"id"`
	Subject     string           `json:"subject"`
	From        string           `json:"from"`
	To          []string         `json:"to"`
	Cc          []string         `json:"cc"`
	Date        time.Time        `json:"date"`
	Text        string           `json:"text"`
	HTML        string           `json:"html"`
	Truncated   bool             `json:"truncated"`
	Attachments []AttachmentMeta `json:"attachments"`
}

type AttachmentMeta struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Size int64  `json:"size"`
}

type MailSummary struct {
	ID      int64     `json:"id"`
	Subject string    `json:"subject"`
	From    string    `json:"from"`
	Date    time.Time `json:"date"`
	Snippet string    `json:"snippet"`
}

type DraftInput struct {
	AccountID int64    `json:"accountId"`
	To        []string `json:"to"`
	Cc        []string `json:"cc"`
	Subject   string   `json:"subject"`
	HTML      string   `json:"html"`
}

// DraftUpdate replaces a draft's body if the draft is still at Revision.
type DraftUpdate struct {
	DraftID  int64  `json:"draftId"`
	Revision int64  `json:"revision"`
	HTML     string `json:"html"`
}

type FetchRequest struct {
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

type FetchResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

type InvocationKind string

const (
	KindInit       InvocationKind = "init"
	KindHook       InvocationKind = "hook"
	KindBeforeSend InvocationKind = "beforeSend"
	KindAction     InvocationKind = "action"
	KindCard       InvocationKind = "card"
	KindTool       InvocationKind = "tool"
)

// Invocation is what the host allows one top-level call into a plugin: whether
// it may write mail, and the hosts it may fetch from (none: no egress). What the
// plugin declared when registering does not widen it.
type Invocation struct {
	ID          string
	Kind        InvocationKind
	Write       bool
	EgressHosts []string
	// Unapproved: the call needed an egress approval it did not get, so it
	// has no hosts; a fetch to a declared host is denied as approval_required.
	Unapproved bool
	// Config is the call's ctx.config, taken when the host admitted the call
	// (before it waits for the VM); nil makes ctx.config throw.
	Config *ConfigSnapshot
}

// ConfigSnapshot is a plugin's configuration at one config version; Err is
// why it could not be read (ctx.config then throws). Never changed once made.
type ConfigSnapshot struct {
	Values  map[string]any
	Version int64
	Err     error
}

// EgressFor is the egress rule for one call of kind into plugin m (plan 4
// 规格解释): the hosts its Invocation may fetch from, and whether the call
// needs an egress approval listing them first. granted: the plugin's current
// permissions are granted; holdsMail: the plugin was ever handed mail content
// (its storage may keep it, across calls, updates and restarts); scopeReadMail:
// the call's input may carry mail content (a Wit run that has read mail calling
// a tool, an action on a selection). Init, card and beforeSend never fetch; a
// hook gets every declared host; an action or a tool needs approval when the
// plugin holds net and any of mail:read, holdsMail or scopeReadMail. Since
// ctx.storage outlives a call, "this call read no mail" is never trusted for
// a plugin that has or ever had mail.
func EgressFor(kind InvocationKind, m Manifest, granted, holdsMail, scopeReadMail bool) (hosts []string, needsApproval bool) {
	net := m.Permissions.Net
	if !granted || len(net) == 0 {
		return nil, false
	}
	readMail := slices.Contains(m.Permissions.Mail, "read")
	switch kind {
	case KindHook:
	case KindAction, KindTool:
		needsApproval = readMail || holdsMail || scopeReadMail
	default: // init, card, beforeSend, and anything unknown
		return nil, false
	}
	return slices.Clone(net), needsApproval
}

var (
	ErrPermission  = errors.New("plugin: permission denied")
	ErrTimeout     = errors.New("plugin: call timed out")
	ErrMemoryLimit = errors.New("plugin: memory limit exceeded")
	ErrClosed      = errors.New("plugin: closed")
	ErrTooLarge    = errors.New("plugin: data too large")
)

// PermissionError is a ctx API call the manifest or the invocation does not
// allow. Its message names only the permission.
type PermissionError struct{ Perm string }

func (e *PermissionError) Error() string { return "permission denied: " + e.Perm }

func (e *PermissionError) Is(target error) bool { return target == ErrPermission }
