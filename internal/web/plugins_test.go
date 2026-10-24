package web

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"folio/internal/plugin"
	"folio/internal/storage"
)

// testApplication mirrors testServer but lets a test attach plugins.
func testApplication(t *testing.T, backend *testBackend, plugins *plugin.Registry) (*Server, string) {
	t.Helper()
	root := t.TempDir()
	assets := filepath.Join(root, "assets")
	if err := os.MkdirAll(assets, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"index.html": "index", "app.js": "script", "i18n.js": "i18n", "styles.css": "style"} {
		if err := os.WriteFile(filepath.Join(assets, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	passwordFile := filepath.Join(root, "secrets", "admin-password")
	if err := os.MkdirAll(filepath.Dir(passwordFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(passwordFile, []byte("correct horse\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	application, err := New(backend, Options{
		DataDir: filepath.Join(root, "data"), Plugins: plugins, PasswordFile: passwordFile,
		AssetsDir: assets, TrustedHosts: []string{"127.0.0.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return application, "correct horse"
}

type demoPlugin struct {
	calls int
}

func (d *demoPlugin) ID() string    { return "demo" }
func (d *demoPlugin) Title() string { return "Demo" }
func (d *demoPlugin) Assets() fs.FS {
	return fstest.MapFS{
		"plugin.js":  &fstest.MapFile{Data: []byte("console.log('demo')")},
		"plugin.css": &fstest.MapFile{Data: []byte("#demo{}")},
		"secret.txt": &fstest.MapFile{Data: []byte("not served")},
	}
}
func (d *demoPlugin) Storage() storage.Remote { return nil }
func (d *demoPlugin) Routes() []plugin.Route {
	return []plugin.Route{
		{Method: "GET", Path: "/status", Handler: func(ctx context.Context, request plugin.Request) (map[string]any, error) {
			return map[string]any{"status": "ok", "exec": request.Exec == nil}, nil
		}},
		{Method: "POST", Path: "/action", Handler: func(ctx context.Context, request plugin.Request) (map[string]any, error) {
			d.calls++
			return map[string]any{"ran": true, "body": request.Body}, nil
		}},
	}
}

func TestPluginRoutesAndAssetsAreMounted(t *testing.T) {
	demo := &demoPlugin{}
	backend := &testBackend{}
	application, password := testApplication(t, backend, plugin.NewRegistry(demo))
	server := httptest.NewServer(application.Handler())
	defer server.Close()
	client := &testClient{server: server}

	status, _, listing := client.request(t, http.MethodGet, "/api/plugins", nil, "")
	if status != http.StatusOK {
		t.Fatalf("list status=%d", status)
	}
	items := listing["plugins"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != "demo" {
		t.Fatalf("listing=%v", listing)
	}

	status, headers, _ := client.request(t, http.MethodGet, "/plugins/demo/plugin.js", nil, "")
	if status != http.StatusOK || headers.Get("Content-Type") != "text/javascript; charset=utf-8" {
		t.Fatalf("asset status=%d type=%q", status, headers.Get("Content-Type"))
	}
	status, _, _ = client.request(t, http.MethodGet, "/plugins/demo/secret.txt", nil, "")
	if status != http.StatusNotFound {
		t.Fatalf("non-allowlisted asset status=%d", status)
	}
	status, _, _ = client.request(t, http.MethodGet, "/plugins/demo/../app.js", nil, "")
	if status == http.StatusOK {
		t.Fatal("traversal was served")
	}

	status, _, _ = client.request(t, http.MethodGet, "/api/plugins/demo/status", nil, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated plugin status=%d", status)
	}
	client.login(t, password)
	status, _, statusBody := client.request(t, http.MethodGet, "/api/plugins/demo/status", nil, "")
	if status != http.StatusOK || statusBody["status"] != "ok" {
		t.Fatalf("plugin status=%d body=%v", status, statusBody)
	}
	status, _, _ = client.request(t, http.MethodPost, "/api/plugins/demo/action", map[string]any{"x": "1"}, "")
	if status != http.StatusForbidden {
		t.Fatalf("missing csrf status=%d", status)
	}
	status, _, response := client.request(t, http.MethodPost, "/api/plugins/demo/action", map[string]any{"x": "1"}, server.URL)
	if status != http.StatusOK || response["ran"] != true || demo.calls != 1 {
		t.Fatalf("post status=%d body=%v calls=%d", status, response, demo.calls)
	}
	status, _, _ = client.request(t, http.MethodGet, "/api/plugins/unknown/status", nil, "")
	if status != http.StatusNotFound {
		t.Fatalf("unknown plugin status=%d", status)
	}
}
