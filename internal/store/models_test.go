package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestModelCatalogUsesCodexCatalogAndDSHDefaults(t *testing.T) {
	store := newTestStore(t)
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
	if !ok || len(dsh) != 2 || dsh[0]["id"] != "deepseek-flash" || dsh[1]["id"] != "deepseek-v4-pro" {
		t.Fatalf("dsh catalog=%v", catalog["dsh"])
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
