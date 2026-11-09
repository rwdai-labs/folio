package contextrepo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskModelConnectionOverridesOnlyOneInvocation(t *testing.T) {
	home := t.TempDir()
	config := []byte("model_provider = \"legacy\"\nmodel = \"old-model\"\n[model_providers.legacy]\nname = \"Legacy\"\nbase_url = \"https://legacy.example/v1\"\nwire_api = \"responses\"\n")
	if err := os.WriteFile(filepath.Join(home, "config.toml"), config, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", home)
	t.Setenv("FOLIO_MODEL_BASE_URL", "http://127.0.0.1:3425/v1")
	t.Setenv("FOLIO_MODEL_ID", "group/maintenance")
	t.Setenv("FOLIO_MODEL_KEY", "synthetic-task-only-key")
	t.Setenv("UNRELATED_SECRET", "must-not-forward")
	args, envKey, err := CodexConnectionArgs()
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(args, " ")
	if envKey != "FOLIO_MODEL_KEY" || !strings.Contains(all, "group/maintenance") || strings.Contains(all, "synthetic-task-only-key") || strings.Contains(all, "legacy.example") {
		t.Fatal("connection override or credential isolation failed")
	}
	var router Router
	env, err := router.buildWorkerEnv(envKey, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range env {
		if item == "FOLIO_MODEL_KEY=synthetic-task-only-key" {
			found = true
		}
		if strings.HasPrefix(item, "UNRELATED_SECRET=") || strings.HasPrefix(item, "FOLIO_MODEL_BASE_URL=") {
			t.Fatal("unrelated environment forwarded")
		}
	}
	if !found {
		t.Fatal("selected key not forwarded")
	}
	after, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil || string(after) != string(config) {
		t.Fatal("legacy profile changed")
	}
	t.Setenv("FOLIO_MODEL_BASE_URL", "")
	t.Setenv("FOLIO_MODEL_ID", "")
	t.Setenv("FOLIO_MODEL_KEY", "")
	args, _, err = CodexConnectionArgs()
	if err != nil || !strings.Contains(strings.Join(args, " "), "old-model") {
		t.Fatal("legacy fallback not restored")
	}
}

func TestTaskModelConnectionRejectsInvalidBeforeRunner(t *testing.T) {
	for _, tc := range []struct{ name, base, model, key string }{
		{"public-http", "http://models.example/v1", "group/report", "synthetic"},
		{"userinfo", "https://secret@models.example/v1", "group/report", "synthetic"},
		{"query", "https://models.example/v1?key=secret", "group/report", "synthetic"},
		{"empty-query", "https://models.example/v1?", "group/report", "synthetic"},
		{"bad-model", "https://models.example/v1", "x\n-c", "synthetic"},
		{"missing-key", "https://models.example/v1", "group/report", ""},
		{"missing-base", "", "group/report", "synthetic"},
		{"key-newline", "https://models.example/v1", "group/report", "secret\nnext"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FOLIO_MODEL_BASE_URL", tc.base)
			t.Setenv("FOLIO_MODEL_ID", tc.model)
			t.Setenv("FOLIO_MODEL_KEY", tc.key)
			_, _, err := CodexConnectionArgs()
			if err == nil || err.Error() != "task model connection is invalid" {
				t.Fatal("invalid override did not fail with fixed error")
			}
		})
	}
}
