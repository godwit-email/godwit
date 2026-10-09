package contract_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/godwit-email/godwit/contract"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/core/conn"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/ownerlock"
	"github.com/godwit-email/godwit/rpc"
	"github.com/godwit-email/godwit/rpcbind"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scenario drives api and returns its business results; env is the Env
// behind api, for setup and fakes only.
type scenario func(t *testing.T, api contract.API, env *coretest.Env) any

// runContract runs s against a fresh typed *core.Service, then against a
// fresh one behind the Local API (full role), and compares the normalized
// results.
func runContract(t *testing.T, s scenario) {
	var got [2]string
	t.Run("typed", func(t *testing.T) {
		env := coretest.New(t, coretest.WithoutAutoSync())
		got[0] = normalize(t, s(t, env.Svc, env))
	})
	t.Run("rpc", func(t *testing.T) {
		env := coretest.New(t, coretest.WithoutAutoSync())
		r := serve(t, env.Svc)
		got[1] = normalize(t, s(t, r, env))
		assert.Empty(t, r.StreamErrs(), "remote streams ended abnormally")
	})
	if os.Getenv("CONTRACT_DUMP") != "" {
		t.Logf("typed, normalized:\n%s", got[0])
	}
	if got[0] != "" && got[1] != "" {
		assert.Equal(t, got[0], got[1], "typed (expected) vs JSON-RPC (actual), normalized")
	}
}

// serve puts svc behind the Local API on a fresh short directory (Unix
// socket paths are short) and dials it with the full role.
func serve(t *testing.T, svc *core.Service) *contract.Remote {
	t.Helper()
	dir, err := os.MkdirTemp("", "gw")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	release, err := ownerlock.Acquire(dir)
	require.NoError(t, err)
	t.Cleanup(func() { release() })
	s, err := rpcbind.Listen(dir, svc, "contract")
	require.NoError(t, err)
	go s.Serve(context.Background())
	t.Cleanup(func() { s.Close() })
	c, err := rpc.Dial(dir, rpc.RoleFull, "contract-test", rpcbind.NewCodec())
	require.NoError(t, err)
	t.Cleanup(func() { c.Close() })
	return &contract.Remote{C: c}
}

// normalize renders v as indented JSON with every numeric ID (under a key
// ending in ID or IDs) and every timestamp replaced by a placeholder numbered in order of
// first appearance: the same value always gets the same placeholder, so
// associations survive and both sides must agree on them.
func normalize(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var tree any
	require.NoError(t, d.Decode(&tree))
	n := &normalizer{seen: map[string]string{}, count: map[string]int{}}
	out, err := json.MarshalIndent(n.walk(tree, false), "", "  ")
	require.NoError(t, err)
	return string(out)
}

type normalizer struct {
	seen  map[string]string
	count map[string]int
}

func (n *normalizer) placeholder(kind, value string) string {
	key := kind + "\x00" + value
	if p, ok := n.seen[key]; ok {
		return p
	}
	n.count[kind]++
	p := fmt.Sprintf("%s#%d", kind, n.count[kind])
	n.seen[key] = p
	return p
}

func (n *normalizer) walk(v any, id bool) any {
	switch v := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(map[string]any, len(v))
		for _, k := range keys {
			out[k] = n.walk(v[k], strings.HasSuffix(k, "ID") || strings.HasSuffix(k, "IDs"))
		}
		return out
	case []any:
		for i := range v {
			v[i] = n.walk(v[i], id)
		}
		return v
	case json.Number:
		if id && v.String() != "0" {
			return n.placeholder("id", v.String())
		}
	case string:
		if ts, err := time.Parse(time.RFC3339Nano, v); err == nil && !ts.IsZero() {
			return n.placeholder("time", v)
		}
	}
	return v
}

// sentinels are every exported error value a scenario may meet.
var sentinels = map[string]error{
	"use_assist": core.ErrUseAssist, "stale_approval": core.ErrStaleApproval, "too_large": core.ErrTooLarge,
	"not_on_server_yet": core.ErrNotOnServerYet, "stale_draft": core.ErrStaleDraft, "run_active": core.ErrRunActive,
	"keychain_unavailable": core.ErrKeychainUnavailable, "wrong_account": core.ErrWrongAccount,
	"provider_mismatch": core.ErrProviderMismatch, "needs_grant": core.ErrNeedsGrant,
	"plugin_not_found": core.ErrPluginNotFound, "mcp_not_found": core.ErrMCPNotFound,
	"ai_not_configured": core.ErrAINotConfigured, "model_service_not_found": core.ErrModelServiceNotFound,
	"model_not_found": core.ErrModelNotFound, "model_auth": core.ErrModelAuth, "base_url_invalid": core.ErrBaseURLInvalid,
	"base_url_scheme": core.ErrBaseURLScheme, "no_model_list": ai.ErrNoModelList, "key_needs_confirm": core.ErrKeyNeedsConfirm,
	"unknown_flow": core.ErrUnknownFlow, "invalid_state": core.ErrInvalidState, "skill_disabled": core.ErrSkillDisabled,
	"skill_not_found": core.ErrSkillNotFound, "closed": core.ErrClosed, "canceled": context.Canceled,
	"deadline": context.DeadlineExceeded, "oauth_not_configured": auth.ErrOAuthNotConfigured,
	"auth_failed": conn.ErrAuthFailed, "flow_expired": auth.ErrFlowExpired, "invalid_grant": auth.ErrInvalidGrant, "access_denied": auth.ErrAccessDenied,
}

// errInfo is what callers can learn from err with errors.Is and errors.As:
// the sentinels it is and the registered types it is, with their fields.
// Unregistered errors cross as "internal error" by design, so their text is
// not compared.
func errInfo(err error) any {
	if err == nil {
		return nil
	}
	is := []string{}
	for code, s := range sentinels {
		if errors.Is(err, s) {
			is = append(is, code)
		}
	}
	sort.Strings(is)
	as := map[string]any{}
	var ce *core.CheckError
	if errors.As(err, &ce) {
		as["CheckError"] = ce.Report
	}
	var we *core.SendWarningsError
	if errors.As(err, &we) {
		as["SendWarningsError"] = we.Check
	}
	var be *core.SendBlockedError
	if errors.As(err, &be) {
		as["SendBlockedError"] = *be
	}
	var ea *core.EgressApprovalRequired
	if errors.As(err, &ea) {
		as["EgressApprovalRequired"] = *ea
	}
	var ae *ai.Error
	if errors.As(err, &ae) {
		as["ai.Error"] = *ae
	}
	return map[string]any{"is": is, "as": as}
}

// res is one call's outcome.
func res[T any](v T, err error) any { return map[string]any{"value": v, "err": errInfo(err)} }

func res2[A, B any](a A, b B, err error) any {
	return map[string]any{"values": []any{a, b}, "err": errInfo(err)}
}

// collect reads ch until it closes.
func collect[T any](t *testing.T, ch <-chan T) []T {
	t.Helper()
	out := []T{}
	timeout := time.After(15 * time.Second)
	for {
		select {
		case v, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, v)
		case <-timeout:
			t.Fatal("stream did not end")
		}
	}
}

func TestNormalizeKeepsAssociations(t *testing.T) {
	a := normalize(t, map[string]any{"ID": 7, "List": []map[string]any{{"AccountID": 7, "ThreadIDs": []int{9, 7}}},
		"At": "2026-10-01T09:00:00Z", "Zero": "0001-01-01T00:00:00Z", "N": 7})
	b := normalize(t, map[string]any{"ID": 3, "List": []map[string]any{{"AccountID": 3, "ThreadIDs": []int{4, 3}}},
		"At": "2027-01-01T00:00:00Z", "Zero": "0001-01-01T00:00:00Z", "N": 7})
	assert.Equal(t, a, b)
	c := normalize(t, map[string]any{"ID": 3, "List": []map[string]any{{"AccountID": 4, "ThreadIDs": []int{4, 3}}},
		"At": "2027-01-01T00:00:00Z", "Zero": "0001-01-01T00:00:00Z", "N": 7})
	assert.NotEqual(t, a, c, "a broken association must show")
}
