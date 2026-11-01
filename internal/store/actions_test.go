package store

import "testing"

func TestActionPlanForKind(t *testing.T) {
	maintenance, err := actionPlanForKind(taskKind{ID: "maintenance", Action: kindActionMaintenance})
	if err != nil {
		t.Fatalf("maintenance plan: %v", err)
	}
	if maintenance.ID != "maintenance" || len(maintenance.Steps) != 1 || maintenance.Steps[0].Type != actionStepMaintenance {
		t.Fatalf("maintenance plan=%+v", maintenance)
	}

	report, err := actionPlanForKind(taskKind{
		ID: "triage", Action: kindActionReport, Prompt: "Triage {{title}}", Runner: "codex",
	})
	if err != nil {
		t.Fatalf("report plan: %v", err)
	}
	if report.ID != "triage" || len(report.Steps) != 1 || report.Steps[0].Type != actionStepReport {
		t.Fatalf("report plan=%+v", report)
	}
	if report.Steps[0].Report.Prompt != "Triage {{title}}" || report.Steps[0].Report.Runner != "codex" {
		t.Fatalf("report step lost configuration: %+v", report.Steps[0].Report)
	}

	if _, err := actionPlanForKind(taskKind{ID: "bad", Action: "deploy"}); err == nil {
		t.Fatal("unknown action produced a plan")
	}
}

func TestBuiltinActionPlanIsLimitedToMaintenance(t *testing.T) {
	plan, err := builtinActionPlan(taskKindMaintenance)
	if err != nil || plan.ID != taskKindMaintenance || plan.Steps[0].Type != actionStepMaintenance {
		t.Fatalf("builtin plan=%+v err=%v", plan, err)
	}
	if _, err := builtinActionPlan("triage"); err == nil {
		t.Fatal("configured kind was exposed as a builtin action")
	}
}

func TestRunActionRejectsInvalidPlans(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.runAction(actionPlan{ID: "empty"}, taskRecord{}, "task"); err == nil {
		t.Fatal("empty plan was accepted")
	}
	if _, err := store.runAction(actionPlan{
		ID:    "two-steps",
		Steps: []actionStep{{Type: actionStepMaintenance}, {Type: actionStepMaintenance}},
	}, taskRecord{}, "task"); err == nil {
		t.Fatal("multi-step plan was accepted in Phase 1")
	}
	if _, err := store.runAction(actionPlan{
		ID:    "unknown",
		Steps: []actionStep{{Type: "deploy"}},
	}, taskRecord{}, "task"); err == nil {
		t.Fatal("unknown step was accepted")
	}
	if _, err := store.runAction(actionPlan{
		ID:    "report",
		Steps: []actionStep{{Type: actionStepReport, Report: taskKind{ID: "triage", Action: kindActionReport}}},
	}, taskRecord{}, "scheduler"); err == nil {
		t.Fatal("report plan ran outside task dispatch")
	}
}
