package store

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModelServicesPersistenceRedactsKeys(t *testing.T) {
	s := newTestStore(t)
	cfg := map[string]any{"version": 1, "connections": map[string]any{"local": map[string]any{"name": "Local", "base_url": "http://localhost:1234", "api_key": "synthetic-key"}}, "defaults": map[string]any{"assistant": map[string]any{"connection": "local", "model": "group/foo"}, "report": map[string]any{}, "maintenance": map[string]any{}}, "overrides": map[string]any{"report": map[string]any{}}}
	got, err := s.PutModelServices(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(mustJSON(t, got)), "synthetic-key") {
		t.Fatal("key leaked")
	}
	if got["connections"].(map[string]any)["local"].(map[string]any)["key_present"] != true {
		t.Fatal("key_present missing")
	}
	loaded, err := s.ModelServices()
	if err != nil {
		t.Fatal(err)
	}
	if loaded["connections"].(map[string]any)["local"].(map[string]any)["key_present"] != true {
		t.Fatal("key not retained")
	}
	if _, err := os.Stat(filepath.Join(s.modelServicesSecretsDir, "local")); err != nil {
		t.Fatal(err)
	}
}

func TestTestModelServiceCatalog(t *testing.T) {
	s := newTestStore(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer synthetic" {
			t.Fatalf("auth missing")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"group/foo"},{"id":"plain"}]}`))
	}))
	defer server.Close()
	cfg := map[string]any{"version": 1, "connections": map[string]any{"local": map[string]any{"name": "Local", "base_url": server.URL, "api_key": "synthetic"}}, "defaults": map[string]any{"assistant": map[string]any{}, "report": map[string]any{}, "maintenance": map[string]any{}}, "overrides": map[string]any{"report": map[string]any{}}}
	if _, err := s.PutModelServices(cfg); err != nil {
		t.Fatal(err)
	}
	got, err := s.TestModelService(map[string]any{"connection": "local"})
	if err != nil {
		t.Fatal(err)
	}
	if got["reachable"] != true || got["inference_tested"] != false {
		t.Fatalf("bad result %#v", got)
	}
	if len(got["models"].([]any)) != 2 {
		t.Fatalf("models %#v", got["models"])
	}
}

func TestModelServiceUnsavedCatalogAndFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"unsaved", 200, `{"data":[{"id":"group/foo"}]}`, ""},
		{"unauthorized", 401, `secret response`, "model_service_unauthorized"},
		{"redirect", 302, ``, "model_service_unreachable"},
		{"missing data", 200, `{}`, "model_service_invalid_response"},
		{"null data", 200, `{"data":null}`, "model_service_invalid_response"},
		{"trailing data", 200, `{"data":[]} {}`, "model_service_invalid_response"},
		{"empty id", 200, `{"data":[{"id":""}]}`, "model_service_invalid_response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
					t.Errorf("unexpected catalog request")
				}
				if r.Header.Get("Authorization") != "Bearer synthetic-unsaved" {
					t.Errorf("missing unsaved key")
				}
				if tc.status == 302 {
					w.Header().Set("Location", "/never-follow")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			got, err := s.TestModelService(map[string]any{"connection": "unsaved", "base_url": server.URL + "/v1", "api_key": "synthetic-unsaved"})
			if tc.want == "" {
				if err != nil || got["inference_tested"] != false {
					t.Fatalf("got %#v err %v", got, err)
				}
			} else if err == nil || err.Error() != tc.want {
				t.Fatalf("err %v want %s", err, tc.want)
			}
		})
	}
}

func TestModelServicesMalformedFailsClosed(t *testing.T) {
	s := newTestStore(t)
	if err := os.WriteFile(s.modelServicesPath, []byte(`{"version":1,"connections":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ModelServices(); err == nil || !strings.Contains(err.Error(), "model_services_invalid") {
		t.Fatalf("err %v", err)
	}
}

func TestModelServicesValidationBeforeKeyMutation(t *testing.T) {
	s := newTestStore(t)
	cfg := emptyModelServices()
	cfg["connections"] = map[string]any{"local": map[string]any{"name": "Local", "base_url": "https://example.invalid/v1", "api_key": "synthetic-original"}}
	if _, err := s.PutModelServices(cfg); err != nil {
		t.Fatal(err)
	}
	cfg["connections"].(map[string]any)["local"].(map[string]any)["api_key"] = ""
	if _, err := s.PutModelServices(cfg); err != nil {
		t.Fatal(err)
	}
	key, present := s.readModelServiceKey("local")
	if !present || key != "synthetic-original" {
		t.Fatal("blank key did not retain saved key")
	}
	cfg["connections"].(map[string]any)["local"].(map[string]any)["api_key"] = "synthetic-replacement"
	cfg["connections"].(map[string]any)["second"] = map[string]any{"name": "Second", "base_url": "https://example.invalid", "api_key": "bad\nkey"}
	if _, err := s.PutModelServices(cfg); err == nil {
		t.Fatal("accepted malformed key")
	}
	key, _ = s.readModelServiceKey("local")
	if key != "synthetic-original" {
		t.Fatal("failed validation mutated saved key")
	}
}

func TestModelServicesSymlinksRejected(t *testing.T) {
	s := newTestStore(t)
	cfg := emptyModelServices()
	cfg["connections"] = map[string]any{"local": map[string]any{"name": "Local", "base_url": "https://example.invalid", "api_key": "synthetic"}}
	if err := os.MkdirAll(s.modelServicesSecretsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(target, []byte("synthetic-old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(s.modelServicesSecretsDir, "local")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutModelServices(cfg); err == nil {
		t.Fatal("accepted key symlink")
	}
	if _, present := s.readModelServiceKey("local"); present {
		t.Fatal("read key symlink")
	}
	if err := os.Symlink(target, s.modelServicesPath); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ModelServices(); err == nil {
		t.Fatal("accepted config symlink")
	}
}

func TestModelServicesBoundConfigFailsClosed(t *testing.T) {
	s := newTestStore(t)
	if err := os.WriteFile(s.modelServicesPath, []byte(`{"version":1} garbage`), 0o600); err != nil {
		t.Fatal(err)
	}
	if binding, err := s.resolveModelBinding("assistant", ""); err == nil || binding != nil {
		t.Fatalf("malformed binding fallback: %#v %v", binding, err)
	}
}

func TestModelServiceURLPolicy(t *testing.T) {
	for _, base := range []string{"http://example.com", "https://x.invalid?", "https://x.invalid?q=1", "https://x.invalid#", "https://user@x.invalid", "http://169.254.1.1"} {
		if _, err := modelServiceURL(base); err == nil {
			t.Fatalf("accepted %s", base)
		}
	}
	for _, base := range []string{"http://localhost:1234/v1", "http://127.0.0.1/v1", "http://[::1]/v1", "http://10.0.0.1/v1", "https://example.invalid/v1"} {
		if _, err := modelServiceURL(base); err != nil {
			t.Fatalf("rejected %s", base)
		}
	}
}

func TestModelServicesBindingAndPrivateModes(t *testing.T) {
	s := newTestStore(t)
	if binding, err := s.resolveModelBinding("assistant", ""); err != nil || binding != nil {
		t.Fatalf("absent config %#v %v", binding, err)
	}
	cfg := emptyModelServices()
	cfg["connections"] = map[string]any{"local": map[string]any{"name": "Local", "base_url": "https://example.invalid/v1", "api_key": strings.Repeat("s", 4096)}}
	cfg["defaults"].(map[string]any)["report"] = map[string]any{"connection": "local", "model": "group/default"}
	cfg["defaults"].(map[string]any)["assistant"] = map[string]any{"connection": "", "model": ""}
	cfg["overrides"].(map[string]any)["report"] = map[string]any{"summary": map[string]any{"connection": "local", "model": "group/override"}}
	if _, err := s.PutModelServices(cfg); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{s.modelServicesPath, filepath.Join(s.modelServicesSecretsDir, "local")} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v %v", info, err)
		}
	}
	if binding, err := s.resolveModelBinding("report", "summary"); err != nil || binding == nil || binding.Model != "group/override" || len(binding.Key) != 4096 {
		t.Fatalf("override failed: %v", err)
	}
	if binding, err := s.resolveModelBinding("report", "other"); err != nil || binding == nil || binding.Model != "group/default" {
		t.Fatalf("default failed: %v", err)
	}
	if binding, err := s.resolveModelBinding("assistant", ""); err != nil || binding != nil {
		t.Fatalf("unbound failed: %v", err)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
