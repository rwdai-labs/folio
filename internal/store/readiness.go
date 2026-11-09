package store

// capabilityReadiness describes local configuration, never an upstream credential
// or inference check. Repository availability is independent of chat/report.
func (s *Store) capabilityReadiness(connected bool) map[string]any {
	card := func(available bool, reason string) map[string]any {
		return map[string]any{"available": available, "reason": reason, "verification": "unknown"}
	}
	assistant := card(false, "agent_unavailable")
	// A turn holds agentMu and may call status itself. Avoid recursively taking
	// that lock; an in-flight operation keeps readiness unknown while busy.
	if s.agentMu.TryLock() {
		runtime, _, err := s.boundAgentRuntime()
		if err == nil && runtime != nil && runtime.Available() {
			assistant = card(true, "configured")
		}
		s.agentMu.Unlock()
	} else {
		assistant = card(true, "busy")
	}
	if target, enabled, err := s.AgentRoute("console", "owner.message"); err != nil || !enabled || target != "agent:main" {
		assistant = card(false, "agent_route_disabled")
	}
	report := card(false, "model_unavailable")
	kind, kindErr := s.taskKindFor("report")
	if kindErr == nil && kind.Action == kindActionReport {
		binding, err := s.resolveModelBinding("report", kind.ID)
		if err != nil {
			report = card(false, "model_services_invalid")
		} else if binding != nil || s.runnerAvailable(kind.DefaultRunner()) {
			report = card(true, "configured")
		}
	}
	maintenance := card(false, "repository_unavailable")
	if connected {
		_, err := s.resolveModelBinding("maintenance", "")
		if err != nil {
			maintenance = card(false, "model_services_invalid")
		} else if s.runnerAvailable("codex") {
			maintenance = card(true, "configured")
		} else {
			maintenance = card(false, "model_unavailable")
		}
	}
	context := card(connected, "repository_unavailable")
	if connected {
		context = card(true, "configured")
	}
	// Workshop/source-editing is a separate later phase. Advertising the
	// capability as unavailable lets catalog requirements stay explicit without
	// changing which daily route a deployment starts on.
	workshop := card(false, "workshop_unavailable")
	return map[string]any{"assistant": assistant, "report": report, "maintenance": maintenance, "context": context, "workshop": workshop}
}
