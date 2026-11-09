package store

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const (
	maxAgentConfigBytes = 16 * 1024
	maxAgentKeyBytes    = 4096
	maxAgentArgs        = 16
	maxAgentArgBytes    = 256
	defaultAgentTimeout = 120
	defaultAgentOutput  = 2048
)

var agentProviderRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// agentConfig is owner configuration for the main-agent adapter. The HTTP
// variant is OpenAI-compatible; the command variant preserves the original
// stdin/stdout adapter for a pi wrapper or another local process.
type agentConfig struct {
	Version         int      `json:"version"`
	Kind            string   `json:"kind"`
	Provider        string   `json:"provider,omitempty"`
	BaseURL         string   `json:"base_url,omitempty"`
	Model           string   `json:"model,omitempty"`
	TimeoutSeconds  int      `json:"timeout_seconds,omitempty"`
	MaxOutputTokens int      `json:"max_output_tokens,omitempty"`
	Command         string   `json:"command,omitempty"`
	Args            []string `json:"args,omitempty"`

	// source is runtime metadata, not part of the persisted file.
	source string
}

func defaultAgentConfig(dataDir string) *agentConfig {
	return &agentConfig{
		Version:        1,
		Kind:           "command",
		Command:        filepath.Join(dataDir, "agent", "runtime"),
		TimeoutSeconds: defaultAgentTimeout,
		source:         "default",
	}
}

// loadAgentConfig reads the owner file first, then the documented environment
// fallback. A malformed file fails closed instead of silently switching to a
// different runtime.
func (s *Store) loadAgentConfig() (*agentConfig, string) {
	if info, err := os.Lstat(s.agentConfigPath); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxAgentConfigBytes || info.Mode().Perm()&0o077 != 0 {
			return nil, "agent_config_invalid"
		}
		raw, err := os.ReadFile(s.agentConfigPath)
		if err != nil || len(raw) > maxAgentConfigBytes {
			return nil, "agent_config_invalid"
		}
		var cfg agentConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, "agent_config_invalid"
		}
		cfg.source = "file"
		if err := validateAgentConfig(&cfg); err != nil {
			return nil, "agent_config_invalid"
		}
		return &cfg, ""
	}
	if kind := strings.TrimSpace(os.Getenv("FOLIO_AGENT_KIND")); kind != "" {
		cfg := &agentConfig{
			Version:        1,
			Kind:           strings.ToLower(kind),
			Provider:       strings.TrimSpace(os.Getenv("FOLIO_AGENT_PROVIDER")),
			BaseURL:        strings.TrimSpace(os.Getenv("FOLIO_AGENT_BASE_URL")),
			Model:          strings.TrimSpace(os.Getenv("FOLIO_AGENT_MODEL")),
			TimeoutSeconds: agentEnvInt("FOLIO_AGENT_TIMEOUT_SECONDS", defaultAgentTimeout),
			Command:        strings.TrimSpace(os.Getenv("FOLIO_AGENT_COMMAND")),
			source:         "environment",
		}
		if err := validateAgentConfig(cfg); err != nil {
			return nil, "agent_config_invalid"
		}
		return cfg, ""
	}
	return defaultAgentConfig(s.dataDir), ""
}

func validateAgentConfig(cfg *agentConfig) error {
	if cfg == nil || cfg.Version != 1 {
		return errStore("agent_config_invalid")
	}
	cfg.Kind = strings.TrimSpace(strings.ToLower(cfg.Kind))
	cfg.Provider = strings.TrimSpace(strings.ToLower(cfg.Provider))
	cfg.BaseURL = strings.TrimSpace(cfg.BaseURL)
	cfg.Model = strings.TrimSpace(cfg.Model)
	cfg.Command = strings.TrimSpace(cfg.Command)
	if cfg.TimeoutSeconds == 0 {
		cfg.TimeoutSeconds = defaultAgentTimeout
	}
	if cfg.TimeoutSeconds < 10 || cfg.TimeoutSeconds > 600 {
		return errStore("agent_config_invalid")
	}
	if cfg.Provider != "" && !agentProviderRE.MatchString(cfg.Provider) {
		return errStore("agent_config_invalid")
	}
	switch cfg.Kind {
	case "openai":
		if cfg.Model == "" || !modelRE.MatchString(cfg.Model) {
			return errStore("agent_config_invalid")
		}
		parsed, err := url.Parse(cfg.BaseURL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
			parsed.RawQuery != "" || parsed.Fragment != "" || strings.ContainsAny(cfg.BaseURL, "\r\n\x00") {
			return errStore("agent_config_invalid")
		}
		cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
		if parsed.Path == "" {
			// A host-only base is valid; chatCompletionsURL appends the route.
		}
		if cfg.MaxOutputTokens == 0 {
			cfg.MaxOutputTokens = defaultAgentOutput
		}
		if cfg.MaxOutputTokens < 32 || cfg.MaxOutputTokens > 8192 {
			return errStore("agent_config_invalid")
		}
		if cfg.Provider == "" {
			cfg.Provider = inferAgentProvider(parsed.Hostname())
		}
		cfg.Command, cfg.Args = "", nil
	case "command":
		if cfg.Command == "" || !filepath.IsAbs(cfg.Command) || strings.ContainsAny(cfg.Command, "\r\n\x00") {
			return errStore("agent_config_invalid")
		}
		if len(cfg.Args) > maxAgentArgs {
			return errStore("agent_config_invalid")
		}
		for _, arg := range cfg.Args {
			if len([]byte(arg)) > maxAgentArgBytes || strings.ContainsRune(arg, 0) {
				return errStore("agent_config_invalid")
			}
		}
		cfg.BaseURL, cfg.Model, cfg.Provider = "", "", ""
	default:
		return errStore("agent_config_invalid")
	}
	return nil
}

func inferAgentProvider(host string) string {
	host = strings.ToLower(host)
	switch {
	case strings.Contains(host, "deepseek"):
		return "deepseek"
	case strings.Contains(host, "openrouter"):
		return "openrouter"
	case strings.Contains(host, "anthropic"):
		return "anthropic"
	case strings.Contains(host, "openai"):
		return "openai"
	default:
		return ""
	}
}

func (s *Store) applyAgentConfig(cfg *agentConfig) error {
	if cfg == nil {
		s.agentRuntime = nil
		return nil
	}
	switch cfg.Kind {
	case "openai":
		key, err := s.readAgentKey()
		if err != nil {
			s.agentRuntime = nil
			return err
		}
		s.agentRuntime = newOpenAIAgentRuntime(cfg, key)
	case "command":
		s.agentRuntime = &commandAgentRuntime{store: s, path: cfg.Command, args: append([]string(nil), cfg.Args...)}
	default:
		s.agentRuntime = nil
	}
	return nil
}

func (s *Store) readAgentKey() (string, error) {
	info, err := os.Lstat(s.agentKeyPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxAgentKeyBytes || info.Mode().Perm()&0o077 != 0 {
		return "", errStore("agent_key_unavailable")
	}
	raw, err := os.ReadFile(s.agentKeyPath)
	if err != nil || len(raw) > maxAgentKeyBytes {
		return "", errStore("agent_key_unavailable")
	}
	key := strings.TrimSpace(string(raw))
	if key == "" || strings.ContainsAny(key, "\r\n\x00") {
		return "", errStore("agent_key_unavailable")
	}
	return key, nil
}

// AgentConfig returns the safe configuration surface. It never returns the
// API key, the key path, or a runtime error payload.
func (s *Store) AgentConfig() map[string]any {
	s.agentMu.Lock()
	defer s.agentMu.Unlock()
	return s.agentConfigLocked()
}

func (s *Store) agentConfigLocked() map[string]any {
	runtime, cfg, bindingErr := s.boundAgentRuntime()
	status := map[string]any{
		"configured": cfg != nil, "available": bindingErr == nil && runtime != nil && runtime.Available(),
		"kind": "custom", "provider": "", "base_url": "", "model": "",
		"timeout_seconds": defaultAgentTimeout, "max_output_tokens": defaultAgentOutput,
		"command": "", "args": []string{}, "key_present": s.hasAgentKey(),
	}
	if bindingErr != nil {
		status["error"] = "model_services_invalid"
	} else if cfg != nil && cfg.source == "model-services" {
		status["key_present"] = runtime != nil && runtime.Available()
	} else if s.agentConfigError != "" {
		status["error"] = s.agentConfigError
	}
	if cfg == nil {
		return status
	}
	status["configured"] = true
	status["kind"] = cfg.Kind
	status["provider"] = cfg.Provider
	status["base_url"] = cfg.BaseURL
	status["model"] = cfg.Model
	status["timeout_seconds"] = cfg.TimeoutSeconds
	status["max_output_tokens"] = cfg.MaxOutputTokens
	status["command"] = cfg.Command
	status["args"] = cfg.Args
	status["source"] = cfg.source
	if cfg.Kind == "openai" {
		status["key_required"] = true
	}
	return status
}

func (s *Store) hasAgentKey() bool {
	info, err := os.Lstat(s.agentKeyPath)
	return err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 && info.Size() <= maxAgentKeyBytes && info.Mode().Perm()&0o077 == 0
}

// PutAgentConfig validates and persists owner configuration, writes a supplied
// API key to the private secret file, and swaps the live adapter atomically.
func (s *Store) PutAgentConfig(payload map[string]any) (map[string]any, error) {
	cfg, err := agentConfigFromMap(payload)
	if err != nil {
		return nil, err
	}
	s.agentMu.Lock()
	defer s.agentMu.Unlock()
	if err := ensurePrivateDir(filepath.Dir(s.agentConfigPath)); err != nil {
		return nil, errStore("agent_config_unavailable")
	}
	if err := ensurePrivateDir(filepath.Dir(s.agentKeyPath)); err != nil {
		return nil, errStore("agent_config_unavailable")
	}
	if rawKey, ok := payload["api_key"].(string); ok && strings.TrimSpace(rawKey) != "" {
		key := strings.TrimSpace(rawKey)
		if len([]byte(key)) > maxAgentKeyBytes || strings.ContainsAny(key, "\r\n\x00") {
			return nil, errStore("agent_key_invalid")
		}
		if err := writeAtomic(s.agentKeyPath, []byte(key+"\n"), 0o600); err != nil {
			return nil, errStore("agent_key_unavailable")
		}
	}
	if clear, _ := payload["clear_api_key"].(bool); clear {
		_ = os.Remove(s.agentKeyPath)
	}
	raw, err := json.Marshal(cfg)
	if err != nil || len(raw) > maxAgentConfigBytes {
		return nil, errStore("agent_config_invalid")
	}
	cfg.source = "file"
	if err := writeAtomic(s.agentConfigPath, append(raw, '\n'), 0o600); err != nil {
		return nil, errStore("agent_config_unavailable")
	}
	s.agentConfig = cfg
	s.agentConfigError = ""
	if err := s.applyAgentConfig(cfg); err != nil {
		s.agentConfigError = err.Error()
	}
	return s.agentConfigLocked(), nil
}

func agentConfigFromMap(payload map[string]any) (*agentConfig, error) {
	cfg := &agentConfig{Version: 1}
	readString := func(key string) string {
		if value, ok := payload[key].(string); ok {
			return strings.TrimSpace(value)
		}
		return ""
	}
	cfg.Kind = readString("kind")
	cfg.Provider = readString("provider")
	cfg.BaseURL = readString("base_url")
	cfg.Model = readString("model")
	cfg.Command = readString("command")
	cfg.TimeoutSeconds = agentNumber(payload["timeout_seconds"])
	cfg.MaxOutputTokens = agentNumber(payload["max_output_tokens"])
	if raw, ok := payload["args"].([]any); ok {
		if len(raw) > maxAgentArgs {
			return nil, errStore("agent_config_invalid")
		}
		for _, item := range raw {
			arg, ok := item.(string)
			if !ok {
				return nil, errStore("agent_config_invalid")
			}
			cfg.Args = append(cfg.Args, arg)
		}
	}
	if err := validateAgentConfig(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func agentNumber(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	default:
		return 0
	}
}

func agentEnvInt(name string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(name))
	if err != nil || value < 10 || value > 600 {
		return fallback
	}
	return value
}
