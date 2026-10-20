package store

import (
	"encoding/json"
	"os"
	"strings"
	"time"

	"folio/internal/dshconfig"
)

const (
	maxModelOptions = 64
	modelCatalogTTL = 10 * time.Minute
)

// dshRunnerEnv forwards the deployment's DSH model configuration to the worker
// that launches DSH. Paths are resolved by the worker, so a host mount and a
// service-owned import behave the same way. The worker validates every value
// before use.
func dshRunnerEnv() map[string]string {
	result := make(map[string]string)
	for _, key := range []string{"DSH_CONFIG_PATCH", "DSH_CONFIG_HOME", "DSH_MODEL_PROVIDER", "DSH_CREDENTIALS"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			result[key] = value
		}
	}
	return result
}

// dshProviderForModel resolves the route for a selected model from the actual
// DSH profile patch. A missing configuration returns an empty provider so the
// worker can keep the profile's own default.
func dshProviderForModel(model string) string {
	if provider := strings.TrimSpace(os.Getenv("DSH_MODEL_PROVIDER")); provider != "" {
		return provider
	}
	config, err := dshconfig.LoadFromEnv()
	if err != nil {
		return ""
	}
	return config.ProviderForModel(model)
}

// ModelCatalog returns bounded suggestions for the runner model selector. It
// never exposes credentials or provider endpoints: Codex contributes only the
// display name and slug from its local catalog, and DSH contributes only model
// ids declared by the mounted/active profile patch.
func (s *Store) ModelCatalog() map[string]any {
	s.modelCatalogMu.Lock()
	defer s.modelCatalogMu.Unlock()
	if s.modelCatalogData != nil && time.Since(s.modelCatalogAt) < modelCatalogTTL {
		return s.modelCatalogData
	}
	catalog := map[string]any{
		"codex": s.codexModelOptions(),
		"dsh":   dshModelOptions(),
	}
	s.modelCatalogData = catalog
	s.modelCatalogAt = time.Now()
	return catalog
}

// dshModelOptions reads the configured DSH profile. There is deliberately no
// built-in list: a profile with no model declarations has no suggestions,
// while the free-form field remains available.
func dshModelOptions() []map[string]any {
	config, err := dshconfig.LoadFromEnv()
	if err != nil {
		return []map[string]any{}
	}
	options := make([]map[string]any, 0, len(config.Models))
	for _, model := range config.Models {
		if len(options) >= maxModelOptions {
			break
		}
		if !modelRE.MatchString(model.ID) {
			continue
		}
		label := publicText(model.Label, 120)
		if label == "" {
			label = model.ID
		}
		option := map[string]any{"id": model.ID, "label": label}
		if model.Provider != "" {
			option["provider"] = model.Provider
		}
		options = append(options, option)
	}
	return options
}

// codexModelOptions reads the installed CLI's catalog. Older Codex builds may
// not support --bundled or debug models at all; in that case the selector simply
// has no suggestions and remains free-form.
func (s *Store) codexModelOptions() []map[string]any {
	for _, args := range [][]string{
		{"codex", "debug", "models", "--bundled"},
		{"codex", "debug", "models"},
	} {
		result, err := s.runCommand(args, s.toolRoot, "", 10*time.Second, false, map[string]string{
			"CODEX_HOME": s.codexDir,
		})
		if err != nil || result.code != 0 {
			continue
		}
		options := parseCodexModelOptions(result.stdout)
		if len(options) > 0 {
			return options
		}
	}
	return []map[string]any{}
}

func parseCodexModelOptions(raw string) []map[string]any {
	var catalog struct {
		Models []struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"display_name"`
			Visibility  string `json:"visibility"`
		} `json:"models"`
	}
	if json.Unmarshal([]byte(raw), &catalog) != nil {
		return nil
	}
	options := make([]map[string]any, 0, maxModelOptions)
	seen := make(map[string]bool, len(catalog.Models))
	for _, model := range catalog.Models {
		if len(options) >= maxModelOptions {
			break
		}
		slug := strings.TrimSpace(model.Slug)
		if !modelRE.MatchString(slug) || seen[slug] {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(model.Visibility)) {
		case "hide", "hidden":
			continue
		}
		seen[slug] = true
		label := publicText(strings.TrimSpace(model.DisplayName), 120)
		if label == "" {
			label = slug
		}
		options = append(options, map[string]any{"id": slug, "label": label})
	}
	return options
}
