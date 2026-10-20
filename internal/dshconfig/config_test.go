package dshconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const samplePatch = `# a profile patch, with unrelated entries before and after
- id: ui-settings-general
  name: "@deepseek-ai/dsh-client-ui-settings-general"
  config:
    welcomeNoticeVersion: 2026-09-28.1
- id: agent-default-model
  name: "@deepseek-ai/dsh-agent-default-model"
  config:
    provider: example-route
    model: example-large:1017
    reasoningEffort: max
- id: llm-pi-ai
  name: "@deepseek-ai/dsh-llm-pi-ai"
  config:
    providers:
      example-route:
        displayName: Ollama Cloud
        apiKeyEnv: EXAMPLE_API_KEY
        api: openai-responses
        baseURL: https://example.invalid/v1
        models:
          - id: example-large
            name: example-large
            input:
              - text
              - image
            reasoningEfforts:
              low: low
              high: high
              max: max
          - id: example-large:1017
            name: Example Large
          - id: example-small
            name: "example-small"
# The insert block has a providers list too, and must not be mistaken for a model.
- insert:
    - id: example-route-quota
      name: "dsh-example-route-quota"
      config:
        providers:
          - example-route
- id: ui-conversation
  name: "@deepseek-ai/dsh-client-ui-conversation"
  config:
    busyEnter: steer
`

func writePatch(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cordis.patch.yml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadReadsActualProviderAndModels(t *testing.T) {
	config, err := Load(writePatch(t, samplePatch))
	if err != nil {
		t.Fatal(err)
	}
	if config.DefaultProvider != "example-route" || config.DefaultModel != "example-large:1017" {
		t.Fatalf("unexpected default: %q %q", config.DefaultProvider, config.DefaultModel)
	}
	if len(config.Models) != 3 {
		t.Fatalf("got %d models: %+v", len(config.Models), config.Models)
	}
	labels := make(map[string]string)
	for _, model := range config.Models {
		if model.Provider != "example-route" {
			t.Fatalf("model %q has provider %q", model.ID, model.Provider)
		}
		labels[model.ID] = model.Label
	}
	if labels["example-large:1017"] != "Example Large" {
		t.Fatalf("label was not read: %+v", labels)
	}
	if labels["example-small"] != "example-small" {
		t.Fatalf("quoted label was not read: %+v", labels)
	}
	patch := config.ProviderPatch("example-route")
	for _, wanted := range []string{
		"- id: llm-pi-ai",
		"providers:",
		"example-route:",
		"apiKeyEnv: EXAMPLE_API_KEY",
		"baseURL: https://example.invalid/v1",
		"example-large:1017",
	} {
		if !strings.Contains(patch, wanted) {
			t.Fatalf("provider patch is missing %q:\n%s", wanted, patch)
		}
	}
	if strings.Contains(patch, "example-route-quota") || strings.Contains(patch, "ui-conversation") {
		t.Fatalf("provider patch copied unrelated entries:\n%s", patch)
	}
	if config.ProviderForModel("example-small") != "example-route" {
		t.Fatal("model provider did not resolve")
	}
	if config.ProviderForModel("typed-by-hand") != "example-route" {
		t.Fatal("free-form model did not use the default provider")
	}
}

func TestLoadSkipsDisabledProviderAndUsesDefaultModelOnly(t *testing.T) {
	config, err := Load(writePatch(t, `- id: agent-default-model
  config:
    provider: example-route
    model: example-large:1017
- id: llm-pi-ai
  disabled: true
  config:
    providers:
      example-route:
        models:
          - id: hidden
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Models) != 1 || config.Models[0].ID != "example-large:1017" {
		t.Fatalf("disabled provider should leave only the configured default: %+v", config.Models)
	}
	if config.ProviderPatch("example-route") != "" {
		t.Fatal("disabled provider produced a patch")
	}
}

func TestLoadNoConfigHasNoHardcodedSuggestions(t *testing.T) {
	config, err := Load(writePatch(t, "- id: locale\n  config:\n    preference: zh\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Models) != 0 {
		t.Fatalf("unconfigured profile invented models: %+v", config.Models)
	}
	if config.DefaultProvider != "" || config.DefaultModel != "" {
		t.Fatalf("unconfigured profile invented a default: %+v", config)
	}
}

func TestResolvePathPrefersExplicitAndWebProfile(t *testing.T) {
	root := t.TempDir()
	webDir := filepath.Join(root, "profiles", "web")
	if err := os.MkdirAll(webDir, 0o700); err != nil {
		t.Fatal(err)
	}
	web := filepath.Join(webDir, "cordis.patch.yml")
	if err := os.WriteFile(web, []byte("- id: locale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DSH_CONFIG_PATCH", "")
	t.Setenv("DSH_CONFIG_HOME", root)
	path, err := ResolvePath()
	if err != nil {
		t.Fatal(err)
	}
	if path != web {
		t.Fatalf("got %q, want %q", path, web)
	}
	explicit := filepath.Join(root, "explicit.yml")
	if err := os.WriteFile(explicit, []byte("- id: locale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DSH_CONFIG_PATCH", explicit)
	path, err = ResolvePath()
	if err != nil {
		t.Fatal(err)
	}
	if path != explicit {
		t.Fatalf("explicit path lost: %q", path)
	}
}
