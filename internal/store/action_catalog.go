package store

import (
	"sort"
	"strings"
)

// Action catalog entries are the owner-facing, non-executable view of a task
// action. Configured pipelines and legacy task kinds both appear here; steps,
// prompts, hosts and secrets never do.
const (
	catalogDefaultGroup = "actions"
	catalogDefaultOrder = 100
	catalogReportOrder  = 20
	catalogMaintOrder   = 30
)

type catalogEntry struct {
	ID             string
	Label          string
	LabelKey       string
	Description    string
	DescriptionKey string
	Priority       string
	Enabled        bool
	Group          string
	Order          int
	SubmitLabel    string
	SubmitLabelKey string
	Fields         []pipelineField
	Requires       []string
	Legacy         bool
	Kind           *taskKind
}

func (entry catalogEntry) public() map[string]any {
	fields := make([]any, 0, len(entry.Fields))
	for _, field := range entry.Fields {
		fields = append(fields, catalogFieldValue(field))
	}
	requires := make([]any, 0, len(entry.Requires))
	for _, required := range entry.Requires {
		requires = append(requires, required)
	}
	value := map[string]any{
		"id":           entry.ID,
		"label":        entry.Label,
		"priority":     entry.Priority,
		"enabled":      entry.Enabled,
		"group":        entry.Group,
		"order":        entry.Order,
		"submit_label": entry.SubmitLabel,
		"fields":       fields,
		"requires":     requires,
		"legacy":       entry.Legacy,
	}
	if entry.LabelKey != "" {
		value["label_key"] = entry.LabelKey
	}
	if entry.Description != "" {
		value["description"] = entry.Description
	}
	if entry.DescriptionKey != "" {
		value["description_key"] = entry.DescriptionKey
	}
	if entry.SubmitLabelKey != "" {
		value["submit_label_key"] = entry.SubmitLabelKey
	}
	return value
}

func catalogFieldValue(field pipelineField) map[string]any {
	value := map[string]any{
		"name": field.Name, "label": field.Label, "type": field.Type,
		"required": field.Required,
	}
	if field.MaxLength > 0 {
		value["max_length"] = field.MaxLength
	}
	if field.Placeholder != "" {
		value["placeholder"] = field.Placeholder
	}
	if field.Type == "select" {
		options := make([]any, 0, len(field.Options))
		for _, option := range field.Options {
			options = append(options, map[string]any{"value": option.Value, "label": option.Label})
		}
		value["options"] = options
	}
	return value
}

// ActionCatalog returns only the decoupled action surface. It includes legacy
// task kinds that the owner can still create unless a configured action with
// the same ID shadows them.
func (s *Store) ActionCatalog() (map[string]any, error) {
	entries, err := s.actionCatalogEntries()
	if err != nil {
		return nil, err
	}
	actions := make([]any, 0, len(entries))
	for _, entry := range entries {
		actions = append(actions, entry.public())
	}
	return map[string]any{"actions": actions}, nil
}

func (s *Store) actionCatalogEntries() ([]catalogEntry, error) {
	file, err := s.loadPipelines()
	if err != nil {
		return nil, err
	}
	kinds, err := s.loadTaskKinds()
	if err != nil {
		return nil, err
	}
	entries := make([]catalogEntry, 0, len(file.Actions)+len(kinds.Kinds)+1)
	configured := map[string]bool{}
	for _, definition := range file.Actions {
		configured[definition.ID] = true
		entries = append(entries, configuredCatalogEntry(definition))
	}
	seen := map[string]bool{}
	maintenanceKind := taskKind{ID: taskKindMaintenance, Label: "Context maintenance", Action: kindActionMaintenance, Priority: "P2"}
	for _, kind := range kinds.Kinds {
		if kind.ID == taskKindMaintenance {
			maintenanceKind = kind
			break
		}
	}
	if !configured[taskKindMaintenance] {
		entries = append(entries, legacyCatalogEntry(maintenanceKind))
		seen[taskKindMaintenance] = true
	}
	for _, kind := range kinds.Kinds {
		if configured[kind.ID] || seen[kind.ID] {
			continue
		}
		seen[kind.ID] = true
		entries = append(entries, legacyCatalogEntry(kind))
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Group != entries[j].Group {
			return entries[i].Group < entries[j].Group
		}
		if entries[i].Order != entries[j].Order {
			return entries[i].Order < entries[j].Order
		}
		if entries[i].Label != entries[j].Label {
			return entries[i].Label < entries[j].Label
		}
		return entries[i].ID < entries[j].ID
	})
	return entries, nil
}

func configuredCatalogEntry(definition pipelineDefinition) catalogEntry {
	entry := catalogEntry{
		ID: definition.ID, Label: definition.Label, Description: definition.Description,
		Priority: definition.defaultPriority(), Enabled: definition.isEnabled(),
		Group: catalogDefaultGroup, Order: catalogDefaultOrder, SubmitLabel: definition.Label,
		Requires: append([]string{}, definition.Requires...), Legacy: false,
	}
	if definition.Presentation != nil {
		presentation := definition.Presentation
		if presentation.Group != "" {
			entry.Group = presentation.Group
		}
		if presentation.Order != nil {
			entry.Order = *presentation.Order
		}
		if presentation.SubmitLabel != "" {
			entry.SubmitLabel = presentation.SubmitLabel
		}
		entry.Fields = append([]pipelineField{}, presentation.Fields...)
	}
	return entry
}

func catalogPriorityOptions() []pipelineOption {
	options := make([]pipelineOption, 0, 4)
	for _, priority := range []string{"P0", "P1", "P2", "P3"} {
		options = append(options, pipelineOption{Value: priority, Label: priority})
	}
	return options
}

func legacyCatalogEntry(kind taskKind) catalogEntry {
	entry := catalogEntry{
		ID: kind.ID, Label: kind.Label, Priority: kindDefaultPriority(kind),
		Enabled: true, Group: catalogDefaultGroup, Order: catalogDefaultOrder,
		SubmitLabel: "Run", Legacy: true,
		Fields: []pipelineField{}, Requires: []string{},
	}
	copyKind := kind
	entry.Kind = &copyKind
	switch kind.Action {
	case kindActionReport:
		entry.Group = "daily"
		entry.Order = catalogReportOrder
		entry.SubmitLabel = "Run report"
		entry.Description = "Ask Folio for a focused report."
		entry.Fields = []pipelineField{
			{Name: "request", Label: "Request", Type: "textarea", Required: true, MaxLength: maxActionCatalogTextLength, Placeholder: "What should Folio report on?"},
			{Name: "priority", Label: "Priority", Type: "select", Options: catalogPriorityOptions()},
		}
		entry.Requires = []string{"report"}
		if kind.ID == "report" {
			entry.LabelKey = "catalog.report.label"
			entry.DescriptionKey = "catalog.report.description"
			entry.SubmitLabelKey = "catalog.report.submit"
		}
	case kindActionMaintenance:
		entry.Group = "daily"
		entry.Order = catalogMaintOrder
		entry.SubmitLabel = "Run maintenance"
		entry.Description = "Run incremental context maintenance."
		entry.Requires = []string{"maintenance"}
		if kind.ID == taskKindMaintenance {
			entry.LabelKey = "catalog.maintenance.label"
			entry.DescriptionKey = "catalog.maintenance.description"
			entry.SubmitLabelKey = "catalog.maintenance.submit"
		}
	}
	return entry
}

// RunAction executes one catalog entry with validated owner input. The catalog
// is the allowlist: an ID outside it is unknown even if some legacy internal
// action exists, and a disabled entry never falls back.
func (s *Store) RunAction(id string, fields map[string]any) (map[string]any, error) {
	entries, err := s.actionCatalogEntries()
	if err != nil {
		return nil, err
	}
	var entry *catalogEntry
	for index := range entries {
		if entries[index].ID == id {
			entry = &entries[index]
			break
		}
	}
	if entry == nil {
		return nil, errStore("action_unknown")
	}
	if !entry.Enabled {
		return nil, errStore("action_disabled")
	}
	// Workshop is not implemented in this phase. Enforce it here as well as in
	// the catalog UI so a direct API call cannot enqueue work that cannot run.
	values, err := validateActionLaunch(*entry, fields)
	if err != nil {
		return nil, err
	}
	title := entry.Label
	titleSet := false
	if raw, ok := fields["title"].(string); ok {
		title = strings.TrimSpace(raw)
		titleSet = true
	}
	if value, ok := values["title"].(string); ok {
		title = strings.TrimSpace(value)
		titleSet = true
	}
	priority := entry.Priority
	if raw, ok := fields["priority"].(string); ok {
		priority = strings.TrimSpace(raw)
	}
	if value, ok := values["priority"].(string); ok {
		priority = strings.TrimSpace(value)
	}
	if title == "" {
		title = entry.Label
	}
	if entry.Legacy && entry.Kind != nil && entry.Kind.Action == kindActionReport {
		request, _ := values["request"].(string)
		if !titleSet || title == "" {
			title = actionTitleFromRequest(request)
		}
		if len([]rune(title)) > maxTaskTitleRunes {
			title = string([]rune(title)[:maxTaskTitleRunes])
		}
		return s.CreateTask(entry.ID, "manual", title, priority, map[string]any{"request": request})
	}
	if len([]rune(title)) > maxTaskTitleRunes {
		return nil, errStore("action_input_invalid")
	}
	if entry.Legacy {
		// Maintenance intentionally leaves priority to the task default and has
		// no payload; other legacy kinds do not invent capabilities either.
		return s.CreateTask(entry.ID, "manual", title, "", nil)
	}
	if !validPriority(priority) {
		return nil, errStore("action_input_invalid")
	}
	// Configured actions use the explicit owner-action payload path inside
	// CreateTask; it rejects an oversized or credential-shaped payload instead of
	// silently trimming a declared field.
	return s.CreateTask(entry.ID, "manual", title, priority, values)
}

func actionTitleFromRequest(request string) string {
	runes := []rune(strings.TrimSpace(request))
	if len(runes) > 80 {
		runes = runes[:80]
	}
	return string(runes)
}

// validateActionRequirements is the hard capability gate shared by the catalog,
// CreateTask, hooks and event targets. Workshop is not implemented in this
// phase; other requires values stay UI gating and execution remains fail-closed.
func validateActionRequirements(requires []string) error {
	for _, requirement := range requires {
		if requirement == "workshop" {
			return errStore("action_disabled")
		}
	}
	return nil
}

// validateActionLaunch is the single owner-launch contract for configured
// actions. The catalog endpoint and CreateTask both call it, so a caller cannot
// reach a configured action through /api/tasks or task.create without the same
// capability and presentation validation. Internal hook/event/pipeline callers
// stay on their trusted path and are not required to supply presentation fields.
func validateActionLaunch(entry catalogEntry, fields map[string]any) (map[string]any, error) {
	if err := validateActionRequirements(entry.Requires); err != nil {
		return nil, err
	}
	return validateActionRunFields(entry, fields)
}

func validateActionRunFields(entry catalogEntry, fields map[string]any) (map[string]any, error) {
	declared := make(map[string]pipelineField, len(entry.Fields))
	for _, field := range entry.Fields {
		declared[field.Name] = field
	}
	values := map[string]any{}
	for key, raw := range fields {
		if field, ok := declared[key]; ok {
			if field.Type == "select" && !field.Required {
				if text, ok := raw.(string); ok && text == "" {
					continue
				}
			}
			value, err := validateCatalogFieldValue(field, raw)
			if err != nil {
				return nil, err
			}
			values[key] = value
			continue
		}
		switch key {
		case "title":
			title, ok := raw.(string)
			if !ok || len([]rune(strings.TrimSpace(title))) > maxTaskTitleRunes {
				return nil, errStore("action_input_invalid")
			}
		case "priority":
			priority, ok := raw.(string)
			if !ok || !validPriority(strings.TrimSpace(priority)) {
				return nil, errStore("action_input_invalid")
			}
		default:
			return nil, errStore("action_input_invalid")
		}
	}
	for _, field := range entry.Fields {
		if !field.Required {
			continue
		}
		if _, ok := values[field.Name]; !ok {
			return nil, errStore("action_input_invalid")
		}
	}
	// Configured fields are ordinary task payload values. Reject an oversized
	// combined payload instead of letting CreateTask's projection drop values.
	size := 0
	for key, value := range values {
		size += len(key) + 8 + payloadSize(value)
	}
	if size > maxActionPayloadBytes {
		return nil, errStore("action_input_invalid")
	}
	return values, nil
}

func validateCatalogFieldValue(field pipelineField, raw any) (any, error) {
	switch field.Type {
	case "text", "textarea":
		value, ok := raw.(string)
		if !ok {
			return nil, errStore("action_input_invalid")
		}
		limit := field.MaxLength
		if limit <= 0 {
			limit = maxActionCatalogTextLength
		}
		if len([]rune(value)) > limit || field.Required && strings.TrimSpace(value) == "" {
			return nil, errStore("action_input_invalid")
		}
		return value, nil
	case "select":
		value, ok := raw.(string)
		if !ok {
			return nil, errStore("action_input_invalid")
		}
		for _, option := range field.Options {
			if value == option.Value {
				return value, nil
			}
		}
		return nil, errStore("action_input_invalid")
	case "boolean":
		value, ok := raw.(bool)
		if !ok || field.Required && !value {
			// A required acknowledgment checkbox must be explicitly checked; a
			// present-but-false value is not consent.
			return nil, errStore("action_input_invalid")
		}
		return value, nil
	default:
		return nil, errStore("action_input_invalid")
	}
}
