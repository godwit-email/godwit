package core

import (
	"context"
	"encoding/json"
	"time"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/gateway"
)

// registerExecutorForTest offers a write tool named name whose calls become
// approvals executed by ex. A restarted Service needs it registered again.
func (s *Service) registerExecutorForTest(name string, ex executor) {
	if err := s.registerTool(toolDesc{Spec: ai.ToolSpec{Name: name, Description: "Test write tool."}, Write: true, Exec: &ex}); err != nil {
		panic(err)
	}
}

// extDescsForTest are the test descriptors of extEnv: taint (a Wit read tool
// that marks the run as having read mail), ext_read (an MCP read tool with
// egress, run externally once approved) and ext_write (a plugin write tool run
// externally, bound to e.binding).
func extDescsForTest(e *extEnv, prepare func(context.Context, Conversation, json.RawMessage) (prepared, error)) []toolDesc {
	empty := map[string]any{"type": "object"}
	return []toolDesc{
		{Spec: ai.ToolSpec{Name: "taint", Description: "Reads mail.", Schema: empty,
			Run: func(ctx context.Context, _ json.RawMessage) (any, error) {
				runOf(ctx).scope.MarkReadMail()
				return "mail", nil
			}}},
		{Spec: ai.ToolSpec{Name: "ext_read", Description: "Reads remotely.", Schema: empty,
			Run: func(context.Context, json.RawMessage) (any, error) { e.inRun.Add(1); return "read in run", nil }},
			Source: gateway.SourceMCP, Extension: "srv1", Mode: modeExternal, Prepare: prepare,
			Egress: func(*gateway.Scope) []string { return []string{leakyDest} },
			Run:    func(context.Context, approvalRow) (any, error) { e.runs.Add(1); return "read approved", nil }},
		{Spec: ai.ToolSpec{Name: "ext_write", Description: "Writes remotely.", Schema: empty},
			Source: gateway.SourcePlugin, Extension: "demo", Write: true, Mode: modeExternal, Prepare: prepare,
			Binding: func(context.Context) (string, error) { return e.binding.Load().(string), nil },
			Timeout: 10 * time.Second,
			Run: func(ctx context.Context, _ approvalRow) (any, error) {
				e.runs.Add(1)
				if e.started != nil {
					e.started <- struct{}{}
				}
				if e.block != nil {
					select {
					case <-e.block:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				if e.outcome != nil {
					return e.outcome(ctx)
				}
				return map[string]bool{"wrote": true}, nil
			}},
	}
}

// RunningPluginForTest reports whether the plugin runs, and the source hash of its code.
func (s *Service) RunningPluginForTest(name string) (string, bool) {
	s.pluginsMu.Lock()
	defer s.pluginsMu.Unlock()
	rp, ok := s.running[name]
	if !ok {
		return "", false
	}
	return rp.hash, true
}

// SetPluginHookForTest makes InstallPlugin and loading call f at "switch"
// (before the row points at a new version), "switched" (after) and "load";
// an error from f stops there, as a crash or failure would. It returns a restore func.
func SetPluginHookForTest(f func(stage, name string) error) func() {
	pluginHook = f
	return func() { pluginHook = nil }
}

func (s *Service) ExecForTest(q string) error {
	_, err := s.db.Execute(context.Background(), q)
	return err
}

// PluginConfigForTest is pluginConfig.
func (s *Service) PluginConfigForTest(name string) (map[string]any, int64, error) {
	return s.pluginConfig(context.Background(), name)
}

// PluginSecretForTest reads the plugin secret's keychain entry.
func (s *Service) PluginSecretForTest(name, key string) (string, error) {
	return keychainGet(s.pluginSecretUser(name, key))
}

// SecretsForTest lists the extension secret set.
func (s *Service) SecretsForTest() []string { return s.secrets.Values() }

// KeyringFaultForTest makes every keychain op ("get", "set", "delete") ask
// fail(n), n counting that op's calls from 1; a non-nil error is returned
// instead of calling the keyring. It returns a restore func.
func KeyringFaultForTest(op string, fail func(n int) error) func() {
	n := 0
	switch op {
	case "get":
		orig := keyringGet
		keyringGet = func(svc, user string) (string, error) {
			if n++; fail(n) != nil {
				return "", fail(n)
			}
			return orig(svc, user)
		}
		return func() { keyringGet = orig }
	case "set":
		orig := keyringSet
		keyringSet = func(svc, user, v string) error {
			if n++; fail(n) != nil {
				return fail(n)
			}
			return orig(svc, user, v)
		}
		return func() { keyringSet = orig }
	default:
		orig := keyringDelete
		keyringDelete = func(svc, user string) error {
			if n++; fail(n) != nil {
				return fail(n)
			}
			return orig(svc, user)
		}
		return func() { keyringDelete = orig }
	}
}

// PluginSecretKeysForTest reads the plugin's secret_keys column.
func (s *Service) PluginSecretKeysForTest(name string) (string, error) {
	rows, err := s.db.Query(context.Background(), "SELECT secret_keys FROM plugins WHERE name = ?", name)
	if err != nil {
		return "", err
	}
	if len(rows.Values) == 0 {
		return "", ErrPluginNotFound
	}
	return rows.Values[0][0].(string), nil
}

// SetNativeStartForTest shortens how long a native plugin may take to start.
func SetNativeStartForTest(t interface{ Cleanup(func()) }, d time.Duration) {
	old := nativeStart
	nativeStart = d
	t.Cleanup(func() { nativeStart = old })
}
