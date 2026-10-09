package core

import "github.com/godwit-email/godwit/core/ai"

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
