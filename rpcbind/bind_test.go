package rpcbind_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/core/conn"
	"github.com/godwit-email/godwit/rpc"
	"github.com/godwit-email/godwit/rpcbind"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

// sentinels is every exported error variable of package core
// (TestAllCoreErrorsRegistered keeps it complete) plus the context errors and
// those of core/ai, core/auth and core/conn that core's methods return.
var sentinels = map[string]error{
	"ErrUseAssist": core.ErrUseAssist, "ErrStaleApproval": core.ErrStaleApproval,
	"ErrTooLarge": core.ErrTooLarge, "ErrLinkRef": core.ErrLinkRef, "ErrNotOnServerYet": core.ErrNotOnServerYet,
	"ErrStaleDraft": core.ErrStaleDraft, "ErrRunActive": core.ErrRunActive, "ErrNotSynced": core.ErrNotSynced,
	"ErrCompacting": core.ErrCompacting, "ErrNothingToCompact": core.ErrNothingToCompact, "ErrDraftGone": core.ErrDraftGone,
	"ErrKeychainUnavailable": core.ErrKeychainUnavailable, "ErrWrongAccount": core.ErrWrongAccount,
	"ErrAccountNotFound":  core.ErrAccountNotFound,
	"ErrProviderMismatch": core.ErrProviderMismatch, "ErrNeedsGrant": core.ErrNeedsGrant,
	"ErrPluginNotFound": core.ErrPluginNotFound, "ErrMCPNotFound": core.ErrMCPNotFound,
	"ErrRegistryOff": core.ErrRegistryOff, "ErrRegistrySignature": core.ErrRegistrySignature,
	"ErrTranslatorUnavailable":      core.ErrTranslatorUnavailable,
	"ErrSystemLanguageNotInstalled": core.ErrSystemLanguageNotInstalled,
	"ErrSystemLanguageUnsupported":  core.ErrSystemLanguageUnsupported,
	"ErrModelUnavailable":           core.ErrModelUnavailable,
	"ErrRegistryStale":              core.ErrRegistryStale, "ErrRegistryMismatch": core.ErrRegistryMismatch,
	"ErrAINotConfigured": core.ErrAINotConfigured, "ErrModelServiceNotFound": core.ErrModelServiceNotFound,
	"ErrModelNotFound": core.ErrModelNotFound, "ErrModelAuth": core.ErrModelAuth,
	"ErrModelLogin": core.ErrModelLogin, "ErrModelReauth": core.ErrModelReauth, "ErrLabelExists": core.ErrLabelExists,
	"ErrBaseURLInvalid": core.ErrBaseURLInvalid, "ErrBaseURLScheme": core.ErrBaseURLScheme,
	"ErrKeyNeedsConfirm": core.ErrKeyNeedsConfirm, "ErrUnknownFlow": core.ErrUnknownFlow,
	"ErrInvalidState": core.ErrInvalidState, "ErrSkillDisabled": core.ErrSkillDisabled,
	"ErrSkillNotFound": core.ErrSkillNotFound, "ErrClosed": core.ErrClosed,
	"ErrSignatureImage": core.ErrSignatureImage, "ErrSignatureImageCount": core.ErrSignatureImageCount,
	"ErrSignatureNotFound": core.ErrSignatureNotFound, "ErrSignatureName": core.ErrSignatureName,
	"ErrAccountIcon":  core.ErrAccountIcon,
	"ErrNotGrantable": core.ErrNotGrantable,
	"ErrNeedsAnswer":  core.ErrNeedsAnswer,
	"ErrMCPSendOff":   core.ErrMCPSendOff, "ErrMCPSendComposing": core.ErrMCPSendComposing, "ErrMCPSendBusy": core.ErrMCPSendBusy,
	"ErrNoOutboxDraft": core.ErrNoOutboxDraft,
	"context.Canceled": context.Canceled, "context.DeadlineExceeded": context.DeadlineExceeded,
	"ai.ErrNoModelList": ai.ErrNoModelList, "ai.ErrModelForbidden": ai.ErrModelForbidden, "conn.ErrAuthFailed": conn.ErrAuthFailed,
	"auth.ErrOAuthNotConfigured": auth.ErrOAuthNotConfigured, "auth.ErrFlowExpired": auth.ErrFlowExpired,
	"auth.ErrInvalidGrant": auth.ErrInvalidGrant, "auth.ErrAccessDenied": auth.ErrAccessDenied,
	"auth.ErrScopeMissing": auth.ErrScopeMissing,
}

func wire(t *testing.T, c *rpc.Codec, err error, redact func(string) string) (*rpc.WireError, string, error) {
	t.Helper()
	we := c.Encode(err, redact)
	b, merr := json.Marshal(we)
	require.NoError(t, merr)
	var back rpc.WireError
	require.NoError(t, json.Unmarshal(b, &back))
	return we, string(b), c.Decode(&back)
}

func TestErrorRoundTrip(t *testing.T) {
	c := rpcbind.NewCodec()
	codes := map[string]bool{}
	for name, s := range sentinels {
		for _, err := range []error{s, fmt.Errorf("op with private text: %w", s)} {
			we, raw, got := wire(t, c, err, nil)
			assert.True(t, got == s, name)               // the same value
			assert.Equal(t, s.Error(), we.Message, name) // the fixed message
			assert.NotContains(t, raw, "private text", name)
		}
		codes[c.Encode(s, nil).Data.Code] = true
	}
	assert.Len(t, codes, len(sentinels), "stable codes are distinct")
	assert.Equal(t, "canceled", c.Encode(context.Canceled, nil).Data.Code)
	assert.Equal(t, "deadline", c.Encode(context.DeadlineExceeded, nil).Data.Code)

	// A real CheckError: its first failing step's error stays on the server.
	var orig *core.CheckError
	require.ErrorAs(t, newService(t).TestAccount(context.Background(), core.AccountInput{Email: "a@b.example"}), &orig)
	require.NotNil(t, orig.Unwrap())
	_, _, got := wire(t, c, fmt.Errorf("x: %w", orig), nil)
	var ce *core.CheckError
	require.ErrorAs(t, got, &ce)
	assert.Equal(t, orig.Report, ce.Report)
	assert.Nil(t, ce.Unwrap())
	assert.Equal(t, orig.Error(), ce.Error())

	check := core.SendCheck{DraftID: 7, Revision: 3, Plugins: []string{"p@h@1"}, Warnings: []core.SendWarning{{Plugin: "p", Code: core.WarningPlugin, Message: "m"}}, Digest: "d1"}
	_, _, got = wire(t, c, &core.SendWarningsError{Check: check}, nil)
	var sw *core.SendWarningsError
	require.ErrorAs(t, got, &sw)
	assert.Equal(t, check, sw.Check)

	_, _, got = wire(t, c, &core.SendBlockedError{Plugin: "p", Message: "no"}, nil)
	var sb *core.SendBlockedError
	require.ErrorAs(t, got, &sb)
	assert.Equal(t, core.SendBlockedError{Plugin: "p", Message: "no"}, *sb)

	_, _, got = wire(t, c, &core.ErrNoRoleFolder{Role: "junk"}, nil)
	var nf *core.ErrNoRoleFolder
	require.ErrorAs(t, got, &nf)
	assert.Equal(t, "junk", nf.Role)

	_, _, got = wire(t, c, &core.EgressApprovalRequired{Plugin: "p", Hosts: []string{"a.example", "b.example"}}, nil)
	var ea *core.EgressApprovalRequired
	require.ErrorAs(t, got, &ea)
	assert.Equal(t, core.EgressApprovalRequired{Plugin: "p", Hosts: []string{"a.example", "b.example"}}, *ea)

	for _, code := range []ai.Code{ai.CodeCanceled, ai.CodeRateLimited} {
		_, _, got = wire(t, c, fmt.Errorf("run: %w", &ai.Error{Code: code, Msg: "stopped"}), nil)
		var ae *ai.Error
		require.ErrorAs(t, got, &ae, code)
		assert.Equal(t, ai.Error{Code: code, Msg: "stopped"}, *ae)
		assert.Equal(t, code == ai.CodeCanceled, errors.Is(got, context.Canceled))
	}
}

func TestUnknownErrorIsGeneric(t *testing.T) {
	c := rpcbind.NewCodec()
	we, raw, got := wire(t, c, fmt.Errorf("sync: %w", errors.New("Subject: Quarterly layoffs, From: ceo@corp.example")), nil)
	assert.Equal(t, "internal error", we.Message)
	assert.Equal(t, "internal", we.Data.Code)
	assert.NotContains(t, raw, "layoffs")
	var re *rpc.RemoteError
	require.ErrorAs(t, got, &re)
	assert.Equal(t, "internal error", re.Message)
}

func TestErrorsCarryNoSecrets(t *testing.T) {
	svc := newService(t)
	const secret = "sk-live-0123456789abcdef"
	_, err := svc.AddModelService(context.Background(), core.ModelServiceInput{Name: "m", Kind: "openai", BaseURL: "https://example.com/v1", APIKey: secret, Models: []string{"x"}})
	require.NoError(t, err)
	require.Contains(t, svc.Redact("k="+secret), "[redacted]")

	c := rpcbind.NewCodec()
	for _, e := range []error{
		&core.SendWarningsError{Check: core.SendCheck{Plugins: []string{secret}, Warnings: []core.SendWarning{{Plugin: "p", Code: core.WarningPlugin, Message: "your key is " + secret}}, Digest: "d"}},
		&core.SendBlockedError{Plugin: "p", Message: "leaked " + secret},
		&core.EgressApprovalRequired{Plugin: secret, Hosts: []string{secret + ".example"}},
		&ai.Error{Code: ai.CodeUnauthorized, Msg: "bad key " + secret},
		&core.CheckError{Report: core.CheckReport{Steps: []core.CheckStep{{Name: "imap", Detail: secret}}}},
	} {
		_, raw, _ := wire(t, c, e, svc.Redact)
		assert.NotContains(t, raw, secret, "%T", e)
		assert.Contains(t, raw, "[redacted]", "%T", e)
	}
}

// Every exported error variable of package core is in sentinels, so
// TestErrorRoundTrip proves it is registered.
func TestAllCoreErrorsRegistered(t *testing.T) {
	files, err := filepath.Glob("../core/*.go")
	require.NoError(t, err)
	var names []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(token.NewFileSet(), f, nil, 0)
		require.NoError(t, err)
		for _, d := range af.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != token.VAR {
				continue
			}
			for _, s := range g.Specs {
				for _, n := range s.(*ast.ValueSpec).Names {
					if n.IsExported() && strings.HasPrefix(n.Name, "Err") {
						names = append(names, n.Name)
					}
				}
			}
		}
	}
	require.NotEmpty(t, names)
	for _, n := range names {
		assert.Contains(t, sentinels, n, "core.%s is not in the registered list", n)
	}
	t.Logf("%d core sentinels", len(names))
}

func newService(t *testing.T) *core.Service {
	t.Helper()
	keyring.MockInit()
	svc, err := core.New(context.Background(), core.Options{DataDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { svc.Close() })
	return svc
}

func TestExposeCoversService(t *testing.T) {
	svc := newService(t)
	_, err := rpc.NewDispatcher(svc, rpcbind.Expose)
	require.NoError(t, err)
	for _, m := range []string{"Close", "Redact", "AuditExternal", "SetPluginNetForTest", "SenderAvatar", "ModelServiceAvatar", "InstallPluginFromRegistryWithProgress"} {
		assert.False(t, rpcbind.Expose(m), m)
	}
	var exposed []string
	for m := range reflect.TypeOf(svc).Methods() {
		if rpcbind.Expose(m.Name) {
			exposed = append(exposed, m.Name)
		}
	}
	assert.Contains(t, exposed, "ListAccounts")
	assert.Contains(t, exposed, "Send")
	assert.Contains(t, exposed, "InstallPluginFromRegistry")
	assert.NotContains(t, exposed, "SenderAvatar")
	t.Logf("%d of %d methods exposed", len(exposed), reflect.TypeOf(svc).NumMethod())
}

// Wire codes are API: renaming one must break this test.
func TestWireCodesPinned(t *testing.T) {
	c := rpcbind.NewCodec()
	pinned := map[string]error{
		"use_assist": core.ErrUseAssist, "stale_approval": core.ErrStaleApproval, "too_large": core.ErrTooLarge, "link_ref": core.ErrLinkRef,
		"not_on_server_yet": core.ErrNotOnServerYet, "stale_draft": core.ErrStaleDraft, "run_active": core.ErrRunActive, "not_synced": core.ErrNotSynced,
		"compacting": core.ErrCompacting, "nothing_to_compact": core.ErrNothingToCompact, "draft_gone": core.ErrDraftGone,
		"keychain_unavailable": core.ErrKeychainUnavailable, "wrong_account": core.ErrWrongAccount,
		"account_not_found": core.ErrAccountNotFound,
		"provider_mismatch": core.ErrProviderMismatch, "needs_grant": core.ErrNeedsGrant,
		"plugin_not_found": core.ErrPluginNotFound, "mcp_not_found": core.ErrMCPNotFound,
		"registry_off": core.ErrRegistryOff, "registry_signature": core.ErrRegistrySignature,
		"translator_unavailable": core.ErrTranslatorUnavailable, "model_unavailable": core.ErrModelUnavailable,
		"system_language_not_installed": core.ErrSystemLanguageNotInstalled, "system_language_unsupported": core.ErrSystemLanguageUnsupported,
		"registry_stale": core.ErrRegistryStale, "registry_mismatch": core.ErrRegistryMismatch,
		"ai_not_configured": core.ErrAINotConfigured, "model_service_not_found": core.ErrModelServiceNotFound,
		"model_not_found": core.ErrModelNotFound, "model_auth": core.ErrModelAuth,
		"model_login": core.ErrModelLogin, "model_reauth": core.ErrModelReauth, "label_exists": core.ErrLabelExists,
		"base_url_invalid": core.ErrBaseURLInvalid, "base_url_scheme": core.ErrBaseURLScheme,
		"no_model_list": ai.ErrNoModelList, "model_forbidden": ai.ErrModelForbidden, "key_needs_confirm": core.ErrKeyNeedsConfirm,
		"unknown_flow": core.ErrUnknownFlow, "invalid_state": core.ErrInvalidState,
		"skill_disabled": core.ErrSkillDisabled, "skill_not_found": core.ErrSkillNotFound, "closed": core.ErrClosed,
		"signature_image": core.ErrSignatureImage, "signature_image_count": core.ErrSignatureImageCount,
		"signature_not_found": core.ErrSignatureNotFound, "signature_name": core.ErrSignatureName,
		"account_icon":  core.ErrAccountIcon,
		"not_grantable": core.ErrNotGrantable,
		"needs_answer":  core.ErrNeedsAnswer,
		"canceled":      context.Canceled, "deadline": context.DeadlineExceeded,
		"mcp_send_off": core.ErrMCPSendOff, "mcp_send_composing": core.ErrMCPSendComposing, "mcp_send_busy": core.ErrMCPSendBusy,
		"no_outbox_draft": core.ErrNoOutboxDraft,
		"auth_failed":     conn.ErrAuthFailed, "oauth_not_configured": auth.ErrOAuthNotConfigured,
		"flow_expired": auth.ErrFlowExpired, "invalid_grant": auth.ErrInvalidGrant, "access_denied": auth.ErrAccessDenied,
		"scope_missing":            auth.ErrScopeMissing,
		"check_failed":             &core.CheckError{},
		"send_warnings":            &core.SendWarningsError{},
		"send_blocked":             &core.SendBlockedError{},
		"egress_approval_required": &core.EgressApprovalRequired{},
		"ai_error":                 &ai.Error{Code: ai.CodeCanceled},
		"no_role_folder":           &core.ErrNoRoleFolder{Role: "junk"},
		"model_error":              &core.ModelError{Code: "model_not_ready"},
	}
	assert.Len(t, pinned, len(sentinels)+7, "every registration is pinned")
	for code, err := range pinned {
		we := c.Encode(err, nil)
		assert.Equal(t, rpc.CodeApp, we.Code, code)
		assert.Equal(t, code, we.Data.Code)
	}
}

// Malformed detail for any registered type is a protocol error, never a zero
// or partial business error.
func TestBadErrorData(t *testing.T) {
	c := rpcbind.NewCodec()
	for _, code := range []string{"check_failed", "send_warnings", "send_blocked", "egress_approval_required", "ai_error", "no_role_folder", "model_error"} {
		for name, detail := range map[string]string{"missing": ``, "wrong type": `{"Plugin":1,"Steps":1,"Code":1}`, "partial": `{}`} {
			got := c.Decode(&rpc.WireError{Code: rpc.CodeApp, Message: "m", Data: &rpc.WireErrorData{Code: code, Detail: json.RawMessage(detail)}})
			assert.ErrorIs(t, got, rpc.ErrBadErrorData, "%s %s", code, name)
		}
	}
}
