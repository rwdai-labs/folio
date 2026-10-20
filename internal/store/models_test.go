package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestModelCatalogUsesCodexCatalogAndDSHProfile(t *testing.T) {
	store := newTestStore(t)
	configPath := filepath.Join(t.TempDir(), "cordis.patch.yml")
	if err := os.WriteFile(configPath, []byte(`- id: agent-default-model
  config:
    provider: example-route
    model: example-large:1017
- id: llm-pi-ai
  config:
    providers:
      example-route:
        displayName: Ollama Cloud
        models:
          - id: example-large:1017
            name: Example Large
          - id: example-small
            name: Example Small
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DSH_CONFIG_PATCH", configPath)
	t.Setenv("DSH_CONFIG_HOME", "")
	bin := t.TempDir()
	script := "#!/bin/sh\n" +
		"cat <<'JSON'\n" +
		`{"models":[` +
		`{"slug":"gpt-test","display_name":"GPT Test","visibility":"list"},` +
		`{"slug":"hidden-test","display_name":"Hidden Test","visibility":"hide"},` +
		`{"slug":"plain-test","display_name":"","visibility":"list"}]}` + "\n" +
		"JSON\n"
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")

	catalog := store.ModelCatalog()
	codex, ok := catalog["codex"].([]map[string]any)
	if !ok || len(codex) != 2 {
		t.Fatalf("codex catalog=%v", catalog["codex"])
	}
	if codex[0]["id"] != "gpt-test" || codex[0]["label"] != "GPT Test" {
		t.Fatalf("first codex option=%v", codex[0])
	}
	if codex[1]["id"] != "plain-test" || codex[1]["label"] != "plain-test" {
		t.Fatalf("fallback codex option=%v", codex[1])
	}
	dsh, ok := catalog["dsh"].([]map[string]any)
	if !ok || len(dsh) != 2 {
		t.Fatalf("dsh catalog=%v", catalog["dsh"])
	}
	if dsh[0]["id"] != "example-large:1017" || dsh[0]["provider"] != "example-route" || dsh[0]["label"] != "Example Large" {
		t.Fatalf("first dsh option=%v", dsh[0])
	}
	if dsh[1]["id"] != "example-small" || dsh[1]["provider"] != "example-route" {
		t.Fatalf("second dsh option=%v", dsh[1])
	}
}

func TestModelCatalogRejectsMalformedCodexOutput(t *testing.T) {
	store := newTestStore(t)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\nprintf 'not json\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	catalog := store.ModelCatalog()
	codex, ok := catalog["codex"].([]map[string]any)
	if !ok || len(codex) != 0 {
		t.Fatalf("codex catalog=%v", catalog["codex"])
	}
}

func TestModelCatalogHasNoBuiltInDSHModels(t *testing.T) {
	store := newTestStore(t)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\nprintf '{\"models\":[]}\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Setenv("DSH_CONFIG_PATCH", "")
	t.Setenv("DSH_CONFIG_HOME", t.TempDir())
	catalog := store.ModelCatalog()
	dsh, ok := catalog["dsh"].([]map[string]any)
	if !ok || len(dsh) != 0 {
		t.Fatalf("unconfigured DSH catalog=%v", catalog["dsh"])
	}
}

func TestDSHProviderForModelUsesProfileAndEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cordis.patch.yml")
	if err := os.WriteFile(path, []byte(`- id: agent-default-model
  config:
    provider: example-route
    model: example-large
- id: llm-pi-ai
  config:
    providers:
      example-route:
        models:
          - id: example-large
            name: Example Large
          - id: example-small
            name: Example Small
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DSH_CONFIG_PATCH", path)
	t.Setenv("DSH_MODEL_PROVIDER", "")
	if got := dshProviderForModel("example-small"); got != "example-route" {
		t.Fatalf("configured provider=%q", got)
	}
	if got := dshProviderForModel("typed-by-hand"); got != "example-route" {
		t.Fatalf("default provider=%q", got)
	}
	t.Setenv("DSH_MODEL_PROVIDER", "override-route")
	if got := dshProviderForModel("example-small"); got != "override-route" {
		t.Fatalf("env provider=%q", got)
	}
}
