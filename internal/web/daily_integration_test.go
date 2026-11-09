package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	contextrepo "folio/internal/context"
	"folio/internal/store"
)

// Exercise the real HTTP/store/dispatcher boundary, without a checkout, CLI,
// private data, or an external model. Browser mocks cannot verify this contract.
func TestDailyReportHTTPIntegration(t *testing.T) {
	data, tools := t.TempDir(), t.TempDir()
	backend, err := store.New(data, tools, nil)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer synthetic-daily-key" {
			t.Error("unexpected model route or auth")
		}
		var request map[string]any
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("invalid model request")
		}
		if request["model"] != "group/daily" || request["tools"] != nil {
			t.Error("report did not use tool-free binding")
		}
		raw, _ := json.Marshal(request)
		if !strings.Contains(string(raw), "Synthetic owner request") {
			t.Error("owner request lost before model")
		}
		text := fmt.Sprintf("# Report\n\nSynthetic attempt %d.", calls.Add(1))
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": text}, "finish_reason": "stop"}}})
	}))
	defer gateway.Close()
	_, err = backend.PutModelServices(map[string]any{"version": 1, "connections": map[string]any{"daily": map[string]any{"name": "Synthetic", "base_url": gateway.URL + "/v1", "api_key": "synthetic-daily-key"}}, "defaults": map[string]any{"report": map[string]any{"connection": "daily", "model": "group/daily"}}, "overrides": map[string]any{"report": map[string]any{}}})
	if err != nil {
		t.Fatal(err)
	}
	passwordFile := filepath.Join(data, "synthetic-password")
	if err := os.WriteFile(passwordFile, []byte("correct horse"), 0600); err != nil {
		t.Fatal(err)
	}
	handler, err := New(backend, Options{DataDir: data, PasswordFile: passwordFile, AssetsDir: tools, TrustedHosts: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer handler.auth.db.Close()
	server := httptest.NewServer(handler)
	defer server.Close()
	client := &testClient{server: server}
	body := map[string]any{"kind": "report", "title": "Synthetic report", "priority": "P1", "source": "agent:forged", "payload": map[string]any{"request": "Synthetic owner request"}}
	status, _, _ := client.request(t, http.MethodPost, "/api/tasks", body, server.URL)
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated create: %d", status)
	}
	client.login(t, "correct horse")
	csrf := client.csrf
	client.csrf = ""
	status, _, _ = client.request(t, http.MethodPost, "/api/tasks", body, server.URL)
	if status != http.StatusForbidden {
		t.Fatalf("missing CSRF create: %d", status)
	}
	client.csrf = csrf
	status, _, state := client.request(t, http.MethodGet, "/api/state", nil, "")
	if status != 200 || state["repository"].(map[string]any)["connected"] != false || state["capabilities"].(map[string]any)["report"].(map[string]any)["available"] != true {
		t.Fatalf("standalone readiness: %d %v", status, state)
	}
	status, _, invalid := client.request(t, http.MethodPost, "/api/tasks", map[string]any{"kind": "report", "payload": map[string]any{"request": strings.Repeat("x", 4001)}}, server.URL)
	if status != 400 || invalid["error"] != "report_request_invalid" {
		t.Fatalf("oversized owner intent: %d %v", status, invalid)
	}
	status, _, created := client.request(t, http.MethodPost, "/api/tasks", body, server.URL)
	if status != http.StatusCreated || created["created"] != true {
		t.Fatalf("create: %d %v", status, created)
	}
	task := created["task"].(map[string]any)
	if task["source"] != "manual" || task["status"] != "queued" {
		t.Fatalf("server-owned task metadata: %v", task)
	}
	taskID := task["id"].(string)
	status, _, duplicate := client.request(t, http.MethodPost, "/api/tasks", body, server.URL)
	if status != 200 || duplicate["created"] != false || duplicate["task"].(map[string]any)["id"] != taskID {
		t.Fatalf("deduplication: %d %v", status, duplicate)
	}
	var runIDs []string
	for attempt := 1; attempt <= 2; attempt++ {
		if attempt > 1 {
			status, _, result := client.request(t, http.MethodPost, "/api/tasks/"+taskID+"/run", map[string]any{}, server.URL)
			if status != 200 {
				t.Fatalf("retry: %d %v", status, result)
			}
		}
		status, _, dispatched := client.request(t, http.MethodPost, "/api/tasks/dispatch", map[string]any{}, server.URL)
		if status != 200 || dispatched["run_status"] != "reported" {
			t.Fatalf("dispatch: %d %v", status, dispatched)
		}
		runID := dispatched["run_id"].(string)
		runIDs = append(runIDs, runID)
		status, _, result := client.request(t, http.MethodGet, "/api/automation/runs/"+runID+"/result", nil, "")
		if status != 200 || result["task_id"] != taskID || result["run_id"] != runID || !strings.Contains(result["text"].(string), fmt.Sprintf("attempt %d", attempt)) {
			t.Fatalf("result: %d %v", status, result)
		}
	}
	if runIDs[0] == runIDs[1] {
		t.Fatal("retry reused run ID")
	}
	// Reopening a real store retains both artifacts, and old run IDs never alias
	// the retry's result. No fake HTTP Backend participates in these reads.
	reopened, err := store.New(data, tools, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler.store = reopened
	status, _, first := client.request(t, http.MethodGet, "/api/automation/runs/"+runIDs[0]+"/result", nil, "")
	if status != 200 || !strings.Contains(first["text"].(string), "attempt 1") {
		t.Fatalf("immutable result after restart: %d %v", status, first)
	}
	signedOut := &testClient{server: server}
	status, _, _ = signedOut.request(t, http.MethodGet, "/api/automation/runs/"+runIDs[0]+"/result", nil, "")
	if status != 401 {
		t.Fatalf("private result unauthenticated: %d", status)
	}
	if calls.Load() != 2 {
		t.Fatalf("unexpected model calls: %d", calls.Load())
	}
}

// The subprocess runs Folio's actual deterministic router. It is selected by
// explicit test arguments rather than an environment flag that the executor
// would need to forward across its scrubbed child boundary.
func TestDailyContextCLIHelper(t *testing.T) {
	for i, arg := range os.Args {
		if arg == "--" && i+1 < len(os.Args) && os.Args[i+1] == "build" {
			os.Exit(contextrepo.Run(os.Args[i+1:], os.Stdout, os.Stderr))
		}
	}
}

func TestDailyOwnContextHTTPIntegration(t *testing.T) {
	data, tools := t.TempDir(), t.TempDir()
	backend, err := store.New(data, tools, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Only synthetic reference data enters the checkout, never a real volume.
	repository := filepath.Join(data, "repository")
	for _, dir := range []string{".git", "core"} {
		if err := os.MkdirAll(filepath.Join(repository, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	source := "---\nlayer: L0\nscope: [synthetic]\n---\nThe synthetic owner's preferred writing format is cedar notes.\n"
	if err := os.WriteFile(filepath.Join(repository, "core", "preferences.md"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// A fixed shell launcher only invokes the current test binary; quoted paths
	// preserve arguments even when a temporary directory contains spaces.
	quoted := "'" + strings.ReplaceAll(binary, "'", "'\"'\"'") + "'"
	launcher := "#!/bin/sh\nexec " + quoted + " -test.run='^TestDailyContextCLIHelper$' -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(tools, "folio"), []byte(launcher), 0700); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Role       string `json:"role"`
				Content    string `json:"content"`
				ToolCallID string `json:"tool_call_id"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if calls.Add(1) == 1 {
			_, _ = io.WriteString(w, `{"choices":[{"message":{"tool_calls":[{"id":"source-call","type":"function","function":{"name":"context.search","arguments":"{\"query\":\"synthetic preferred writing format\"}"}}]},"finish_reason":"tool_calls"}]}`)
			return
		}
		evidence := false
		for _, message := range request.Messages {
			if message.Role == "tool" && message.ToolCallID == "source-call" {
				var result struct {
					OK    bool `json:"ok"`
					Value struct {
						Context string `json:"context"`
					} `json:"value"`
				}
				if json.Unmarshal([]byte(message.Content), &result) != nil || !result.OK {
					t.Error("context tool failed")
				}
				evidence = strings.Contains(result.Value.Context, "core/preferences.md") && strings.Contains(result.Value.Context, "cedar notes")
			}
		}
		if !evidence {
			t.Error("gateway did not receive actual source path and matching reference content")
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"Your preferred writing format is cedar notes, according to core/preferences.md. This is retrieved repository evidence."},"finish_reason":"stop"}]}`)
	}))
	defer gateway.Close()
	_, err = backend.PutModelServices(map[string]any{"version": 1, "connections": map[string]any{"daily": map[string]any{"name": "Synthetic", "base_url": gateway.URL + "/v1", "api_key": "synthetic-context-key"}}, "defaults": map[string]any{"assistant": map[string]any{"connection": "daily", "model": "group/assistant"}}, "overrides": map[string]any{"report": map[string]any{}}})
	if err != nil {
		t.Fatal(err)
	}
	passwordFile := filepath.Join(data, "synthetic-password")
	if err := os.WriteFile(passwordFile, []byte("correct horse"), 0600); err != nil {
		t.Fatal(err)
	}
	handler, err := New(backend, Options{DataDir: data, PasswordFile: passwordFile, AssetsDir: tools, TrustedHosts: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer handler.auth.db.Close()
	server := httptest.NewServer(handler)
	defer server.Close()
	client := &testClient{server: server}
	client.login(t, "correct horse")
	payload := []byte(`{"session":"synthetic-context","text":"What writing format do I prefer? Cite retrieved source evidence."}`)
	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/agent/turns", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", server.URL)
	req.Header.Set("Cookie", client.cookie)
	req.Header.Set("X-CSRF-Token", client.csrf)
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	stream, err := io.ReadAll(io.LimitReader(response.Body, 128*1024))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || !strings.Contains(string(stream), "core/preferences.md") || !strings.Contains(string(stream), "cedar notes") || !strings.Contains(string(stream), `"type":"tool.result"`) || calls.Load() != 2 {
		t.Fatalf("source-aware turn failed: status=%d calls=%d stream=%s", response.StatusCode, calls.Load(), stream)
	}
	// --no-record prevents retrieval from adding access telemetry to the checkout.
	entries, err := os.ReadDir(repository)
	if err != nil || len(entries) != 2 {
		t.Fatalf("context search changed checkout: %v %v", entries, err)
	}
}
