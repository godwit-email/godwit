package core

import "github.com/godwit-email/godwit/core/ai"

// registerExecutorForTest offers a write tool named name whose calls become
// approvals executed by ex. A restarted Service needs it registered again.
func (s *Service) registerExecutorForTest(name string, ex executor) {
	s.registerTool(registeredTool{Spec: ai.ToolSpec{Name: name, Description: "Test write tool."}, Write: true, Exec: &ex})
}
