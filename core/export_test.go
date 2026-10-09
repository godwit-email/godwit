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
	h, ok := s.running[name]
	return h, ok
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
