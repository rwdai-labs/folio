// Package dshconfig reads the small part of a DSH profile patch that describes
// model routing. It intentionally does not implement YAML: DSH profile patches
// are a documented list of id-targeted entries, and only llm-pi-ai provider
// declarations and the agent default model are needed here. Parsing this shape
// directly keeps the service dependency-free and avoids evaluating !!js tags.
package dshconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const maxConfigBytes = 2 * 1024 * 1024

var (
	modelIDPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+-]{0,199}$`)
	providerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
)

// Model is one selectable model and the DSH provider route that serves it.
type Model struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Provider string `json:"provider"`
}

// Config is the model routing declared by one DSH profile patch.
type Config struct {
	Source          string
	DefaultProvider string
	DefaultModel    string
	Models          []Model
	providerBlocks  map[string]string
}

// LoadFromEnv resolves and reads the configured DSH profile patch. A missing
// configuration is not an error: it simply means the selector has no
// suggestions and the runner keeps its own profile default.
func LoadFromEnv() (Config, error) {
	path, err := ResolvePath()
	if err != nil || path == "" {
		return Config{}, err
	}
	return Load(path)
}

// ResolvePath finds the patch exposed by DSH_CONFIG_PATCH, or the first useful
// profile patch below DSH_CONFIG_HOME (falling back to /host/dsh for the
// container mount). Explicit configuration wins over discovery.
func ResolvePath() (string, error) {
	if path := strings.TrimSpace(os.Getenv("DSH_CONFIG_PATCH")); path != "" {
		return path, nil
	}
	home := strings.TrimSpace(os.Getenv("DSH_CONFIG_HOME"))
	if home == "" {
		if info, err := os.Stat("/host/dsh"); err == nil && info.IsDir() {
			home = "/host/dsh"
		}
	}
	if home == "" {
		return "", nil
	}
	profilesDir := filepath.Join(home, "profiles")
	for _, name := range []string{"web", "headless"} {
		candidate := filepath.Join(profilesDir, name, "cordis.patch.yml")
		if regularFile(candidate) {
			return candidate, nil
		}
	}
	entries, err := os.ReadDir(profilesDir)
	if err == nil {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			if entry.IsDir() {
				names = append(names, entry.Name())
			}
		}
		sort.Strings(names)
		for _, name := range names {
			candidate := filepath.Join(profilesDir, name, "cordis.patch.yml")
			if regularFile(candidate) {
				return candidate, nil
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	candidate := filepath.Join(home, "cordis.patch.yml")
	if regularFile(candidate) {
		return candidate, nil
	}
	return "", nil
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() <= maxConfigBytes
}

// Load parses one profile patch. Malformed entries are skipped rather than
// guessed at, so a bad model row can never turn into an arbitrary provider.
func Load(path string) (Config, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Config{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxConfigBytes {
		return Config{}, errors.New("DSH model configuration is not a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	config := Config{Source: path, providerBlocks: make(map[string]string)}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	entries := topLevelEntries(lines)
	if len(entries) == 0 {
		return config, nil
	}

	for _, entry := range entries {
		id, _ := entry.scalar("id")
		switch id {
		case "agent-default-model":
			if entry.disabled() {
				continue
			}
			values := entry.configScalars()
			if provider := values["provider"]; provider != "" && providerPattern.MatchString(provider) {
				config.DefaultProvider = provider
			}
			if model := values["model"]; modelIDPattern.MatchString(model) {
				config.DefaultModel = model
			}
		case "llm-pi-ai":
			if entry.disabled() {
				continue
			}
			if blocks := entry.providerBlocks(); len(blocks) > 0 {
				for provider, block := range blocks {
					config.providerBlocks[provider] = block
				}
				config.Models = append(config.Models, entry.providerModels()...)
			}
		}
	}
	config.Models = dedupeModels(config.Models)
	if config.DefaultModel != "" {
		found := false
		for _, model := range config.Models {
			if model.ID == config.DefaultModel {
				found = true
				break
			}
		}
		if !found {
			config.Models = append([]Model{{
				ID:       config.DefaultModel,
				Label:    config.DefaultModel,
				Provider: config.DefaultProvider,
			}}, config.Models...)
		}
	}
	sort.SliceStable(config.Models, func(i, j int) bool {
		left, right := config.Models[i], config.Models[j]
		if left.ID == config.DefaultModel || right.ID == config.DefaultModel {
			return left.ID == config.DefaultModel && right.ID != config.DefaultModel
		}
		if left.Provider != right.Provider {
			return left.Provider < right.Provider
		}
		return left.ID < right.ID
	})
	return config, nil
}

// ProviderForModel resolves the route for a configured model. An unknown
// free-form id keeps the profile's default provider, which is the only sane
// route choice without a provider selector.
func (c Config) ProviderForModel(model string) string {
	model = strings.TrimSpace(model)
	if model != "" {
		if c.DefaultProvider != "" {
			for _, option := range c.Models {
				if option.ID == model && option.Provider == c.DefaultProvider {
					return option.Provider
				}
			}
		}
		for _, option := range c.Models {
			if option.ID == model {
				return option.Provider
			}
		}
	}
	return c.DefaultProvider
}

// ProviderPatch returns a minimal llm-pi-ai overlay for one route. It is empty
// when the route is the built-in default or is not declared in the patch.
func (c Config) ProviderPatch(provider string) string {
	if provider == "" {
		provider = c.DefaultProvider
	}
	block, ok := c.providerBlocks[provider]
	if !ok || block == "" {
		return ""
	}
	return "- id: llm-pi-ai\n  config:\n    providers:\n" + block + "\n"
}

type entry struct {
	lines []string
}

func topLevelEntries(lines []string) []entry {
	var entries []entry
	var current []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(stripComment(line))
		if trimmed == "" {
			if current != nil {
				current = append(current, line)
			}
			continue
		}
		if lineIndent(line) == 0 && (trimmed == "-" || strings.HasPrefix(trimmed, "- ")) {
			if current != nil {
				entries = append(entries, entry{lines: current})
			}
			current = []string{line}
			continue
		}
		if current != nil {
			current = append(current, line)
		}
	}
	if current != nil {
		entries = append(entries, entry{lines: current})
	}
	return entries
}

func (e entry) scalar(key string) (string, bool) {
	for _, line := range e.lines {
		if lineIndent(line) == 0 {
			trimmed := strings.TrimSpace(line)
			if !strings.HasPrefix(trimmed, "- ") {
				continue
			}
			name, raw, ok := splitKeyValue(strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")))
			if ok && name == key {
				return parseScalar(raw), true
			}
			continue
		}
		if lineIndent(line) != 2 {
			continue
		}
		name, raw, ok := splitKeyValue(line)
		if ok && name == key {
			return parseScalar(raw), true
		}
	}
	return "", false
}

func (e entry) disabled() bool {
	value, _ := e.scalar("disabled")
	return strings.EqualFold(value, "true")
}

func (e entry) configScalars() map[string]string {
	result := make(map[string]string)
	base := -1
	for index, line := range e.lines {
		name, raw, ok := splitKeyValue(line)
		if !ok || name != "config" || strings.TrimSpace(raw) != "" {
			continue
		}
		base = lineIndent(line) + 2
		for _, child := range e.lines[index+1:] {
			indent := lineIndent(child)
			if strings.TrimSpace(stripComment(child)) == "" {
				continue
			}
			if indent <= lineIndent(line) {
				break
			}
			if indent != base {
				continue
			}
			childName, childRaw, ok := splitKeyValue(child)
			if ok {
				result[childName] = parseScalar(childRaw)
			}
		}
		break
	}
	return result
}

func (e entry) providerBlocks() map[string]string {
	result := make(map[string]string)
	base := -1
	start := -1
	for index, line := range e.lines {
		name, raw, ok := splitKeyValue(line)
		if !ok || name != "config" || strings.TrimSpace(raw) != "" {
			continue
		}
		base = lineIndent(line) + 2
		for childIndex := index + 1; childIndex < len(e.lines); childIndex++ {
			child := e.lines[childIndex]
			if strings.TrimSpace(stripComment(child)) == "" {
				continue
			}
			indent := lineIndent(child)
			if indent <= lineIndent(line) {
				return result
			}
			if indent != base {
				continue
			}
			childName, childRaw, ok := splitKeyValue(child)
			if ok && childName == "providers" && strings.TrimSpace(childRaw) == "" {
				start = childIndex
				break
			}
		}
		break
	}
	if start < 0 {
		return result
	}
	providerIndent := base + 2
	for index := start + 1; index < len(e.lines); index++ {
		line := e.lines[index]
		trimmed := strings.TrimSpace(stripComment(line))
		if trimmed == "" {
			continue
		}
		indent := lineIndent(line)
		if indent <= base {
			break
		}
		if indent != providerIndent {
			continue
		}
		name, raw, ok := splitKeyValue(line)
		if !ok || !providerPattern.MatchString(name) {
			continue
		}
		if value := strings.TrimSpace(parseScalar(raw)); value != "" && value != "{}" {
			continue
		}
		end := len(e.lines)
		for next := index + 1; next < len(e.lines); next++ {
			candidate := e.lines[next]
			if strings.TrimSpace(stripComment(candidate)) == "" {
				continue
			}
			if lineIndent(candidate) <= providerIndent {
				end = next
				break
			}
		}
		result[name] = strings.Join(e.lines[index:end], "\n")
		index = end - 1
	}
	return result
}

func (e entry) providerModels() []Model {
	models := make([]Model, 0)
	providerIndent := -1
	provider := ""
	modelsIndent := -1
	inModels := false
	for index := 0; index < len(e.lines); index++ {
		line := e.lines[index]
		trimmed := strings.TrimSpace(stripComment(line))
		if trimmed == "" {
			continue
		}
		indent := lineIndent(line)
		name, raw, ok := splitKeyValue(line)
		if ok && name == "providers" && strings.TrimSpace(raw) == "" {
			providerIndent = indent + 2
			continue
		}
		if providerIndent < 0 {
			continue
		}
		if indent == providerIndent {
			inModels = false
			modelsIndent = -1
			provider = ""
			if ok && providerPattern.MatchString(name) {
				provider = name
			}
			continue
		}
		if provider == "" {
			continue
		}
		if ok && name == "models" && strings.TrimSpace(raw) == "" {
			inModels = true
			modelsIndent = indent + 2
			continue
		}
		if !inModels || indent != modelsIndent || !strings.HasPrefix(trimmed, "-") {
			continue
		}
		end := len(e.lines)
		for next := index + 1; next < len(e.lines); next++ {
			candidate := e.lines[next]
			if strings.TrimSpace(stripComment(candidate)) == "" {
				continue
			}
			nextIndent := lineIndent(candidate)
			if nextIndent < modelsIndent {
				end = next
				break
			}
			if nextIndent == modelsIndent && strings.HasPrefix(strings.TrimSpace(stripComment(candidate)), "-") {
				end = next
				break
			}
		}
		model := parseModelItem(trimmed)
		for _, field := range e.lines[index+1 : end] {
			fieldName, fieldRaw, ok := splitKeyValue(field)
			if !ok {
				continue
			}
			switch fieldName {
			case "id":
				model.ID = parseScalar(fieldRaw)
			case "name":
				model.Label = parseScalar(fieldRaw)
			}
		}
		index = end - 1
		if model.ID == "" || !modelIDPattern.MatchString(model.ID) {
			continue
		}
		if model.Label == "" {
			model.Label = model.ID
		}
		model.Provider = provider
		models = append(models, model)
	}
	return models
}

func parseModelItem(line string) Model {
	line = strings.TrimSpace(strings.TrimPrefix(line, "-"))
	if line == "" {
		return Model{}
	}
	var model Model
	if name, raw, ok := splitKeyValue(line); ok {
		if name == "id" {
			model.ID = parseScalar(raw)
		} else if name == "name" {
			model.Label = parseScalar(raw)
		}
	} else {
		model.ID = parseScalar(line)
	}
	return model
}

func dedupeModels(models []Model) []Model {
	seen := make(map[string]bool)
	result := make([]Model, 0, len(models))
	for _, model := range models {
		if model.ID == "" || !modelIDPattern.MatchString(model.ID) {
			continue
		}
		if model.Label == "" {
			model.Label = model.ID
		}
		key := model.Provider + "\x00" + model.ID
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, model)
	}
	return result
}

func splitKeyValue(line string) (string, string, bool) {
	line = stripComment(line)
	quote := rune(0)
	for index, char := range line {
		switch {
		case quote != 0:
			if char == quote {
				quote = 0
			}
		case char == '"' || char == '\'':
			quote = char
		case char == ':':
			key := strings.TrimSpace(line[:index])
			value := strings.TrimSpace(line[index+1:])
			if key == "" || strings.ContainsAny(key, " \t") {
				return "", "", false
			}
			return key, value, true
		}
	}
	return "", "", false
}

func parseScalar(value string) string {
	value = strings.TrimSpace(stripComment(value))
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "!!") {
		return ""
	}
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		unquoted, err := strconvUnquote(value)
		if err == nil {
			return unquoted
		}
		return value[1 : len(value)-1]
	}
	if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
		return strings.ReplaceAll(value[1:len(value)-1], "''", "'")
	}
	return value
}

func strconvUnquote(value string) (string, error) {
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return "", fmt.Errorf("not quoted")
	}
	// Profile strings are simple; this handles the escapes DSH uses without
	// importing a full YAML implementation.
	body := value[1 : len(value)-1]
	var result strings.Builder
	for index := 0; index < len(body); index++ {
		if body[index] != '\\' {
			result.WriteByte(body[index])
			continue
		}
		index++
		if index >= len(body) {
			return "", fmt.Errorf("invalid escape")
		}
		switch body[index] {
		case '\\', '"', '/':
			result.WriteByte(body[index])
		case 'n':
			result.WriteByte('\n')
		case 't':
			result.WriteByte('\t')
		default:
			result.WriteByte(body[index])
		}
	}
	return result.String(), nil
}

func stripComment(line string) string {
	quote := rune(0)
	for index, char := range line {
		switch {
		case quote != 0:
			if char == quote {
				quote = 0
			}
		case char == '"' || char == '\'':
			quote = char
		case char == '#':
			if index == 0 || line[index-1] == ' ' || line[index-1] == '\t' {
				return line[:index]
			}
		}
	}
	return line
}

func lineIndent(line string) int {
	count := 0
	for _, char := range line {
		if char == ' ' {
			count++
			continue
		}
		break
	}
	return count
}
