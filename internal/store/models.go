package store

import (
	"encoding/json"
	"os"
	"strings"
	"time"
)

const (
	maxModelOptions = 24
	modelCatalogTTL = 10 * time.Minute
)

// DSH's headless profile ships these official DeepSeek routes. The model
// selector also accepts free-form ids, so a deployment with a different
// provider catalog is not limited to this list.
var dshModelOptions = []map[string]any{
	{"id": "deepseek-flash", "label": "DeepSeek-V4.1-Flash"},
	{"id": "deepseek-v4-pro", "label": "DeepSeek-V4-Pro"},
}

// dshProviderEnv forwards an optional deployment-level provider override to the
// worker that launches DSH. The worker validates the value before use.
func dshProviderEnv() map[string]string {
	value := strings.TrimSpace(os.Getenv("DSH_MODEL_PROVIDER"))
	if value == "" {
		return nil
	}
	return map[string]string{"DSH_MODEL_PROVIDER": value}
}

// ModelCatalog returns bounded suggestions for the runner model selector. It
// never exposes credentials or provider endpoints: Codex contributes only the
// display name and slug from its local catalog, and DSH contributes the public
// model ids shipped with its headless profile.
func (s *Store) ModelCatalog() map[string]any {
	s.modelCatalogMu.Lock()
	defer s.modelCatalogMu.Unlock()
	if s.modelCatalogData != nil && time.Since(s.modelCatalogAt) < modelCatalogTTL {
		return s.modelCatalogData
	}
	catalog := map[string]any{
		"codex": s.codexModelOptions(),
		"dsh":   dshModelOptions,
	}
	s.modelCatalogData = catalog
	s.modelCatalogAt = time.Now()
	return catalog
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
