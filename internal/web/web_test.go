package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type testBackend struct {
	mu          sync.Mutex
	maintenance []struct{ mode, model string }
	preview     []struct {
		reportID string
		index    int
		target   string
		content  string
	}
	applied  []struct{ reviewID, message string }
	syncRun  chan struct{}
	syncDone chan struct{}
}

func (b *testBackend) State() (map[string]any, error) {
	return map[string]any{"provider": map[string]any{"access_token": "secret"}, "script": "<script>"}, nil
}
func (b *testBackend) GitHubStart() (map[string]any, error) {
	return map[string]any{"status": "pending", "user_code": "ABCD-EFGH", "device_code": "secret"}, nil
}
func (b *testBackend) Poll() (map[string]any, error) {
	return map[string]any{"status": "authenticated"}, nil
}
func (b *testBackend) Logout() (map[string]any, error) {
	return map[string]any{"status": "logged_out"}, nil
}
func (b *testBackend) ConnectRepository(fullName string) (map[string]any, error) {
	return map[string]any{"full_name": fullName}, nil
}
func (b *testBackend) SyncRepository() (map[string]any, error) {
	if b.syncRun != nil {
		select {
		case b.syncRun <- struct{}{}:
		default:
		}
	}
	if b.syncDone != nil {
		<-b.syncDone
	}
	return map[string]any{"status": "synced"}, nil
}
func (b *testBackend) RunMaintenance(mode, model string) (map[string]any, error) {
	b.mu.Lock()
	b.maintenance = append(b.maintenance, struct{ mode, model string }{mode, model})
	b.mu.Unlock()
	return map[string]any{"mode": mode, "model": model, "report": "safe"}, nil
}
func (b *testBackend) SchedulerTick() (map[string]any, error) {
	return map[string]any{"status": "skipped"}, nil
}
func (b *testBackend) ListProposals() (map[string]any, error) {
	return map[string]any{"reports": []any{map[string]any{"report_id": "review-1", "proposals": []any{map[string]any{"index": 0, "target": "inbox/example.md", "content": "new"}}}}}, nil
}
func (b *testBackend) PreviewChange(reportID string, index int, target, content string) (map[string]any, error) {
	b.mu.Lock()
	b.preview = append(b.preview, struct {
		reportID string
		index    int
		target   string
		content  string
	}{reportID, index, target, content})
	b.mu.Unlock()
	return map[string]any{"review_id": "review-id", "diff": "-old\n+new"}, nil
}
func (b *testBackend) ApplyChange(reviewID, message string) (map[string]any, error) {
	b.mu.Lock()
	b.applied = append(b.applied, struct{ reviewID, message string }{reviewID, message})
	b.mu.Unlock()
	return map[string]any{"status": "applied"}, nil
}

func testServer(t *testing.T, backend *testBackend) (*httptest.Server, string) {
	t.Helper()
	root := t.TempDir()
	assets := filepath.Join(root, "assets")
	if err := os.MkdirAll(assets, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"index.html": "index", "app.js": "script", "styles.css": "style", "secret.txt": "secret"} {
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
	server, err := New(backend, Options{DataDir: filepath.Join(root, "data"), PasswordFile: passwordFile, AssetsDir: assets, TrustedHosts: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(server.Handler()), "correct horse"
}

type testClient struct {
	server *httptest.Server
	cookie string
	csrf   string
}

func (c *testClient) request(t *testing.T, method, path string, payload any, origin string) (int, http.Header, map[string]any) {
	t.Helper()
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body = strings.NewReader(string(encoded))
	}
	req, err := http.NewRequestWithContext(context.Background(), method, c.server.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = strings.TrimPrefix(c.server.URL, "http://")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if c.cookie != "" {
		req.Header.Set("Cookie", c.cookie)
	}
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	response, err := c.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if setCookie := response.Header.Get("Set-Cookie"); setCookie != "" {
		c.cookie = strings.SplitN(setCookie, ";", 2)[0]
	}
	var result map[string]any
	_ = json.Unmarshal(raw, &result)
	return response.StatusCode, response.Header, result
}

func (c *testClient) login(t *testing.T, password string) {
	t.Helper()
	status, _, result := c.request(t, http.MethodPost, "/api/login", map[string]any{"password": password}, c.server.URL)
	if status != http.StatusOK {
		t.Fatalf("login status=%d result=%v", status, result)
	}
	c.csrf, _ = result["csrf_token"].(string)
}

func TestHTTPBoundaryAndStaticAllowlist(t *testing.T) {
	backend := &testBackend{}
	server, _ := testServer(t, backend)
	defer server.Close()
	client := &testClient{server: server}

	status, _, _ := client.request(t, http.MethodGet, "/secret.txt", nil, "")
	if status != http.StatusNotFound {
		t.Fatalf("secret static status=%d", status)
	}
	status, _, _ = client.request(t, http.MethodPost, "/api/login", map[string]any{"password": "correct horse"}, "http://localhost:1")
	if status != http.StatusForbidden {
		t.Fatalf("cross-origin login status=%d", status)
	}
	client.login(t, "correct horse")
	status, _, result := client.request(t, http.MethodGet, "/api/state", nil, "")
	if status != http.StatusOK || result["script"] != "<script>" {
		t.Fatalf("state status=%d result=%v", status, result)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "secret") {
		t.Fatalf("state leaked secret: %s", encoded)
	}
	status, _, result = client.request(t, http.MethodPost, "/api/logout", map[string]any{}, "")
	if status != http.StatusForbidden || result["error"] != "origin_forbidden" {
		t.Fatalf("missing origin status=%d result=%v", status, result)
	}
}

func TestLegacyPasswordHashLayoutRemainsCompatible(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	passwordFile := filepath.Join(root, "secrets", "admin-password")
	if err := os.MkdirAll(filepath.Dir(passwordFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(passwordFile, []byte("correct horse\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "secrets"), 0o700); err != nil {
		t.Fatal(err)
	}
	legacyHash := `{"salt":"000102030405060708090a0b0c0d0e0f","digest":"3bb34e709cf5f519f165234eac97a16c0413ce5cfdb8bad045a483bd139c1559"}`
	if err := os.WriteFile(filepath.Join(dataDir, "secrets", "admin-password.hash"), []byte(legacyHash+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	auth, err := loadPassword(dataDir, passwordFile)
	if err != nil || !auth.verify("correct horse") || auth.verify("wrong") {
		t.Fatalf("legacy password hash was not accepted: err=%v", err)
	}
}

func TestResponseRedactsSecretsInsideText(t *testing.T) {
	if got := redactText("token=abc secret:xyz https://user:pass@example.invalid bearer qwerty"); strings.Contains(got, "abc") || strings.Contains(got, "xyz") || strings.Contains(got, "user:pass") || strings.Contains(got, "qwerty") {
		t.Fatalf("text secret was not redacted: %q", got)
	}
}

func TestMaintenanceModesAndIdempotentJobs(t *testing.T) {
	backend := &testBackend{syncRun: make(chan struct{}, 1), syncDone: make(chan struct{})}
	server, _ := testServer(t, backend)
	defer server.Close()
	client := &testClient{server: server}
	client.login(t, "correct horse")
	origin := server.URL
	status, _, report := client.request(t, http.MethodPost, "/api/maintenance", map[string]any{"mode": "report"}, origin)
	if status != http.StatusAccepted {
		t.Fatalf("report status=%d result=%v", status, report)
	}
	status, _, agent := client.request(t, http.MethodPost, "/api/maintenance", map[string]any{"mode": "agent", "model": "gpt-test"}, origin)
	if status != http.StatusAccepted {
		t.Fatalf("agent status=%d result=%v", status, agent)
	}
	waitJob := func(jobID string) map[string]any {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for {
			status, _, job := client.request(t, http.MethodGet, "/api/jobs/"+jobID, nil, "")
			if status != http.StatusOK {
				t.Fatalf("job status=%d result=%v", status, job)
			}
			switch job["status"] {
			case "succeeded":
				if _, ok := job["result"].(map[string]any); !ok {
					t.Fatalf("job result shape=%v", job)
				}
				return job
			case "failed":
				t.Fatalf("job failed: %v", job)
			}
			if !time.Now().Before(deadline) {
				t.Fatalf("job did not finish: %v", job)
			}
			time.Sleep(time.Millisecond)
		}
	}
	waitJob(report["job_id"].(string))
	waitJob(agent["job_id"].(string))

	backend.mu.Lock()
	calls := make(map[string]string, len(backend.maintenance))
	for _, call := range backend.maintenance {
		if _, exists := calls[call.mode]; exists {
			t.Fatalf("duplicate maintenance mode calls=%v", backend.maintenance)
		}
		calls[call.mode] = call.model
	}
	if len(calls) != 2 || calls["report"] != "" || calls["agent"] != "gpt-test" {
		t.Fatalf("maintenance calls=%v", backend.maintenance)
	}
	backend.mu.Unlock()

	status, _, first := client.request(t, http.MethodPost, "/api/repository/sync", map[string]any{}, origin)
	if status != http.StatusAccepted {
		t.Fatalf("sync status=%d result=%v", status, first)
	}
	<-backend.syncRun
	status, _, second := client.request(t, http.MethodPost, "/api/repository/sync", map[string]any{}, origin)
	if status != http.StatusAccepted || first["job_id"] != second["job_id"] {
		t.Fatalf("sync jobs first=%v second=%v", first, second)
	}
	close(backend.syncDone)
}

func TestPreviewIsSessionBoundAndUsesBackendReviewID(t *testing.T) {
	backend := &testBackend{}
	server, _ := testServer(t, backend)
	defer server.Close()
	client := &testClient{server: server}
	client.login(t, "correct horse")
	origin := server.URL
	status, _, preview := client.request(t, http.MethodPost, "/api/proposals/preview", map[string]any{
		"report_id": "review-1", "proposal_index": 0, "target": "inbox/example.md", "content": "new",
	}, origin)
	if status != http.StatusOK || preview["preview_id"] == nil {
		t.Fatalf("preview status=%d result=%v", status, preview)
	}
	previewID := preview["preview_id"].(string)
	otherClient := &testClient{server: server}
	otherClient.login(t, "correct horse")
	status, _, _ = otherClient.request(t, http.MethodPost, "/api/proposals/apply", map[string]any{
		"confirm": true, "preview_id": previewID, "target": "inbox/example.md", "content": "new", "message": "review",
	}, origin)
	if status != http.StatusConflict {
		t.Fatalf("cross-session apply status=%d", status)
	}
	status, _, _ = client.request(t, http.MethodPost, "/api/proposals/apply", map[string]any{
		"confirm": true, "preview_id": previewID, "target": "inbox/example.md", "content": "changed", "message": "review",
	}, origin)
	if status != http.StatusConflict {
		t.Fatalf("mismatch apply status=%d", status)
	}
	status, _, _ = client.request(t, http.MethodPost, "/api/logout", map[string]any{}, origin)
	if status != http.StatusOK {
		t.Fatalf("logout status=%d", status)
	}
	client.login(t, "correct horse")
	status, _, _ = client.request(t, http.MethodPost, "/api/proposals/apply", map[string]any{
		"confirm": true, "preview_id": previewID, "target": "inbox/example.md", "content": "new", "message": "review",
	}, origin)
	if status != http.StatusConflict {
		t.Fatalf("post-login stale preview status=%d", status)
	}
	status, _, preview = client.request(t, http.MethodPost, "/api/proposals/preview", map[string]any{
		"report_id": "review-1", "proposal_index": 0, "target": "inbox/example.md", "content": "new",
	}, origin)
	if status != http.StatusOK || preview["preview_id"] == nil {
		t.Fatalf("second preview status=%d result=%v", status, preview)
	}
	previewID = preview["preview_id"].(string)
	status, _, _ = client.request(t, http.MethodPost, "/api/proposals/apply", map[string]any{
		"confirm": true, "preview_id": previewID, "target": "inbox/example.md", "content": "new", "message": "review",
	}, origin)
	if status != http.StatusOK {
		t.Fatalf("apply status=%d", status)
	}
	status, _, _ = client.request(t, http.MethodPost, "/api/proposals/apply", map[string]any{
		"confirm": true, "preview_id": previewID, "target": "inbox/example.md", "content": "new", "message": "review",
	}, origin)
	if status != http.StatusConflict {
		t.Fatalf("replay apply status=%d", status)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.preview) != 2 || backend.preview[0].reportID != "review-1" || backend.preview[0].index != 0 || backend.preview[1].reportID != "review-1" || backend.preview[1].index != 0 || len(backend.applied) != 1 || backend.applied[0].reviewID != "review-id" {
		t.Fatalf("preview=%v applied=%v", backend.preview, backend.applied)
	}
}
