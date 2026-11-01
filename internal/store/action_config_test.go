package store

import (
	"os"
	"strings"
	"testing"
)

func validPipelineDefinition() pipelineDefinition {
	return pipelineDefinition{
		ID:         "morning-brief",
		Label:      "Morning brief",
		Priority:   "P1",
		ReportFrom: "brief",
		Steps: []pipelineStep{
			{ID: "ctx", Type: "context", Query: "today"},
			{ID: "brief", Type: "agent", Prompt: "Write a brief about {{payload.focus}}", Runner: "codex"},
			{ID: "send", Type: "push", Channel: "weixin", Title: "Morning brief", Body: "{{steps.brief.output}}"},
		},
	}
}

func TestPutActionLoadsAndListsRuntimeActions(t *testing.T) {
	store := newTestStore(t)
	definition := validPipelineDefinition()
	if err := store.PutAction(definition); err != nil {
		t.Fatalf("put action: %v", err)
	}
	info, err := os.Stat(pipelinePathFor(store.dataDir))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("actions file mode=%v err=%v", info, err)
	}
	file, err := store.loadPipelines()
	if err != nil || len(file.Actions) != 1 || file.Actions[0].ID != definition.ID {
		t.Fatalf("loaded actions=%+v err=%v", file, err)
	}
	listing, err := store.ListActions()
	if err != nil {
		t.Fatalf("list actions: %v", err)
	}
	actions := listing["actions"].([]any)
	if len(actions) != 1 || actions[0].(map[string]any)["enabled"] != true {
		t.Fatalf("listing=%v", listing)
	}
	plan, err := store.resolveTaskAction(definition.ID)
	if err != nil || plan.Legacy || plan.ID != definition.ID || len(plan.Steps) != 3 {
		t.Fatalf("resolved plan=%+v err=%v", plan, err)
	}
}

func TestActionVersionsAndRestore(t *testing.T) {
	store := newTestStore(t)
	first := validPipelineDefinition()
	if err := store.PutAction(first); err != nil {
		t.Fatal(err)
	}
	second := validPipelineDefinition()
	second.Label = "Morning brief v2"
	if err := store.PutAction(second); err != nil {
		t.Fatal(err)
	}
	versions, err := store.ActionVersions()
	if err != nil {
		t.Fatalf("versions: %v", err)
	}
	list := versions["versions"].([]any)
	if len(list) == 0 {
		t.Fatal("no action version was retained")
	}
	name := list[0].(map[string]any)["name"].(string)
	if err := store.RestoreActionVersion(name); err != nil {
		t.Fatalf("restore: %v", err)
	}
	file, err := store.loadPipelines()
	if err != nil || len(file.Actions) != 1 {
		t.Fatalf("restored file=%+v err=%v", file, err)
	}
	if file.Actions[0].Label != first.Label {
		t.Fatalf("restored label=%q want %q", file.Actions[0].Label, first.Label)
	}
}

func TestActionValidationRejectsUnsafeDefinitions(t *testing.T) {
	store := newTestStore(t)
	cases := map[string]pipelineDefinition{
		"script outside root": {
			ID: "bad-script", Label: "Bad", Steps: []pipelineStep{{
				ID: "run", Type: "script", Command: []string{"/bin/sh"},
			}},
		},
		"http host missing": {
			ID: "bad-http", Label: "Bad", Steps: []pipelineStep{{
				ID: "fetch", Type: "http", URL: "https://api.example.com/x",
			}},
		},
		"secret in prompt": {
			ID: "bad-prompt", Label: "Bad", Steps: []pipelineStep{{
				ID: "ask", Type: "agent", Prompt: "Use {{secrets.token}}", Runner: "codex",
			}},
		},
		"missing report step": {
			ID: "bad-report", Label: "Bad", ReportFrom: "missing", Steps: []pipelineStep{{
				ID: "ask", Type: "agent", Prompt: "Write", Runner: "codex",
			}},
		},
		"unknown step": {
			ID: "bad-step", Label: "Bad", Steps: []pipelineStep{{ID: "x", Type: "deploy"}},
		},
		"bad priority": {
			ID: "bad-priority", Label: "Bad", Priority: "P9", Steps: []pipelineStep{{
				ID: "ask", Type: "agent", Prompt: "Write", Runner: "codex",
			}},
		},
	}
	for name, definition := range cases {
		t.Run(name, func(t *testing.T) {
			if err := store.ValidateAction(definition); err == nil {
				t.Fatal("invalid action was accepted")
			}
		})
	}
}

func TestDisabledActionFailsClosed(t *testing.T) {
	store := newTestStore(t)
	if err := store.PutAction(validPipelineDefinition()); err != nil {
		t.Fatal(err)
	}
	if err := store.DisableAction("morning-brief"); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := store.resolveTaskAction("morning-brief"); err == nil || err.Error() != "action_disabled" {
		t.Fatalf("disabled action resolved: %v", err)
	}
}

func TestTaskKindsIncludeRuntimeActions(t *testing.T) {
	store := newTestStore(t)
	if err := store.PutAction(validPipelineDefinition()); err != nil {
		t.Fatal(err)
	}
	listing, err := store.TaskKinds()
	if err != nil {
		t.Fatalf("task kinds: %v", err)
	}
	kinds := listing["kinds"].([]any)
	found := false
	for _, item := range kinds {
		entry := item.(map[string]any)
		if entry["id"] == "morning-brief" {
			found = true
			if entry["action"] != "pipeline" || entry["priority"] != "P1" {
				t.Fatalf("runtime kind=%v", entry)
			}
		}
	}
	if !found {
		t.Fatalf("runtime action missing from kinds: %v", kinds)
	}
}

func TestActionDefinitionRejectsUnknownFields(t *testing.T) {
	store := newTestStore(t)
	raw := strings.TrimSpace(`
{
  "version": 2,
  "actions": [{
    "id": "bad",
    "label": "Bad",
    "steps": [{"id": "run", "type": "script", "command": ["/data/scripts/x.sh"], "shell": "true"}]
  }]
}
`)
	if err := os.WriteFile(pipelinePathFor(store.dataDir), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.loadPipelines(); err == nil {
		t.Fatal("unknown field was accepted")
	}
}

func TestPutActionHostsRevalidatesActions(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.PutActionHostsJSON([]byte(`{"hosts":["api.example.com"]}`)); err != nil {
		t.Fatalf("put hosts: %v", err)
	}
	if err := store.PutAction(pipelineDefinition{
		ID: "fetch", Label: "Fetch",
		Steps: []pipelineStep{{ID: "get", Type: "http", URL: "https://api.example.com/x"}},
	}); err != nil {
		t.Fatalf("put action: %v", err)
	}
	if _, err := store.PutActionHostsJSON([]byte(`{"hosts":[]}`)); err == nil {
		t.Fatal("host removal left an unroutable action")
	}
	if _, err := store.PutActionHostsJSON([]byte(`{"hosts":["bad host"]}`)); err == nil {
		t.Fatal("invalid host was accepted")
	}
}
