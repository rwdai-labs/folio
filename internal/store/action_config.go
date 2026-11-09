package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Runtime actions are owner configuration, not code. An action is a bounded,
// ordered list of steps over the primitive set below. The file is private,
// validated as a whole, and never read from an event body.
const (
	maxPipelineFileBytes        = 256 * 1024
	maxPipelines                = 64
	maxPipelineSteps            = 16
	maxPipelineLabelRunes       = 64
	maxPipelinePromptRunes      = 8000
	maxPipelineDescriptionRunes = 1000
	maxActionCatalogGroupRunes  = 32
	maxActionCatalogSubmitRunes = 64
	maxActionCatalogFields      = 8
	maxActionCatalogFieldLabel  = 120
	// Declared catalog fields are owner input. They use the explicit bounded
	// owner-action payload path rather than the 512-rune hook projection.
	maxActionCatalogTextLength  = maxReportRequestRunes
	maxActionCatalogOptions     = 20
	maxActionCatalogOptionRunes = 64
	maxActionCatalogPlaceholder = 120
	maxPipelineHTTPHosts        = 32
	maxPipelineOutputBytes      = 1024 * 1024
	maxPipelineStdinBytes       = 64 * 1024
	maxPipelineEnv              = 16
	maxPipelineHeaders          = 16
	maxPipelineVersions         = 40

	defaultPipelineStepTimeout  = 300
	defaultPipelineAgentTimeout = 900
	maxPipelineStepTimeout      = 3600
)

var (
	pipelineStepRE   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	pipelineSecretRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	pipelineEnvRE    = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	pipelineHeaderRE = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	pipelineHostRE   = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?$`)
	actionFieldRE    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
)

// pipelineFile is /data/actions.json, version 2. A missing file is an empty
// registry; existing task-kinds.json files keep working as legacy actions.
type pipelineFile struct {
	Version   int                  `json:"version"`
	HTTPHosts []string             `json:"http_hosts,omitempty"`
	Actions   []pipelineDefinition `json:"actions"`
}

type pipelineDefinition struct {
	ID           string                `json:"id"`
	Label        string                `json:"label"`
	Description  string                `json:"description,omitempty"`
	Priority     string                `json:"priority,omitempty"`
	Lane         string                `json:"lane,omitempty"`
	Enabled      *bool                 `json:"enabled,omitempty"`
	Requires     []string              `json:"requires,omitempty"`
	Presentation *pipelinePresentation `json:"presentation,omitempty"`
	ReportFrom   string                `json:"report_from,omitempty"`
	Steps        []pipelineStep        `json:"steps"`
}

// pipelinePresentation is optional owner-facing metadata for the action
// catalog. It never changes execution: fields describe the run form, while the
// pipeline steps remain private to the owner action editor.
type pipelinePresentation struct {
	Group       string          `json:"group,omitempty"`
	Order       *int            `json:"order,omitempty"`
	SubmitLabel string          `json:"submit_label,omitempty"`
	Fields      []pipelineField `json:"fields,omitempty"`
}

type pipelineField struct {
	Name        string           `json:"name"`
	Label       string           `json:"label"`
	Type        string           `json:"type"`
	Required    bool             `json:"required,omitempty"`
	MaxLength   int              `json:"max_length,omitempty"`
	Options     []pipelineOption `json:"options,omitempty"`
	Placeholder string           `json:"placeholder,omitempty"`
}

type pipelineOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type pipelineStep struct {
	ID             string            `json:"id"`
	Type           string            `json:"type"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
	MaxOutputBytes int               `json:"max_output_bytes,omitempty"`
	OnError        string            `json:"on_error,omitempty"`
	Query          string            `json:"query,omitempty"`
	Prompt         string            `json:"prompt,omitempty"`
	Runner         string            `json:"runner,omitempty"`
	Model          string            `json:"model,omitempty"`
	Provider       string            `json:"provider,omitempty"`
	Command        []string          `json:"command,omitempty"`
	Cwd            string            `json:"cwd,omitempty"`
	Stdin          string            `json:"stdin,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	Channel        string            `json:"channel,omitempty"`
	Title          string            `json:"title,omitempty"`
	Body           string            `json:"body,omitempty"`
	Method         string            `json:"method,omitempty"`
	URL            string            `json:"url,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	Action         string            `json:"action,omitempty"`
	Priority       string            `json:"priority,omitempty"`
	Payload        map[string]any    `json:"payload,omitempty"`
}

func pipelinePathFor(data string) string { return filepath.Join(data, "actions.json") }

func pipelineVersionsDirFor(data string) string {
	return filepath.Join(data, "runtime", "action-versions")
}

func (p pipelineDefinition) isEnabled() bool {
	return p.Enabled == nil || *p.Enabled
}

func (p pipelineDefinition) defaultPriority() string {
	if p.Priority != "" {
		return p.Priority
	}
	return "P2"
}

// loadPipelines reads and validates the private registry. A missing file is not
// an error: it means no runtime actions are configured yet.
func (s *Store) loadPipelines() (*pipelineFile, error) {
	path := pipelinePathFor(s.dataDir)
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &pipelineFile{Version: 2}, nil
		}
		return nil, errStore("actions are unavailable")
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxPipelineFileBytes {
		return nil, errStore("actions are invalid")
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > maxPipelineFileBytes {
		return nil, errStore("actions are unavailable")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var file pipelineFile
	if err := decoder.Decode(&file); err != nil {
		return nil, errStore("actions are invalid")
	}
	if err := validatePipelines(&file, s.dataDir); err != nil {
		return nil, err
	}
	return &file, nil
}

// pipelineForID resolves one configured action. An invalid registry is an
// error, never a partial fallback to legacy kinds.
func (s *Store) pipelineForID(id string) (pipelineDefinition, bool, error) {
	file, err := s.loadPipelines()
	if err != nil {
		return pipelineDefinition{}, false, err
	}
	for _, definition := range file.Actions {
		if definition.ID == id {
			return definition, true, nil
		}
	}
	return pipelineDefinition{}, false, nil
}

func validatePipelines(file *pipelineFile, dataDir string) error {
	if file.Version != 2 || len(file.Actions) > maxPipelines {
		return errStore("actions are invalid")
	}
	hosts := map[string]bool{}
	if len(file.HTTPHosts) > maxPipelineHTTPHosts {
		return errStore("actions are invalid")
	}
	for index := range file.HTTPHosts {
		host := strings.ToLower(strings.TrimSpace(file.HTTPHosts[index]))
		if !pipelineHostRE.MatchString(host) || hosts[host] {
			return errStore("actions are invalid")
		}
		file.HTTPHosts[index] = host
		hosts[host] = true
	}
	seen := map[string]bool{}
	for index := range file.Actions {
		definition := &file.Actions[index]
		definition.ID = strings.TrimSpace(definition.ID)
		definition.Label = strings.TrimSpace(definition.Label)
		definition.Description = strings.TrimSpace(definition.Description)
		definition.Priority = strings.TrimSpace(definition.Priority)
		definition.ReportFrom = strings.TrimSpace(definition.ReportFrom)
		if !taskKindRE.MatchString(definition.ID) || seen[definition.ID] {
			return errStore("actions are invalid")
		}
		seen[definition.ID] = true
		if definition.Label == "" || len([]rune(definition.Label)) > maxPipelineLabelRunes {
			return errStore("actions are invalid")
		}
		if len([]rune(definition.Description)) > maxPipelineDescriptionRunes {
			return errStore("actions are invalid")
		}
		requires, err := validateActionRequires(definition.Requires)
		if err != nil {
			return err
		}
		definition.Requires = requires
		if err := validatePipelinePresentation(definition.Presentation); err != nil {
			return err
		}
		if definition.Priority != "" && !validPriority(definition.Priority) {
			return errStore("actions are invalid")
		}
		if definition.Lane != "" && definition.Lane != "batch" && definition.Lane != "interactive" {
			return errStore("actions are invalid")
		}
		if len(definition.Steps) == 0 || len(definition.Steps) > maxPipelineSteps {
			return errStore("actions are invalid")
		}
		stepIDs := map[string]bool{}
		for stepIndex := range definition.Steps {
			step := &definition.Steps[stepIndex]
			if err := validatePipelineStep(step, dataDir, hosts); err != nil {
				return err
			}
			if stepIDs[step.ID] {
				return errStore("actions are invalid")
			}
			stepIDs[step.ID] = true
		}
		if definition.ReportFrom != "" && !stepIDs[definition.ReportFrom] {
			return errStore("actions are invalid")
		}
	}
	return nil
}

var actionRequiresAllowed = map[string]bool{
	"assistant": true, "report": true, "maintenance": true, "context": true, "workshop": true,
}

func validateActionRequires(values []string) ([]string, error) {
	clean := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if !actionRequiresAllowed[value] || seen[value] {
			return nil, errStore("actions are invalid")
		}
		seen[value] = true
		clean = append(clean, value)
	}
	return clean, nil
}

func validatePipelinePresentation(presentation *pipelinePresentation) error {
	if presentation == nil {
		return nil
	}
	presentation.Group = strings.TrimSpace(presentation.Group)
	if len([]rune(presentation.Group)) > maxActionCatalogGroupRunes {
		return errStore("actions are invalid")
	}
	if presentation.Order != nil && (*presentation.Order < 0 || *presentation.Order > 9999) {
		return errStore("actions are invalid")
	}
	presentation.SubmitLabel = strings.TrimSpace(presentation.SubmitLabel)
	if len([]rune(presentation.SubmitLabel)) > maxActionCatalogSubmitRunes {
		return errStore("actions are invalid")
	}
	if len(presentation.Fields) > maxActionCatalogFields {
		return errStore("actions are invalid")
	}
	names := map[string]bool{}
	for index := range presentation.Fields {
		field := &presentation.Fields[index]
		field.Name = strings.TrimSpace(field.Name)
		field.Label = strings.TrimSpace(field.Label)
		field.Type = strings.TrimSpace(field.Type)
		field.Placeholder = strings.TrimSpace(field.Placeholder)
		if !actionFieldRE.MatchString(field.Name) || names[field.Name] {
			return errStore("actions are invalid")
		}
		// title/priority are reserved launch controls; a declared field with the
		// same name would be ambiguous (and could be a non-string type).
		if field.Name == "title" || field.Name == "priority" {
			return errStore("actions are invalid")
		}
		// The task payload projector drops credential-shaped keys. Reject them
		// here so a catalog field never disappears between the form and the task.
		if sensitivePayloadKey(field.Name) {
			return errStore("actions are invalid")
		}
		names[field.Name] = true
		if field.Label == "" || len([]rune(field.Label)) > maxActionCatalogFieldLabel {
			return errStore("actions are invalid")
		}
		if len([]rune(field.Placeholder)) > maxActionCatalogPlaceholder {
			return errStore("actions are invalid")
		}
		switch field.Type {
		case "text", "textarea":
			if field.MaxLength == 0 {
				field.MaxLength = maxActionCatalogTextLength
			}
			if field.MaxLength < 1 || field.MaxLength > maxActionCatalogTextLength || len(field.Options) != 0 {
				return errStore("actions are invalid")
			}
		case "select":
			if field.MaxLength != 0 || len(field.Options) == 0 || len(field.Options) > maxActionCatalogOptions {
				return errStore("actions are invalid")
			}
			optionValues := map[string]bool{}
			for optionIndex := range field.Options {
				option := &field.Options[optionIndex]
				option.Value = strings.TrimSpace(option.Value)
				option.Label = strings.TrimSpace(option.Label)
				if option.Value == "" || option.Label == "" ||
					len([]rune(option.Value)) > maxActionCatalogOptionRunes ||
					len([]rune(option.Label)) > maxActionCatalogOptionRunes ||
					optionValues[option.Value] {
					return errStore("actions are invalid")
				}
				optionValues[option.Value] = true
			}
		case "boolean":
			if field.MaxLength != 0 || len(field.Options) != 0 {
				return errStore("actions are invalid")
			}
		default:
			return errStore("actions are invalid")
		}
	}
	return nil
}

func actionPresentationMap(presentation pipelinePresentation) map[string]any {
	raw, err := json.Marshal(presentation)
	if err != nil {
		return map[string]any{}
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return map[string]any{}
	}
	return value
}

func validatePipelineStep(step *pipelineStep, dataDir string, hosts map[string]bool) error {
	step.ID = strings.TrimSpace(step.ID)
	step.Type = strings.TrimSpace(step.Type)
	step.OnError = strings.TrimSpace(step.OnError)
	if !pipelineStepRE.MatchString(step.ID) {
		return errStore("actions are invalid")
	}
	if step.OnError != "" && step.OnError != "stop" && step.OnError != "continue" {
		return errStore("actions are invalid")
	}
	if step.TimeoutSeconds != 0 && (step.TimeoutSeconds < 1 || step.TimeoutSeconds > maxPipelineStepTimeout) {
		return errStore("actions are invalid")
	}
	if step.MaxOutputBytes != 0 && (step.MaxOutputBytes < 1 || step.MaxOutputBytes > maxPipelineOutputBytes) {
		return errStore("actions are invalid")
	}
	switch step.Type {
	case "context":
		step.Query = strings.TrimSpace(step.Query)
		if step.Query == "" {
			return errStore("actions are invalid")
		}
		if templateUsesSecret(step.Query) {
			return errStore("actions are invalid")
		}
	case "agent":
		step.Prompt = strings.TrimSpace(step.Prompt)
		step.Runner = strings.TrimSpace(step.Runner)
		step.Model = strings.TrimSpace(step.Model)
		step.Provider = strings.TrimSpace(step.Provider)
		if step.Prompt == "" || len([]rune(step.Prompt)) > maxPipelinePromptRunes {
			return errStore("actions are invalid")
		}
		if templateUsesSecret(step.Prompt) {
			return errStore("actions are invalid")
		}
		if step.Runner != "" && step.Runner != "codex" && step.Runner != "dsh" {
			return errStore("actions are invalid")
		}
		if step.Model != "" && !modelRE.MatchString(step.Model) {
			return errStore("actions are invalid")
		}
		if step.Provider != "" && !modelRE.MatchString(step.Provider) {
			return errStore("actions are invalid")
		}
		if step.Provider != "" && step.Model == "" {
			return errStore("actions are invalid")
		}
		if step.TimeoutSeconds != 0 && step.TimeoutSeconds < 60 {
			return errStore("actions are invalid")
		}
		if step.TimeoutSeconds == 0 {
			step.TimeoutSeconds = defaultPipelineAgentTimeout
		}
	case "script":
		if len(step.Command) == 0 || len(step.Command) > 32 {
			return errStore("actions are invalid")
		}
		scriptsRoot := filepath.Join(dataDir, "scripts")
		command := strings.TrimSpace(step.Command[0])
		if !filepath.IsAbs(command) || !isWithin(filepath.Clean(command), scriptsRoot) {
			return errStore("actions are invalid")
		}
		step.Command[0] = command
		for index := range step.Command[1:] {
			if len(step.Command[index+1]) > 4096 {
				return errStore("actions are invalid")
			}
		}
		step.Cwd = strings.TrimSpace(step.Cwd)
		if step.Cwd != "" {
			if !filepath.IsAbs(step.Cwd) || !isWithin(filepath.Clean(step.Cwd), dataDir) {
				return errStore("actions are invalid")
			}
		}
		if len(step.Stdin) > maxPipelineStdinBytes || templateUsesSecret(step.Stdin) {
			return errStore("actions are invalid")
		}
		if len(step.Env) > maxPipelineEnv {
			return errStore("actions are invalid")
		}
		for key, value := range step.Env {
			if !pipelineEnvRE.MatchString(key) || len(value) > 4096 || strings.ContainsRune(value, 0) {
				return errStore("actions are invalid")
			}
		}
		if step.TimeoutSeconds == 0 {
			step.TimeoutSeconds = defaultPipelineStepTimeout
		}
	case "push":
		step.Channel = strings.TrimSpace(step.Channel)
		step.Body = strings.TrimSpace(step.Body)
		if step.Channel != "weixin" && step.Channel != "bark" {
			return errStore("actions are invalid")
		}
		if step.Body == "" || len(step.Body) > 4000 || templateUsesSecret(step.Body) || templateUsesSecret(step.Title) {
			return errStore("actions are invalid")
		}
		if step.TimeoutSeconds == 0 {
			step.TimeoutSeconds = defaultPipelineStepTimeout
		}
	case "http":
		step.Method = strings.ToUpper(strings.TrimSpace(step.Method))
		step.URL = strings.TrimSpace(step.URL)
		if step.Method == "" {
			step.Method = "GET"
		}
		if step.Method != "GET" && step.Method != "POST" && step.Method != "PUT" && step.Method != "PATCH" && step.Method != "DELETE" {
			return errStore("actions are invalid")
		}
		if step.URL == "" || len(step.URL) > 2048 || templateUsesSecret(step.URL) {
			return errStore("actions are invalid")
		}
		if !strings.Contains(step.URL, "{{") {
			if !validHTTPURL(step.URL, hosts) {
				return errStore("actions are invalid")
			}
		} else if !strings.HasPrefix(step.URL, "https://") {
			return errStore("actions are invalid")
		}
		if len(step.Headers) > maxPipelineHeaders {
			return errStore("actions are invalid")
		}
		for key, value := range step.Headers {
			if !pipelineHeaderRE.MatchString(key) || strings.EqualFold(key, "Host") || len(value) > 2048 || strings.ContainsAny(value, "\r\n") {
				return errStore("actions are invalid")
			}
		}
		if len(step.Body) > maxPipelineOutputBytes {
			return errStore("actions are invalid")
		}
		if step.TimeoutSeconds == 0 {
			step.TimeoutSeconds = defaultPipelineStepTimeout
		}
	case "task":
		step.Action = strings.TrimSpace(step.Action)
		step.Priority = strings.TrimSpace(step.Priority)
		if !taskKindRE.MatchString(step.Action) {
			return errStore("actions are invalid")
		}
		if step.Priority != "" && !validPriority(step.Priority) {
			return errStore("actions are invalid")
		}
		if len(step.Payload) > 16 || len([]rune(step.Title)) > 120 || templateUsesSecret(step.Title) {
			return errStore("actions are invalid")
		}
		if step.TimeoutSeconds == 0 {
			step.TimeoutSeconds = defaultPipelineStepTimeout
		}
	default:
		return errStore("actions are invalid")
	}
	return nil
}

func validHTTPURL(raw string, hosts map[string]bool) bool {
	parsed, err := urlParseHTTPS(raw)
	if err != nil {
		return false
	}
	return hosts[parsed]
}

// urlParseHTTPS is a small wrapper kept separate so validation and execution
// share the same scheme and host rules.
func urlParseHTTPS(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return "", errStore("actions are invalid")
	}
	return strings.ToLower(parsed.Hostname()), nil
}

func templateUsesSecret(value string) bool {
	return strings.Contains(value, "{{secrets.")
}

func (s *Store) writePipelines(file *pipelineFile) error {
	if err := validatePipelines(file, s.dataDir); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return errStore("actions are invalid")
	}
	raw = append(raw, '\n')
	if len(raw) > maxPipelineFileBytes {
		return errStore("actions are invalid")
	}
	if err := s.snapshotPipelines(); err != nil {
		return err
	}
	path := pipelinePathFor(s.dataDir)
	if err := writeAtomic(path, raw, 0o600); err != nil {
		return err
	}
	return nil
}

func (s *Store) snapshotPipelines() error {
	path := pipelinePathFor(s.dataDir)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxPipelineFileBytes {
		return errStore("actions are unavailable")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return errStore("actions are unavailable")
	}
	dir := pipelineVersionsDirFor(s.dataDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return errStore("actions are unavailable")
	}
	digest := sha256.Sum256(raw)
	name := time.Now().UTC().Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(digest[:6]) + ".json"
	if err := writePrivateFile(filepath.Join(dir, name), raw); err != nil {
		return errStore("actions are unavailable")
	}
	return prunePipelineVersions(dir)
}

func prunePipelineVersions(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return errStore("actions are unavailable")
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for len(names) > maxPipelineVersions {
		if err := os.Remove(filepath.Join(dir, names[0])); err != nil {
			return errStore("actions are unavailable")
		}
		names = names[1:]
	}
	return nil
}

func (s *Store) listPipelineVersions() ([]string, error) {
	dir := pipelineVersionsDirFor(s.dataDir)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, errStore("actions are unavailable")
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			names = append(names, entry.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	return names, nil
}

// ListActions returns the full runtime action registry for the owner. It is
// session-protected at the Web boundary; prompts and commands are not secrets,
// but the file itself remains private.
func (s *Store) ListActions() (map[string]any, error) {
	file, err := s.loadPipelines()
	if err != nil {
		return nil, err
	}
	actions := make([]any, 0, len(file.Actions))
	for _, definition := range file.Actions {
		steps := make([]any, 0, len(definition.Steps))
		for _, step := range definition.Steps {
			steps = append(steps, pipelineStepMap(step))
		}
		entry := map[string]any{
			"id":          definition.ID,
			"label":       definition.Label,
			"priority":    definition.defaultPriority(),
			"lane":        definition.Lane,
			"enabled":     definition.isEnabled(),
			"requires":    definition.Requires,
			"report_from": definition.ReportFrom,
			"steps":       steps,
		}
		if definition.Description != "" {
			entry["description"] = definition.Description
		}
		if definition.Presentation != nil {
			entry["presentation"] = actionPresentationMap(*definition.Presentation)
		}
		actions = append(actions, entry)
	}
	return map[string]any{"actions": actions, "http_hosts": file.HTTPHosts}, nil
}

// ValidateAction checks one definition against the current HTTP allowlist. It
// never writes.
func (s *Store) ValidateAction(definition pipelineDefinition) error {
	current, err := s.loadPipelines()
	if err != nil {
		return err
	}
	file := &pipelineFile{Version: 2, HTTPHosts: current.HTTPHosts, Actions: []pipelineDefinition{definition}}
	return validatePipelines(file, s.dataDir)
}

// PutAction creates or replaces one action and snapshots the previous registry.
// It takes effect immediately; schema validation is the only gate.
func (s *Store) PutAction(definition pipelineDefinition) error {
	s.actionMu.Lock()
	defer s.actionMu.Unlock()
	file, err := s.loadPipelines()
	if err != nil {
		return err
	}
	replaced := false
	for index := range file.Actions {
		if file.Actions[index].ID == definition.ID {
			file.Actions[index] = definition
			replaced = true
			break
		}
	}
	if !replaced {
		file.Actions = append(file.Actions, definition)
	}
	return s.writePipelines(file)
}

// DisableAction turns one action off without deleting its definition. The next
// task or hook that resolves it fails closed instead of falling back.
func (s *Store) DisableAction(id string) error {
	s.actionMu.Lock()
	defer s.actionMu.Unlock()
	file, err := s.loadPipelines()
	if err != nil {
		return err
	}
	for index := range file.Actions {
		if file.Actions[index].ID != id {
			continue
		}
		disabled := false
		file.Actions[index].Enabled = &disabled
		return s.writePipelines(file)
	}
	return errStore("action_unknown")
}

// ActionVersions lists retained registry snapshots, newest first.
func (s *Store) ActionVersions() (map[string]any, error) {
	names, err := s.listPipelineVersions()
	if err != nil {
		return nil, err
	}
	dir := pipelineVersionsDirFor(s.dataDir)
	versions := make([]any, 0, len(names))
	for _, name := range names {
		info, statErr := os.Stat(filepath.Join(dir, name))
		if statErr != nil {
			continue
		}
		versions = append(versions, map[string]any{
			"name":        name,
			"bytes":       info.Size(),
			"modified_at": info.ModTime().UTC().Format(time.RFC3339Nano),
		})
	}
	return map[string]any{"versions": versions}, nil
}

var pipelineVersionRE = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}\.[0-9]{9}Z-[0-9a-f]{12}\.json$`)

// RestoreActionVersion republishes one retained snapshot. The restore itself is
// snapshotted, so rollback is reversible.
func (s *Store) RestoreActionVersion(name string) error {
	if !pipelineVersionRE.MatchString(name) {
		return errStore("action_version_invalid")
	}
	s.actionMu.Lock()
	defer s.actionMu.Unlock()
	path := filepath.Join(pipelineVersionsDirFor(s.dataDir), name)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxPipelineFileBytes {
		return errStore("action_version_unavailable")
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > maxPipelineFileBytes {
		return errStore("action_version_unavailable")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var file pipelineFile
	if err := decoder.Decode(&file); err != nil {
		return errStore("action_version_invalid")
	}
	return s.writePipelines(&file)
}

func decodeActionDefinition(raw []byte) (pipelineDefinition, error) {
	if len(raw) == 0 || len(raw) > maxPipelineFileBytes {
		return pipelineDefinition{}, errStore("action_invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var definition pipelineDefinition
	if err := decoder.Decode(&definition); err != nil {
		return pipelineDefinition{}, errStore("action_invalid")
	}
	return definition, nil
}

// ValidateActionJSON validates one owner-supplied definition against the
// current HTTP allowlist without writing it.
func (s *Store) ValidateActionJSON(raw []byte) error {
	definition, err := decodeActionDefinition(raw)
	if err != nil {
		return err
	}
	return s.ValidateAction(definition)
}

// PutActionJSON publishes one owner-supplied definition immediately and returns
// the resulting registry.
func (s *Store) PutActionJSON(raw []byte) (map[string]any, error) {
	definition, err := decodeActionDefinition(raw)
	if err != nil {
		return nil, err
	}
	if err := s.PutAction(definition); err != nil {
		return nil, err
	}
	return s.ListActions()
}

func pipelineStepMap(step pipelineStep) map[string]any {
	raw, err := json.Marshal(step)
	if err != nil {
		return map[string]any{}
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return map[string]any{}
	}
	return value
}

// PutActionHostsJSON replaces the HTTP host allowlist. Existing actions are
// revalidated so a host removal cannot silently leave an unroutable step.
func (s *Store) PutActionHostsJSON(raw []byte) (map[string]any, error) {
	if len(raw) == 0 || len(raw) > maxPipelineFileBytes {
		return nil, errStore("action_invalid")
	}
	var value struct {
		Hosts []string `json:"hosts"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return nil, errStore("action_invalid")
	}
	s.actionMu.Lock()
	defer s.actionMu.Unlock()
	file, err := s.loadPipelines()
	if err != nil {
		return nil, err
	}
	file.HTTPHosts = value.Hosts
	if err := s.writePipelines(file); err != nil {
		return nil, err
	}
	return s.ListActions()
}
