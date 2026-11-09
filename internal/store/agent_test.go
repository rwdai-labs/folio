package store

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

func TestAgentConfigPersistsWithoutReturningKey(t *testing.T) {
	store := newTestStore(t)
	config, err := store.PutAgentConfig(map[string]any{
		"kind": "openai", "provider": "deepseek",
		"base_url": "https://api.deepseek.com/v1", "model": "deepseek-chat",
		"api_key": "synthetic-key", "timeout_seconds": float64(45),
	})
	if err != nil {
		t.Fatalf("put config: %v", err)
	}
	if config["available"] != true || config["key_present"] != true || config["model"] != "deepseek-chat" {
		t.Fatalf("config=%v", config)
	}
	if _, leaked := config["api_key"]; leaked {
		t.Fatalf("config returned the key: %v", config)
	}
	raw, err := os.ReadFile(filepath.Join(store.dataDir, "agent", "config.json"))
	if err != nil || strings.Contains(string(raw), "synthetic-key") {
		t.Fatalf("config file=%q err=%v", raw, err)
	}
	key, err := os.ReadFile(filepath.Join(store.dataDir, "secrets", "agent-api-key"))
	if err != nil || strings.TrimSpace(string(key)) != "synthetic-key" {
		t.Fatalf("key file=%q err=%v", key, err)
	}
	if _, err := store.PutAgentConfig(map[string]any{
		"kind": "openai", "base_url": "http://example.com/v1", "model": "x",
	}); err == nil {
		t.Fatal("plain HTTP base URL was accepted")
	}
	// A later start must load the same file and remain available.
	reloaded, err := New(store.dataDir, store.toolRoot, store.remote)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !reloaded.AgentAvailable() || reloaded.AgentConfig()["key_present"] != true {
		t.Fatalf("reloaded config=%v", reloaded.AgentConfig())
	}
}

func TestOpenAIAgentRuntimeToolLoop(t *testing.T) {
	store := newTestStore(t)
	writeTaskKinds(t, store, taskKind{ID: "maintenance", Label: "Context maintenance", Action: kindActionMaintenance})
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer synthetic-key" {
			t.Errorf("request path=%q auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		requests = append(requests, body)
		w.Header().Set("Content-Type", "application/json")
		if len(requests) == 1 {
			fmt.Fprint(w, `{"choices":[{"message":{"content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"task.create","arguments":"{\"action\":\"maintenance\",\"title\":\"From HTTP\"}"}}]}}],"usage":{"prompt_tokens":100,"completion_tokens":10,"total_tokens":110,"prompt_tokens_details":{"cached_tokens":20}}}`)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"Queued it."}}],"usage":{"prompt_tokens":120,"completion_tokens":5,"total_tokens":125}}`)
	}))
	defer server.Close()

	cfg := &agentConfig{
		Version: 1, Kind: "openai", BaseURL: server.URL + "/v1", Model: "synthetic-model",
		TimeoutSeconds: 30, MaxOutputTokens: 256, Provider: "deepseek",
	}
	runtime := newOpenAIAgentRuntime(cfg, "synthetic-key")
	runtime.client = server.Client()
	store.SetAgentRuntime(runtime)
	result, err := store.AgentTurn(context.Background(), "console-main", "queue a task", eventTrustOwner, nil)
	if err != nil {
		t.Fatalf("turn: %v", err)
	}
	if result.Text != "Queued it." || result.Usage == nil || result.Usage["total_tokens"] != int64(235) {
		t.Fatalf("result=%+v", result)
	}
	if len(requests) != 2 {
		t.Fatalf("requests=%d", len(requests))
	}
	messages, _ := requests[1]["messages"].([]any)
	if len(messages) < 4 {
		t.Fatalf("second request messages=%v", messages)
	}
	assistant, _ := messages[len(messages)-2].(map[string]any)
	tool, _ := messages[len(messages)-1].(map[string]any)
	if assistant["role"] != "assistant" || len(assistant["tool_calls"].([]any)) != 1 {
		t.Fatalf("assistant message=%v", assistant)
	}
	if tool["role"] != "tool" || tool["tool_call_id"] != "call_1" {
		t.Fatalf("tool message=%v", tool)
	}
	tasks, err := store.ListTasks()
	if err != nil || len(tasks["tasks"].([]any)) != 1 {
		t.Fatalf("tasks=%v err=%v", tasks, err)
	}
}

func TestAgentTaskCreateCannotBypassOwnerLaunchValidation(t *testing.T) {
	store := newTestStore(t)
	if err := store.PutAction(ownerSelectDefinition()); err != nil {
		t.Fatal(err)
	}
	runtime := &fakeAgentRuntime{available: true, responses: []AgentResponse{
		{ToolCalls: []AgentToolCall{{Name: "task.create", Arguments: map[string]any{
			"action": "owner-select",
			"title":  "Bypass",
			"payload": map[string]any{
				"mode": "evil",
			},
		}}}},
		{Text: "Rejected."},
	}}
	store.SetAgentRuntime(runtime)
	var events []AgentEvent
	result, err := store.AgentTurn(context.Background(), "console-main", "queue an invalid action", eventTrustOwner, func(event AgentEvent) {
		events = append(events, event)
	})
	if err != nil || result.Text != "Rejected." {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	tasks, err := store.ListTasks()
	if err != nil {
		t.Fatal(err)
	}
	if list := tasks["tasks"].([]any); len(list) != 0 {
		t.Fatalf("invalid assistant task was queued: %v", list)
	}
	run, err := store.AutomationRun(result.RunID)
	if err != nil {
		t.Fatal(err)
	}
	calls := run["tool_calls"].([]any)
	if len(calls) != 1 || calls[0].(map[string]any)["status"] != "failed" {
		t.Fatalf("invalid assistant task was audited as success: %v", calls)
	}
	failed := false
	for _, event := range events {
		if event.Type == "tool.result" && event.Status == "failed" && event.Summary == "action_input_invalid" {
			failed = true
		}
	}
	if !failed {
		t.Fatalf("assistant did not receive the launch validation error: %v", events)
	}
}
