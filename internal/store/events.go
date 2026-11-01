package store

import (
	"strings"
	"time"
)

// Event trust is assigned by the ingress, never read from an event body. Phase
// 1 only routes scheduler ticks; Phase 3 adds owner and integration ingress.
const (
	eventTrustOwner       = "owner"
	eventTrustIntegration = "integration"
	eventTrustSystem      = "system"
)

type event struct {
	ID         string
	Source     string
	Type       string
	Trust      string
	Subject    string
	OccurredAt time.Time
	Payload    map[string]any

	// route is a target supplied by trusted ingress configuration. It is never
	// decoded from a request body. Phase 1 only uses the default table below;
	// Phase 3 adds per-hook and owner targets.
	route string
}

type eventRoute struct {
	Source string
	Type   string
	Target string
}

// defaultEventRoutes are the internal Phase 1 routes. Hook actions still come
// from the hook record; a routes file arrives with the owner/chat ingress.
var defaultEventRoutes = []eventRoute{
	{Source: "scheduler", Type: "tick", Target: "action:" + taskKindMaintenance},
}

// resolveEventRoute returns an action target for one authenticated event. The
// trust class is checked before either an explicit target or the table is used.
func resolveEventRoute(e event) (string, bool) {
	if !validEventTrust(e.Trust) {
		return "", false
	}
	if e.route != "" {
		return e.route, validActionTarget(e.route)
	}
	for _, route := range defaultEventRoutes {
		if route.Source == e.Source && route.Type == e.Type {
			return route.Target, validActionTarget(route.Target)
		}
	}
	return "", false
}

func validEventTrust(trust string) bool {
	switch trust {
	case eventTrustOwner, eventTrustIntegration, eventTrustSystem:
		return true
	}
	return false
}

func validActionTarget(target string) bool {
	_, ok := actionTargetID(target)
	return ok
}

// actionTargetID validates an "action:<kind>" target without resolving it. The
// kind is still resolved against the configured kinds at plan time.
func actionTargetID(target string) (string, bool) {
	const prefix = "action:"
	if !strings.HasPrefix(target, prefix) {
		return "", false
	}
	id := strings.TrimPrefix(target, prefix)
	if !taskKindRE.MatchString(id) {
		return "", false
	}
	return id, true
}

// planForTarget resolves a validated route target to an action plan. The
// built-in maintenance action does not require a kinds file; configured
// actions are resolved from data.
func (s *Store) planForTarget(target string) (actionPlan, error) {
	id, ok := actionTargetID(target)
	if !ok {
		return actionPlan{}, errStore("route_invalid")
	}
	if id == taskKindMaintenance {
		return builtinActionPlan(id)
	}
	plan, err := s.resolveTaskAction(id)
	if err != nil {
		return actionPlan{}, err
	}
	return plan, nil
}
