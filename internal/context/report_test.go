package contextrepo

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
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

func TestRunnerFailuresCarryOnlyAClass(t *testing.T) {
	cases := []struct {
		name    string
		script  string
		timeout time.Duration
		want    string
	}{
		{"auth", "#!/bin/sh\ncat > /dev/null\necho 'ERROR: unexpected status 401 Unauthorized: Incorrect API key provided' >&2\nexit 1\n", 10 * time.Second, RunnerAuth},
		{"network", "#!/bin/sh\ncat > /dev/null\necho 'ERROR: dial tcp: lookup api.invalid: no such host' >&2\nexit 1\n", 10 * time.Second, RunnerNetwork},
		{"failed", "#!/bin/sh\ncat > /dev/null\nexit 1\n", 10 * time.Second, RunnerFailed},
		{"timeout", "#!/bin/sh\ncat > /dev/null\nsleep 30\n", 300 * time.Millisecond, RunnerTimeout},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router := testRouter(t)
			runtime := t.TempDir()
			binDir := t.TempDir()
			writeTestExecutable(t, filepath.Join(binDir, "codex"), tc.script)
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+"/usr/bin:/bin")
			prompt := filepath.Join(runtime, "prompt.txt")
			if err := os.WriteFile(prompt, []byte("question"), 0o600); err != nil {
				t.Fatal(err)
			}
			err := router.RunAgent(AgentOptions{
				Runner: "codex", RuntimeDir: runtime, Timeout: tc.timeout,
				ReportPrompt: prompt, ReportOutput: filepath.Join(runtime, "out.json"),
			}, io.Discard, io.Discard)
			var failure *RunnerFailure
			if !errors.As(err, &failure) || failure.Class != tc.want {
				t.Fatalf("failure=%v want=%q err=%v", failure, tc.want, err)
			}
			if strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "api.invalid") {
				t.Fatalf("runner output leaked into the error: %v", err)
			}
		})
	}
}

func TestRunMapsRunnerFailureToExitCode(t *testing.T) {
	root := t.TempDir()
	for _, directory := range []string{"core", "active", "domains", "projects", "decisions", "knowledge", "history", "inbox"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	runtime := t.TempDir()
	t.Setenv("FOLIO_CONTEXT_ROOT", root)
	t.Setenv("CODEX_HOME", t.TempDir())
	binDir := t.TempDir()
	writeTestExecutable(t, filepath.Join(binDir, "codex"), "#!/bin/sh\ncat > /dev/null\necho '401 Unauthorized: Incorrect API key provided' >&2\nexit 1\n")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+"/usr/bin:/bin")
	prompt := filepath.Join(runtime, "prompt.txt")
	if err := os.WriteFile(prompt, []byte("question"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := Run([]string{"agent", "--report-prompt", prompt, "--report-out", filepath.Join(runtime, "out.json"),
		"--runtime-dir", runtime, "--runner", "codex", "--timeout", "20"}, &out, &errOut)
	if code != ExitRunnerAuth {
		t.Fatalf("exit=%d stderr=%s", code, errOut.String())
	}
	if strings.Contains(errOut.String(), "Incorrect API key") {
		t.Fatalf("raw runner output reached the caller: %s", errOut.String())
	}
}
