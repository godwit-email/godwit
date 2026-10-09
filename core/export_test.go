package core

import (
	"context"

	"github.com/godwit-email/godwit/core/ai"
)

// registerExecutorForTest offers a write tool named name whose calls become
// approvals executed by ex. A restarted Service needs it registered again.
func (s *Service) registerExecutorForTest(name string, ex executor) {
	s.registerTool(registeredTool{Spec: ai.ToolSpec{Name: name, Description: "Test write tool."}, Write: true, Exec: &ex})
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
