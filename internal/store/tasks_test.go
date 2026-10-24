package store

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func minutes(value int) *int { return &value }

func writeTaskKinds(t *testing.T, store *Store, kinds ...taskKind) {
	t.Helper()
	raw, err := json.Marshal(taskKindsFile{Version: 1, Kinds: kinds})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(taskKindsPathFor(store.dataDir), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeTaskPolicy(t *testing.T, store *Store, deferMap map[string]bool, start, end string, offset int) {
	t.Helper()
	policy := map[string]any{
		"defer":  deferMap,
		"window": map[string]any{"start": start, "end": end, "utc_offset_minutes": offset},
	}
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.policyPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTaskWindowMath(t *testing.T) {
	window := taskWindow{Start: "00:30", End: "07:30", OffsetMinutes: minutes(480)}
	inside := time.Date(2026, 10, 24, 3, 0, 0, 0, time.FixedZone("x", 480*60))
	outside := time.Date(2026, 10, 24, 9, 0, 0, 0, time.FixedZone("x", 480*60))
	if !window.inWindow(inside) {
		t.Fatal("03:00 should be inside 00:30-07:30")
	}
	if window.inWindow(outside) {
		t.Fatal("09:00 should be outside 00:30-07:30")
	}
	next := window.nextStart(outside)
	want := time.Date(2026, 10, 25, 0, 30, 0, 0, time.FixedZone("x", 480*60))
	if !next.Equal(want) {
		t.Fatalf("nextStart=%v want %v", next, want)
	}
	if window.nextStart(inside) != inside {
		t.Fatal("inside the window, nextStart is now")
	}
}

func TestTaskWindowCrossesMidnight(t *testing.T) {
	window := taskWindow{Start: "22:00", End: "06:00", OffsetMinutes: minutes(480)}
	zone := time.FixedZone("x", 480*60)
	if !window.inWindow(time.Date(2026, 10, 24, 2, 0, 0, 0, zone)) {
		t.Fatal("02:00 should be inside 22:00-06:00")
	}
	if !window.inWindow(time.Date(2026, 10, 24, 23, 0, 0, 0, zone)) {
		t.Fatal("23:00 should be inside 22:00-06:00")
	}
	if window.inWindow(time.Date(2026, 10, 24, 12, 0, 0, 0, zone)) {
		t.Fatal("12:00 should be outside 22:00-06:00")
	}
}

func TestCreateTaskDefersByPriority(t *testing.T) {
	store := newTestStore(t)
	// A window two hours ahead is always outside "now" and always has a next
	// occurrence, so the assertion does not depend on the test clock.
	start := time.Now().Add(2 * time.Hour)
	writeTaskPolicy(t, store,
		map[string]bool{"P0": false, "P1": false, "P2": true, "P3": true},
		start.Format("15:04"), start.Add(time.Hour).Format("15:04"), 0)

	immediate, err := store.CreateTask("maintenance", "manual", "Now", "P1", nil)
	if err != nil {
		t.Fatalf("create P1: %v", err)
	}
	if task := immediate["task"].(map[string]any); task["not_before"] != nil {
		t.Fatalf("P1 should not be deferred: %v", task)
	}
	deferred, err := store.CreateTask("maintenance", "hook:x", "Later", "P2", nil)
	if err != nil {
		t.Fatalf("create P2: %v", err)
	}
	task := deferred["task"].(map[string]any)
	notBefore, _ := task["not_before"].(string)
	if notBefore == "" {
		t.Fatal("P2 should carry a deferral time")
	}
	at, err := time.Parse(time.RFC3339Nano, notBefore)
	if err != nil || !at.After(time.Now()) {
		t.Fatalf("not_before=%q", notBefore)
	}
}

func TestCreateTaskDeduplicatesActiveWork(t *testing.T) {
	store := newTestStore(t)
	first, err := store.CreateTask("maintenance", "hook:ctx", "Maintenance", "P0", nil)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := store.CreateTask("maintenance", "hook:ctx", "Maintenance", "P0", nil)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if second["created"] != false {
		t.Fatalf("duplicate was created: %v", second)
	}
	if first["task"].(map[string]any)["id"] != second["task"].(map[string]any)["id"] {
		t.Fatal("dedupe returned a different task")
	}
}

func TestTaskPauseAndRunNow(t *testing.T) {
	store := newTestStore(t)
	// Always inside the window, so the only gate is the pause switch.
	writeTaskPolicy(t, store, map[string]bool{"P2": true}, "00:00", "00:00", 0)
	created, err := store.CreateTask("maintenance", "hook:ctx", "Maintenance", "P2", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id := created["task"].(map[string]any)["id"].(string)

	if _, err := store.SetTasksPaused(true); err != nil {
		t.Fatalf("pause: %v", err)
	}
	result, err := store.DispatchDue()
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if result["reason"] != "paused" {
		t.Fatalf("result=%v", result)
	}

	if _, err := store.SetTasksPaused(false); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if _, err := store.UpdateTask(id, false); err != nil {
		t.Fatalf("disable task: %v", err)
	}
	if result, _ := store.DispatchDue(); result["dispatched"] != 0 {
		t.Fatalf("paused task was dispatched: %v", result)
	}

	if _, err := store.RunTaskNow(id); err != nil {
		t.Fatalf("run now: %v", err)
	}
	listing, err := store.ListTasks()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	task := listing["tasks"].([]any)[0].(map[string]any)
	if task["enabled"] != true || task["due"] != true {
		t.Fatalf("task after run-now=%v", task)
	}
}

func TestDispatchDuePicksHighestPriority(t *testing.T) {
	store := newTestStore(t)
	writeTaskPolicy(t, store, map[string]bool{"P0": false, "P3": true}, "00:00", "00:00", 0)
	if _, err := store.CreateTask("maintenance", "hook:batch", "Batch", "P3", nil); err != nil {
		t.Fatalf("create P3: %v", err)
	}
	if _, err := store.CreateTask("maintenance", "hook:urgent", "Urgent", "P0", nil); err != nil {
		t.Fatalf("create P0: %v", err)
	}
	result, err := store.DispatchDue()
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if result["dispatched"] != 1 {
		t.Fatalf("result=%v", result)
	}
	listing, _ := store.ListTasks()
	for _, item := range listing["tasks"].([]any) {
		task := item.(map[string]any)
		if task["priority"] == "P0" && task["attempts"] != 1 {
			t.Fatalf("P0 task was not picked first: %v", task)
		}
		if task["priority"] == "P3" && task["attempts"] != 0 {
			t.Fatalf("P3 task ran before P0: %v", task)
		}
	}
}

func TestUnknownTaskKindIsRejectedAndFailsWithoutLooping(t *testing.T) {
	store := newTestStore(t)
	writeTaskPolicy(t, store, map[string]bool{}, "00:00", "00:00", 0)
	if _, err := store.CreateTask("issue-triage", "manual", "Triage", "P1", nil); err == nil {
		t.Fatal("a kind that is not configured was accepted")
	}
	if _, err := store.CreateTask("maintenance", "manual", "Clean up", "P1", nil); err != nil {
		t.Fatalf("create: %v", err)
	}
	// The owner removes the kind between queueing and dispatch. The task must
	// fail once with a clear reason instead of spinning or guessing an action.
	writeTaskKinds(t, store, taskKind{ID: "triage", Label: "Issue triage", Action: kindActionReport, Prompt: "Summarize {{payload.title}}"})
	result, err := store.DispatchDue()
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if result["reason"] != "kind_unknown" {
		t.Fatalf("result=%v", result)
	}
	listing, _ := store.ListTasks()
	task := listing["tasks"].([]any)[0].(map[string]any)
	if task["status"] != "failed" || task["reason"] != "kind_unknown" {
		t.Fatalf("task=%v", task)
	}
}

func TestTasksArePrunedAndInterruptedWorkIsRecovered(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tasks := tasksFile{Version: 1, Tasks: []taskRecord{{
		ID: "t-000000000001", Kind: "maintenance", Title: "stuck", Priority: "P2",
		Source: "manual", Status: taskStatusRunning, Enabled: true,
		CreatedAt: now, UpdatedAt: now,
	}}}
	raw, _ := json.Marshal(tasks)
	if err := os.WriteFile(store.tasksPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	store.tasksCache = nil
	store.recoverInterruptedTasks()
	listing, err := store.ListTasks()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	task := listing["tasks"].([]any)[0].(map[string]any)
	if task["status"] != "failed" || task["reason"] != "interrupted" {
		t.Fatalf("recovered task=%v", task)
	}
}

func TestCreateTaskProjectsPluginPayload(t *testing.T) {
	store := newTestStore(t)
	created, err := store.CreateTask("maintenance", "hook:issues", "Issue 12", "P0", map[string]any{
		"repo":   "owner/name",
		"issue":  float64(12),
		"nested": map[string]any{"inside": "drop me"},
		"list":   []any{"drop", "me"},
		"big":    strings.Repeat("x", 600),
		"token":  "must-not-be-stored",
		"Title":  "upper keys are dropped",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	payload, ok := created["task"].(map[string]any)["payload"].(map[string]any)
	if !ok {
		t.Fatalf("payload missing: %v", created["task"])
	}
	if payload["repo"] != "owner/name" || payload["issue"] != float64(12) {
		t.Fatalf("payload=%v", payload)
	}
	for _, key := range []string{"nested", "list", "big", "token", "Title"} {
		if _, present := payload[key]; present {
			t.Fatalf("payload kept %q: %v", key, payload)
		}
	}
}
