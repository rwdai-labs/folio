package store

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCapabilitiesAreConfigurationReadinessAndDoNotRequireRepository(t *testing.T) {
	store := newTestStore(t)
	store.SetAgentRuntime(&fakeAgentRuntime{available: true})
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "codex"), "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", bin)

	state, err := store.State()
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	capabilities := state["capabilities"].(map[string]any)
	check := func(name string, available bool, reason string) {
		t.Helper()
		card := capabilities[name].(map[string]any)
		if card["available"] != available || card["reason"] != reason || card["verification"] != "unknown" {
			t.Fatalf("%s=%v", name, card)
		}
	}
	check("assistant", true, "configured")
	check("report", true, "configured")
	// Chat and report remain usable without a repository. Maintenance and
	// context do not: no credential was tested, so verification stays unknown.
	check("maintenance", false, "repository_unavailable")
	check("context", false, "repository_unavailable")
	if state["repository"].(map[string]any)["connected"] != false {
		t.Fatalf("repository=%v", state["repository"])
	}
}

func TestReportResultUsesRunOwnedBoundedContent(t *testing.T) {
	store := newTestStore(t)
	runID := "20261109T000000.000000000Z-abcdef12"
	writeRun := func(id, status, taskID, name string) {
		t.Helper()
		run := map[string]any{"version": 1, "run_id": id, "status": status, "report_file": name}
		if taskID != "" {
			run["task_id"] = taskID
		}
		if err := store.saveAutomationRun(run); err != nil {
			t.Fatal(err)
		}
	}
	if err := writePrivateFile(filepath.Join(store.reportsDir, "run-"+runID+".md"), []byte("# Result\n\nCited fact.\n")); err != nil {
		t.Fatal(err)
	}
	writeRun(runID, "reported", "t-000000000001", "run-"+runID+".md")
	value, err := store.ReportResult(runID)
	if err != nil {
		t.Fatalf("result: %v", err)
	}
	if value["text"] != "# Result\n\nCited fact." || value["format"] != "markdown" || value["task_id"] != "t-000000000001" {
		t.Fatalf("value=%v", value)
	}

	// Historical mutable files may contain another attempt. Never attribute
	// those bytes to an earlier run; read only that run's audit snapshot.
	oldID := "20261109T000001.000000000Z-abcdef13"
	if err := writePrivateFile(filepath.Join(store.reportsDir, "task-t-000000000002.md"), []byte("later retry result\n")); err != nil {
		t.Fatal(err)
	}
	writeRun(oldID, "reported", "t-000000000002", "task-t-000000000002.md")
	if _, err := store.ReportResult(oldID); err == nil {
		t.Fatal("legacy mutable artifact returned without a snapshot")
	}
	if err := store.saveAutomationRun(map[string]any{"run_id": oldID, "status": "reported", "task_id": "t-000000000002", "report_file": "task-t-000000000002.md", "report_text": "original attempt snapshot"}); err != nil {
		t.Fatal(err)
	}
	old, err := store.ReportResult(oldID)
	if err != nil || old["text"] != "original attempt snapshot" || old["historical_snapshot"] != true || old["truncated"] != true {
		t.Fatalf("legacy result=%v err=%v", old, err)
	}
	pipelineID := "20261109T000002.000000000Z-abcdef14"
	if err := store.saveAutomationRun(map[string]any{"run_id": pipelineID, "status": "completed", "task_id": "t-000000000003", "report_file": "action-t-000000000003.md", "report_text": "pipeline snapshot"}); err != nil {
		t.Fatal(err)
	}
	pipeline, err := store.ReportResult(pipelineID)
	if err != nil || pipeline["text"] != "pipeline snapshot" || pipeline["historical_snapshot"] != true {
		t.Fatalf("pipeline result=%v err=%v", pipeline, err)
	}
}

func TestReportResultRejectsUnsafeOrForeignContent(t *testing.T) {
	store := newTestStore(t)
	runID := "20261109T000000.000000000Z-abcdef12"
	otherID := "20261109T000001.000000000Z-abcdef13"
	if err := writePrivateFile(filepath.Join(store.reportsDir, "run-"+otherID+".md"), []byte("another run\n")); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		file string
	}{
		{"path escape", "../outside.md"},
		{"foreign run", "run-" + otherID + ".md"},
		{"nested path", "sub/report.md"},
		{"absolute path", "/etc/passwd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := runID
			run := map[string]any{"version": 1, "run_id": id, "status": "reported", "task_id": "t-000000000001", "report_file": tc.file}
			if err := store.saveAutomationRun(run); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ReportResult(id); err == nil || err.Error() != "report_result_unavailable" {
				t.Fatalf("unsafe result accepted: %v", err)
			}
		})
	}

	// A symlinked report and a report containing credential-shaped material
	// fail closed even when the run record names the expected file.
	symlinkID := "20261109T000002.000000000Z-abcdef14"
	target := filepath.Join(t.TempDir(), "target.md")
	if err := os.WriteFile(target, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(store.reportsDir, "run-"+symlinkID+".md")); err != nil {
		t.Fatal(err)
	}
	if err := store.saveAutomationRun(map[string]any{"version": 1, "run_id": symlinkID, "status": "reported", "report_file": "run-" + symlinkID + ".md"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReportResult(symlinkID); err == nil {
		t.Fatal("symlinked report accepted")
	}
	secretID := "20261109T000003.000000000Z-abcdef15"
	if err := writePrivateFile(filepath.Join(store.reportsDir, "run-"+secretID+".md"), []byte("token: OPENAI_API_KEY=sk-1234567890abcdefghijkl\n")); err != nil {
		t.Fatal(err)
	}
	if err := store.saveAutomationRun(map[string]any{"version": 1, "run_id": secretID, "status": "reported", "report_file": "run-" + secretID + ".md"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReportResult(secretID); err == nil {
		t.Fatal("secret-shaped report accepted")
	}
}

func TestLegacyKindsGainGenericReportWithoutBeingRewritten(t *testing.T) {
	store := newTestStore(t)
	writeTaskKinds(t, store, taskKind{ID: "maintenance", Label: "Context maintenance", Action: kindActionMaintenance})
	path := taskKindsPathFor(store.dataDir)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	kinds, err := store.loadTaskKinds()
	if err != nil {
		t.Fatalf("kinds: %v", err)
	}
	if len(kinds.Kinds) != 2 || kinds.Kinds[1].ID != "report" || kinds.Kinds[1].Priority != "P1" {
		t.Fatalf("kinds=%v", kinds.Kinds)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("legacy kinds file was rewritten: %q", after)
	}
}

func TestReportTaskDeduplicationKeepsDistinctRequests(t *testing.T) {
	store := newTestStore(t)
	first, err := store.CreateTask("report", "agent:console-main", "Summarize release", "P1", map[string]any{"request": "first"})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := store.CreateTask("report", "agent:console-main", "Summarize incident", "P1", map[string]any{"request": "second"})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first["created"] != true || second["created"] != true {
		t.Fatalf("distinct report requests were coalesced: first=%v second=%v", first, second)
	}
	duplicate, err := store.CreateTask("report", "agent:console-main", "Summarize release", "P1", map[string]any{"request": "first"})
	if err != nil {
		t.Fatalf("duplicate: %v", err)
	}
	if duplicate["created"] != false {
		t.Fatalf("identical report request was duplicated: %v", duplicate)
	}
	listing, _ := store.ListTasks()
	if len(listing["tasks"].([]any)) != 2 {
		t.Fatalf("tasks=%v", listing["tasks"])
	}
}

func TestOwnerReportRequestBoundPreservesIntentAndHookProjection(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.CreateTask("report", "manual", "Missing request", "P1", nil); err == nil || err.Error() != "report_request_invalid" {
		t.Fatalf("missing generic request error=%v", err)
	}
	request := strings.Repeat("界", maxReportRequestRunes)
	created, err := store.CreateTask("report", "manual", "Long request", "P1", map[string]any{"request": request})
	if err != nil {
		t.Fatal(err)
	}
	if created["task"].(map[string]any)["payload"].(map[string]any)["request"] != request {
		t.Fatal("owner request was dropped or truncated")
	}
	store.taskMu.Lock()
	store.tasksCache = nil // Force validation of durable record after restart.
	file, err := store.loadTasksLocked()
	store.taskMu.Unlock()
	if err != nil || len(file.Tasks) != 1 || file.Tasks[0].Payload["request"] != request {
		t.Fatalf("durable request lost: err=%v", err)
	}
	snapshot := snapshotTaskPayload(file.Tasks[0])
	prompt := renderKindPrompt(genericReportKind(), taskRecord{Title: "Long request", Payload: snapshot})
	if !strings.Contains(prompt, request) {
		t.Fatal("dispatch/prompt truncated owner request")
	}
	if _, err := store.CreateTask("report", "manual", "Too long", "P1", map[string]any{"request": request + "x"}); err == nil || err.Error() != "report_request_invalid" {
		t.Fatalf("oversized request error=%v", err)
	}
	if _, err := store.CreateTask("report", "agent:console-main", "Wrong type", "P1", map[string]any{"request": 7}); err == nil || err.Error() != "report_request_invalid" {
		t.Fatalf("wrong type request error=%v", err)
	}
	hook, err := store.CreateTask("report", "hook:synthetic", "Hook report", "P1", map[string]any{"request": request})
	if err != nil {
		t.Fatal(err)
	}
	if payload, _ := hook["task"].(map[string]any)["payload"].(map[string]any); payload["request"] != nil {
		t.Fatal("hook projection admitted owner-only large request")
	}
}

func TestStandaloneBoundReportDispatchAndFollowup(t *testing.T) {
	store := newTestStore(t)
	t.Setenv("PATH", t.TempDir()) // No CLI runner and no context repository.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model":"synthetic","choices":[{"message":{"content":"# Summary\n\nA synthetic result with no external verification."},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()
	bindSyntheticService(t, store, upstream.URL, map[string]any{"report": syntheticBinding("synthetic")}, nil)
	state, err := store.State()
	if err != nil || state["capabilities"].(map[string]any)["report"].(map[string]any)["available"] != true {
		t.Fatalf("report unavailable without repository/CLI: state=%v err=%v", state, err)
	}
	created, err := store.CreateTask("report", "agent:console-main", "Synthetic report", "", map[string]any{"request": "Summarize synthetic information"})
	if err != nil {
		t.Fatal(err)
	}
	if created["task"].(map[string]any)["priority"] != "P1" {
		t.Fatal("generic report did not default to P1")
	}
	dispatched, err := store.DispatchDue()
	if err != nil || dispatched["run_status"] != "reported" {
		t.Fatalf("dispatch=%v err=%v", dispatched, err)
	}
	runID := dispatched["run_id"].(string)
	result, err := store.ReportResult(runID)
	if err != nil || !strings.Contains(result["text"].(string), "synthetic result") {
		t.Fatalf("result=%v err=%v", result, err)
	}
	followup, err := store.runAgentTool(context.Background(), AgentToolCall{Name: "report.get", Arguments: map[string]any{"id": runID}}, eventTrustOwner, "console-main")
	if err != nil || followup["ok"] != true || followup["value"].(map[string]any)["run_id"] != runID {
		t.Fatalf("followup=%v err=%v", followup, err)
	}
	denied, _ := store.runAgentTool(context.Background(), AgentToolCall{Name: "report.get", Arguments: map[string]any{"id": runID}}, eventTrustIntegration, "observer")
	if denied["error"] != "tool_not_granted" {
		t.Fatalf("observer read report: %v", denied)
	}
}

func TestAgentTaskEventAndHistoryAssociation(t *testing.T) {
	store := newTestStore(t)
	runtime := &fakeAgentRuntime{available: true, responses: []AgentResponse{
		{ToolCalls: []AgentToolCall{{Name: "task.create", Arguments: map[string]any{
			"action": "report", "title": "Release summary", "payload": map[string]any{"request": "summarize 1.0"},
		}}}},
		{Text: "Queued the report."},
	}}
	store.SetAgentRuntime(runtime)
	var created AgentEvent
	_, err := store.AgentTurn(context.Background(), "console-main", "summarize 1.0", eventTrustOwner, func(event AgentEvent) {
		if event.Type == "task.created" {
			created = event
		}
	})
	if err != nil {
		t.Fatalf("turn: %v", err)
	}
	if !taskIDRE.MatchString(created.TaskID) || created.Title != "Release summary" || created.Status != "queued" {
		t.Fatalf("task event=%+v map=%v", created, created.Map())
	}
	history, err := store.AgentHistory("console-main")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	tasks := history["tasks"].([]any)
	if len(tasks) != 1 || tasks[0].(map[string]any)["task_id"] != created.TaskID || tasks[0].(map[string]any)["title"] != "Release summary" {
		t.Fatalf("history tasks=%v", tasks)
	}
	if _, err := store.AgentHistory("console-other"); err != nil {
		t.Fatalf("other history: %v", err)
	}
	if !strings.Contains(renderKindPrompt(genericReportKind(), taskRecord{Title: "Release summary", Payload: map[string]any{"request": "summarize 1.0"}}), "summarize 1.0") {
		t.Fatal("generic report prompt did not carry the user request")
	}
}
