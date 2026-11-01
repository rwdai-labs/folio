package store

// A task kind is user-facing configuration; an action plan is the internal
// execution shape behind it. Phase 1 plans have one step, but the dispatcher
// already goes through the plan so a later multi-step action does not change
// the task or audit contract.
const (
	actionStepMaintenance = kindActionMaintenance
	actionStepReport      = kindActionReport
)

type actionStep struct {
	Type   string
	Report taskKind
}

type actionPlan struct {
	ID    string
	Steps []actionStep
}

// actionPlanForKind maps one configured kind to the internal step chain.
// Unknown actions are rejected here as well as at kind validation, so a
// corrupted file can never silently select a different executor.
func actionPlanForKind(kind taskKind) (actionPlan, error) {
	switch kind.Action {
	case kindActionMaintenance:
		return actionPlan{
			ID:    kind.ID,
			Steps: []actionStep{{Type: actionStepMaintenance}},
		}, nil
	case kindActionReport:
		return actionPlan{
			ID:    kind.ID,
			Steps: []actionStep{{Type: actionStepReport, Report: kind}},
		}, nil
	default:
		return actionPlan{}, errStore("kind_unknown")
	}
}

// builtinActionPlan exposes the actions Folio implements without configuration.
// The maintenance action stays available even when the owner removes its kind
// from task-kinds.json; the scheduler must not depend on that file.
func builtinActionPlan(id string) (actionPlan, error) {
	if id == taskKindMaintenance {
		return actionPlanForKind(taskKind{ID: id, Action: kindActionMaintenance})
	}
	return actionPlan{}, errStore("kind_unknown")
}

// runAction executes one internal action plan. A Phase 1 plan is exactly one
// step: the task queue has one executor, and the run ledger has one result.
func (s *Store) runAction(plan actionPlan, task taskRecord, trigger string) (map[string]any, error) {
	if len(plan.Steps) != 1 {
		return nil, errStore("action_plan_invalid")
	}
	step := plan.Steps[0]
	switch step.Type {
	case actionStepMaintenance:
		return s.runAutomation(trigger, "codex", "", task.ID)
	case actionStepReport:
		// Report runs are always one task attempt. An event that wants a report
		// must create a task; it cannot run the report inline under a different
		// trigger.
		if trigger != "task" {
			return nil, errStore("action_plan_invalid")
		}
		return s.runReportTask(step.Report, task)
	default:
		return nil, errStore("action_plan_invalid")
	}
}
