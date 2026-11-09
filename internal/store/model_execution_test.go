package store

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func bindSyntheticService(t *testing.T, s *Store, base string, defaults map[string]any, overrides map[string]any) {
	t.Helper()
	cfg := emptyModelServices()
	cfg["connections"] = map[string]any{"gateway": map[string]any{"name": "Synthetic gateway", "base_url": base, "api_key": "synthetic-execution-key"}}
	for purpose, binding := range defaults {
		cfg["defaults"].(map[string]any)[purpose] = binding
	}
	if overrides != nil {
		cfg["overrides"] = map[string]any{"report": overrides}
	}
	if _, err := s.PutModelServices(cfg); err != nil {
		t.Fatal(err)
	}
}
func syntheticBinding(model string) map[string]any {
	return map[string]any{"connection": "gateway", "model": model}
}

func TestBoundReportUsesTaskOverrideAndActualAttribution(t *testing.T) {
	s := newTestStore(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer synthetic-execution-key" || r.Header.Get("X-Magpie-Session") == "" {
			t.Error("wrong request route/auth/correlation")
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload["model"] != "group/special" {
			t.Error("per-task override not used")
		}
		if _, ok := payload["tools"]; ok {
			t.Error("report sent tools")
		}
		messages := payload["messages"].([]any)
		if len(messages) != 2 || strings.Contains(messages[0].(map[string]any)["content"].(string), "main agent") {
			t.Error("report inherited agent history")
		}
		w.Header().Set("X-Magpie-Provider", "deepseek")
		w.Header().Set("X-Magpie-Model", "deepseek-chat")
		_, _ = w.Write([]byte(`{"model":"group/special","choices":[{"message":{"content":"A synthetic report with source evidence."},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,"prompt_tokens_details":{"cached_tokens":30}}}`))
	}))
	defer server.Close()
	bindSyntheticService(t, s, server.URL+"/v1", map[string]any{"report": syntheticBinding("group/default")}, map[string]any{"special": syntheticBinding("group/special")})
	task := taskRecord{ID: "t-123456abcdef", Kind: "special", Title: "Synthetic report"}
	result, err := s.runReportTask(taskKind{ID: "special", Action: "report", Prompt: "Read the synthetic sources."}, task)
	if err != nil || result["status"] != "reported" {
		t.Fatalf("report did not complete: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(s.runsDir, result["run_id"].(string)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var run map[string]any
	if err := json.Unmarshal(raw, &run); err != nil {
		t.Fatal(err)
	}
	usage := run["usage"].(map[string]any)
	if usage["input_tokens"] != float64(70) || usage["cached_tokens"] != float64(30) {
		t.Fatal("usage normalization failed")
	}
	entry := run["model_usage"].([]any)[0].(map[string]any)
	if entry["provider"] != "deepseek" || entry["model"] != "deepseek-chat" || entry["requested_model"] != "group/special" {
		t.Fatal("served attribution lost")
	}
}

func TestRejectedModelOutputStillRecordsUsageAndDoesNotWriteReport(t *testing.T) {
	s := newTestStore(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"partial"},"finish_reason":"length"}],"usage":{"prompt_tokens":80,"completion_tokens":10,"total_tokens":90}}`))
	}))
	defer server.Close()
	bindSyntheticService(t, s, server.URL, map[string]any{"report": syntheticBinding("group/report")}, nil)
	task := taskRecord{ID: "t-aabbccddeeff", Kind: "brief"}
	result, err := s.runReportTask(taskKind{ID: "brief", Prompt: "Synthetic"}, task)
	if err != nil || result["status"] != "failed" {
		t.Fatal("truncated output was not rejected")
	}
	raw, err := os.ReadFile(filepath.Join(s.runsDir, result["run_id"].(string)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var run map[string]any
	_ = json.Unmarshal(raw, &run)
	if run["usage"] == nil {
		t.Fatal("failed paid work lost usage")
	}
	if _, err := os.Stat(filepath.Join(s.reportsDir, "run-"+result["run_id"].(string)+".md")); !os.IsNotExist(err) {
		t.Fatal("rejected content created report")
	}
	if cost := s.runCost(run); cost["estimated"] != false {
		t.Fatal("unattributed group acquired invented price")
	}
}

func TestBoundAssistantUsesSelectedConnection(t *testing.T) {
	s := newTestStore(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		_ = json.NewDecoder(r.Body).Decode(&p)
		if p["model"] != "group/assistant" {
			t.Error("assistant ignored assignment")
		}
		w.Header().Set("X-Magpie-Provider", "deepseek")
		w.Header().Set("X-Magpie-Model", "deepseek-chat")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Synthetic answer."}}],"usage":{"prompt_tokens":40,"completion_tokens":5,"total_tokens":45}}`))
	}))
	defer server.Close()
	bindSyntheticService(t, s, server.URL, map[string]any{"assistant": syntheticBinding("group/assistant")}, nil)
	if !s.AgentAvailable() {
		t.Fatal("bound assistant unavailable without legacy runtime")
	}
	result, err := s.AgentTurn(context.Background(), "synthetic-session", "Synthetic question", "owner", nil)
	if err != nil || result.Text != "Synthetic answer." {
		t.Fatalf("assistant binding failed: %v", err)
	}
}

func TestBoundPipelineUsesGroupWithoutRunner(t *testing.T) {
	s := newTestStore(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Synthetic action report."}}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`))
	}))
	defer server.Close()
	bindSyntheticService(t, s, server.URL, map[string]any{"report": syntheticBinding("group/report")}, nil)
	definition := pipelineDefinition{ID: "brief", Label: "Brief", ReportFrom: "answer", Steps: []pipelineStep{{ID: "answer", Type: "agent", Prompt: "Synthetic report", TimeoutSeconds: 60}}}
	plan, err := actionPlanForPipeline(definition, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.runPipeline(plan, taskRecord{ID: "t-001122334455", Kind: "brief"})
	if err != nil || result["status"] != "completed" {
		t.Fatalf("pipeline binding failed: %v", err)
	}
}

func TestBoundMaintenancePreservesTransactionAndTaskCredentials(t *testing.T) {
	s := newGitStore(t)
	installAutoFixture(t, s, `{"version":1,"status":"pending-apply","summary":"No structural changes needed.","edits":[],"usage":{"input_tokens":9,"cached_tokens":0,"output_tokens":2,"total_tokens":11,"requests":1,"origin":"stdout","source":"reported"}}`)
	tool := filepath.Join(s.toolRoot, "folio")
	raw, err := os.ReadFile(tool)
	if err != nil {
		t.Fatal(err)
	}
	guard := "#!/bin/sh\n[ \"$FOLIO_MODEL_BASE_URL\" = 'http://127.0.0.1:3425/v1' ] || exit 31\n[ \"$FOLIO_MODEL_ID\" = 'group/maintenance' ] || exit 32\n[ \"$FOLIO_MODEL_KEY\" = 'synthetic-execution-key' ] || exit 33\n"
	if err := os.WriteFile(tool, []byte(guard+strings.TrimPrefix(string(raw), "#!/bin/sh\n")), 0755); err != nil {
		t.Fatal(err)
	}
	bindSyntheticService(t, s, "http://127.0.0.1:3425/v1", map[string]any{"maintenance": syntheticBinding("group/maintenance")}, nil)
	result, err := s.RunAutomation("manual", "codex", "")
	if err != nil || result["status"] != "no_changes" {
		t.Fatalf("maintenance binding failed: %v %v", err, result["reason"])
	}
	if result["model"] != "group/maintenance" {
		t.Fatal("requested maintenance group missing")
	}
	if strings.Contains(string(mustJSON(t, result)), "synthetic-execution-key") {
		t.Fatal("task secret leaked to audit")
	}
	if cost := s.runCost(result); cost != nil && cost["estimated"] == true {
		t.Fatal("CLI group cost was guessed")
	}
}

func TestRejectedAssistantUsageRetained(t *testing.T) {
	s := newTestStore(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"truncated"},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))
	defer server.Close()
	bindSyntheticService(t, s, server.URL, map[string]any{"assistant": syntheticBinding("group/assistant")}, nil)
	result, err := s.AgentTurn(context.Background(), "rejected-session", "Synthetic question", "owner", nil)
	if err != nil || result.Text != "" || result.Usage == nil || result.Usage["total_tokens"] != int64(15) {
		t.Fatal("rejected assistant response lost usage or exposed text")
	}
	raw, err := os.ReadFile(filepath.Join(s.runsDir, result.RunID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var run map[string]any
	_ = json.Unmarshal(raw, &run)
	if run["status"] != "failed" || run["usage"] == nil {
		t.Fatal("rejected assistant attempt not audited")
	}
}
func TestServedAttributionDoesNotInferVendorFromGatewayName(t *testing.T) {
	provider, model := servedModel(http.Header{}, "gpt-example", "gpt-example", "https://not-openai.example/v1")
	if provider != "" || model != "gpt-example" {
		t.Fatal("arbitrary gateway assigned vendor pricing")
	}
	provider, model = servedModel(http.Header{}, "group/report", "group/report", "https://api.openai.com/v1")
	if provider != "" || model != "" {
		t.Fatal("group echo treated as physical model")
	}
	provider, _ = servedModel(http.Header{}, "gpt-example", "gpt-example", "https://api.openai.com/v1")
	if provider != "openai" {
		t.Fatal("known vendor endpoint not identified")
	}
}
