package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeAgentRuntime struct {
	available bool
	responses []AgentResponse
	requests  []AgentRequest
}

func (f *fakeAgentRuntime) Available() bool { return f.available }

func (f *fakeAgentRuntime) Turn(_ context.Context, request AgentRequest) (AgentResponse, error) {
	f.requests = append(f.requests, request)
	if len(f.responses) == 0 {
		return AgentResponse{Text: "done"}, nil
	}
	response := f.responses[0]
	f.responses = f.responses[1:]
	return response, nil
}

func TestAgentTurnRunsToolAndAudits(t *testing.T) {
	store := newTestStore(t)
	writeTaskKinds(t, store, taskKind{ID: "maintenance", Label: "Context maintenance", Action: kindActionMaintenance})
	runtime := &fakeAgentRuntime{available: true, responses: []AgentResponse{
		{ToolCalls: []AgentToolCall{{Name: "task.create", Arguments: map[string]any{
			"action": "maintenance", "title": "From agent",
		}}}},
		{Text: "Queued it."},
	}}
	store.SetAgentRuntime(runtime)
	var events []AgentEvent
	result, err := store.AgentTurn(context.Background(), "console-main", "queue a task", eventTrustOwner, func(event AgentEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("turn: %v", err)
	}
	if result.Text != "Queued it." || result.RunID == "" {
		t.Fatalf("result=%+v", result)
	}
	if len(events) < 4 {
		t.Fatalf("events=%v", events)
	}
	tasks, err := store.ListTasks()
	if err != nil {
		t.Fatalf("tasks: %v", err)
	}
	list := tasks["tasks"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["source"] != "agent:console-main" {
		t.Fatalf("tasks=%v", tasks)
	}
	run, err := store.AutomationRun(result.RunID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if run["status"] != "reported" || run["trigger"] != "agent" || run["session"] != "console-main" {
		t.Fatalf("run=%v", run)
	}
	if calls := run["tool_calls"].([]any); len(calls) != 1 || calls[0].(map[string]any)["tool"] != "task.create" {
		t.Fatalf("tool audit=%v", run["tool_calls"])
	}
	history, err := store.AgentHistory("console-main")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	messages := history["messages"].([]any)
	if len(messages) < 3 || messages[len(messages)-1].(map[string]any)["content"] != "Queued it." {
		t.Fatalf("history=%v", history)
	}
	if len(runtime.requests[0].Tools) == 0 {
		t.Fatal("runtime received no tool specs")
	}
}

func TestAgentObserverCannotWrite(t *testing.T) {
	store := newTestStore(t)
	runtime := &fakeAgentRuntime{available: true, responses: []AgentResponse{
		{ToolCalls: []AgentToolCall{{Name: "action.put", Arguments: map[string]any{
			"action": map[string]any{"id": "injected", "label": "Injected"},
		}}}},
		{Text: "Not allowed."},
	}}
	store.SetAgentRuntime(runtime)
	result, err := store.AgentTurn(context.Background(), "console-main", "write an action", eventTrustIntegration, nil)
	if err != nil || result.Text != "Not allowed." {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if _, err := os.Stat(pipelinePathFor(store.dataDir)); !os.IsNotExist(err) {
		t.Fatal("observer wrote an action")
	}
	run, err := store.AutomationRun(result.RunID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	calls := run["tool_calls"].([]any)
	if calls[0].(map[string]any)["status"] != "failed" {
		t.Fatalf("denied tool was audited as success: %v", calls)
	}
}

func TestCommandAgentRuntimeBoundary(t *testing.T) {
	store := newTestStore(t)
	dir := filepath.Join(store.dataDir, "agent")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	runtimePath := filepath.Join(dir, "runtime")
	writeExecutable(t, runtimePath, `#!/bin/sh
cat >/dev/null
printf '%s' '{"text":"from command"}'
`)
	store.SetAgentRuntime(&commandAgentRuntime{store: store, path: runtimePath})
	if !store.AgentAvailable() {
		t.Fatal("command runtime was not available")
	}
	result, err := store.AgentTurn(context.Background(), "console-main", "hello", eventTrustOwner, nil)
	if err != nil || result.Text != "from command" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestAgentUnavailableFailsClosed(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.AgentTurn(context.Background(), "console-main", "hello", eventTrustOwner, nil); err == nil || err.Error() != "agent_unavailable" {
		t.Fatalf("unavailable runtime error=%v", err)
	}
	if _, err := store.AgentTurn(context.Background(), "bad session!", "hello", eventTrustOwner, nil); err == nil {
		t.Fatal("invalid session was accepted")
	}
}

func TestAgentToolResultErrorMarksStepFailed(t *testing.T) {
	store := newTestStore(t)
	runtime := &fakeAgentRuntime{available: true, responses: []AgentResponse{
		{ToolCalls: []AgentToolCall{{Name: "task.create", Arguments: map[string]any{"action": "missing"}}}},
		{Text: "Could not queue."},
	}}
	store.SetAgentRuntime(runtime)
	result, err := store.AgentTurn(context.Background(), "console-main", "queue", eventTrustOwner, nil)
	if err != nil {
		t.Fatalf("turn: %v", err)
	}
	run, _ := store.AutomationRun(result.RunID)
	calls := run["tool_calls"].([]any)
	if calls[0].(map[string]any)["status"] != "failed" {
		t.Fatalf("failed tool was audited as success: %v", calls)
	}
}

func TestAgentRejectsOversizedRuntimeText(t *testing.T) {
	store := newTestStore(t)
	runtime := &fakeAgentRuntime{available: true, responses: []AgentResponse{{Text: strings.Repeat("x", maxAgentOutput+1)}}}
	store.SetAgentRuntime(runtime)
	result, err := store.AgentTurn(context.Background(), "console-main", "hello", eventTrustOwner, nil)
	if err != nil || result.Text != "" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	run, _ := store.AutomationRun(result.RunID)
	if run["status"] != "failed" || run["reason"] != "agent_output_too_large" {
		t.Fatalf("run=%v", run)
	}
}
