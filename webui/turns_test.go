package webui

// approvalsOf are h's approvals: on their calls, then each turn's own, in
// turn order.
func approvalsOf(h WitHistory) []Approval {
	out := []Approval{}
	for _, t := range h.Turns {
		for _, c := range t.Calls {
			if c.Approval != nil {
				out = append(out, *c.Approval)
			}
		}
		out = append(out, t.Approvals...)
	}
	return out
}
