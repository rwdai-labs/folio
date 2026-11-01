package store

import (
	"os"
	"testing"
)

func TestEventRoutesDefaultAndOverride(t *testing.T) {
	store := newTestStore(t)
	target, ok, err := store.eventRouteFor(event{Source: "console", Type: "owner.message", Trust: eventTrustOwner})
	if err != nil || !ok || target != "agent:main" {
		t.Fatalf("console route=%q ok=%v err=%v", target, ok, err)
	}
	target, ok, err = store.eventRouteFor(event{Source: "scheduler", Type: "tick", Trust: eventTrustSystem})
	if err != nil || !ok || target != "action:maintenance" {
		t.Fatalf("scheduler route=%q ok=%v err=%v", target, ok, err)
	}
	raw := []byte(`{"version":1,"routes":[
		{"source":"console","type":"owner.message","target":"action:maintenance"},
		{"source":"scheduler","type":"tick","target":"action:maintenance"}
	]}`)
	if _, err := store.PutRoutesJSON(raw); err != nil {
		t.Fatalf("put routes: %v", err)
	}
	info, err := os.Stat(routesPathFor(store.dataDir))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("routes mode=%v err=%v", info, err)
	}
	target, ok, err = store.eventRouteFor(event{Source: "console", Type: "owner.message", Trust: eventTrustOwner})
	if err != nil || !ok || target != "action:maintenance" {
		t.Fatalf("overridden route=%q ok=%v err=%v", target, ok, err)
	}
}

func TestEventRoutesRejectInvalidTargets(t *testing.T) {
	store := newTestStore(t)
	for _, raw := range [][]byte{
		[]byte(`{"version":1,"routes":[{"source":"console","type":"owner.message","target":"shell:root"}]}`),
		[]byte(`{"version":1,"routes":[{"source":"console","type":"owner.message","target":"agent:main"},{"source":"console","type":"owner.message","target":"audit:only"}]}`),
		[]byte(`{"version":2,"routes":[]}`),
	} {
		if _, err := store.PutRoutesJSON(raw); err == nil {
			t.Fatalf("invalid routes accepted: %s", raw)
		}
	}
}

func TestEventRouteExplicitTargetStillWins(t *testing.T) {
	store := newTestStore(t)
	target, ok, err := store.eventRouteFor(event{
		Source: "hook:x", Type: "event", Trust: eventTrustIntegration, route: "action:triage",
	})
	if err != nil || !ok || target != "action:triage" {
		t.Fatalf("explicit route=%q ok=%v err=%v", target, ok, err)
	}
	if target, ok, _ := store.eventRouteFor(event{
		Source: "hook:x", Type: "event", Trust: eventTrustIntegration, route: "agent:main",
	}); !ok || target != "agent:main" {
		t.Fatalf("integration observer route=%q ok=%v", target, ok)
	}
}
