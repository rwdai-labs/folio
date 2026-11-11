package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	maxAgentInput      = 16 * 1024
	maxAgentOutput     = 32 * 1024
	maxAgentSession    = 128 * 1024
	maxAgentMessages   = 40
	maxAgentRounds     = 6
	maxAgentToolCalls  = 8
	maxAgentToolArgs   = 16 * 1024
	maxAgentToolResult = 64 * 1024
	maxAgentRequest    = 256 * 1024
	// A feedback intake turn attaches one re-encoded screenshot. The image is
	// downscaled and JPEG-encoded before it reaches this bound, so the request
	// stays a small multiple of the 1.5 MiB source budget.
	maxAgentImageRequest = 8 * 1024 * 1024
	maxAgentRuntimeOut   = 2 * 1024 * 1024
	agentRuntimeTimeout  = 120 * time.Second
)

var agentSessionRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// AgentRuntime is the main-agent adapter boundary. Folio owns the tool loop,
// authorization and audit; the runtime only proposes text and tool calls.
// A deployment can back it with pi, a command wrapper, or a test fake.
type AgentRuntime interface {
	Available() bool
	Turn(context.Context, AgentRequest) (AgentResponse, error)
}

type AgentRequest struct {
	Session string         `json:"session"`
	Input   string         `json:"input"`
	History []AgentMessage `json:"history"`
	Tools   []AgentTool    `json:"tools"`
	Event   map[string]any `json:"event,omitempty"`
	// System overrides the default main-agent framing for a scoped turn such as
	// feedback intake. The host still owns the tool grants and execution.
	System string `json:"system,omitempty"`
	// ImageDataURL attaches one bounded PNG/JPEG data URL to the latest user
	// message. It is never persisted with the session.
	ImageDataURL string `json:"image_data_url,omitempty"`
	// ToolChoice is "auto" (default), "required" or "none". Feedback intake
	// uses "required" so the model must record a verdict through a host tool.
	ToolChoice string `json:"tool_choice,omitempty"`
}

type AgentMessage struct {
	Role       string          `json:"role"`
	Name       string          `json:"name,omitempty"`
	Content    string          `json:"content"`
	ToolCalls  []AgentToolCall `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

type AgentTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type AgentToolCall struct {
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

type AgentResponse struct {
	Text           string          `json:"text"`
	ToolCalls      []AgentToolCall `json:"tool_calls,omitempty"`
	Usage          map[string]any  `json:"usage,omitempty"`
	ServedProvider string          `json:"served_provider,omitempty"`
	ServedModel    string          `json:"served_model,omitempty"`
}

type AgentEvent struct {
	Type    string
	Text    string
	Tool    string
	Status  string
	Summary string
	Code    string
	RunID   string
	TaskID  string
	Title   string
}

func (e AgentEvent) Map() map[string]any {
	value := map[string]any{"type": e.Type}
	if e.Text != "" {
		value["text"] = e.Text
	}
	if e.Tool != "" {
		value["tool"] = e.Tool
	}
	if e.Status != "" {
		value["status"] = e.Status
	}
	if e.Summary != "" {
		value["summary"] = e.Summary
	}
	if e.Code != "" {
		value["code"] = e.Code
	}
	if e.RunID != "" {
		value["run_id"] = e.RunID
	}
	if e.TaskID != "" {
		value["task_id"] = e.TaskID
		value["title"] = e.Title
	}
	return value
}

type AgentTurnResult struct {
	Text  string
	RunID string
	Usage map[string]any
}

type agentSessionFile struct {
	Version   int            `json:"version"`
	UpdatedAt string         `json:"updated_at"`
	Messages  []AgentMessage `json:"messages"`
}

// SetAgentRuntime wires the main-agent adapter once during startup. Tests use
// it with a fake; production uses the command adapter below.
func (s *Store) SetAgentRuntime(runtime AgentRuntime) {
	s.agentMu.Lock()
	defer s.agentMu.Unlock()
	s.agentRuntime = runtime
}

func (s *Store) AgentAvailable() bool {
	s.agentMu.Lock()
	defer s.agentMu.Unlock()
	runtime, _, err := s.boundAgentRuntime()
	return err == nil && runtime != nil && runtime.Available()
}

// AgentHistory returns bounded persisted messages for one console session.
// AgentRoute reports where the owner message route points. The caller falls
// back to the legacy path when the owner disabled the agent route.
func (s *Store) AgentRoute(source, typ string) (string, bool, error) {
	return s.eventRouteFor(event{Source: source, Type: typ, Trust: eventTrustOwner})
}

func (s *Store) AgentHistory(session string) (map[string]any, error) {
	session = strings.TrimSpace(session)
	if !agentSessionRE.MatchString(session) {
		return nil, errStore("agent_session_invalid")
	}
	file, err := s.loadAgentSession(session)
	if err != nil {
		return nil, err
	}
	messages := make([]any, 0, len(file.Messages))
	for _, message := range file.Messages {
		messages = append(messages, map[string]any{
			"role": message.Role, "name": message.Name, "content": message.Content,
		})
	}
	tasks := []any{}
	s.taskMu.Lock()
	queue, queueErr := s.loadTasksLocked()
	if queueErr == nil {
		for _, task := range queue.Tasks {
			if task.Source == "agent:"+session {
				tasks = append(tasks, map[string]any{"task_id": task.ID, "title": publicText(task.Title, maxTaskTitleRunes), "status": task.Status})
			}
		}
	}
	s.taskMu.Unlock()
	return map[string]any{"session": session, "messages": messages, "tasks": tasks, "updated_at": file.UpdatedAt}, nil
}

// AgentTurnMap adapts the typed event stream to the Web boundary.
func (s *Store) AgentTurnMap(ctx context.Context, session, text, trust string, emit func(map[string]any)) (map[string]any, error) {
	result, err := s.AgentTurn(ctx, session, text, trust, func(event AgentEvent) {
		if emit != nil {
			emit(event.Map())
		}
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"text": result.Text, "run_id": result.RunID, "usage": result.Usage}, nil
}

// agentTurnOptions scopes one host turn. Zero values preserve the owner-console
// behavior: default tools, default system prompt and no image.
type agentTurnOptions struct {
	Session      string
	Input        string
	Trust        string
	System       string
	Tools        []AgentTool
	ImageDataURL string
	RequireTool  bool
	StopTools    map[string]bool
}

// AgentTurn runs one bounded main-agent turn. It loads session history, calls
// the runtime, executes only granted tools, and records one audit run.
func (s *Store) AgentTurn(ctx context.Context, session, text, trust string, emit func(AgentEvent)) (AgentTurnResult, error) {
	return s.agentTurn(ctx, agentTurnOptions{Session: session, Input: text, Trust: trust}, emit)
}

func (s *Store) agentTurn(ctx context.Context, opts agentTurnOptions, emit func(AgentEvent)) (AgentTurnResult, error) {
	text := strings.TrimSpace(opts.Input)
	trust := opts.Trust
	session := strings.TrimSpace(opts.Session)
	if text == "" || len([]byte(text)) > maxAgentInput {
		return AgentTurnResult{}, errStore("agent_input_invalid")
	}
	if !validEventTrust(trust) {
		return AgentTurnResult{}, errStore("agent_trust_invalid")
	}
	if session == "" {
		session = "console-main"
	}
	if !agentSessionRE.MatchString(session) {
		return AgentTurnResult{}, errStore("agent_session_invalid")
	}
	s.agentMu.Lock()
	defer s.agentMu.Unlock()
	runtime, runtimeConfig, runtimeErr := s.boundAgentRuntime()
	if runtimeErr != nil || runtime == nil || !runtime.Available() {
		return AgentTurnResult{}, errStore("agent_unavailable")
	}
	tools := opts.Tools
	if tools == nil {
		tools = s.agentTools(trust)
	}
	send := func(event AgentEvent) {
		if emit != nil {
			emit(event)
		}
	}
	started := time.Now()
	runID, err := newAutoRunID()
	if err != nil {
		return AgentTurnResult{}, errStore("agent_run_failed")
	}
	sessionFile, err := s.loadAgentSession(session)
	if err != nil {
		return AgentTurnResult{}, err
	}
	history := append([]AgentMessage(nil), sessionFile.Messages...)
	history = append(history, AgentMessage{Role: "user", Content: text})
	toolAudit := make([]any, 0, maxAgentToolCalls)
	modelUsage := make([]any, 0, maxAgentRounds)
	usage := map[string]int64{}
	send(AgentEvent{Type: "turn.started"})
	run := map[string]any{
		"version":     1,
		"run_id":      runID,
		"trigger":     "agent",
		"status":      "running",
		"stage":       "agent",
		"started_at":  started.UTC().Format(time.RFC3339Nano),
		"runner":      "agent",
		"session":     session,
		"trust":       trust,
		"summary":     "",
		"duration_ms": 0,
	}
	if runtimeConfig != nil {
		run["model"] = runtimeConfig.Model
		run["provider"] = runtimeConfig.Provider
		run["agent_kind"] = runtimeConfig.Kind
	}
	finish := func(status, reason, final string) (AgentTurnResult, error) {
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
		if final != "" {
			run["summary"] = publicText(firstParagraph(final), maxReportSummary)
		}
		if len(toolAudit) > 0 {
			run["tool_calls"] = toolAudit
		}
		var usageMap map[string]any
		if len(usage) > 0 {
			usageMap = pipelineUsageMap(usage)
			run["usage"] = usageMap
		}
		if len(modelUsage) > 0 {
			run["model_usage"] = modelUsage
		}
		_ = s.saveAutomationRun(run)
		send(AgentEvent{Type: "turn.finished", Status: status, Code: reason, RunID: runID})
		return AgentTurnResult{Text: final, RunID: runID, Usage: usageMap}, nil
	}
	for round := 0; round < maxAgentRounds; round++ {
		if ctx.Err() != nil {
			return finish("canceled", "agent_canceled", "")
		}
		toolChoice := ""
		if opts.RequireTool && round == 0 {
			toolChoice = "required"
		}
		response, callErr := runtime.Turn(ctx, AgentRequest{
			Session:      session,
			Input:        text,
			History:      history,
			Tools:        tools,
			Event:        map[string]any{"type": "owner.message", "trust": trust},
			System:       opts.System,
			ImageDataURL: opts.ImageDataURL,
			ToolChoice:   toolChoice,
		})
		if normalized := reportUsage(response.Usage); normalized != nil {
			mergePipelineUsage(usage, normalized)
			if runtimeConfig != nil && runtimeConfig.Kind == "openai" {
				modelUsage = append(modelUsage, map[string]any{"provider": response.ServedProvider, "model": response.ServedModel, "requested_model": runtimeConfig.Model, "usage": normalized})
			}
		}
		if callErr != nil {
			code := "agent_runtime_failed"
			if strings.Contains(callErr.Error(), "timed out") {
				code = "agent_runtime_timeout"
			}
			send(AgentEvent{Type: "error", Code: code})
			return finish("failed", code, "")
		}
		if response.Text != "" {
			if len([]byte(response.Text)) > maxAgentOutput {
				return finish("failed", "agent_output_too_large", "")
			}
		}

		if len(response.ToolCalls) == 0 {
			final := strings.TrimSpace(response.Text)
			if final == "" {
				return finish("failed", "agent_empty", "")
			}
			history = append(history, AgentMessage{Role: "assistant", Content: final})
			if err := s.saveAgentSession(session, history); err != nil {
				return finish("failed", "agent_session_failed", "")
			}
			send(AgentEvent{Type: "text.delta", Text: final})
			return finish("reported", "", final)
		}
		if len(response.ToolCalls) > maxAgentToolCalls {
			return finish("failed", "agent_tool_limit", "")
		}
		for index := range response.ToolCalls {
			if response.ToolCalls[index].ID == "" {
				response.ToolCalls[index].ID = fmt.Sprintf("call_%d_%d", round, index)
			}
		}
		// Keep the assistant tool-call message in history: OpenAI-compatible
		// runtimes require it to be paired with each tool result by id.
		history = append(history, AgentMessage{Role: "assistant", Content: response.Text, ToolCalls: response.ToolCalls})
		stopped := false
		for _, call := range response.ToolCalls {
			args, _ := json.Marshal(call.Arguments)
			if len(args) > maxAgentToolArgs {
				return finish("failed", "agent_tool_args_too_large", "")
			}
			send(AgentEvent{Type: "tool.call", Tool: call.Name, Status: "running"})
			stepStart := time.Now()
			result, toolErr := s.runAgentTool(ctx, call, trust, session, tools)
			status := "completed"
			if toolErr != nil {
				status = "failed"
			} else if ok, _ := result["ok"].(bool); !ok {
				status = "failed"
			}
			if call.Name == "task.create" && status == "completed" {
				created := mapValue(result, "value")
				task := mapValue(created, "task")
				id := responseString(task, "id")
				if taskIDRE.MatchString(id) {
					send(AgentEvent{Type: "task.created", TaskID: id, Title: publicText(task["title"], maxTaskTitleRunes), Status: responseString(task, "status")})
				}
			}
			resultText, _ := json.Marshal(result)
			if len(resultText) > maxAgentToolResult {
				resultText = []byte(`{"ok":false,"error":"tool_result_too_large"}`)
				status = "failed"
			}
			toolAudit = append(toolAudit, map[string]any{
				"tool":        call.Name,
				"status":      status,
				"duration_ms": time.Since(stepStart).Milliseconds(),
			})
			send(AgentEvent{Type: "tool.result", Tool: call.Name, Status: status,
				Summary: publicText(toolResultSummary(result), 200)})
			history = append(history, AgentMessage{Role: "tool", Name: call.Name, ToolCallID: call.ID, Content: string(resultText)})
			if status == "completed" && opts.StopTools[call.Name] {
				stopped = true
				break
			}
		}
		if stopped {
			if err := s.saveAgentSession(session, history); err != nil {
				return finish("failed", "agent_session_failed", "")
			}
			final := strings.TrimSpace(response.Text)
			if final != "" {
				send(AgentEvent{Type: "text.delta", Text: final})
			}
			return finish("reported", "", final)
		}
	}
	return finish("failed", "agent_tool_limit", "")
}

func toolResultSummary(result map[string]any) string {
	if errText, ok := result["error"].(string); ok {
		return errText
	}
	if value, ok := result["summary"].(string); ok {
		return value
	}
	return "ok"
}

func (s *Store) agentSessionPath(session string) (string, error) {
	if !agentSessionRE.MatchString(session) {
		return "", errStore("agent_session_invalid")
	}
	return filepath.Join(s.agentSessions, session+".json"), nil
}

func (s *Store) loadAgentSession(session string) (agentSessionFile, error) {
	path, err := s.agentSessionPath(session)
	if err != nil {
		return agentSessionFile{}, err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return agentSessionFile{Version: 1, Messages: []AgentMessage{}}, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxAgentSession {
		return agentSessionFile{}, errStore("agent_session_unavailable")
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > maxAgentSession {
		return agentSessionFile{}, errStore("agent_session_unavailable")
	}
	var file agentSessionFile
	if err := json.Unmarshal(raw, &file); err != nil || file.Version != 1 || len(file.Messages) > maxAgentMessages {
		return agentSessionFile{}, errStore("agent_session_invalid")
	}
	return file, nil
}

func (s *Store) saveAgentSession(session string, messages []AgentMessage) error {
	path, err := s.agentSessionPath(session)
	if err != nil {
		return err
	}
	if len(messages) > maxAgentMessages {
		messages = messages[len(messages)-maxAgentMessages:]
	}
	file := agentSessionFile{
		Version:   1,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Messages:  messages,
	}
	raw, err := json.Marshal(file)
	if err != nil || len(raw) > maxAgentSession {
		return errStore("agent_session_too_large")
	}
	if err := os.MkdirAll(s.agentSessions, 0o700); err != nil {
		return errStore("agent_session_unavailable")
	}
	if err := writeAtomic(path, append(raw, byte(10)), 0o600); err != nil {
		return errStore("agent_session_unavailable")
	}
	return nil
}

func agentSessionID(channel, account, conversation string) string {
	digest := sha256.Sum256([]byte(channel + "\x00" + account + "\x00" + conversation))
	return "weixin-" + hex.EncodeToString(digest[:8])
}

// commandAgentRuntime is the adapter boundary for an installed agent wrapper.
// The wrapper receives one JSON request on stdin and returns one JSON response
// on stdout. A pi-backed wrapper belongs here; the adapter itself is unverified
// until a real pi installation is exercised.
type commandAgentRuntime struct {
	store *Store
	path  string
	args  []string
}

func (r *commandAgentRuntime) Available() bool {
	if r == nil || r.store == nil {
		return false
	}
	info, err := os.Lstat(r.path)
	return err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm()&0o111 != 0
}

func (r *commandAgentRuntime) Turn(ctx context.Context, request AgentRequest) (AgentResponse, error) {
	if !r.Available() {
		return AgentResponse{}, errStore("agent_unavailable")
	}
	raw, err := json.Marshal(request)
	if err != nil || len(raw) > maxAgentRequest {
		return AgentResponse{}, errStore("agent_request_invalid")
	}
	env := map[string]string{
		"FOLIO_CONTEXT_ROOT": r.store.repositoryDir,
		"FOLIO_RUNTIME":      r.store.runtimeDir,
		"CODEX_HOME":         r.store.codexDir,
		"DSH_HOME":           r.store.dshDir,
	}
	command := append([]string{r.path}, r.args...)
	result, err := r.store.runCommandContext(ctx, command, r.store.dataDir, string(raw), agentRuntimeTimeout, false, env)
	if err != nil {
		if strings.Contains(err.Error(), "timed out") {
			return AgentResponse{}, errStore("agent runtime timed out")
		}
		return AgentResponse{}, errStore("agent runtime failed")
	}
	if result.code != 0 || result.truncated || len(result.stdout) > maxAgentRuntimeOut {
		return AgentResponse{}, errStore("agent runtime failed")
	}
	var response AgentResponse
	if err := json.Unmarshal([]byte(result.stdout), &response); err != nil {
		return AgentResponse{}, errStore("agent output invalid")
	}
	if len(response.ToolCalls) > maxAgentToolCalls || len([]byte(response.Text)) > maxAgentOutput {
		return AgentResponse{}, errStore("agent output invalid")
	}
	for _, call := range response.ToolCalls {
		if !agentToolNameRE.MatchString(call.Name) {
			return AgentResponse{}, errStore("agent output invalid")
		}
	}
	return response, nil
}

var agentToolNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){0,2}$`)
