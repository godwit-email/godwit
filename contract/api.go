// Package contract checks that the Local API behaves like *core.Service: the
// same scenarios run against the typed service and through JSON-RPC.
package contract

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/rpc"
)

// API is the subset of *core.Service the contract scenarios use, with the
// same signatures.
type API interface {
	AddAccount(ctx context.Context, in core.AccountInput) (core.Account, error)
	CheckAccount(ctx context.Context, in core.AccountInput) (core.CheckReport, error)
	DetectProvider(ctx context.Context, email string) (auth.Provider, auth.Source, error)
	ExportAccounts(ctx context.Context) ([]byte, error)
	ListAccounts(ctx context.Context) ([]core.Account, error)
	ListFolders(ctx context.Context, accountID int64) ([]core.Folder, error)
	ListThreads(ctx context.Context, v core.View, cursor string, limit int) (core.ThreadPage, error)
	GetThread(ctx context.Context, id int64) (core.Thread, error)
	ListMessages(ctx context.Context, v core.View, cursor string, limit int) (core.MessagePage, error)
	GetMessage(ctx context.Context, id int64, loadRemoteImages bool) (core.Message, error)
	GetMessageText(ctx context.Context, id int64) (core.MessageText, error)
	Search(ctx context.Context, accountID int64, query string, limit int) ([]core.MessageSummary, error)
	GetAttachment(ctx context.Context, attachmentID int64) (string, core.AttachmentInfo, error)
	NewDraft(ctx context.Context, accountID int64, mode string, sourceMessageID int64) (core.Draft, error)
	SaveDraft(ctx context.Context, d core.Draft) (core.Draft, error)
	Send(ctx context.Context, draftID int64) error
	SendAcknowledged(ctx context.Context, draftID int64, digest string) error
	RequestMCPSend(ctx context.Context, draftID, revision int64) (sent bool, err error)
	ListOutbox(ctx context.Context, accountID int64) ([]core.OutboxItem, error)
	CancelOutbox(ctx context.Context, id int64) error
	GetSettings(ctx context.Context) (core.Settings, error)
	SetSettings(ctx context.Context, st core.Settings) (core.Settings, error)
	TrustedImageSenders(ctx context.Context) ([]string, error)
	TrustImageSender(ctx context.Context, from string) error
	UntrustImageSender(ctx context.Context, addr string) error
	BlockedSenders(ctx context.Context) ([]string, error)
	BlockSender(ctx context.Context, from string) error
	UnblockSender(ctx context.Context, addr string) error
	CheckModelService(ctx context.Context, id int64, in core.ModelServiceInput) (core.ServiceCheck, error)
	RunSkill(ctx context.Context, name string, t core.Target) (<-chan ai.Event, error)
	NewConversation(ctx context.Context, kind string, draftID int64) (core.Conversation, error)
	Ask(ctx context.Context, conversationID int64, input string) (<-chan core.WitEvent, error)
	SubscribeConversation(id int64) (<-chan core.WitEvent, func())
	ResolveApproval(ctx context.Context, id int64, accept bool) error
	ResolveApprovalWithArgs(ctx context.Context, id int64, accept bool, args json.RawMessage) error
	ListApprovals(ctx context.Context, conversationID int64) ([]core.Approval, error)
	Subscribe() (<-chan events.Event, func())
	RunAction(ctx context.Context, id string, t core.Target, opts core.RunActionOptions) (core.ActionResult, error)
	BeginOAuth(ctx context.Context, providerID, loginHint string, client core.OAuthClient) (flowID, authURL string, err error)
	CancelOAuth(flowID string)
}

// UIAPI extends API with the methods exposed only to RoleUI.
type UIAPI interface {
	API
	SetUIForeground(ctx context.Context, on bool) error
	SetComposeOpen(ctx context.Context, draftIDs []int64) error
	SetInboxOnly(on bool)
	InboxOnly() bool
	GetMessageLazyImages(ctx context.Context, id int64, loadRemoteImages bool) (core.Message, error)
	InlineImagesStream(ctx context.Context, id int64) (<-chan core.InlineImage, error)
	FetchAttachmentsStream(ctx context.Context, attachmentIDs []int64) (<-chan core.FetchedAttachment, error)
	CachedImage(ctx context.Context, src string) (core.CachedImageFile, error)
	CachedImageDims(src string) core.ImageDims
	ForgetImageFailure(urls []string)
	SystemTranslatorLanguages() []string
	SenderAvatar(ctx context.Context, messageID int64, allowRemote bool) (core.Avatar, error)
	ModelServiceAvatar(ctx context.Context, id int64) (core.Avatar, error)
	PluginReadme(ctx context.Context, name, lang string) (string, error)
	InstallPluginFromRegistryStream(ctx context.Context, name string) (<-chan core.PluginInstallEvent, error)
	PrepareModelStream(ctx context.Context, ref string) (<-chan core.PrepareModelEvent, error)
	DownloadRecognitionModelStream(ctx context.Context) (<-chan core.RecognitionModelProgress, error)
}

var (
	_ API   = (*core.Service)(nil)
	_ UIAPI = (*core.Service)(nil)
)

// Remote implements API over a Local API connection (full role). Streams
// that end abnormally, or carry an event that does not decode, are recorded
// in StreamErrs: the typed signatures have no place for them.
type Remote struct {
	C *rpc.Client

	mu   sync.Mutex
	errs []error
}

var (
	_ API   = (*Remote)(nil)
	_ UIAPI = (*Remote)(nil)
)

// StreamErrs returns every abnormal stream end so far.
func (r *Remote) StreamErrs() []error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]error(nil), r.errs...)
}

func (r *Remote) fail(err error) {
	r.mu.Lock()
	r.errs = append(r.errs, err)
	r.mu.Unlock()
}

func call[T any](r *Remote, ctx context.Context, method string, args ...any) (T, error) {
	var v T
	err := r.C.Call(ctx, method, &v, args...)
	if err != nil {
		var zero T
		return zero, err
	}
	return v, nil
}

// call2 decodes a two-value result, which crosses as a JSON array.
func call2[A, B any](r *Remote, ctx context.Context, method string, args ...any) (A, B, error) {
	var a A
	var b B
	var raw []json.RawMessage
	err := r.C.Call(ctx, method, &raw, args...)
	if err == nil && len(raw) != 2 {
		err = fmt.Errorf("contract: %s returned %d values, want 2", method, len(raw))
	}
	if err == nil {
		err = json.Unmarshal(raw[0], &a)
	}
	if err == nil {
		err = json.Unmarshal(raw[1], &b)
	}
	if err != nil {
		var za A
		var zb B
		return za, zb, err
	}
	return a, b, nil
}

// stream opens method's stream and decodes its events into a typed channel,
// closed when the stream ends; the returned func cancels it.
func stream[T any](r *Remote, ctx context.Context, method string, args ...any) (<-chan T, func(), error) {
	raw, cancel, err := r.C.Stream(ctx, method, args...)
	if err != nil {
		return nil, nil, err
	}
	out, done, exited := make(chan T, rpc.StreamBuffer), make(chan struct{}), make(chan struct{})
	record := func() {
		if err := r.C.StreamErr(raw); err != nil {
			r.fail(fmt.Errorf("%s: %w", method, err))
		}
	}
	go func() {
		defer close(exited)
		defer close(out)
		for b := range raw {
			var v T
			if err := json.Unmarshal(b, &v); err != nil {
				r.fail(fmt.Errorf("%s: event %s: %w", method, b, err))
				continue
			}
			select {
			case out <- v:
			case <-done:
				return
			}
		}
		record()
	}()
	// Cancelling forgets why the stream ended, so that is recorded first (the
	// forwarder may be blocked, or gone, before it could); cancel returns once
	// the forwarder has exited. A stream that ends just between the two counts
	// as cancelled, since the caller had already given up on it.
	var once sync.Once
	return out, func() { once.Do(func() { close(done); record(); cancel(); <-exited }) }, nil
}

// streamErr is a (<-chan T, error) method: ctx ending cancels it.
func streamErr[T any](r *Remote, ctx context.Context, method string, args ...any) (<-chan T, error) {
	ch, _, err := stream[T](r, ctx, method, args...)
	return ch, err
}

// streamStop is a (<-chan T, func()) method: it cannot fail, so a failure to
// open it is recorded and the channel comes back closed.
func streamStop[T any](r *Remote, method string, args ...any) (<-chan T, func()) {
	ch, cancel, err := stream[T](r, context.Background(), method, args...)
	if err != nil {
		r.fail(fmt.Errorf("%s: %w", method, err))
		c := make(chan T)
		close(c)
		return c, func() {}
	}
	return ch, cancel
}

func (r *Remote) AddAccount(ctx context.Context, in core.AccountInput) (core.Account, error) {
	return call[core.Account](r, ctx, "AddAccount", in)
}

func (r *Remote) CheckAccount(ctx context.Context, in core.AccountInput) (core.CheckReport, error) {
	return call[core.CheckReport](r, ctx, "CheckAccount", in)
}

func (r *Remote) DetectProvider(ctx context.Context, email string) (auth.Provider, auth.Source, error) {
	return call2[auth.Provider, auth.Source](r, ctx, "DetectProvider", email)
}

func (r *Remote) ExportAccounts(ctx context.Context) ([]byte, error) {
	return call[[]byte](r, ctx, "ExportAccounts")
}

func (r *Remote) ListAccounts(ctx context.Context) ([]core.Account, error) {
	return call[[]core.Account](r, ctx, "ListAccounts")
}

func (r *Remote) ListFolders(ctx context.Context, accountID int64) ([]core.Folder, error) {
	return call[[]core.Folder](r, ctx, "ListFolders", accountID)
}

func (r *Remote) ListThreads(ctx context.Context, v core.View, cursor string, limit int) (core.ThreadPage, error) {
	return call[core.ThreadPage](r, ctx, "ListThreads", v, cursor, limit)
}

func (r *Remote) GetThread(ctx context.Context, id int64) (core.Thread, error) {
	return call[core.Thread](r, ctx, "GetThread", id)
}

func (r *Remote) ListMessages(ctx context.Context, v core.View, cursor string, limit int) (core.MessagePage, error) {
	return call[core.MessagePage](r, ctx, "ListMessages", v, cursor, limit)
}

func (r *Remote) GetMessage(ctx context.Context, id int64, loadRemoteImages bool) (core.Message, error) {
	return call[core.Message](r, ctx, "GetMessage", id, loadRemoteImages)
}

func (r *Remote) GetMessageText(ctx context.Context, id int64) (core.MessageText, error) {
	return call[core.MessageText](r, ctx, "GetMessageText", id)
}

func (r *Remote) Search(ctx context.Context, accountID int64, query string, limit int) ([]core.MessageSummary, error) {
	return call[[]core.MessageSummary](r, ctx, "Search", accountID, query, limit)
}

func (r *Remote) GetAttachment(ctx context.Context, attachmentID int64) (string, core.AttachmentInfo, error) {
	return call2[string, core.AttachmentInfo](r, ctx, "GetAttachment", attachmentID)
}

func (r *Remote) NewDraft(ctx context.Context, accountID int64, mode string, sourceMessageID int64) (core.Draft, error) {
	return call[core.Draft](r, ctx, "NewDraft", accountID, mode, sourceMessageID)
}

func (r *Remote) SaveDraft(ctx context.Context, d core.Draft) (core.Draft, error) {
	return call[core.Draft](r, ctx, "SaveDraft", d)
}

func (r *Remote) Send(ctx context.Context, draftID int64) error {
	return r.C.Call(ctx, "Send", nil, draftID)
}

func (r *Remote) SendAcknowledged(ctx context.Context, draftID int64, digest string) error {
	return r.C.Call(ctx, "SendAcknowledged", nil, draftID, digest)
}

func (r *Remote) RequestMCPSend(ctx context.Context, draftID, revision int64) (bool, error) {
	return call[bool](r, ctx, "RequestMCPSend", draftID, revision)
}

func (r *Remote) ListOutbox(ctx context.Context, accountID int64) ([]core.OutboxItem, error) {
	return call[[]core.OutboxItem](r, ctx, "ListOutbox", accountID)
}

func (r *Remote) CancelOutbox(ctx context.Context, id int64) error {
	return r.C.Call(ctx, "CancelOutbox", nil, id)
}

func (r *Remote) GetSettings(ctx context.Context) (core.Settings, error) {
	return call[core.Settings](r, ctx, "GetSettings")
}

func (r *Remote) SetSettings(ctx context.Context, st core.Settings) (core.Settings, error) {
	return call[core.Settings](r, ctx, "SetSettings", st)
}

func (r *Remote) TrustedImageSenders(ctx context.Context) ([]string, error) {
	return call[[]string](r, ctx, "TrustedImageSenders")
}

func (r *Remote) BlockedSenders(ctx context.Context) ([]string, error) {
	return call[[]string](r, ctx, "BlockedSenders")
}

func (r *Remote) TrustImageSender(ctx context.Context, from string) error {
	return r.C.Call(ctx, "TrustImageSender", nil, from)
}

func (r *Remote) UntrustImageSender(ctx context.Context, addr string) error {
	return r.C.Call(ctx, "UntrustImageSender", nil, addr)
}

func (r *Remote) BlockSender(ctx context.Context, from string) error {
	return r.C.Call(ctx, "BlockSender", nil, from)
}

func (r *Remote) UnblockSender(ctx context.Context, addr string) error {
	return r.C.Call(ctx, "UnblockSender", nil, addr)
}

func (r *Remote) CheckModelService(ctx context.Context, id int64, in core.ModelServiceInput) (core.ServiceCheck, error) {
	return call[core.ServiceCheck](r, ctx, "CheckModelService", id, in)
}

func (r *Remote) RunSkill(ctx context.Context, name string, t core.Target) (<-chan ai.Event, error) {
	return streamErr[ai.Event](r, ctx, "RunSkill", name, t)
}

func (r *Remote) NewConversation(ctx context.Context, kind string, draftID int64) (core.Conversation, error) {
	return call[core.Conversation](r, ctx, "NewConversation", kind, draftID)
}

func (r *Remote) Ask(ctx context.Context, conversationID int64, input string) (<-chan core.WitEvent, error) {
	return streamErr[core.WitEvent](r, ctx, "Ask", conversationID, input)
}

func (r *Remote) SubscribeConversation(id int64) (<-chan core.WitEvent, func()) {
	return streamStop[core.WitEvent](r, "SubscribeConversation", id)
}

func (r *Remote) ResolveApproval(ctx context.Context, id int64, accept bool) error {
	return r.C.Call(ctx, "ResolveApproval", nil, id, accept)
}

func (r *Remote) ResolveApprovalWithArgs(ctx context.Context, id int64, accept bool, args json.RawMessage) error {
	return r.C.Call(ctx, "ResolveApprovalWithArgs", nil, id, accept, args)
}

func (r *Remote) ListApprovals(ctx context.Context, conversationID int64) ([]core.Approval, error) {
	return call[[]core.Approval](r, ctx, "ListApprovals", conversationID)
}

func (r *Remote) Subscribe() (<-chan events.Event, func()) {
	return streamStop[events.Event](r, "Subscribe")
}

func (r *Remote) RunAction(ctx context.Context, id string, t core.Target, opts core.RunActionOptions) (core.ActionResult, error) {
	return call[core.ActionResult](r, ctx, "RunAction", id, t, opts)
}

func (r *Remote) BeginOAuth(ctx context.Context, providerID, loginHint string, client core.OAuthClient) (string, string, error) {
	return call2[string, string](r, ctx, "BeginOAuth", providerID, loginHint, client)
}

// CancelOAuth has no result to fail with; a failed call is recorded.
func (r *Remote) CancelOAuth(flowID string) {
	if err := r.C.Call(context.Background(), "CancelOAuth", nil, flowID); err != nil {
		r.fail(fmt.Errorf("CancelOAuth: %w", err))
	}
}

func callVal[T any](r *Remote, method string, args ...any) T {
	var v T
	if err := r.C.Call(context.Background(), method, &v, args...); err != nil {
		r.fail(fmt.Errorf("%s: %w", method, err))
	}
	return v
}

// streamFallible decodes a fallible stream whose application end error crosses
// via $/stream.end and is yielded as wrapErr(err) on the returned channel.
func streamFallible[T any](r *Remote, ctx context.Context, wrapErr func(error) T, method string, args ...any) (<-chan T, error) {
	raw, cancel, err := r.C.Stream(ctx, method, args...)
	if err != nil {
		return nil, err
	}
	out := make(chan T, rpc.StreamBuffer)
	go func() {
		defer close(out)
		for b := range raw {
			var v T
			if err := json.Unmarshal(b, &v); err != nil {
				r.fail(fmt.Errorf("%s: event %s: %w", method, b, err))
				continue
			}
			out <- v
		}
		serr := r.C.StreamErr(raw)
		cancel()
		if serr != nil {
			if errors.Is(serr, rpc.ErrClosed) || errors.Is(serr, rpc.ErrStreamOverflow) || errors.Is(serr, rpc.ErrBadErrorData) {
				r.fail(fmt.Errorf("%s: %w", method, serr))
				return
			}
			out <- wrapErr(serr)
		}
	}()
	return out, nil
}

func (r *Remote) SetUIForeground(ctx context.Context, on bool) error {
	return r.C.Call(ctx, "SetUIForeground", nil, on)
}

func (r *Remote) SetComposeOpen(ctx context.Context, draftIDs []int64) error {
	return r.C.Call(ctx, "SetComposeOpen", nil, draftIDs)
}

func (r *Remote) SetInboxOnly(on bool) {
	if err := r.C.Call(context.Background(), "SetInboxOnly", nil, on); err != nil {
		r.fail(fmt.Errorf("SetInboxOnly: %w", err))
	}
}

func (r *Remote) InboxOnly() bool {
	return callVal[bool](r, "InboxOnly")
}

func (r *Remote) GetMessageLazyImages(ctx context.Context, id int64, loadRemoteImages bool) (core.Message, error) {
	return call[core.Message](r, ctx, "GetMessageLazyImages", id, loadRemoteImages)
}

func (r *Remote) InlineImagesStream(ctx context.Context, id int64) (<-chan core.InlineImage, error) {
	return streamFallible(r, ctx, func(err error) core.InlineImage { return core.InlineImage{Err: err} }, "InlineImagesStream", id)
}

func (r *Remote) FetchAttachmentsStream(ctx context.Context, attachmentIDs []int64) (<-chan core.FetchedAttachment, error) {
	return streamFallible(r, ctx, func(err error) core.FetchedAttachment { return core.FetchedAttachment{Err: err} }, "FetchAttachmentsStream", attachmentIDs)
}

func (r *Remote) CachedImage(ctx context.Context, src string) (core.CachedImageFile, error) {
	return call[core.CachedImageFile](r, ctx, "CachedImage", src)
}

func (r *Remote) CachedImageDims(src string) core.ImageDims {
	return callVal[core.ImageDims](r, "CachedImageDims", src)
}

func (r *Remote) ForgetImageFailure(urls []string) {
	if err := r.C.Call(context.Background(), "ForgetImageFailure", nil, urls); err != nil {
		r.fail(fmt.Errorf("ForgetImageFailure: %w", err))
	}
}

func (r *Remote) SystemTranslatorLanguages() []string {
	return callVal[[]string](r, "SystemTranslatorLanguages")
}

func (r *Remote) SenderAvatar(ctx context.Context, messageID int64, allowRemote bool) (core.Avatar, error) {
	return call[core.Avatar](r, ctx, "SenderAvatar", messageID, allowRemote)
}

func (r *Remote) ModelServiceAvatar(ctx context.Context, id int64) (core.Avatar, error) {
	return call[core.Avatar](r, ctx, "ModelServiceAvatar", id)
}

func (r *Remote) PluginReadme(ctx context.Context, name, lang string) (string, error) {
	return call[string](r, ctx, "PluginReadme", name, lang)
}

func (r *Remote) InstallPluginFromRegistryStream(ctx context.Context, name string) (<-chan core.PluginInstallEvent, error) {
	return streamFallible(r, ctx, func(err error) core.PluginInstallEvent { return core.PluginInstallEvent{Err: err} }, "InstallPluginFromRegistryStream", name)
}

func (r *Remote) PrepareModelStream(ctx context.Context, ref string) (<-chan core.PrepareModelEvent, error) {
	return streamFallible(r, ctx, func(err error) core.PrepareModelEvent { return core.PrepareModelEvent{Err: err} }, "PrepareModelStream", ref)
}

func (r *Remote) DownloadRecognitionModelStream(ctx context.Context) (<-chan core.RecognitionModelProgress, error) {
	return streamFallible(r, ctx, func(err error) core.RecognitionModelProgress { return core.RecognitionModelProgress{Err: err} }, "DownloadRecognitionModelStream")
}
