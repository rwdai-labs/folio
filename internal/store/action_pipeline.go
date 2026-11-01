package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"folio/internal/notify"
)

const (
	pipelineDefaultOutput = 64 * 1024
	pipelineTotalOutput   = 4 * 1024 * 1024
	pipelineTemplateLimit = 256 * 1024
	pipelineReportLimit   = 64 * 1024
)

var pipelineTemplateRE = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_.-]+)\s*\}\}`)

type pipelineStepResult struct {
	Output   string
	Usage    map[string]any
	ExitCode int
	Runner   string
	Model    string
	Provider string
}

// runPipeline executes one configured action and records a single run with
// per-step evidence. A step error stops the plan unless on_error is "continue";
// a retry reruns the plan with the same task identity.
func (s *Store) runPipeline(plan actionPlan, task taskRecord) (map[string]any, error) {
	started := time.Now()
	runID, err := newAutoRunID()
	if err != nil {
		return nil, errStore("action run could not start")
	}
	run := map[string]any{
		"version":     1,
		"run_id":      runID,
		"trigger":     "task",
		"status":      "running",
		"stage":       "action",
		"started_at":  started.UTC().Format(time.RFC3339Nano),
		"runner":      "pipeline",
		"action":      plan.ID,
		"kind":        plan.ID,
		"task_id":     task.ID,
		"summary":     "",
		"duration_ms": 0,
	}
	if err := s.saveAutomationRun(run); err != nil {
		return nil, err
	}
	finish := func(status, reason string) (map[string]any, error) {
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
		if err := s.saveAutomationRun(run); err != nil {
			return nil, err
		}
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
		return result, nil
	}

	outputs := map[string]string{}
	steps := make([]any, 0, len(plan.Steps))
	modelUsage := make([]any, 0, len(plan.Steps))
	usage := map[string]int64{}
	totalOutput := 0
	lastOutput := ""
	for _, step := range plan.Steps {
		if step.Pipeline == nil {
			return finish("failed", "action_plan_invalid")
		}
		spec := step.Pipeline
		stepStarted := time.Now()
		result, stepErr := s.runPipelineStep(spec, plan, task, outputs, runID)
		record := map[string]any{
			"id":          spec.ID,
			"type":        spec.Type,
			"status":      "completed",
			"duration_ms": time.Since(stepStarted).Milliseconds(),
		}
		if result.Output != "" {
			record["output_bytes"] = len(result.Output)
			record["output_digest"] = pipelineDigest(result.Output)
		}
		if result.ExitCode != 0 {
			record["exit_code"] = result.ExitCode
		}
		if stepErr != nil {
			record["status"] = "failed"
			record["reason"] = stepErr.Error()
			steps = append(steps, record)
			run["steps"] = steps
			_ = s.saveAutomationRun(run)
			if spec.OnError == "continue" {
				continue
			}
			return finish("failed", stepErr.Error())
		}
		if result.Output != "" {
			totalOutput += len(result.Output)
			if totalOutput > pipelineTotalOutput {
				steps = append(steps, record)
				run["steps"] = steps
				return finish("failed", "action_output_too_large")
			}
			outputs[spec.ID] = result.Output
			lastOutput = result.Output
		}
		if result.Usage != nil {
			mergePipelineUsage(usage, result.Usage)
			run["usage"] = pipelineUsageMap(usage)
			record["usage"] = result.Usage
			entry := map[string]any{"usage": result.Usage}
			if result.Runner != "" {
				entry["runner"] = result.Runner
			}
			if result.Model != "" {
				entry["model"] = result.Model
			}
			if result.Provider != "" {
				entry["provider"] = result.Provider
			}
			modelUsage = append(modelUsage, entry)
			run["model_usage"] = modelUsage
		}
		steps = append(steps, record)
		run["steps"] = steps
		_ = s.saveAutomationRun(run)
	}
	if len(usage) > 0 {
		run["usage"] = pipelineUsageMap(usage)
	}
	if len(modelUsage) > 0 {
		run["model_usage"] = modelUsage
	}
	if plan.ReportFrom != "" {
		text := strings.TrimSpace(outputs[plan.ReportFrom])
		if text == "" {
			return finish("failed", "report_empty")
		}
		text = notify.ClampBytes(text, pipelineReportLimit)
		reportFile := "action-" + task.ID + ".md"
		if err := writePrivateFile(filepath.Join(s.reportsDir, reportFile), []byte(text+"\n")); err != nil {
			return finish("failed", "report_write_failed")
		}
		run["report_file"] = reportFile
		run["report_text"] = publicText(text, maxKindReportRunes)
		run["summary"] = publicText(firstParagraph(text), maxReportSummary)
	} else if summary := firstParagraph(lastOutput); summary != "" {
		run["summary"] = publicText(summary, maxReportSummary)
	}
	return finish("completed", "")
}

func (s *Store) runPipelineStep(spec *pipelineStep, plan actionPlan, task taskRecord, outputs map[string]string, runID string) (pipelineStepResult, error) {
	values := pipelineTemplateValues(plan, task, outputs)
	limit := pipelineOutputLimit(spec)
	switch spec.Type {
	case "context":
		query, err := s.renderPipeline(spec.Query, values, false)
		if err != nil {
			return pipelineStepResult{}, err
		}
		packet, ok := s.contextPacket(context.Background(), query)
		if !ok {
			return pipelineStepResult{}, errStore("context_unavailable")
		}
		return pipelineStepResult{Output: notify.ClampBytes(packet, limit)}, nil
	case "agent":
		prompt, err := s.renderPipeline(spec.Prompt, values, false)
		if err != nil {
			return pipelineStepResult{}, err
		}
		result, err := s.runPipelineAgent(prompt, spec, limit)
		if err != nil {
			return pipelineStepResult{}, err
		}
		return result, nil
	case "script":
		return s.runPipelineScript(spec, plan, task, values, runID, limit)
	case "push":
		title, err := s.renderPipeline(spec.Title, values, false)
		if err != nil {
			return pipelineStepResult{}, err
		}
		body, err := s.renderPipeline(spec.Body, values, false)
		if err != nil {
			return pipelineStepResult{}, err
		}
		receipt, err := s.runPipelinePush(spec.Channel, title, body, plan.ID, runID, limit)
		if err != nil {
			return pipelineStepResult{}, err
		}
		return pipelineStepResult{Output: receipt}, nil
	case "http":
		return s.runPipelineHTTP(spec, plan, values, limit)
	case "task":
		return s.runPipelineTask(spec, plan, values)
	default:
		return pipelineStepResult{}, errStore("action_plan_invalid")
	}
}

func pipelineTemplateValues(plan actionPlan, task taskRecord, outputs map[string]string) map[string]string {
	values := map[string]string{
		"task.id":       task.ID,
		"task.title":    task.Title,
		"task.source":   task.Source,
		"task.priority": task.Priority,
		"task.kind":     task.Kind,
		"action.id":     plan.ID,
		"action.label":  plan.Label,
	}
	if raw, err := json.Marshal(task.Payload); err == nil {
		values["payload"] = string(raw)
	}
	for key, value := range task.Payload {
		values["payload."+key] = pipelineValue(value)
	}
	for id, output := range outputs {
		values["steps."+id+".output"] = output
	}
	return values
}

func pipelineValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		return jsonNumber(typed)
	case bool:
		if typed {
			return "true"
		}
		return "false"
	default:
		raw, _ := json.Marshal(typed)
		return string(raw)
	}
}

func (s *Store) renderPipeline(input string, values map[string]string, allowSecrets bool) (string, error) {
	var renderErr error
	output := pipelineTemplateRE.ReplaceAllStringFunc(input, func(match string) string {
		if renderErr != nil {
			return ""
		}
		key := strings.TrimSpace(match[2 : len(match)-2])
		if strings.HasPrefix(key, "secrets.") {
			if !allowSecrets {
				renderErr = errStore("template_secret_forbidden")
				return ""
			}
			name := strings.TrimPrefix(key, "secrets.")
			value, err := s.pipelineSecret(name)
			if err != nil {
				renderErr = err
				return ""
			}
			return value
		}
		value, ok := values[key]
		if !ok {
			renderErr = errStore("template_value_missing")
			return ""
		}
		return value
	})
	if renderErr != nil {
		return "", renderErr
	}
	if len(output) > pipelineTemplateLimit {
		return "", errStore("template_too_large")
	}
	return output, nil
}

func (s *Store) pipelineSecret(name string) (string, error) {
	if !pipelineSecretRE.MatchString(name) {
		return "", errStore("secret_invalid")
	}
	path := filepath.Join(s.secretsDir, "actions", name)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 8192 || info.Mode().Perm()&0o077 != 0 {
		return "", errStore("secret_unavailable")
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 8192 {
		return "", errStore("secret_unavailable")
	}
	value := strings.TrimRight(string(raw), "\r\n")
	if strings.ContainsRune(value, 0) {
		return "", errStore("secret_invalid")
	}
	return value, nil
}

func pipelineOutputLimit(spec *pipelineStep) int {
	if spec.MaxOutputBytes > 0 {
		return spec.MaxOutputBytes
	}
	return pipelineDefaultOutput
}

func pipelineStepTimeout(spec *pipelineStep) time.Duration {
	seconds := spec.TimeoutSeconds
	if seconds <= 0 {
		seconds = defaultPipelineStepTimeout
	}
	return time.Duration(seconds) * time.Second
}

func pipelineDigest(text string) string {
	digest := sha256.Sum256([]byte(text))
	return hex.EncodeToString(digest[:6])
}

func mergePipelineUsage(sum map[string]int64, usage map[string]any) {
	for _, key := range []string{"input_tokens", "cached_tokens", "output_tokens", "total_tokens", "requests"} {
		value, ok := usage[key].(int64)
		if !ok {
			if number, ok := usage[key].(float64); ok {
				value = int64(number)
			}
		}
		if value > 0 {
			sum[key] += value
		}
	}
}

func pipelineUsageMap(sum map[string]int64) map[string]any {
	result := map[string]any{
		"input_tokens":  sum["input_tokens"],
		"cached_tokens": sum["cached_tokens"],
		"output_tokens": sum["output_tokens"],
		"total_tokens":  sum["total_tokens"],
		"requests":      sum["requests"],
		"origin":        "pipeline",
		"source":        "reported",
	}
	return result
}

func (s *Store) runPipelineAgent(prompt string, spec *pipelineStep, limit int) (pipelineStepResult, error) {
	if len(prompt) > maxReportInputBytes {
		return pipelineStepResult{}, errStore("agent_prompt_too_large")
	}
	runner := strings.TrimSpace(spec.Runner)
	if runner == "" {
		runner = "codex"
	}
	if !s.runnerAvailable(runner) {
		return pipelineStepResult{}, errStore("agent_unavailable")
	}
	promptFile, err := os.CreateTemp(s.runtimeDir, "pipeline-prompt-*")
	if err != nil {
		return pipelineStepResult{}, errStore("agent_prepare_failed")
	}
	promptPath := promptFile.Name()
	defer os.Remove(promptPath)
	if err := promptFile.Chmod(0o600); err != nil {
		_ = promptFile.Close()
		return pipelineStepResult{}, errStore("agent_prepare_failed")
	}
	if _, err := promptFile.WriteString(prompt); err != nil {
		_ = promptFile.Close()
		return pipelineStepResult{}, errStore("agent_prepare_failed")
	}
	if err := promptFile.Close(); err != nil {
		return pipelineStepResult{}, errStore("agent_prepare_failed")
	}
	outputFile, err := os.CreateTemp(s.runtimeDir, "pipeline-report-*")
	if err != nil {
		return pipelineStepResult{}, errStore("agent_prepare_failed")
	}
	outputPath := outputFile.Name()
	_ = outputFile.Close()
	defer os.Remove(outputPath)
	command, err := s.installedFolioCommand()
	if err != nil {
		return pipelineStepResult{}, errStore("agent_prepare_failed")
	}
	timeoutSeconds := spec.TimeoutSeconds
	if timeoutSeconds <= 0 {
		timeoutSeconds = defaultPipelineAgentTimeout
	}
	command = append(command, "agent",
		"--report-prompt", promptPath,
		"--report-out", outputPath,
		"--runtime-dir", s.runtimeDir,
		"--runner", runner,
		"--timeout", strconv.Itoa(timeoutSeconds),
	)
	if spec.Model != "" {
		command = append(command, "--model", spec.Model)
		if runner == "dsh" && spec.Provider != "" {
			command = append(command, "--provider", spec.Provider)
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
	timeout := time.Duration(timeoutSeconds+120) * time.Second
	s.modelMu.Lock()
	_, runErr := s.runCommand(command, s.toolRoot, "", timeout, true, env)
	s.modelMu.Unlock()
	if runErr != nil {
		return pipelineStepResult{}, errStore("agent_run_failed")
	}
	info, statErr := os.Lstat(outputPath)
	if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxReportOutputBytes {
		return pipelineStepResult{}, errStore("agent_output_invalid")
	}
	raw, err := os.ReadFile(outputPath)
	if err != nil || len(raw) > maxReportOutputBytes {
		return pipelineStepResult{}, errStore("agent_output_invalid")
	}
	var parsed reportResult
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return pipelineStepResult{}, errStore("agent_output_invalid")
	}
	text := strings.TrimSpace(parsed.Text)
	if text == "" || len(text) > maxReportTextBytes || secretMaterialRE.MatchString(text) {
		return pipelineStepResult{}, errStore("agent_output_rejected")
	}
	text = notify.ClampBytes(text, limit)
	var usage map[string]any
	if normalized := reportUsage(parsed.Usage); normalized != nil {
		usage = normalized
	}
	provider := spec.Provider
	if provider == "" {
		if runner == "codex" {
			provider = "openai"
		} else if runner == "dsh" {
			provider = dshProviderForModel(spec.Model)
		}
	}
	return pipelineStepResult{Output: text, Usage: usage, Runner: runner, Model: spec.Model, Provider: provider}, nil
}

func (s *Store) runPipelineScript(spec *pipelineStep, plan actionPlan, task taskRecord, values map[string]string, runID string, limit int) (pipelineStepResult, error) {
	command := make([]string, 0, len(spec.Command))
	for index, raw := range spec.Command {
		value, err := s.renderPipeline(raw, values, false)
		if err != nil {
			return pipelineStepResult{}, err
		}
		if index == 0 {
			value = spec.Command[0]
		}
		command = append(command, value)
	}
	scriptsRoot := filepath.Join(s.dataDir, "scripts")
	resolved, err := filepath.EvalSymlinks(strings.TrimSpace(command[0]))
	if err != nil {
		return pipelineStepResult{}, errStore("script_unavailable")
	}
	resolvedRoot, err := filepath.EvalSymlinks(scriptsRoot)
	if err != nil || !isWithin(resolved, resolvedRoot) {
		return pipelineStepResult{}, errStore("script_unavailable")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return pipelineStepResult{}, errStore("script_unavailable")
	}
	command[0] = resolved
	cwd := scriptsRoot
	if spec.Cwd != "" {
		cwd = spec.Cwd
	}
	resolvedCwd, err := filepath.EvalSymlinks(cwd)
	if err != nil || !isWithin(resolvedCwd, s.dataDir) {
		return pipelineStepResult{}, errStore("script_unavailable")
	}
	stdin, err := s.renderPipeline(spec.Stdin, values, false)
	if err != nil {
		return pipelineStepResult{}, err
	}
	if len(stdin) > maxPipelineStdinBytes {
		return pipelineStepResult{}, errStore("script_input_too_large")
	}
	env := map[string]string{
		"FOLIO_RUN_ID":    runID,
		"FOLIO_STEP_ID":   spec.ID,
		"FOLIO_ACTION_ID": plan.ID,
		"FOLIO_TASK_ID":   task.ID,
	}
	for key, raw := range spec.Env {
		value, renderErr := s.renderPipeline(raw, values, true)
		if renderErr != nil {
			return pipelineStepResult{}, renderErr
		}
		env[key] = value
	}
	result, runErr := s.runCommandContext(context.Background(), command, resolvedCwd, stdin, pipelineStepTimeout(spec), false, env)
	if runErr != nil {
		if strings.Contains(runErr.Error(), "timed out") {
			return pipelineStepResult{}, errStore("script_timeout")
		}
		return pipelineStepResult{}, errStore("script_failed")
	}
	if result.truncated {
		return pipelineStepResult{}, errStore("script_output_too_large")
	}
	output := notify.ClampBytes(strings.TrimSpace(result.stdout), limit)
	if result.code != 0 {
		return pipelineStepResult{Output: output, ExitCode: result.code}, errStore("script_failed")
	}
	return pipelineStepResult{Output: output, ExitCode: result.code}, nil
}

func (s *Store) runPipelinePush(channel, title, body, actionID, runID string, limit int) (string, error) {
	settings, err := s.loadChannelSettings()
	if err != nil {
		return "", errStore("push_unavailable")
	}
	message := notify.Message{Title: title, Body: body}.Normalize()
	receipt := notify.Failed(channel, "unavailable")
	switch channel {
	case "weixin":
		sender := s.weixinSenderLocked()
		if sender == nil || !settings.Weixin.Enabled {
			return "", errStore("push_disabled")
		}
		ctx, cancel := context.WithTimeout(context.Background(), pushNotifyTimeout)
		defer cancel()
		receipt = weixinReceipt(sender.SendWeixinText(ctx, weixinText(message)))
	case "bark":
		if !settings.Bark.Enabled || strings.TrimSpace(settings.Bark.DeviceKey) == "" {
			return "", errStore("push_disabled")
		}
		sender, err := notify.NewBark(notify.BarkConfig{
			Server:    settings.Bark.Server,
			DeviceKey: settings.Bark.DeviceKey,
			Group:     settings.Bark.Group,
			Sound:     settings.Bark.Sound,
			Level:     settings.Bark.Level,
		})
		if err != nil {
			return "", errStore("push_unavailable")
		}
		ctx, cancel := context.WithTimeout(context.Background(), pushNotifyTimeout)
		defer cancel()
		receipt = sender.Send(ctx, message)
	default:
		return "", errStore("push_invalid")
	}
	s.recordDelivery("action:"+actionID, receipt, runID, message.Title)
	if receipt.Status != notify.StatusDelivered {
		return "", errStore("push_failed")
	}
	return notify.ClampBytes(receipt.Status, limit), nil
}

func (s *Store) runPipelineHTTP(spec *pipelineStep, plan actionPlan, values map[string]string, limit int) (pipelineStepResult, error) {
	rawURL, err := s.renderPipeline(spec.URL, values, false)
	if err != nil {
		return pipelineStepResult{}, err
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return pipelineStepResult{}, errStore("http_url_invalid")
	}
	allowed := false
	for _, host := range plan.HTTPHosts {
		if strings.EqualFold(host, parsed.Hostname()) {
			allowed = true
			break
		}
	}
	if !allowed {
		return pipelineStepResult{}, errStore("http_host_not_allowed")
	}
	body, err := s.renderPipeline(spec.Body, values, true)
	if err != nil {
		return pipelineStepResult{}, err
	}
	request, err := http.NewRequestWithContext(context.Background(), spec.Method, parsed.String(), strings.NewReader(body))
	if err != nil {
		return pipelineStepResult{}, errStore("http_request_invalid")
	}
	for key, raw := range spec.Headers {
		value, renderErr := s.renderPipeline(raw, values, true)
		if renderErr != nil {
			return pipelineStepResult{}, renderErr
		}
		if strings.ContainsAny(value, "\r\n") || strings.EqualFold(key, "Host") {
			return pipelineStepResult{}, errStore("http_header_invalid")
		}
		request.Header.Set(key, value)
	}
	if request.Header.Get("User-Agent") == "" {
		request.Header.Set("User-Agent", "folio-action")
	}
	client := &http.Client{
		Timeout: pipelineStepTimeout(spec),
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return pipelineStepResult{}, errStore("http_failed")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, int64(limit)+1))
	if err != nil {
		return pipelineStepResult{}, errStore("http_failed")
	}
	if len(raw) > limit {
		return pipelineStepResult{}, errStore("http_output_too_large")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return pipelineStepResult{}, errStore("http_status_" + strconv.Itoa(response.StatusCode))
	}
	return pipelineStepResult{Output: notify.ClampBytes(strings.TrimSpace(string(raw)), limit)}, nil
}

func (s *Store) runPipelineTask(spec *pipelineStep, plan actionPlan, values map[string]string) (pipelineStepResult, error) {
	if spec.Action == plan.ID {
		return pipelineStepResult{}, errStore("task_recursion")
	}
	title, err := s.renderPipeline(spec.Title, values, false)
	if err != nil {
		return pipelineStepResult{}, err
	}
	if strings.TrimSpace(title) == "" {
		title = "Follow-up: " + plan.Label
	}
	payload := map[string]any{}
	for key, value := range spec.Payload {
		if text, ok := value.(string); ok {
			rendered, renderErr := s.renderPipeline(text, values, false)
			if renderErr != nil {
				return pipelineStepResult{}, renderErr
			}
			payload[key] = rendered
			continue
		}
		payload[key] = value
	}
	created, err := s.CreateTask(spec.Action, "action:"+plan.ID, title, spec.Priority, payload)
	if err != nil {
		return pipelineStepResult{}, errStore("task_create_failed")
	}
	task, _ := created["task"].(map[string]any)
	id, _ := task["id"].(string)
	return pipelineStepResult{Output: id}, nil
}
