package store

// A task kind is user-facing configuration; an action plan is the internal
// execution shape behind it. Legacy kinds are one-step plans; configured
// actions may have several steps. The dispatcher always goes through a plan so
// the task and audit contract does not depend on which configuration produced
// the work.
const (
	actionStepMaintenance = kindActionMaintenance
	actionStepReport      = kindActionReport
)

type actionStep struct {
	Type     string
	Report   taskKind
	Pipeline *pipelineStep
}

type actionPlan struct {
	ID         string
	Label      string
	Priority   string
	Legacy     bool
	HTTPHosts  []string
	ReportFrom string
	Steps      []actionStep
}

// actionPlanForKind maps one legacy kind to the internal step chain. Unknown
// actions are rejected here as well as at kind validation, so a corrupted file
// can never silently select a different executor.
func actionPlanForKind(kind taskKind) (actionPlan, error) {
	switch kind.Action {
	case kindActionMaintenance:
		return actionPlan{
			ID:       kind.ID,
			Label:    kind.Label,
			Priority: kindDefaultPriority(kind),
			Legacy:   true,
			Steps:    []actionStep{{Type: actionStepMaintenance}},
		}, nil
	case kindActionReport:
		return actionPlan{
			ID:       kind.ID,
			Label:    kind.Label,
			Priority: kindDefaultPriority(kind),
			Legacy:   true,
			Steps:    []actionStep{{Type: actionStepReport, Report: kind}},
		}, nil
	default:
		return actionPlan{}, errStore("kind_unknown")
	}
}

// actionPlanForPipeline converts one validated actions.json entry to the same
// execution shape. The step values are copied so a concurrent reload cannot
// mutate a running plan.
func actionPlanForPipeline(definition pipelineDefinition, hosts []string) (actionPlan, error) {
	if len(definition.Steps) == 0 {
		return actionPlan{}, errStore("action_plan_invalid")
	}
	steps := make([]actionStep, 0, len(definition.Steps))
	for index := range definition.Steps {
		step := definition.Steps[index]
		steps = append(steps, actionStep{Type: step.Type, Pipeline: &step})
	}
	return actionPlan{
		ID:         definition.ID,
		Label:      definition.Label,
		Priority:   definition.defaultPriority(),
		HTTPHosts:  append([]string(nil), hosts...),
		ReportFrom: definition.ReportFrom,
		Steps:      steps,
	}, nil
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

// resolveTaskAction is the single action lookup used by task creation, hooks
// and dispatch. Runtime actions win over a legacy kind with the same id; a
// disabled action is not a silent fallback to a legacy executor.
func (s *Store) resolveTaskAction(id string) (actionPlan, error) {
	// Feedback planning is an internal reserved kind; owner actions must never
	// shadow it with arbitrary pipeline steps.
	if id == taskKindFeedbackPlan {
		return actionPlanForKind(feedbackPlanKind())
	}
	file, err := s.loadPipelines()
	if err != nil {
		return actionPlan{}, err
	}
	for _, definition := range file.Actions {
		if definition.ID != id {
			continue
		}
		if !definition.isEnabled() {
			return actionPlan{}, errStore("action_disabled")
		}
		return actionPlanForPipeline(definition, file.HTTPHosts)
	}
	kind, err := s.taskKindFor(id)
	if err != nil {
		// The scheduler and catalog may still use maintenance when the owner
		// removes its kind from task-kinds.json.
		if id == taskKindMaintenance {
			return builtinActionPlan(id)
		}
		return actionPlan{}, err
	}
	return actionPlanForKind(kind)
}

func taskResolveReason(err error) string {
	if err == nil {
		return "kind_unknown"
	}
	switch err.Error() {
	case "action_disabled":
		return "action_disabled"
	case "actions are invalid", "actions are unavailable":
		return "actions_invalid"
	default:
		return "kind_unknown"
	}
}

func (p actionPlan) defaultPriority() string {
	if p.Priority != "" {
		return p.Priority
	}
	return "P2"
}

func (p actionPlan) hasMaintenance() bool {
	for _, step := range p.Steps {
		if step.Type == actionStepMaintenance {
			return true
		}
	}
	return false
}

// runAction executes one internal action plan. Legacy plans keep their exact
// one-step behavior; configured actions run through the pipeline executor.
func (s *Store) runAction(plan actionPlan, task taskRecord, trigger string) (map[string]any, error) {
	if len(plan.Steps) == 0 {
		return nil, errStore("action_plan_invalid")
	}
	if !plan.Legacy {
		return s.runPipeline(plan, task)
	}
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
