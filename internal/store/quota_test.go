package store

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreQuotasWiresOptionalSource(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	toolDir := filepath.Join(root, "tools")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(toolDir, 0o700); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"provider":"codex","name":"Codex","kind":"subscription","windows":[]}]}`))
	}))
	defer server.Close()

	if err := os.MkdirAll(filepath.Join(dataDir, "secrets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "secrets", "magpie-quota-key"), []byte("test-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config, _ := json.Marshal(map[string]any{
		"version": 1,
		"source": map[string]any{
			"kind": "magpie", "url": server.URL,
			"key_file": "secrets/magpie-quota-key",
		},
	})
	if err := os.WriteFile(filepath.Join(dataDir, "quotas.json"), config, 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := New(dataDir, toolDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.Quotas(false)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Configured || len(snapshot.Cards) != 1 || snapshot.Cards[0].Provider != "codex" {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}
