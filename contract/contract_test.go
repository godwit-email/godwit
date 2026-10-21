package contract_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
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
// results. It returns the server's log of the rpc run: one redacted
// "internal error" line per unregistered error the scenario recorded, with
// none of the test secrets in it.
func runContract(t *testing.T, s scenario) (rpcLog string) {
	var got [2]string
	t.Run("typed", func(t *testing.T) {
		logged := captureLogs(t)
		env := coretest.New(t, coretest.WithoutAutoSync())
		start := time.Now()
		got[0] = normalize(t, s(t, env.Svc, env), start, time.Now())
		assert.Empty(t, logged(), "typed calls log nothing")
	})
	t.Run("rpc", func(t *testing.T) {
		logged := captureLogs(t)
		env := coretest.New(t, coretest.WithoutAutoSync())
		r, token := serve(t, env.Svc)
		start := time.Now()
		v := s(t, r, env)
		got[1] = normalize(t, v, start, time.Now())
		assert.Empty(t, r.StreamErrs(), "remote streams ended abnormally")
		b, err := json.Marshal(v)
		require.NoError(t, err)
		unregistered := strings.Count(string(b), `{"as":{},"is":[]}`)
		rpcLog = logged()
		lines := strings.FieldsFunc(rpcLog, func(r rune) bool { return r == '\n' })
		assert.Len(t, lines, unregistered, "one log line per unregistered error:\n%s", rpcLog)
		for _, l := range lines {
			assert.Contains(t, l, `level=ERROR msg="rpc: internal error"`)
		}
		for _, secret := range []string{token, "secret", "test-key", "good-key", "bad-key"} {
			assert.NotContains(t, rpcLog, secret)
		}
	})
	if os.Getenv("CONTRACT_DUMP") != "" {
		t.Logf("typed, normalized:\n%s", got[0])
	}
	if got[0] != "" && got[1] != "" {
		assert.Equal(t, got[0], got[1], "typed (expected) vs JSON-RPC (actual), normalized")
	}
	return rpcLog
}

// captureLogs sends slog output to a buffer until the test ends (tests here
// run one at a time) and returns a func reading it.
func captureLogs(t *testing.T) func() string {
	var mu sync.Mutex
	var b strings.Builder
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return b.Write(p)
	}), nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return func() string { mu.Lock(); defer mu.Unlock(); return b.String() }
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// serve puts svc behind the Local API on a fresh short directory (Unix
// socket paths are short) and dials it with the full role.
func serve(t *testing.T, svc *core.Service) (r *contract.Remote, token string) {
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
	tok, err := os.ReadFile(filepath.Join(dir, rpc.TokenName))
	require.NoError(t, err)
	return &contract.Remote{C: c}, string(tok)
}

// normalize renders v as indented JSON with every numeric ID (under a key
// ending in ID or IDs), every timestamp, every RFC 5322 Message-ID (in a
// string under MessageID, InReplyTo or References) and every opaque value
// replaced by a placeholder numbered in order of first appearance: the same
// value always gets the same placeholder, so associations survive and both
// sides must agree on them. A timestamp taken from the wall clock while the
// scenario ran (within [start, end], at the store's one-second resolution)
// becomes "time:now" instead: which of those fall in the same second is
// timing, not behavior.
func normalize(t *testing.T, v any, start, end time.Time) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var tree any
	require.NoError(t, d.Decode(&tree))
	n := &normalizer{seen: map[string]string{}, count: map[string]int{}, start: start.Truncate(time.Second), end: end}
	out, err := json.MarshalIndent(n.walk(tree, false), "", "  ")
	require.NoError(t, err)
	return string(out)
}

type normalizer struct {
	seen       map[string]string
	count      map[string]int
	start, end time.Time
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
			if str, ok := v[k].(string); ok && msgIDKeys[k] {
				out[k] = n.msgIDs(str)
				continue
			}
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
		if o, ok := strings.CutPrefix(v, opaquePrefix); ok {
			return n.placeholder("opaque", o)
		}
		if ts, err := time.Parse(time.RFC3339Nano, v); err == nil && !ts.IsZero() {
			if !ts.Before(n.start) && !ts.After(n.end) {
				return "time:now"
			}
			return n.placeholder("time", v)
		}
	}
	return v
}

// msgIDKeys hold RFC 5322 message identifiers (one, or a list in References).
var msgIDKeys = map[string]bool{"MessageID": true, "InReplyTo": true, "References": true}

var msgIDRE = regexp.MustCompile(`<[^<>\s]+@[^<>\s]+>`)

// msgIDs replaces every <id@host> in s with its placeholder.
func (n *normalizer) msgIDs(s string) string {
	return msgIDRE.ReplaceAllStringFunc(s, func(id string) string { return n.placeholder("msgid", id) })
}

const opaquePrefix = "opaque:"

// opaque marks v, a random value the product generates, for an
// association-preserving placeholder once it has the expected format;
// otherwise v stays as it is, so the difference shows.
func opaque(format string, v string) string {
	if regexp.MustCompile(format).MatchString(v) {
		return opaquePrefix + v
	}
	return "bad format " + format + ": " + v
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
	"base_url_scheme": core.ErrBaseURLScheme, "no_model_list": ai.ErrNoModelList, "model_forbidden": ai.ErrModelForbidden, "key_needs_confirm": core.ErrKeyNeedsConfirm,
	"unknown_flow": core.ErrUnknownFlow, "invalid_state": core.ErrInvalidState, "skill_disabled": core.ErrSkillDisabled,
	"signature_image": core.ErrSignatureImage, "signature_image_count": core.ErrSignatureImageCount,
	"signature_not_found": core.ErrSignatureNotFound, "signature_name": core.ErrSignatureName,
	"not_grantable":   core.ErrNotGrantable,
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
		"At": "2026-10-01T09:00:00Z", "Zero": "0001-01-01T00:00:00Z", "N": 7}, never, never)
	b := normalize(t, map[string]any{"ID": 3, "List": []map[string]any{{"AccountID": 3, "ThreadIDs": []int{4, 3}}},
		"At": "2027-01-01T00:00:00Z", "Zero": "0001-01-01T00:00:00Z", "N": 7}, never, never)
	assert.Equal(t, a, b)
	c := normalize(t, map[string]any{"ID": 3, "List": []map[string]any{{"AccountID": 4, "ThreadIDs": []int{4, 3}}},
		"At": "2027-01-01T00:00:00Z", "Zero": "0001-01-01T00:00:00Z", "N": 7}, never, never)
	assert.NotEqual(t, a, c, "a broken association must show")

	// Wall-clock times inside the run's window compare as "now"; one outside it shows.
	start := time.Date(2026, 10, 10, 9, 0, 0, 500, time.UTC)
	end := start.Add(3 * time.Second)
	in := func(at ...string) string { return normalize(t, map[string]any{"At": at}, start, end) }
	assert.Equal(t, in("2026-10-10T09:00:00Z", "2026-10-10T09:00:00Z"), in("2026-10-10T09:00:00Z", "2026-10-10T09:00:01Z"))
	assert.Contains(t, in("2026-10-10T09:00:02Z"), `"time:now"`)
	assert.NotEqual(t, in("2026-10-10T09:00:00Z"), in("2026-10-10T09:00:04Z"), "a time outside the run shows")
}

var never time.Time // an empty wall-clock window

func TestNormalizeMessageIDs(t *testing.T) {
	sent := func(own, parent, root, from string) string {
		return normalize(t, map[string]any{"Sent": map[string]any{"MessageID": own, "InReplyTo": parent,
			"References": root + " " + parent, "From": from}, "Incoming": map[string]any{"MessageID": parent}}, never, never)
	}
	a := sent("<A1@godwit>", "<p1@x>", "<r1@x>", "<bob@x>")
	assert.Equal(t, a, sent("<B7@godwit>", "<p2@y>", "<r2@y>", "<bob@x>"), "other values, same associations")
	assert.NotEqual(t, a, sent("<B7@godwit>", "<p2@y>", "<p2@y>", "<bob@x>"), "a changed association must show")
	assert.NotEqual(t, a, sent("<A1@godwit>", "<p1@x>", "<r1@x>", "<carol@x>"), "other strings are compared literally")
	assert.Contains(t, a, `"From": "\u003cbob@x\u003e"`)
	assert.Contains(t, a, `"References": "msgid#3 msgid#1"`)

	o := func(vs ...string) string {
		var out []string
		for _, v := range vs {
			out = append(out, opaque(`^[a-z]{3}$`, v))
		}
		return normalize(t, out, never, never)
	}
	assert.Equal(t, o("abc", "def", "abc"), o("xyz", "uvw", "xyz"))
	assert.NotEqual(t, o("abc", "def", "abc"), o("xyz", "uvw", "uvw"), "a changed association must show")
	assert.NotEqual(t, o("abc"), o("ABCD"), "a bad format must show")
}

// A stream that delivered its business events and then ended abnormally
// reports that end even when the caller cancels right after: the error is
// recorded before the stream is released, and cancel returns only once the
// forwarder is done. The fake peer keeps the forwarder blocked (nobody reads
// past the first event), the case where a cancel used to drop the error.
func TestRemoteStreamErrorSurvivesCancel(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })
	queued := make(chan func() int, 1) // the typed channel's length, once the test took the first event
	go func() {
		r := bufio.NewReader(server)
		next := func() (id json.RawMessage) {
			line, err := r.ReadBytes('\n')
			if err != nil {
				return nil
			}
			var m struct{ ID json.RawMessage }
			json.Unmarshal(line, &m)
			return m.ID
		}
		send := func(v any) { b, _ := json.Marshal(v); server.Write(append(b, '\n')) }
		send(map[string]any{"jsonrpc": "2.0", "id": next(), "result": map[string]any{"stream": 7}}) // Subscribe
		event := func() {
			send(map[string]any{"jsonrpc": "2.0", "method": "$/stream", "params": []any{7, map[string]any{"Type": "message.received"}}})
		}
		// In two batches, so the raw stream never overflows however late the
		// forwarder runs: the first fits it; the second comes once the forwarder
		// has moved the first, and fills the typed channel, blocking the forwarder.
		for range rpc.StreamBuffer {
			event()
		}
		for n := <-queued; n() < rpc.StreamBuffer-1; {
			time.Sleep(time.Millisecond)
		}
		for range 45 {
			event()
		}
		send(map[string]any{"jsonrpc": "2.0", "method": "$/stream.end", "params": []any{7, map[string]any{"code": -32000, "message": "transport broke"}}})
		send(map[string]any{"jsonrpc": "2.0", "id": next(), "result": nil}) // ping: the end is processed by now
		for next() != nil {                                                 // $/stream.cancel and the rest
		}
	}()
	r := &contract.Remote{C: rpc.NewClient(client)}
	ch, cancel := r.Subscribe()
	ev := <-ch
	queued <- func() int { return len(ch) }
	require.Equal(t, "message.received", ev.Type, "the business event arrived")
	require.NoError(t, r.C.Call(t.Context(), "ping", nil))
	cancel()
	errs := r.StreamErrs()
	require.Len(t, errs, 1)
	assert.ErrorContains(t, errs[0], "transport broke")
}
