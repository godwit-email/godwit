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
	"github.com/godwit-email/godwit/rpc"
	"github.com/godwit-email/godwit/rpcbind"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

// sentinels is every exported error variable of package core
// (TestAllCoreErrorsRegistered keeps it complete) plus the context errors.
var sentinels = map[string]error{
	"ErrUseAssist": core.ErrUseAssist, "ErrStaleApproval": core.ErrStaleApproval,
	"ErrTooLarge": core.ErrTooLarge, "ErrNotOnServerYet": core.ErrNotOnServerYet,
	"ErrStaleDraft": core.ErrStaleDraft, "ErrRunActive": core.ErrRunActive,
	"ErrKeychainUnavailable": core.ErrKeychainUnavailable, "ErrWrongAccount": core.ErrWrongAccount,
	"ErrProviderMismatch": core.ErrProviderMismatch, "ErrNeedsGrant": core.ErrNeedsGrant,
	"ErrPluginNotFound": core.ErrPluginNotFound, "ErrMCPNotFound": core.ErrMCPNotFound,
	"ErrAINotConfigured": core.ErrAINotConfigured, "ErrModelServiceNotFound": core.ErrModelServiceNotFound,
	"ErrModelNotFound": core.ErrModelNotFound, "ErrModelAuth": core.ErrModelAuth,
	"ErrBaseURLInvalid": core.ErrBaseURLInvalid, "ErrBaseURLScheme": core.ErrBaseURLScheme,
	"ErrKeyNeedsConfirm": core.ErrKeyNeedsConfirm, "ErrUnknownFlow": core.ErrUnknownFlow,
	"ErrInvalidState": core.ErrInvalidState, "ErrSkillDisabled": core.ErrSkillDisabled,
	"ErrSkillNotFound": core.ErrSkillNotFound, "ErrClosed": core.ErrClosed,
	"context.Canceled": context.Canceled, "context.DeadlineExceeded": context.DeadlineExceeded,
	"ai.ErrNoModelList": ai.ErrNoModelList,
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
	for _, m := range []string{"Close", "Redact", "SetPluginNetForTest"} {
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
	t.Logf("%d of %d methods exposed", len(exposed), reflect.TypeOf(svc).NumMethod())
}
