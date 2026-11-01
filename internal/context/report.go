package contextrepo

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"folio/internal/dshconfig"
)

const (
	maxReportPromptBytes = 128 * 1024
	maxReportTextBytes   = 64 * 1024
)

// Runner failure classes are the only description of a failed model call that
// may leave this package. Stderr, provider URLs, credentials and model output
// stay here; a caller receives one fixed label.
const (
	RunnerUnavailable = "unavailable"
	RunnerAuth        = "auth"
	RunnerNetwork     = "network"
	RunnerTimeout     = "timeout"
	RunnerFailed      = "failed"
)

// RunnerFailure reports which class of model-run failure stopped a report.
type RunnerFailure struct{ Class string }

func (e *RunnerFailure) Error() string { return "model runner failed: " + e.Class }

// FailureClass exposes the fixed label without the underlying output.
func (e *RunnerFailure) FailureClass() string { return e.Class }

// runnerClass maps a terminated runner to one stable label. Only a fixed set of
// markers is inspected; nothing from the buffer is retained.
func runnerClass(runErr error, stderr []byte) string {
	if errors.Is(runErr, errRunnerTimeout) {
		return RunnerTimeout
	}
	text := strings.ToLower(string(stderr))
	authMarkers := []string{"invalid_api_key", "incorrect api key", "unauthorized", "authentication failed", "401", "403"}
	if containsAny(text, authMarkers) {
		return RunnerAuth
	}
	networkMarkers := []string{"dial tcp", "no such host", "connection refused", "proxyconnect", "tls handshake", "connection reset", "i/o timeout"}
	if containsAny(text, networkMarkers) {
		return RunnerNetwork
	}
	return RunnerFailed
}

func containsAny(text string, markers []string) bool {
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// runReport answers one owner task with a single read-only model call. It is
// the generic half of a data-defined task kind: the prompt and the model come
// from configuration, Folio only guarantees the sandbox and the output shape.
func (r *Router) runReport(options AgentOptions, runtime string) error {
	prompt, err := readBoundedFile(options.ReportPrompt, maxReportPromptBytes)
	if err != nil || len(bytes.TrimSpace(prompt)) == 0 {
		return errors.New("report prompt is unavailable")
	}
	outputPath, err := safeReportOutput(options.ReportOutput, runtime)
	if err != nil {
		return err
	}
	work, err := os.MkdirTemp(runtime, "report-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	_ = os.Chmod(work, 0o700)

	var (
		text  string
		usage *usageReport
	)
	switch options.Runner {
	case "dsh":
		text, usage, err = r.runDSHReport(options, work, runtime, prompt)
	default:
		text, usage, err = r.runCodexReport(options, work, runtime, prompt)
	}
	if err != nil {
		return err
	}
	text = strings.TrimSpace(text)
	if text == "" || len(text) > maxReportTextBytes {
		return errors.New("report result is invalid")
	}
	result := map[string]any{"text": text}
	if usage != nil {
		result["usage"] = map[string]any{
			"input_tokens":  usage.Input,
			"cached_tokens": usage.Cached,
			"output_tokens": usage.Output,
			"total_tokens":  usage.Total,
			"requests":      usage.Requests,
			"origin":        usage.Origin,
		}
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return errors.New("report result is invalid")
	}
	if err := writeReportOutput(outputPath, raw); err != nil {
		return err
	}
	return nil
}

func (r *Router) runCodexReport(options AgentOptions, work, runtime string, prompt []byte) (string, *usageReport, error) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		return "", nil, &RunnerFailure{Class: RunnerUnavailable}
	}
	outputPath := filepath.Join(work, "report.txt")
	connectionArgs, providerEnv, err := CodexConnectionArgs()
	if err != nil {
		return "", nil, err
	}
	args := workerCodexArgs(work, "", outputPath, connectionArgs, options.Model)
	cmd := exec.Command(codex, args...)
	cmd.Dir = work
	cmd.Stdin = bytes.NewReader(prompt)
	cmd.Env, err = r.buildWorkerEnv(providerEnv, runtime)
	if err != nil {
		return "", nil, err
	}
	stdout := &limitedWriter{limit: codexOutputLimit}
	cmd.Stdout = stdout
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if runErr := runWithTimeout(cmd, options.Timeout); runErr != nil {
		return "", nil, &RunnerFailure{Class: runnerClass(runErr, stderr.Bytes())}
	}
	info, statErr := os.Lstat(outputPath)
	if statErr != nil || !info.Mode().IsRegular() || info.Size() > maxReportTextBytes {
		return "", nil, errors.New("report result is invalid")
	}
	raw, err := os.ReadFile(outputPath)
	if err != nil {
		return "", nil, errors.New("report result is invalid")
	}
	var usage *usageReport
	if parsed, ok := parseCodexUsage(stdout.buffer.Bytes()); ok {
		usage = &parsed
	}
	return string(raw), usage, nil
}

func (r *Router) runDSHReport(options AgentOptions, work, runtime string, prompt []byte) (string, *usageReport, error) {
	dsh, err := exec.LookPath("dsh")
	if err != nil {
		return "", nil, &RunnerFailure{Class: RunnerUnavailable}
	}
	dshConfig, configErr := dshconfig.LoadFromEnv()
	if configErr != nil && options.Model != "" {
		return "", nil, errors.New("DSH model configuration is unavailable")
	}
	provider, err := dshModelProvider(options.Model, options.Provider, dshConfig)
	if err != nil {
		return "", nil, err
	}
	patch := dshModelPatch(options.Model, provider, dshConfig, dshStoredCredentials())
	modelPatch := ""
	if len(patch) > 0 {
		modelPatch = filepath.Join(work, "model.yml")
		if err := os.WriteFile(modelPatch, patch, 0o600); err != nil {
			return "", nil, errors.New("report run could not be prepared")
		}
	}
	cmd := exec.Command(dsh, workerDSHArgs(modelPatch)...)
	cmd.Dir = work
	cmd.Stdin = bytes.NewReader(prompt)
	cmd.Env, err = r.buildWorkerEnv("", runtime)
	if err != nil {
		return "", nil, err
	}
	cmd.Env = workerDSHEnv(cmd.Env, strings.TrimSpace(os.Getenv("DSH_HOME")))
	stdout := &limitedWriter{limit: dshOutputLimit}
	cmd.Stdout = stdout
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if runErr := runWithTimeout(cmd, options.Timeout); runErr != nil {
		return "", nil, &RunnerFailure{Class: runnerClass(runErr, stderr.Bytes())}
	}
	text, ok := extractDSHFinal(stdout.buffer.Bytes())
	if !ok {
		return "", nil, errors.New("report result is invalid")
	}
	var usage *usageReport
	if parsed, ok := parseDSHUsage(stdout.buffer.Bytes()); ok {
		usage = &parsed
	}
	return text, usage, nil
}

// safeReportOutput keeps the write inside the runtime data directory the
// service configured, not where a caller points it.
func safeReportOutput(path, runtime string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) || !withinDir(path, runtime) {
		return "", errors.New("report output path must be inside the runtime directory")
	}
	return path, nil
}

func withinDir(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "."
}

func readBoundedFile(path string, limit int) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > int64(limit) {
		return nil, errors.New("file is unavailable")
	}
	return os.ReadFile(path)
}

func writeReportOutput(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}
