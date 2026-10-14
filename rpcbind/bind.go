// Package rpcbind composes core with rpc: core's error codes, redaction and
// the methods exposed over the Local API. core does not depend on rpc.
package rpcbind

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/core/conn"
	"github.com/godwit-email/godwit/rpc"
)

// sentinels are core's exported errors (with those of core/ai, core/auth and
// core/conn that its methods return, and the context ones) by stable wire code. Their messages are fixed English text.
var sentinels = []struct {
	code string
	err  error
}{
	{"use_assist", core.ErrUseAssist},
	{"stale_approval", core.ErrStaleApproval},
	{"too_large", core.ErrTooLarge},
	{"not_on_server_yet", core.ErrNotOnServerYet},
	{"stale_draft", core.ErrStaleDraft},
	{"not_synced", core.ErrNotSynced},
	{"run_active", core.ErrRunActive},
	{"keychain_unavailable", core.ErrKeychainUnavailable},
	{"wrong_account", core.ErrWrongAccount},
	{"account_not_found", core.ErrAccountNotFound},
	{"provider_mismatch", core.ErrProviderMismatch},
	{"needs_grant", core.ErrNeedsGrant},
	{"plugin_not_found", core.ErrPluginNotFound},
	{"mcp_not_found", core.ErrMCPNotFound},
	{"ai_not_configured", core.ErrAINotConfigured},
	{"model_service_not_found", core.ErrModelServiceNotFound},
	{"model_not_found", core.ErrModelNotFound},
	{"model_auth", core.ErrModelAuth},
	{"model_login", core.ErrModelLogin},
	{"model_reauth", core.ErrModelReauth},
	{"label_exists", core.ErrLabelExists},
	{"base_url_invalid", core.ErrBaseURLInvalid},
	{"base_url_scheme", core.ErrBaseURLScheme},
	{"no_model_list", ai.ErrNoModelList},
	{"model_forbidden", ai.ErrModelForbidden},
	{"key_needs_confirm", core.ErrKeyNeedsConfirm},
	{"unknown_flow", core.ErrUnknownFlow},
	{"invalid_state", core.ErrInvalidState},
	{"skill_disabled", core.ErrSkillDisabled},
	{"skill_not_found", core.ErrSkillNotFound},
	{"closed", core.ErrClosed},
	{"signature_image", core.ErrSignatureImage},
	{"signature_image_count", core.ErrSignatureImageCount},
	{"not_grantable", core.ErrNotGrantable},
	{"canceled", context.Canceled},
	{"deadline", context.DeadlineExceeded},
	{"auth_failed", conn.ErrAuthFailed}, // also a failed account check's cause
	{"oauth_not_configured", auth.ErrOAuthNotConfigured},
	{"flow_expired", auth.ErrFlowExpired},
	{"invalid_grant", auth.ErrInvalidGrant},
	{"access_denied", auth.ErrAccessDenied},
	{"scope_missing", auth.ErrScopeMissing},
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
	rpc.RegisterType(c, "ai_error",
		func(e *ai.Error) any { return ai.Error{Code: e.Code, Msg: e.Msg, Detail: e.Detail} },
		rpc.DecodeDetail[ai.Error])
	for _, s := range sentinels {
		c.RegisterSentinel(s.code, s.err, s.err.Error())
	}
	return c
}

// Expose picks the *core.Service methods callable over RPC: every exported
// one but Close, Redact, AuditExternal, the avatars and the test hooks.
func Expose(method string) bool {
	return method != "Close" && method != "Redact" && method != "AuditExternal" && method != "SenderAvatar" && method != "ModelServiceAvatar" && !strings.HasSuffix(method, "ForTest")
}

// Listen serves svc's exposed methods on <dataDir>/godwit.sock (see
// rpc.Listen; the caller holds the owner lock). Errors go out through
// NewCodec; the API token and svc's secrets are redacted from them and from
// the log. Sends by the full role and drafts by the mcp role are audited
// before they are dispatched.
func Listen(dataDir string, svc *core.Service, version string) (*rpc.Server, error) {
	d, err := rpc.NewDispatcher(svc, Expose)
	if err != nil {
		return nil, err
	}
	return rpc.Listen(dataDir, d, rpc.ServerOptions{
		Version: version,
		Codec:   NewCodec(),
		Redact:  svc.Redact,
		Audit:   func(role, method string, params []json.RawMessage) error { return audit(svc, role, method, params) },
	})
}

// audit records full's Send and SendAcknowledged (by draft) and
// ResolveOutbox "resend" (by outbox item), and mcp's NewDraft (by source
// message) and SaveDraft (by draft); nothing else.
func audit(svc *core.Service, role, method string, params []json.RawMessage) error {
	source := ""
	switch {
	case role == rpc.RoleFull && (method == "Send" || method == "SendAcknowledged" || method == "ResolveOutbox" && isResend(params)):
		source = core.AuditLocalAPI
	case role == rpc.RoleMCP && (method == "NewDraft" || method == "SaveDraft"):
		source = core.AuditMCP
	default:
		return nil
	}
	var target int64
	switch {
	case method == "NewDraft" && len(params) == 3:
		json.Unmarshal(params[2], &target)
	case method == "SaveDraft" && len(params) == 1:
		var d struct{ ID int64 }
		json.Unmarshal(params[0], &d)
		target = d.ID
	case len(params) > 0:
		json.Unmarshal(params[0], &target)
	}
	return svc.AuditExternal(context.Background(), source, method, target)
}

// isResend: ResolveOutbox's params ask to send the item again.
func isResend(params []json.RawMessage) bool {
	var action string
	return len(params) == 2 && json.Unmarshal(params[1], &action) == nil && action == "resend"
}
