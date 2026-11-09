package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
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

func dailyFeedbackPNG(t *testing.T, total int) []byte {
	t.Helper()
	var buffer bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatal(err)
	}
	raw := buffer.Bytes()
	if total == 0 {
		return append([]byte{}, raw...)
	}
	if total < len(raw) {
		t.Fatalf("requested image size %d is smaller than the encoded PNG %d", total, len(raw))
	}
	return append(append([]byte{}, raw...), make([]byte, total-len(raw))...)
}

// Cover the real store and HTTP boundary for the catalog, owner task launch,
// feedback multipart/image, and a store failure. Fakes cannot prove that the
// endpoint and CreateTask share the same validated owner path.
func TestDailyCatalogFeedbackAndOwnerLaunchHTTPIntegration(t *testing.T) {
	data, tools := t.TempDir(), t.TempDir()
	backend, err := store.New(data, tools, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"id":"workshop-only","label":"Workshop only","requires":["workshop"],"steps":[{"id":"ask","type":"agent","prompt":"Synthetic workshop prompt"}]}`,
		`{"id":"select-action","label":"Select action","presentation":{"fields":[{"name":"mode","label":"Mode","type":"select","required":true,"options":[{"value":"safe","label":"Safe"}]}]},"steps":[{"id":"ask","type":"agent","prompt":"Synthetic select prompt"}]}`,
	} {
		if _, err := backend.PutActionJSON([]byte(raw)); err != nil {
			t.Fatalf("configure action: %v", err)
		}
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

	status, _, catalog := client.request(t, http.MethodGet, "/api/catalog", nil, "")
	if status != http.StatusOK {
		t.Fatalf("catalog status=%d value=%v", status, catalog)
	}
	ids := map[string]bool{}
	for _, item := range catalog["actions"].([]any) {
		ids[item.(map[string]any)["id"].(string)] = true
	}
	if !ids["workshop-only"] || !ids["select-action"] {
		t.Fatalf("configured catalog entries missing: %v", ids)
	}

	status, _, result := client.request(t, http.MethodPost, "/api/actions/workshop-only/run", map[string]any{"fields": map[string]any{}}, server.URL)
	if status != http.StatusConflict || result["error"] != "action_disabled" {
		t.Fatalf("direct workshop launch status=%d result=%v", status, result)
	}
	status, _, result = client.request(t, http.MethodPost, "/api/tasks", map[string]any{"kind": "workshop-only", "title": "Bypass"}, server.URL)
	if status != http.StatusConflict || result["error"] != "action_disabled" {
		t.Fatalf("workshop task bypass status=%d result=%v", status, result)
	}
	status, _, result = client.request(t, http.MethodPost, "/api/tasks", map[string]any{
		"kind": "select-action", "title": "Bypass", "payload": map[string]any{"mode": "evil"},
	}, server.URL)
	if status != http.StatusBadRequest || result["error"] != "action_input_invalid" {
		t.Fatalf("select task bypass status=%d result=%v", status, result)
	}
	status, _, result = client.request(t, http.MethodPost, "/api/tasks", map[string]any{
		"kind": "select-action", "title": "Missing mode",
	}, server.URL)
	if status != http.StatusBadRequest || result["error"] != "action_input_invalid" {
		t.Fatalf("required task field status=%d result=%v", status, result)
	}
	status, _, created := client.request(t, http.MethodPost, "/api/tasks", map[string]any{
		"kind": "select-action", "title": "Validated", "payload": map[string]any{"mode": "safe"},
	}, server.URL)
	if status != http.StatusCreated || created["created"] != true {
		t.Fatalf("validated task status=%d result=%v", status, created)
	}
	if payload := created["task"].(map[string]any)["payload"].(map[string]any); payload["mode"] != "safe" {
		t.Fatalf("validated task payload=%v", payload)
	}

	fields := validFeedbackFields()
	small := dailyFeedbackPNG(t, 0)
	status, _, _, feedback := client.multipartRequest(t, "/api/feedback", fields, small, true, server.URL)
	if status != http.StatusCreated || feedback["accepted"] != true {
		t.Fatalf("feedback status=%d result=%v", status, feedback)
	}
	record := feedback["feedback"].(map[string]any)
	id := record["id"].(string)
	if record["has_image"] != true {
		t.Fatalf("feedback record=%v", record)
	}
	status, headers, image, _ := client.multipartGet(t, "/api/feedback/"+id+"/image")
	if status != http.StatusOK || len(image) == 0 || headers.Get("Content-Type") != "image/png" || headers.Get("Cache-Control") != "no-store" {
		t.Fatalf("feedback image status=%d headers=%v bytes=%d", status, headers, len(image))
	}
	if status, _, detail := client.request(t, http.MethodGet, "/api/feedback/"+id, nil, ""); status != http.StatusOK || detail["feedback"].(map[string]any)["title"] != fields["title"] {
		t.Fatalf("feedback detail status=%d result=%v", status, detail)
	}

	// A max-size image plus multipart overhead must pass the total-body cap;
	// the image cap itself still applies.
	boundary := dailyFeedbackPNG(t, maxFeedbackUploadBytes)
	status, _, _, feedback = client.multipartRequest(t, "/api/feedback", fields, boundary, true, server.URL)
	if status != http.StatusCreated || feedback["accepted"] != true {
		t.Fatalf("max image feedback status=%d result=%v", status, feedback)
	}
	oversized := dailyFeedbackPNG(t, maxFeedbackUploadBytes+1)
	status, _, _, feedback = client.multipartRequest(t, "/api/feedback", fields, oversized, true, server.URL)
	if status != http.StatusBadRequest || feedback["error"] != "feedback_invalid" || feedback["reason"] != "too_large" {
		t.Fatalf("oversized image feedback status=%d result=%v", status, feedback)
	}

	// A broken registry is a store failure, not a fake client contract.
	if err := os.WriteFile(filepath.Join(data, "actions.json"), []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	status, _, result = client.request(t, http.MethodGet, "/api/catalog", nil, "")
	if status != http.StatusInternalServerError || result["error"] != "action_unavailable" {
		t.Fatalf("catalog store failure status=%d result=%v", status, result)
	}
}
