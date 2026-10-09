// Package plugins runs third-party TypeScript plugins (spec 5.3–5.5): manifest
// validation, the sandbox and the ctx API. It does not import core; the host
// side is injected.
package plugins

import (
	"errors"
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
