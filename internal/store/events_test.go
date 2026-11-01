package store

import "testing"

func TestResolveEventRoute(t *testing.T) {
	target, ok := resolveEventRoute(event{Source: "scheduler", Type: "tick", Trust: eventTrustSystem})
	if !ok || target != "action:maintenance" {
		t.Fatalf("scheduler route target=%q ok=%v", target, ok)
	}

	target, ok = resolveEventRoute(event{
		Source: "hook:context", Type: "event", Trust: eventTrustIntegration, route: "action:triage",
	})
	if !ok || target != "action:triage" {
		t.Fatalf("explicit route target=%q ok=%v", target, ok)
	}

	if _, ok := resolveEventRoute(event{Source: "scheduler", Type: "tick", Trust: "root"}); ok {
		t.Fatal("unknown trust class was routed")
	}
	if _, ok := resolveEventRoute(event{Source: "scheduler", Type: "tick", Trust: eventTrustSystem, route: "action:../bad"}); ok {
		t.Fatal("invalid explicit target was routed")
	}
	if _, ok := resolveEventRoute(event{Source: "unknown", Type: "tick", Trust: eventTrustSystem}); ok {
		t.Fatal("unmatched event was routed")
	}
}

func TestActionTargetID(t *testing.T) {
	for target, want := range map[string]string{
		"action:maintenance": "maintenance",
		"action:triage":      "triage",
	} {
		got, ok := actionTargetID(target)
		if !ok || got != want {
			t.Fatalf("target %q => %q ok=%v", target, got, ok)
		}
	}
	for _, target := range []string{"", "maintenance", "action:", "action:../outside", "action:Triage"} {
		if _, ok := actionTargetID(target); ok {
			t.Fatalf("invalid target %q was accepted", target)
		}
	}
}

func TestPlanForTarget(t *testing.T) {
	store := newTestStore(t)
	writeTaskKinds(t, store,
		taskKind{ID: "maintenance", Label: "Context maintenance", Action: kindActionMaintenance},
		taskKind{ID: "triage", Label: "Issue triage", Action: kindActionReport, Prompt: "Triage {{title}}"},
	)

	maintenance, err := store.planForTarget("action:maintenance")
	if err != nil || maintenance.ID != "maintenance" || maintenance.Steps[0].Type != actionStepMaintenance {
		t.Fatalf("maintenance plan=%+v err=%v", maintenance, err)
	}

	triage, err := store.planForTarget("action:triage")
	if err != nil || triage.ID != "triage" || triage.Steps[0].Type != actionStepReport {
		t.Fatalf("triage plan=%+v err=%v", triage, err)
	}

	if _, err := store.planForTarget("action:missing"); err == nil {
		t.Fatal("unknown configured action resolved")
	}
	if _, err := store.planForTarget("action:Bad"); err == nil {
		t.Fatal("invalid target resolved")
	}
}

// The scheduler route must resolve the built-in maintenance action without
// reading task-kinds.json: removing the owner-facing maintenance kind must not
// disable scheduled maintenance.
func TestSchedulerTickUsesBuiltinMaintenanceRoute(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		kinds []taskKind
	}{
		{name: "default kinds"},
		{name: "maintenance kind removed", kinds: []taskKind{
			{ID: "triage", Label: "Issue triage", Action: kindActionReport, Prompt: "Triage {{title}}"},
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			store := newTestStore(t)
			if len(testCase.kinds) > 0 {
				writeTaskKinds(t, store, testCase.kinds...)
			}
			result, err := store.SchedulerTick()
			if err != nil {
				t.Fatalf("scheduler tick: %v", err)
			}
			if result["status"] != "skipped" || result["reason"] != "repository_not_connected" {
				t.Fatalf("unexpected result: %v", result)
			}
			listed, err := store.ListAutomation()
			if err != nil || listed["count"] != 1 {
				t.Fatalf("scheduler run was not recorded: %v %v", listed, err)
			}
		})
	}
}
