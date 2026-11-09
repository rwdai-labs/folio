package store

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

func catalogDefinitions(t *testing.T, store *Store) []map[string]any {
	t.Helper()
	value, err := store.ActionCatalog()
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	items, ok := value["actions"].([]any)
	if !ok {
		t.Fatalf("catalog actions: %#v", value["actions"])
	}
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		result = append(result, item.(map[string]any))
	}
	return result
}

func catalogEntryByID(t *testing.T, store *Store, id string) map[string]any {
	t.Helper()
	for _, entry := range catalogDefinitions(t, store) {
		if entry["id"] == id {
			return entry
		}
	}
	t.Fatalf("catalog entry %q missing", id)
	return nil
}

func TestActionCatalogIncludesLegacyAndConfiguredActions(t *testing.T) {
	store := newTestStore(t)
	order := 10
	definition := validPipelineDefinition()
	definition.ID = "custom"
	definition.Label = "Custom action"
	definition.Description = "Synthetic action description"
	definition.Requires = []string{"assistant", "workshop"}
	definition.Presentation = &pipelinePresentation{
		Group: "tools", Order: &order, SubmitLabel: "Custom run",
		Fields: []pipelineField{
			{Name: "note", Label: "Note", Type: "textarea", Required: true, MaxLength: 200, Placeholder: "Synthetic note"},
			{Name: "mode", Label: "Mode", Type: "select", Options: []pipelineOption{{Value: "fast", Label: "Fast"}, {Value: "safe", Label: "Safe"}}},
			{Name: "confirm", Label: "Confirm", Type: "boolean"},
		},
	}
	if err := store.PutAction(definition); err != nil {
		t.Fatal(err)
	}
	kinds := &taskKindsFile{Version: 1, Kinds: []taskKind{
		{ID: "maintenance", Label: "Context maintenance", Action: "maintenance", Priority: "P2"},
		{ID: "triage", Label: "Triage", Action: "report", Priority: "P2"},
	}}
	if err := store.writeTaskKinds(taskKindsPathFor(store.dataDir), kinds); err != nil {
		t.Fatal(err)
	}
	entries := catalogDefinitions(t, store)
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry["id"].(string))
	}
	want := []string{"report", "triage", "maintenance", "custom"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("catalog order=%v want %v", ids, want)
	}
	report := catalogEntryByID(t, store, "report")
	if report["label_key"] != "catalog.report.label" || report["description_key"] != "catalog.report.description" || report["submit_label_key"] != "catalog.report.submit" {
		t.Fatalf("report keys=%v", report)
	}
	if report["group"] != "daily" || report["order"] != 20 || report["legacy"] != true || report["requires"].([]any)[0] != "report" {
		t.Fatalf("report metadata=%v", report)
	}
	fields := report["fields"].([]any)
	if len(fields) != 2 {
		t.Fatalf("report fields=%v", fields)
	}
	request := fields[0].(map[string]any)
	if request["name"] != "request" || request["type"] != "textarea" || request["required"] != true || request["max_length"] != 4000 {
		t.Fatalf("request field=%v", request)
	}
	priority := fields[1].(map[string]any)
	options := priority["options"].([]any)
	if priority["name"] != "priority" || len(options) != 2 || options[1].(map[string]any)["value"] != "P2" {
		t.Fatalf("priority field=%v", priority)
	}
	custom := catalogEntryByID(t, store, "custom")
	if custom["group"] != "tools" || custom["order"] != 10 || custom["submit_label"] != "Custom run" || custom["description"] != "Synthetic action description" {
		t.Fatalf("custom metadata=%v", custom)
	}
	if custom["legacy"] != false || len(custom["fields"].([]any)) != 3 {
		t.Fatalf("custom fields=%v", custom)
	}
	requires := custom["requires"].([]any)
	if len(requires) != 2 || requires[0] != "assistant" || requires[1] != "workshop" {
		t.Fatalf("custom requires=%v", requires)
	}
	maintenance := catalogEntryByID(t, store, "maintenance")
	if len(maintenance["fields"].([]any)) != 0 || maintenance["order"] != 30 || maintenance["submit_label_key"] != "catalog.maintenance.submit" {
		t.Fatalf("maintenance metadata=%v", maintenance)
	}
	encoded, _ := json.Marshal(entries)
	if strings.Contains(string(encoded), `"steps"`) || strings.Contains(string(encoded), "Write a brief") || strings.Contains(string(encoded), "http_hosts") {
		t.Fatalf("catalog exposed execution detail: %s", encoded)
	}
}

func TestActionCatalogShadowsConfiguredLegacyID(t *testing.T) {
	store := newTestStore(t)
	definition := validPipelineDefinition()
	definition.ID = "report"
	definition.Label = "Custom report"
	if err := store.PutAction(definition); err != nil {
		t.Fatal(err)
	}
	entries := catalogDefinitions(t, store)
	count := 0
	for _, entry := range entries {
		if entry["id"] == "report" {
			count++
			if entry["legacy"] != false || entry["label"] != "Custom report" {
				t.Fatalf("shadowed report=%v", entry)
			}
		}
	}
	if count != 1 {
		t.Fatalf("configured report was not the only report entry: %v", entries)
	}
}

func TestOldActionsFileVersionTwoStillLoads(t *testing.T) {
	store := newTestStore(t)
	raw := `{
  "version": 2,
  "actions": [{
    "id": "old-action",
    "label": "Old action",
    "priority": "P1",
    "steps": [{"id": "ask", "type": "agent", "prompt": "Synthetic old prompt"}]
  }]
}`
	if err := os.WriteFile(pipelinePathFor(store.dataDir), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := store.loadPipelines()
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Actions) != 1 || file.Actions[0].Presentation != nil || file.Actions[0].Requires == nil {
		t.Fatalf("old definition=%+v", file.Actions)
	}
	listing, err := store.ListActions()
	if err != nil {
		t.Fatal(err)
	}
	entry := listing["actions"].([]any)[0].(map[string]any)
	if _, ok := entry["presentation"]; ok {
		t.Fatalf("nil presentation was serialized: %v", entry)
	}
	if entry["description"] != nil || entry["requires"] == nil {
		t.Fatalf("new list fields missing: %v", entry)
	}
	catalog := catalogEntryByID(t, store, "old-action")
	if catalog["group"] != catalogDefaultGroup || catalog["order"] != catalogDefaultOrder || catalog["submit_label"] != "Old action" || catalog["legacy"] != false {
		t.Fatalf("old catalog defaults=%v", catalog)
	}
}

func TestActionPresentationValidation(t *testing.T) {
	store := newTestStore(t)
	order := 1
	baseFields := func() []pipelineField {
		return []pipelineField{{Name: "note", Label: "Note", Type: "text", MaxLength: 100}}
	}
	valid := func() pipelineDefinition {
		definition := validPipelineDefinition()
		definition.Description = "Synthetic description"
		definition.Requires = []string{"assistant"}
		definition.Presentation = &pipelinePresentation{Group: "tools", Order: &order, SubmitLabel: "Run", Fields: baseFields()}
		return definition
	}
	if err := store.ValidateAction(valid()); err != nil {
		t.Fatalf("valid presentation rejected: %v", err)
	}
	maxValid := valid()
	maxValid.Presentation.Fields[0].MaxLength = maxActionCatalogTextLength
	if err := store.ValidateAction(maxValid); err != nil {
		t.Fatalf("4000-rune text field rejected: %v", err)
	}
	tooLong := strings.Repeat("x", maxActionCatalogGroupRunes+1)
	cases := map[string]func(*pipelineDefinition){
		"description too long": func(d *pipelineDefinition) { d.Description = strings.Repeat("x", maxPipelineDescriptionRunes+1) },
		"unknown requires":     func(d *pipelineDefinition) { d.Requires = []string{"deploy"} },
		"duplicate requires":   func(d *pipelineDefinition) { d.Requires = []string{"assistant", "assistant"} },
		"group too long":       func(d *pipelineDefinition) { d.Presentation.Group = tooLong },
		"order negative":       func(d *pipelineDefinition) { value := -1; d.Presentation.Order = &value },
		"order too high":       func(d *pipelineDefinition) { value := 10000; d.Presentation.Order = &value },
		"submit too long":      func(d *pipelineDefinition) { d.Presentation.SubmitLabel = strings.Repeat("x", 65) },
		"too many fields": func(d *pipelineDefinition) {
			fields := make([]pipelineField, 9)
			for index := range fields {
				fields[index] = pipelineField{Name: "f" + string(rune('a'+index)), Label: "F", Type: "text"}
			}
			d.Presentation.Fields = fields
		},
		"bad field name": func(d *pipelineDefinition) { d.Presentation.Fields[0].Name = "Bad Name" },
		"duplicate field": func(d *pipelineDefinition) {
			d.Presentation.Fields = append(d.Presentation.Fields, d.Presentation.Fields[0])
		},
		"empty label":          func(d *pipelineDefinition) { d.Presentation.Fields[0].Label = "" },
		"label too long":       func(d *pipelineDefinition) { d.Presentation.Fields[0].Label = strings.Repeat("x", 121) },
		"bad type":             func(d *pipelineDefinition) { d.Presentation.Fields[0].Type = "file" },
		"max length high":      func(d *pipelineDefinition) { d.Presentation.Fields[0].MaxLength = 4001 },
		"sensitive field name": func(d *pipelineDefinition) { d.Presentation.Fields[0].Name = "api_key" },
		"reserved title":       func(d *pipelineDefinition) { d.Presentation.Fields[0].Name = "title" },
		"reserved priority":    func(d *pipelineDefinition) { d.Presentation.Fields[0].Name = "priority" },
		"select without options": func(d *pipelineDefinition) {
			d.Presentation.Fields[0] = pipelineField{Name: "mode", Label: "Mode", Type: "select"}
		},
		"options on text": func(d *pipelineDefinition) {
			d.Presentation.Fields[0].Options = []pipelineOption{{Value: "x", Label: "X"}}
		},
		"placeholder long": func(d *pipelineDefinition) { d.Presentation.Fields[0].Placeholder = strings.Repeat("x", 121) },
		"select max length": func(d *pipelineDefinition) {
			d.Presentation.Fields[0] = pipelineField{Name: "mode", Label: "Mode", Type: "select", MaxLength: 10}
		},
		"too many options": func(d *pipelineDefinition) {
			options := make([]pipelineOption, 21)
			for index := range options {
				options[index] = pipelineOption{Value: "v" + string(rune('a'+index)), Label: "V"}
			}
			d.Presentation.Fields[0] = pipelineField{Name: "mode", Label: "Mode", Type: "select", Options: options}
		},
		"duplicate option": func(d *pipelineDefinition) {
			d.Presentation.Fields[0] = pipelineField{Name: "mode", Label: "Mode", Type: "select", Options: []pipelineOption{{Value: "x", Label: "X"}, {Value: "x", Label: "Y"}}}
		},
		"option too long": func(d *pipelineDefinition) {
			d.Presentation.Fields[0] = pipelineField{Name: "mode", Label: "Mode", Type: "select", Options: []pipelineOption{{Value: strings.Repeat("x", 65), Label: "X"}}}
		},
		"boolean with max length": func(d *pipelineDefinition) {
			d.Presentation.Fields[0] = pipelineField{Name: "flag", Label: "Flag", Type: "boolean", MaxLength: 1}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			definition := valid()
			mutate(&definition)
			if err := store.ValidateAction(definition); err == nil {
				t.Fatal("invalid presentation was accepted")
			}
		})
	}
}

func TestActionPresentationRejectsUnknownFields(t *testing.T) {
	store := newTestStore(t)
	raw := `{
  "version": 2,
  "actions": [{
    "id": "bad",
    "label": "Bad",
    "presentation": {"group": "tools", "colour": "red"},
    "steps": [{"id": "ask", "type": "agent", "prompt": "Synthetic"}]
  }]
}`
	if err := os.WriteFile(pipelinePathFor(store.dataDir), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.loadPipelines(); err == nil {
		t.Fatal("unknown presentation field was accepted")
	}
}

func TestRunActionLegacyReportAndMaintenance(t *testing.T) {
	store := newTestStore(t)
	request := "Investigate the synthetic issue and report the exact evidence."
	result, err := store.RunAction("report", map[string]any{"request": request, "priority": "P2"})
	if err != nil {
		t.Fatalf("run report: %v", err)
	}
	task := result["task"].(map[string]any)
	if result["created"] != true || task["kind"] != "report" || task["source"] != "manual" || task["priority"] != "P2" {
		t.Fatalf("report task=%v", result)
	}
	wantRunes := []rune(request)
	if len(wantRunes) > 80 {
		wantRunes = wantRunes[:80]
	}
	wantTitle := string(wantRunes)
	if task["title"] != wantTitle {
		t.Fatalf("report title=%q want %q", task["title"], wantTitle)
	}
	payload := task["payload"].(map[string]any)
	if payload["request"] != request {
		t.Fatalf("report payload=%v", payload)
	}
	result, err = store.RunAction("report", map[string]any{"request": request, "title": "Explicit report title", "priority": "P1"})
	if err != nil {
		t.Fatalf("run explicit report title: %v", err)
	}
	if result["task"].(map[string]any)["title"] != "Explicit report title" {
		t.Fatalf("explicit title was replaced: %v", result["task"])
	}
	result, err = store.RunAction("maintenance", map[string]any{"title": "Synthetic maintenance"})
	if err != nil {
		t.Fatalf("run maintenance: %v", err)
	}
	task = result["task"].(map[string]any)
	if task["kind"] != "maintenance" || task["title"] != "Synthetic maintenance" || task["priority"] != "P2" {
		t.Fatalf("maintenance task=%v", task)
	}
	if payload, ok := task["payload"]; ok {
		t.Fatalf("maintenance payload=%v", payload)
	}
}

func TestRunActionConfiguredPayloadBoundPreservesText(t *testing.T) {
	store := newTestStore(t)
	fields := make([]pipelineField, 8)
	for index := range fields {
		fields[index] = pipelineField{Name: fmt.Sprintf("f%d", index), Label: "Field", Type: "text", MaxLength: maxActionCatalogTextLength}
	}
	definition := validPipelineDefinition()
	definition.ID = "bounded-fields"
	definition.Label = "Bounded fields"
	definition.Presentation = &pipelinePresentation{Fields: fields}
	if err := store.PutAction(definition); err != nil {
		t.Fatalf("put bounded action: %v", err)
	}
	for key, value := range map[string]string{
		"f0": strings.Repeat("x", maxActionCatalogTextLength),
		"f1": strings.Repeat("界", maxActionCatalogTextLength),
	} {
		result, err := store.RunAction("bounded-fields", map[string]any{key: value})
		if err != nil {
			t.Fatalf("run bounded action %s: %v", key, err)
		}
		payload := result["task"].(map[string]any)["payload"].(map[string]any)
		if payload[key] != value {
			t.Fatalf("configured text %s was truncated or dropped: %d bytes", key, len(payload[key].(string)))
		}
	}
	listing, err := store.ListTasks()
	if err != nil {
		t.Fatal(err)
	}
	if len(listing["tasks"].([]any)) != 2 {
		t.Fatalf("distinct configured runs were coalesced: %v", listing["tasks"])
	}
	duplicate, err := store.RunAction("bounded-fields", map[string]any{"f0": strings.Repeat("x", maxActionCatalogTextLength)})
	if err != nil || duplicate["created"] != false {
		t.Fatalf("identical configured run was not deduplicated: %v err=%v", duplicate, err)
	}
	all := map[string]any{}
	for index := range fields {
		all[fields[index].Name] = strings.Repeat("界", maxActionCatalogTextLength)
	}
	if _, err := store.RunAction("bounded-fields", all); err == nil || err.Error() != "action_input_invalid" {
		t.Fatalf("oversized combined payload err=%v", err)
	}
}

func TestRunActionConfiguredFields(t *testing.T) {
	store := newTestStore(t)
	order := 40
	definition := validPipelineDefinition()
	definition.ID = "custom-run"
	definition.Label = "Custom run"
	definition.Presentation = &pipelinePresentation{
		Group: "tools", Order: &order, SubmitLabel: "Run",
		Fields: []pipelineField{
			{Name: "note", Label: "Note", Type: "textarea", Required: true, MaxLength: 20},
			{Name: "flag", Label: "Flag", Type: "boolean"},
			{Name: "mode", Label: "Mode", Type: "select", Options: []pipelineOption{{Value: "fast", Label: "Fast"}}},
			{Name: "choice", Label: "Choice", Type: "select", Required: true, Options: []pipelineOption{{Value: "yes", Label: "Yes"}}},
		},
	}
	if err := store.PutAction(definition); err != nil {
		t.Fatal(err)
	}
	result, err := store.RunAction("custom-run", map[string]any{
		"note": "synthetic", "flag": true, "choice": "yes", "title": "Custom title", "mode": "",
	})
	if err != nil {
		t.Fatalf("run custom: %v", err)
	}
	task := result["task"].(map[string]any)
	if task["kind"] != "custom-run" || task["title"] != "Custom title" || task["priority"] != definition.defaultPriority() {
		t.Fatalf("custom task=%v", task)
	}
	payload := task["payload"].(map[string]any)
	if payload["note"] != "synthetic" || payload["flag"] != true || payload["choice"] != "yes" {
		t.Fatalf("custom payload=%v", payload)
	}
	if _, ok := payload["mode"]; ok {
		t.Fatalf("empty optional select was persisted: %v", payload)
	}
	failures := []map[string]any{
		{"note": "synthetic", "choice": "yes", "unknown": "x"},
		{"choice": "yes"},
		{"note": 3.0, "choice": "yes"},
		{"note": "synthetic", "choice": "yes", "mode": "slow"},
		{"note": strings.Repeat("x", 21), "choice": "yes"},
		{"note": "synthetic", "choice": "yes", "flag": "true"},
		{"note": "synthetic", "choice": ""},
		{"note": "synthetic", "choice": "yes", "title": strings.Repeat("x", 121)},
		{"note": "synthetic", "choice": "yes", "priority": "P9"},
	}
	for index, fields := range failures {
		if _, err := store.RunAction("custom-run", fields); err == nil || err.Error() != "action_input_invalid" {
			t.Fatalf("failure %d err=%v fields=%v", index, err, fields)
		}
	}
	if _, err := store.RunAction("missing", nil); err == nil || err.Error() != "action_unknown" {
		t.Fatalf("unknown action err=%v", err)
	}
	if err := store.DisableAction("custom-run"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RunAction("custom-run", map[string]any{"note": "synthetic", "choice": "yes"}); err == nil || err.Error() != "action_disabled" {
		t.Fatalf("disabled action err=%v", err)
	}
	if _, err := store.RunAction("report", map[string]any{"request": "   "}); err == nil {
		t.Fatal("blank report request was accepted")
	}
}

func TestOwnerActionPayloadSurvivesTaskReload(t *testing.T) {
	store := newTestStore(t)
	definition := validPipelineDefinition()
	definition.ID = "owner-long"
	definition.Label = "Owner long"
	definition.Presentation = &pipelinePresentation{Fields: []pipelineField{
		{Name: "note", Label: "Note", Type: "textarea", Required: true, MaxLength: maxActionCatalogTextLength},
	}}
	if err := store.PutAction(definition); err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("界", maxActionCatalogTextLength)
	result, err := store.RunAction("owner-long", map[string]any{"note": text})
	if err != nil {
		t.Fatalf("run owner action: %v", err)
	}
	id := result["task"].(map[string]any)["id"].(string)
	store.taskMu.Lock()
	store.tasksCache = nil
	file, err := store.loadTasksLocked()
	store.taskMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range file.Tasks {
		if task.ID != id {
			continue
		}
		if task.Payload["note"] != text {
			t.Fatalf("owner action payload did not survive reload: %d runes", len([]rune(task.Payload["note"].(string))))
		}
		return
	}
	t.Fatalf("owner action task %s was dropped on reload", id)
}

func TestRunActionRejectsUnavailableWorkshopRequirement(t *testing.T) {
	store := newTestStore(t)
	definition := validPipelineDefinition()
	definition.ID = "workshop-run"
	definition.Label = "Workshop run"
	definition.Requires = []string{"workshop"}
	if err := store.PutAction(definition); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RunAction("workshop-run", nil); err == nil || err.Error() != "action_disabled" {
		t.Fatalf("workshop action err=%v", err)
	}
}
