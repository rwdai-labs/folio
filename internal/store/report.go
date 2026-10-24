package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"folio/internal/notify"
)

// A report task runs a read-only model call framed by a data-defined kind. It
// never touches the checkout: the result is a Markdown report in runtime
// reports/, a run record for the audit trail, and a notification.
const (
	maxReportInputBytes  = 128 * 1024
	maxReportOutputBytes = 2 * 1024 * 1024
	maxReportTextBytes   = 64 * 1024
	maxReportSummary     = 600
)

type reportResult struct {
	Text  string         `json:"text"`
	Usage map[string]any `json:"usage,omitempty"`
}

func (s *Store) runReportTask(kind taskKind, task taskRecord) (map[string]any, error) {
	started := time.Now()
	runID, err := newAutoRunID()
	if err != nil {
		return nil, errStore("report run could not start")
	}
	runner := kind.DefaultRunner()
	run := map[string]any{
		"version":     1,
		"run_id":      runID,
		"trigger":     "task",
		"status":      "running",
		"stage":       "report",
		"started_at":  started.UTC().Format(time.RFC3339Nano),
		"runner":      runner,
		"summary":     "",
		"duration_ms": 0,
		"kind":        kind.ID,
		"task_id":     task.ID,
	}
	if kind.Model != "" {
		run["model"] = kind.Model
	}
	run["model_available"] = s.runnerAvailable(runner)
	if err := s.saveAutomationRun(run); err != nil {
		return nil, err
	}
	finish := func(status, reason string) map[string]any {
		run["status"] = status
		run["stage"] = "done"
		run["finished_at"] = time.Now().UTC().Format(time.RFC3339Nano)
		run["duration_ms"] = time.Since(started).Milliseconds()
		if reason != "" {
			run["reason"] = reason
			if status == "failed" {
				run["error"] = reason
			}
		}
		_ = s.saveAutomationRun(run)
		// Best effort and asynchronous, like a maintenance run.
		s.notifyRun(run)
		result := map[string]any{"status": status, "run_id": runID}
		if reason != "" {
			result["reason"] = reason
		}
		if summary, ok := run["summary"].(string); ok && summary != "" {
			result["summary"] = summary
		}
		if file, ok := run["report_file"].(string); ok && file != "" {
			result["report_file"] = file
		}
		return result
	}

	if !s.runnerAvailable(runner) {
		return finish("failed", "model_unavailable"), nil
	}

	prompt := renderKindPrompt(kind, task)
	if len(prompt) > maxReportInputBytes {
		return finish("failed", "prompt_too_large"), nil
	}
	promptPath := filepath.Join(s.runtimeDir, "task-prompt-"+task.ID+".txt")
	outputPath := filepath.Join(s.runtimeDir, "task-report-"+task.ID+".json")
	defer os.Remove(promptPath)
	defer os.Remove(outputPath)
	if err := os.WriteFile(promptPath, []byte(prompt), 0o600); err != nil {
		return finish("failed", "report_prepare_failed"), nil
	}
	command, err := s.installedFolioCommand()
	if err != nil {
		return finish("failed", "report_prepare_failed"), nil
	}
	command = append(command, "agent",
		"--report-prompt", promptPath,
		"--report-out", outputPath,
		"--runtime-dir", s.runtimeDir,
		"--runner", runner,
		"--timeout", strconv.Itoa(kind.Timeout()),
	)
	if kind.Model != "" {
		command = append(command, "--model", kind.Model)
		if runner == "dsh" {
			if provider := dshProviderForModel(kind.Model); provider != "" {
				command = append(command, "--provider", provider)
			}
		}
	}
	env := map[string]string{
		"FOLIO_CONTEXT_ROOT": s.repositoryDir,
		"FOLIO_RUNTIME":      s.runtimeDir,
		"CODEX_HOME":         s.codexDir,
		"DSH_HOME":           s.dshDir,
	}
	for key, value := range dshRunnerEnv() {
		env[key] = value
	}
	s.modelMu.Lock()
	_, runErr := s.runCommand(command, s.toolRoot, "", time.Duration(kind.Timeout()+120)*time.Second, true, env)
	s.modelMu.Unlock()
	if runErr != nil {
		return finish("failed", "report_run_failed"), nil
	}
	info, statErr := os.Lstat(outputPath)
	if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxReportOutputBytes {
		return finish("failed", "report_invalid"), nil
	}
	raw, err := os.ReadFile(outputPath)
	if err != nil || len(raw) > maxReportOutputBytes {
		return finish("failed", "report_invalid"), nil
	}
	var result reportResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return finish("failed", "report_invalid"), nil
	}
	text := strings.TrimSpace(result.Text)
	if text == "" || len(text) > maxReportTextBytes || secretMaterialRE.MatchString(text) {
		return finish("failed", "report_rejected"), nil
	}
	reportFile := "task-" + task.ID + ".md"
	if err := writePrivateFile(filepath.Join(s.reportsDir, reportFile), []byte(text+"\n")); err != nil {
		return finish("failed", "report_write_failed"), nil
	}
	summary := notify.ClampRunes(firstParagraph(text), maxReportSummary)
	run["summary"] = publicText(summary, maxReportSummary)
	run["report_file"] = reportFile
	run["report_text"] = publicText(text, maxKindReportRunes)
	if usage := reportUsage(result.Usage); usage != nil {
		run["usage"] = usage
	}
	return finish("reported", ""), nil
}

// reportUsage normalizes the runner's counters and drops impossible values.
func reportUsage(usage map[string]any) map[string]any {
	if usage == nil {
		return nil
	}
	read := func(key string) (int64, bool) {
		value, ok := usage[key].(float64)
		if !ok || value < 0 || value > 1<<40 {
			return 0, false
		}
		return int64(value), true
	}
	input, okInput := read("input_tokens")
	cached, okCached := read("cached_tokens")
	output, okOutput := read("output_tokens")
	total, okTotal := read("total_tokens")
	if !okInput || !okCached || !okOutput || !okTotal {
		return nil
	}
	origin, _ := usage["origin"].(string)
	origin = publicText(origin, 32)
	if origin == "" {
		origin = "runner"
	}
	requests, _ := usage["requests"].(float64)
	if requests < 0 || requests > 1000 {
		requests = 0
	}
	return map[string]any{
		"input_tokens": input, "cached_tokens": cached, "output_tokens": output,
		"total_tokens": total, "requests": int64(requests), "origin": origin, "source": "reported",
	}
}

// firstParagraph picks a human-readable summary: the first block that is not a
// Markdown heading, list marker, or code fence.
func firstParagraph(text string) string {
	for _, block := range strings.Split(text, "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" || strings.HasPrefix(block, "#") || strings.HasPrefix(block, "```") ||
			strings.HasPrefix(block, "- ") || strings.HasPrefix(block, "* ") || strings.HasPrefix(block, ">") {
			continue
		}
		return strings.Join(strings.Fields(block), " ")
	}
	return ""
}

func writePrivateFile(path string, data []byte) error {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errStore("report path is invalid")
		}
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return errStore("report path is unavailable")
	}
	return nil
}
