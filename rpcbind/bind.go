// Package rpcbind composes core with rpc: core's error codes, redaction and
// the methods exposed over the Local API. core does not depend on rpc.
//
// Its error table is the only place an error's wire code and i18n key are
// written; the native UI and the pages show errors by Key.
package rpcbind

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/godwit-email/godwit/rpc"
)

// table is the one list of an error's identity: core's exported errors (with
// those of core/ai, core/auth and core/conn that its methods return, and the
// context ones) by stable wire code, and the i18n key of what each means to the
// user, "error.<code>" unless Key says otherwise. Their messages are fixed
// English text. A row without a code has a message but does not cross the
// Local API; the first row an error is wins.
var table = []Error{
	// A native plugin's failures: its stderr is the plugin's text, not the user's.
	{Code: "plugin_exited", Err: plugins.ErrNativeExited, Key: "error.plugin_exited"},
	{Code: "plugin_timeout", Err: plugins.ErrTimeout, Key: "error.plugin_timeout"},
	{Code: "plugin_abi", Err: plugins.ErrNativeABI, Key: "error.plugin_abi"},
	{Code: "redirect_port_busy", Err: auth.ErrRedirectPortBusy, Key: "settings.models.login.port_busy"},
	{Code: "use_assist", Err: core.ErrUseAssist},
	{Code: "stale_approval", Err: core.ErrStaleApproval},
	{Code: "too_large", Err: core.ErrTooLarge},
	// The Local API's own: a request, result or stream event over one message's budget.
	{Code: "message_too_large", Err: rpc.ErrTooLarge},
	{Code: "link_ref", Err: core.ErrLinkRef},
	{Code: "placeholder", Err: core.ErrPlaceholder},
	{Code: "mask_kind", Err: core.ErrMaskKind},
	{Code: "mask_reserved_kind", Err: core.ErrMaskReservedKind},
	{Code: "mask_empty_match", Err: core.ErrMaskEmptyMatch},
	{Code: "mask_too_many_rules", Err: core.ErrMaskTooManyRules},
	{Code: "mask_too_many_allow", Err: core.ErrMaskTooManyAllow},
	{Code: "not_on_server_yet", Err: core.ErrNotOnServerYet},
	{Code: "stale_draft", Err: core.ErrStaleDraft},
	{Code: "not_synced", Err: core.ErrNotSynced},
	{Code: "run_active", Err: core.ErrRunActive},
	{Code: "security_scan_busy", Err: core.ErrSecurityScanBusy, Key: "settings.security.rescan.busy"},
	{Code: "security_scan_disabled", Err: core.ErrSecurityScanDisabled},
	{Code: "compacting", Err: core.ErrCompacting},
	{Code: "nothing_to_compact", Err: core.ErrNothingToCompact},
	{Code: "draft_gone", Err: core.ErrDraftGone, Key: "wit.draft.gone"},
	{Code: "keychain_unavailable", Err: core.ErrKeychainUnavailable, Key: "error.keychain"},
	{Code: "secret_not_found", Err: core.ErrSecretNotFound},
	{Code: "wrong_account", Err: core.ErrWrongAccount},
	{Code: "account_not_found", Err: core.ErrAccountNotFound},
	{Code: "account_exists", Err: core.ErrAccountExists},
	{Code: "bad_account_export", Err: core.ErrBadAccountExport},
	{Code: "unsupported_export_version", Err: core.ErrUnsupportedExportVersion},
	{Code: "address_book_not_found", Err: core.ErrAddressBookNotFound},
	{Code: "no_address_books", Err: core.ErrNoAddressBooks},
	{Code: "local_archive", Err: core.ErrLocalArchive},
	{Code: "not_local_archive", Err: core.ErrNotLocalArchive},
	{Code: "receive_only", Err: core.ErrReceiveOnly},
	{Code: "jmap_off", Err: core.ErrJMAPOff},
	{Code: "ews_off", Err: core.ErrEWSOff},
	{Code: "graph_off", Err: core.ErrGraphOff},
	{Code: "role_folder", Err: core.ErrRoleFolder},
	{Code: "folder_not_empty", Err: core.ErrFolderNotEmpty},
	{Code: "folder_name_empty", Err: core.ErrFolderNameEmpty},
	{Code: "folder_exists", Err: core.ErrFolderExists},
	{Code: "provider_mismatch", Err: core.ErrProviderMismatch},
	{Code: "needs_grant", Err: core.ErrNeedsGrant},
	{Code: "plugin_not_found", Err: core.ErrPluginNotFound},
	{Code: "translator_unavailable", Err: core.ErrTranslatorUnavailable},
	{Code: "system_language_not_installed", Err: core.ErrSystemLanguageNotInstalled},
	{Code: "system_language_unsupported", Err: core.ErrSystemLanguageUnsupported},
	{Code: "model_unavailable", Err: core.ErrModelUnavailable},
	{Code: "registry_off", Err: core.ErrRegistryOff},
	{Code: "registry_signature", Err: core.ErrRegistrySignature},
	{Code: "registry_stale", Err: core.ErrRegistryStale},
	{Code: "registry_mismatch", Err: core.ErrRegistryMismatch},
	{Code: "recognition_model_missing", Err: core.ErrRecognitionModelMissing},
	{Code: "recognition_model_mismatch", Err: core.ErrRecognitionModelMismatch},
	{Code: "mcp_not_found", Err: core.ErrMCPNotFound},
	{Code: "mcp_managed", Err: core.ErrMCPManaged},
	{Code: "ai_not_configured", Err: core.ErrAINotConfigured},
	{Code: "model_service_not_found", Err: core.ErrModelServiceNotFound},
	{Code: "model_service_managed", Err: core.ErrModelServiceManaged},
	{Code: "model_not_found", Err: core.ErrModelNotFound},
	{Code: "model_auth", Err: core.ErrModelAuth},
	{Code: "model_login", Err: core.ErrModelLogin},
	{Code: "model_reauth", Err: core.ErrModelReauth, Key: "settings.models.login.expired"},
	{Code: "label_exists", Err: core.ErrLabelExists},
	{Code: "base_url_invalid", Err: core.ErrBaseURLInvalid},
	{Code: "base_url_scheme", Err: core.ErrBaseURLScheme},
	{Code: "no_model_list", Err: ai.ErrNoModelList},
	{Code: "model_forbidden", Err: ai.ErrModelForbidden},
	{Code: "key_needs_confirm", Err: core.ErrKeyNeedsConfirm, Key: "settings.models.key_moved"},
	{Code: "unknown_flow", Err: core.ErrUnknownFlow},
	{Code: "invalid_state", Err: core.ErrInvalidState},
	{Code: "skill_disabled", Err: core.ErrSkillDisabled},
	{Code: "skill_not_found", Err: core.ErrSkillNotFound},
	{Code: "closed", Err: core.ErrClosed},
	{Code: "signature_image", Err: core.ErrSignatureImage},
	{Code: "signature_image_count", Err: core.ErrSignatureImageCount},
	{Code: "signature_not_found", Err: core.ErrSignatureNotFound},
	{Code: "signature_name", Err: core.ErrSignatureName},
	{Code: "invalid_rule", Err: core.ErrInvalidRule},
	{Code: "invalid_view", Err: core.ErrInvalidView},
	{Code: "recipient_unconfirmed", Err: core.ErrRecipientUnconfirmed},
	{Code: "account_icon", Err: core.ErrAccountIcon},
	{Code: "not_grantable", Err: core.ErrNotGrantable},
	{Code: "needs_answer", Err: core.ErrNeedsAnswer},
	{Code: "mcp_send_off", Err: core.ErrMCPSendOff},
	{Code: "mcp_send_no_ui", Err: core.ErrMCPSendNoUI},
	{Code: "mcp_send_composing", Err: core.ErrMCPSendComposing},
	{Code: "mcp_send_busy", Err: core.ErrMCPSendBusy},
	{Code: "no_ui", Err: core.ErrNoUI},
	{Code: "no_outbox_draft", Err: core.ErrNoOutboxDraft},
	{Code: "no_unsubscribe", Err: core.ErrNoUnsubscribe},
	{Code: "unsubscribe_web", Err: core.ErrUnsubscribeWeb},
	{Code: "unsubscribing", Err: core.ErrUnsubscribing},
	{Code: "purge_too_late", Err: core.ErrPurgeTooLate, Key: "mail.purge.too_late"},
	{Code: "canceled", Err: context.Canceled},
	{Code: "deadline", Err: context.DeadlineExceeded},
	{Code: "auth_failed", Err: backend.ErrAuthFailed, Key: "settings.account.detail.auth_rejected"}, // also a failed account check's cause; the account's own words for it
	{Code: "oauth_not_configured", Err: auth.ErrOAuthNotConfigured},
	{Code: "flow_expired", Err: auth.ErrFlowExpired},
	{Code: "invalid_grant", Err: auth.ErrInvalidGrant},
	{Code: "access_denied", Err: auth.ErrAccessDenied},
	{Code: "scope_missing", Err: auth.ErrScopeMissing},
}

// Error is a row of the table.
type Error struct {
	Code string // "" when it does not cross the Local API
	Err  error
	Key  string // "" for "error." + Code
}

// All is every row of the table.
func All() []Error { return table }

// Key is the i18n key of what err means to the user, by the first row it is;
// "" when it is none. The key may have no message in the catalog yet.
func Key(err error) string {
	for _, e := range table {
		if errors.Is(err, e.Err) {
			if e.Key != "" {
				return e.Key
			}
			return "error." + e.Code
		}
	}
	return ""
}

// NewCodec registers core's error types (tried first, so *ai.Error with Code
// canceled stays an *ai.Error) and sentinels.
func NewCodec() *rpc.Codec {
	c := &rpc.Codec{}
	// The first failing step's error (Unwrap) stays on the server; only the
	// sentinel it is crosses, as the cause.
	rpc.RegisterType(c, "check_failed",
		func(e *core.CheckError) any { return e.Report },
		func(raw json.RawMessage) (*core.CheckError, error) {
			r, err := rpc.DecodeDetail[core.CheckReport](raw)
			if err != nil {
				return nil, err
			}
			return &core.CheckError{Report: *r}, nil
		})
	rpc.RegisterType(c, "send_warnings",
		func(e *core.SendWarningsError) any { return e.Check },
		func(raw json.RawMessage) (*core.SendWarningsError, error) {
			ch, err := rpc.DecodeDetail[core.SendCheck](raw)
			if err != nil {
				return nil, err
			}
			return &core.SendWarningsError{Check: *ch}, nil
		})
	rpc.RegisterType(c, "send_blocked",
		func(e *core.SendBlockedError) any { return core.SendBlockedError{Plugin: e.Plugin, Message: e.Message} },
		rpc.DecodeDetail[core.SendBlockedError])
	rpc.RegisterType(c, "egress_approval_required",
		func(e *core.EgressApprovalRequired) any {
			return core.EgressApprovalRequired{Plugin: e.Plugin, Hosts: e.Hosts}
		},
		rpc.DecodeDetail[core.EgressApprovalRequired])
	rpc.RegisterType(c, "no_role_folder",
		func(e *core.ErrNoRoleFolder) any { return *e },
		rpc.DecodeDetail[core.ErrNoRoleFolder])
	rpc.RegisterType(c, "model_error",
		func(e *core.ModelError) any {
			return core.ModelError{Code: e.Code, Message: e.Message, Retryable: e.Retryable}
		},
		rpc.DecodeDetail[core.ModelError])
	rpc.RegisterType(c, "ai_error",
		func(e *ai.Error) any { return ai.Error{Code: e.Code, Msg: e.Msg, Detail: e.Detail} },
		rpc.DecodeDetail[ai.Error])
	for _, e := range table {
		if e.Code != "" {
			c.RegisterSentinel(e.Code, e.Err, e.Err.Error())
		}
	}
	return c
}

// hidden are the exported *core.Service methods kept off the Local API for
// every role, each for its reason.
var hidden = map[string]bool{
	"Close":   true, // the owner's lifetime, not a client's
	"Publish": true, // the owner's own events; a client could forge a click or a quit
	"Redact":  true, // a client could test guesses of the owner's secrets against it
	"Proxy":   true, // a custom proxy's URL carries its password
	// Owner-side UI presence queries; RoleUI reports state via SetUIForeground and SetComposeOpen.
	"UIConnected": true, "UIForeground": true, "UIReady": true, "IsComposeOpen": true,
	// The owner tray's 退出并停止收信 (a stop func).
	"StopWhenUIQuits": true,
	// A channel and a stop func; RPC clients have SubscribeConversation.
	"FollowConversation": true,
	// In-process callback forms; RoleUI has the *Stream counterparts in uiOnly.
	"InstallPluginFromRegistryWithProgress": true, "PrepareModelWithProgress": true, "DownloadRecognitionModel": true,
	"InlineImages": true, "FetchAttachments": true,
}

// uiOnly are the exported *core.Service methods exposed only to RoleUI
// (Godwit's own interface, ADR 0038), never to RoleFull or RoleMCP, pinned in
// testdata/ui_methods.txt. Every other non-hidden exported method reaches full
// by default (testdata/full_methods.txt).
var uiOnly = map[string]bool{
	// Remote fetches for the interface's avatars and directory dialog.
	"SenderAvatar": true, "ContactAvatar": true, "ModelServiceAvatar": true, "PluginReadme": true,
	// Progress streams for the interface's downloads and directory installs.
	"InstallPluginFromRegistryStream": true, "PrepareModelStream": true, "DownloadRecognitionModelStream": true,
	// The reader's two-step body load, remote images downloaded into the disk cache and read
	// from it (CachedImage fetches a caller-given URL), and the multi-attachment save stream.
	"GetMessageLazyImages": true, "InlineImagesStream": true, "FetchAttachmentsStream": true,
	"CachedImage": true, "CachedImageDims": true, "ForgetImageFailure": true,
	// Results too large for one response, written as 0600 files in the data directory: a
	// message's original (up to MaxMessageBytes) and the account export.
	"RawMessageFile": true, "ExportAccountsFile": true,
	// Account import from the file the interface's user picked, read by the backend: an export
	// can outgrow one request. The preview's digest binds the import to the bytes shown.
	"PreviewAccountImportFile": true, "ImportAccountsFile": true,
	// Wire helper for the operating system's installed translation languages.
	"SystemTranslatorLanguages": true,
	// The Store package's About check: egress to the Store's catalog, through the user's proxy.
	"StoreLatestVersion": true,
	// Redacted recent log lines for bug reports, and the interface's IANA time zone for Wit runs.
	"RecentLog": true, "SetTimeZone": true,
	// Window foreground, open compose drafts, and background-round narrowing; external clients use SyncNow.
	"SetUIForeground": true, "SetUIReady": true, "SetComposeOpen": true, "SetInboxOnly": true, "InboxOnly": true,
	// Quitting Godwit stops the backend its interface started; a user-started one refuses.
	// Replace stops it for an interface of another version, even under 一直继续.
	"StopUIBackend": true, "ReplaceUIBackend": true,
	// Send later (ADR 0042): a send that leaves unattended, scheduled only by the user in the
	// interface. Programs of the full role can Send at the time they choose; mcp sends through
	// RequestMCPSend.
	"SendLater": true,
	// The interface's user cancelled a quit the tray's 退出并停止收信 began.
	"CancelTrayQuit": true,
	// A development build's preview of the backend tray's sign-in code card.
	"DevPreviewSignInCode": true,
	// Whether the backend's tray shows its icon: the interface's window hides only where it can come back.
	"TrayShown": true,
}

// Expose picks the *core.Service methods callable by the full role: every
// exported one except hidden, uiOnly, and the test hooks (…ForTest).
func Expose(method string) bool {
	return !hidden[method] && !uiOnly[method] && !strings.HasSuffix(method, "ForTest")
}

// ExposeUI picks the *core.Service methods callable by the ui role: everything
// Expose allows plus uiOnly.
func ExposeUI(method string) bool {
	return Expose(method) || uiOnly[method]
}

// ListenOptions configures ListenWith.
type ListenOptions struct {
	Version   string
	External  bool // allow RoleFull and RoleMCP connections and write api-token
	UIStarted bool // see rpc.ServerOptions
}

// Listen serves svc's exposed methods on <dataDir>/godwit.sock with external
// access enabled (see ListenWith).
func Listen(dataDir string, svc *core.Service, version string) (*rpc.Server, error) {
	return ListenWith(dataDir, svc, ListenOptions{Version: version, External: true})
}

// ListenWith serves svc's exposed methods on <dataDir>/godwit.sock (see
// rpc.Listen; the caller holds the owner lock). Errors go out through
// NewCodec; the UI and API tokens and svc's secrets are redacted from them and
// from the log. Each connection's context carries its role's audit source
// (core.AuditLocalAPI, core.AuditUI, core.AuditMCP): the core methods that send
// or leave state that egresses later write their own audit rows from it (ADR
// 0003), ui's with no external-source gating.
func ListenWith(dataDir string, svc *core.Service, opts ListenOptions) (*rpc.Server, error) {
	d, err := rpc.NewDispatcher(svc, Expose)
	if err != nil {
		return nil, err
	}
	ui, err := rpc.NewDispatcher(svc, ExposeUI)
	if err != nil {
		return nil, err
	}
	return rpc.Listen(dataDir, d, rpc.ServerOptions{
		Version:         opts.Version,
		DisableExternal: !opts.External,
		UIStarted:       opts.UIStarted,
		Codec:           NewCodec(),
		Redact:          svc.Redact,
		UIDispatcher:    ui,
		Context: func(ctx context.Context, role string) context.Context {
			switch role {
			case rpc.RoleFull:
				return core.WithExternalSource(ctx, core.AuditLocalAPI)
			case rpc.RoleMCP:
				return core.WithExternalSource(ctx, core.AuditMCP)
			case rpc.RoleUI:
				return core.WithUISession(ctx, svc)
			}
			return ctx
		},
	})
}
