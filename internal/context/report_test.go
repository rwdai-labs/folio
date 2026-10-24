package contextrepo

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunReportWritesResultAndUsage(t *testing.T) {
	router := testRouter(t)
	runtime := t.TempDir()
	binDir := t.TempDir()
	writeTestExecutable(t, filepath.Join(binDir, "codex"), `#!/bin/sh
out=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o|--output-last-message) out="$2"; shift 2 ;;
    *) shift ;;
  esac
done
cat > /dev/null
[ -n "$out" ] || exit 2
printf 'Crash on start is reproducible.' > "$out"
printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":10,"output_tokens":20,"total_tokens":120}}'
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	promptPath := filepath.Join(runtime, "prompt.txt")
	if err := os.WriteFile(promptPath, []byte("Triage this task"), 0o600); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(runtime, "result.json")
	var stdout, stderr bytes.Buffer
	err := router.RunAgent(AgentOptions{
		Runner: "codex", RuntimeDir: runtime, Timeout: 30 * time.Second,
		ReportPrompt: promptPath, ReportOutput: outputPath,
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("report run: %v (stderr=%s)", err, stderr.String())
	}
	raw, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("output: %v", err)
	}
	var result struct {
		Text  string         `json:"text"`
		Usage map[string]any `json:"usage"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("output json: %v (%s)", err, raw)
	}
	if result.Text != "Crash on start is reproducible." {
		t.Fatalf("text=%q", result.Text)
	}
	want := map[string]float64{"input_tokens": 90, "cached_tokens": 10, "output_tokens": 20, "total_tokens": 120}
	for key, value := range want {
		if result.Usage[key] != value {
			t.Fatalf("usage[%s]=%v want %v (%v)", key, result.Usage[key], value, result.Usage)
		}
	}
	if info, err := os.Stat(outputPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("output mode=%v err=%v", info, err)
	}
}

func TestReportRejectsPathsOutsideRuntime(t *testing.T) {
	router := testRouter(t)
	runtime := t.TempDir()
	promptPath := filepath.Join(runtime, "prompt.txt")
	if err := os.WriteFile(promptPath, []byte("do work"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "result.json")
	err := router.RunAgent(AgentOptions{
		Runner: "codex", RuntimeDir: runtime, Timeout: 30 * time.Second,
		ReportPrompt: promptPath, ReportOutput: outside,
	}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("an output path outside the runtime directory was accepted")
	}
}

func TestReportOptionsAreValidated(t *testing.T) {
	cases := []AgentOptions{
		{Auto: true, AutoReport: "/tmp/x", ReportPrompt: "/tmp/p", ReportOutput: "/tmp/o"},
		{ReportPrompt: "/tmp/p"},
		{ReportOutput: "/tmp/o"},
	}
	for index, options := range cases {
		if err := validateAgentOptions(options); err == nil {
			t.Fatalf("case %d was accepted: %+v", index, options)
		}
	}
	if err := validateAgentOptions(AgentOptions{ReportPrompt: "/tmp/p", ReportOutput: "/tmp/o"}); err != nil {
		t.Fatalf("valid report options rejected: %v", err)
	}
}

func writeTestExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
}
