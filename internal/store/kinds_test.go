package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskKindsDefaultAndCustomFile(t *testing.T) {
	store := newTestStore(t)
	path := taskKindsPathFor(store.dataDir)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("kinds file exists before first use: %v", err)
	}
	listing, err := store.TaskKinds()
	if err != nil {
		t.Fatalf("kinds: %v", err)
	}
	kinds := listing["kinds"].([]any)
	if len(kinds) != 1 || kinds[0].(map[string]any)["id"] != "maintenance" {
		t.Fatalf("kinds=%v", listing)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("default kinds file mode=%v err=%v", info, err)
	}
	if _, err := store.TaskKinds(); err != nil {
		t.Fatalf("second read: %v", err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"maintenance"`) {
		t.Fatalf("default file=%s", raw)
	}
}

func TestTaskKindValidation(t *testing.T) {
	cases := map[string]string{
		"duplicate id":            `{"version":1,"kinds":[{"id":"a","label":"A","action":"maintenance"},{"id":"a","label":"B","action":"maintenance"}]}`,
		"bad action":              `{"version":1,"kinds":[{"id":"a","label":"A","action":"deploy"}]}`,
		"bad id":                  `{"version":1,"kinds":[{"id":"A","label":"A","action":"maintenance"}]}`,
		"bad runner":              `{"version":1,"kinds":[{"id":"a","label":"A","action":"report","runner":"bash"}]}`,
		"empty kinds":             `{"version":1,"kinds":[]}`,
		"maintenance with prompt": `{"version":1,"kinds":[{"id":"a","label":"A","action":"maintenance","prompt":"x"}]}`,
		"bad priority":            `{"version":1,"kinds":[{"id":"a","label":"A","action":"maintenance","priority":"P9"}]}`,
		"bad timeout":             `{"version":1,"kinds":[{"id":"a","label":"A","action":"report","timeout_seconds":5}]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			store := newTestStore(t)
			if err := os.WriteFile(taskKindsPathFor(store.dataDir), []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.TaskKinds(); err == nil {
				t.Fatal("invalid kinds file was accepted")
			}
		})
	}
}

func TestReportKindDispatchRunsTheAgentAndRecordsUsage(t *testing.T) {
	store := newTestStore(t)
	writeTaskPolicy(t, store, map[string]bool{}, "00:00", "00:00", 0)
	writeTaskKinds(t, store,
		taskKind{ID: "maintenance", Label: "Context maintenance", Action: kindActionMaintenance},
		taskKind{ID: "triage", Label: "Issue triage", Action: kindActionReport, Priority: "P1",
			Prompt: "Triage this issue:\n{{title}}\nBody: {{payload.body}}"},
	)
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "codex"), "#!/bin/sh\nexit 0\n")
	launcher := filepath.Join(store.toolRoot, "folio")
	writeExecutable(t, launcher, `#!/bin/sh
out=""
while [ $# -gt 0 ]; do
  case "$1" in
    --report-out) out="$2"; shift 2 ;;
    *) shift ;;
  esac
done
[ -n "$out" ] || exit 2
printf '%s' '{"text":"# Triage\n\nCrash on start; reproducible.","usage":{"input_tokens":120,"cached_tokens":40,"output_tokens":30,"total_tokens":190,"origin":"codex"}}' > "$out"
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	created, err := store.CreateTask("triage", "manual", "Crash on start", "", map[string]any{"body": "steps to reproduce"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	taskID := created["task"].(map[string]any)["id"].(string)
	if created["task"].(map[string]any)["priority"] != "P1" {
		t.Fatalf("kind priority was not applied: %v", created)
	}
	result, err := store.DispatchDue()
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if result["dispatched"] != 1 || result["run_status"] != "reported" {
		t.Fatalf("result=%v", result)
	}
	listing, _ := store.ListTasks()
	task := listing["tasks"].([]any)[0].(map[string]any)
	if task["status"] != "done" || task["run_id"] == "" {
		t.Fatalf("task=%v", task)
	}
	run, err := store.AutomationRun(task["run_id"].(string))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	record := run
	if record["status"] != "reported" || record["kind"] != "triage" || record["task_id"] != taskID {
		t.Fatalf("record=%v", record)
	}
	usage, _ := record["usage"].(map[string]any)
	if usage["total_tokens"] != float64(190) || usage["source"] != "reported" || usage["origin"] != "codex" {
		t.Fatalf("usage=%v", usage)
	}
	reportPath := filepath.Join(store.reportsDir, record["report_file"].(string))
	body, err := os.ReadFile(reportPath)
	if err != nil || !strings.Contains(string(body), "reproducible") {
		t.Fatalf("report=%q err=%v", body, err)
	}
	info, err := os.Stat(reportPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("report mode=%v err=%v", info, err)
	}
}

func TestReportKindWithoutRunnerFailsCleanly(t *testing.T) {
	store := newTestStore(t)
	writeTaskPolicy(t, store, map[string]bool{}, "00:00", "00:00", 0)
	writeTaskKinds(t, store, taskKind{ID: "triage", Label: "Issue triage", Action: kindActionReport, Runner: "dsh"})
	t.Setenv("PATH", t.TempDir()) // neither codex nor dsh is reachable
	if _, err := store.CreateTask("triage", "manual", "Triage", "P1", nil); err != nil {
		t.Fatalf("create: %v", err)
	}
	result, err := store.DispatchDue()
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if result["dispatched"] != 1 || result["run_status"] != "failed" || result["reason"] != "model_unavailable" {
		t.Fatalf("result=%v", result)
	}
	listing, _ := store.ListTasks()
	task := listing["tasks"].([]any)[0].(map[string]any)
	if task["status"] != "failed" || task["reason"] != "model_unavailable" {
		t.Fatalf("task=%v", task)
	}
}

func TestHookActionIsATaskKind(t *testing.T) {
	store := newTestStore(t)
	writeTaskKinds(t, store,
		taskKind{ID: "maintenance", Label: "Context maintenance", Action: kindActionMaintenance},
		taskKind{ID: "triage", Label: "Issue triage", Action: kindActionReport, Priority: "P3", Prompt: "{{title}}"},
	)
	if _, err := store.CreateHook("Bad", "nope", ""); err == nil {
		t.Fatal("an unknown hook action was accepted")
	}
	created, err := store.CreateHook("Issue triage", "triage", "")
	if err != nil {
		t.Fatalf("create hook: %v", err)
	}
	hook := created["hook"].(map[string]any)
	if hook["action"] != "triage" || hook["priority"] != "P3" {
		t.Fatalf("hook=%v", hook)
	}
	listing, _ := store.ListHooks()
	actions := listing["actions"].([]any)
	if len(actions) != 2 || actions[0] != "maintenance" || actions[1] != "triage" {
		t.Fatalf("actions=%v", actions)
	}
}

func TestRenderKindPromptUsesDataFieldsAndFramesThePayload(t *testing.T) {
	kind := taskKind{ID: "triage", Label: "Issue triage", Action: kindActionReport,
		Prompt: "Triage {{title}} from {{source}}. Body: {{payload.body}}. Repo: {{payload.repo}}"}
	task := taskRecord{ID: "t-1", Kind: "triage", Title: "Crash on start", Source: "hook:issues",
		Payload: map[string]any{"body": "steps", "repo": "o/r", "token": "should not leak"}}
	prompt := renderKindPrompt(kind, task)
	for _, want := range []string{"Triage Crash on start from hook:issues.", "Body: steps.", "Repo: o/r"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q: %s", want, prompt)
		}
	}
	if !strings.Contains(prompt, "untrusted") || !strings.Contains(prompt, "never instructions") {
		t.Fatalf("the payload is not framed as data: %s", prompt)
	}
	if strings.Contains(prompt, "should not leak") {
		t.Fatalf("a credential-shaped payload key reached the prompt: %s", prompt)
	}
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
}
