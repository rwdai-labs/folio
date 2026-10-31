package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"folio/internal/quota"
	"folio/internal/storage"
)

type testBackend struct {
	taskCreates []string
	dispatches  int
	mu          sync.Mutex
	maintenance []struct{ mode, model, runner string }
	preview     []struct {
		reportID string
		index    int
		target   string
		content  string
	}
	applied      []struct{ reviewID, message string }
	automation   []struct{ trigger, runner, model string }
	syncRun      chan struct{}
	syncDone     chan struct{}
	channelSaves []map[string]any
	quotaCalls   []bool
	quotaValue   quota.Snapshot
}

func (b *testBackend) Executor() storage.Executor { return nil }

func (b *testBackend) State() (map[string]any, error) {
	return map[string]any{
		"provider": map[string]any{"access_token": "secret"},
		"script":   "<script>",
		"runners": map[string]any{
			"codex": map[string]any{"available": true},
			"dsh":   map[string]any{"available": false},
		},
	}, nil
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
func (b *testBackend) RunMaintenance(mode, model, runner string) (map[string]any, error) {
	b.mu.Lock()
	b.maintenance = append(b.maintenance, struct{ mode, model, runner string }{mode, model, runner})
	b.mu.Unlock()
	return map[string]any{"mode": mode, "model": model, "runner": runner, "report": "safe"}, nil
}
func (b *testBackend) RunAutomation(trigger, runner, model string) (map[string]any, error) {
	b.mu.Lock()
	b.automation = append(b.automation, struct{ trigger, runner, model string }{trigger, runner, model})
	b.mu.Unlock()
	return map[string]any{"run_id": "20261020T000000.000000000Z-abcdef12", "status": "pushed", "trigger": trigger, "runner": runner}, nil
}
func (b *testBackend) UsageSummary(days int) (map[string]any, error) {
	return map[string]any{
		"window_days": days,
		"totals": map[string]any{
			"input_tokens": 100, "cached_tokens": 50, "output_tokens": 20,
			"total_tokens": 170, "runs": 2, "reported_runs": 1, "unreported_runs": 1,
		},
		"days":   []any{},
		"models": []any{},
	}, nil
}

func (b *testBackend) Quotas(refresh bool) (quota.Snapshot, error) {
	b.mu.Lock()
	b.quotaCalls = append(b.quotaCalls, refresh)
	value := b.quotaValue
	b.mu.Unlock()
	return value, nil
}

func (b *testBackend) ListAutomation() (map[string]any, error) {
	return map[string]any{"runs": []any{map[string]any{"run_id": "20261020T000000.000000000Z-abcdef12", "status": "pushed"}}, "count": 1, "latest": map[string]any{"run_id": "20261020T000000.000000000Z-abcdef12", "status": "pushed", "edits": []any{map[string]any{"target": "core/profile.md", "diff": "-old\n+new"}}}}, nil
}
func (b *testBackend) AutomationRun(runID string) (map[string]any, error) {
	return map[string]any{"run_id": runID, "status": "pushed"}, nil
}
func (b *testBackend) ModelCatalog() map[string]any {
	return map[string]any{
		"codex": []any{map[string]any{"id": "gpt-test", "label": "GPT Test"}},
		"dsh":   []any{map[string]any{"id": "example-large", "label": "Example Large", "provider": "example-route"}},
	}
}
func (b *testBackend) ChannelState() (map[string]any, error) {
	return map[string]any{
		"language": "en",
		"bark": map[string]any{
			"enabled": false, "configured": false,
			"server": "https://api.day.app", "level": "active",
		},
		"deliveries": []any{},
	}, nil
}

func (b *testBackend) SaveBarkChannel(payload map[string]any) (map[string]any, error) {
	if key, _ := payload["device_key"].(string); key == "" {
		return nil, errors.New("device_key_required")
	}
	b.mu.Lock()
	b.channelSaves = append(b.channelSaves, payload)
	b.mu.Unlock()
	return map[string]any{
		"language": "en",
		"bark": map[string]any{
			"enabled": true, "configured": true,
			"server": "https://api.day.app", "level": "active",
		},
		"deliveries": []any{map[string]any{"kind": "test", "channel": "bark", "status": "delivered"}},
	}, nil
}

func (b *testBackend) TestBarkChannel(override map[string]any) (map[string]any, error) {
	return map[string]any{
		"receipt": map[string]any{"channel": "bark", "status": "delivered", "at": "2026-10-23T00:00:00Z"},
	}, nil
}

func (b *testBackend) ListHooks() (map[string]any, error) {
	return map[string]any{"hooks": []any{}, "actions": []any{"maintenance"}, "max_hooks": 16}, nil
}

func (b *testBackend) CreateHook(name, action, priority string) (map[string]any, error) {
	return map[string]any{
		"hook":   map[string]any{"id": "watch", "name": name, "action": "maintenance", "enabled": true},
		"secret": "test-secret",
	}, nil
}

func (b *testBackend) UpdateHook(id string, enabled bool, name, priority string) (map[string]any, error) {
	return map[string]any{"hooks": []any{}}, nil
}

func (b *testBackend) RotateHookSecret(id string) (map[string]any, error) {
	return map[string]any{"secret": "rotated-secret"}, nil
}

func (b *testBackend) DeleteHook(id string) (map[string]any, error) {
	return map[string]any{"hooks": []any{}}, nil
}

func (b *testBackend) AcceptHook(id string, timestamp int64, eventID, signature string, body []byte) (map[string]any, string) {
	if signature != "good" {
		return nil, "invalid_signature"
	}
	return map[string]any{"name": "Watch context", "action": "maintenance", "priority": "P2"}, "ok"
}

func (b *testBackend) TaskKinds() (map[string]any, error) {
	return map[string]any{
		"actions": []string{"maintenance", "triage"},
		"kinds": []any{
			map[string]any{"id": "maintenance", "label": "Context maintenance", "action": "maintenance", "priority": "P2"},
			map[string]any{"id": "triage", "label": "Issue triage", "action": "report", "priority": "P2"},
		},
	}, nil
}

func (b *testBackend) ListTasks() (map[string]any, error) {
	return map[string]any{
		"paused": false,
		"policy": map[string]any{"defer": map[string]bool{"P0": false, "P2": true}, "window_start": "00:30", "window_end": "07:30"},
		"counts": map[string]int{"queued": 1},
		"tasks": []any{map[string]any{
			"id": "t-000000000001", "kind": "maintenance", "title": "Watch context",
			"priority": "P2", "status": "queued", "enabled": true, "due": false,
		}},
	}, nil
}

func (b *testBackend) CreateTask(kind, source, title, priority string, payload map[string]any) (map[string]any, error) {
	b.taskCreates = append(b.taskCreates, kind+"|"+source+"|"+priority)
	return map[string]any{
		"task": map[string]any{
			"id": "t-000000000001", "kind": kind, "title": title, "source": source,
			"priority": priority, "status": "queued", "enabled": true,
			"not_before": "2999-01-01T00:00:00Z",
		},
		"created": true,
	}, nil
}

func (b *testBackend) SetTasksPaused(paused bool) (map[string]any, error) {
	return map[string]any{"paused": paused}, nil
}

func (b *testBackend) UpdateTask(id string, enabled bool) (map[string]any, error) {
	return map[string]any{"tasks": []any{}}, nil
}

func (b *testBackend) RunTaskNow(id string) (map[string]any, error) {
	return map[string]any{"tasks": []any{}}, nil
}

func (b *testBackend) CancelTask(id string) (map[string]any, error) {
	return map[string]any{"tasks": []any{}}, nil
}

func (b *testBackend) DispatchDue() (map[string]any, error) {
	b.dispatches++
	return map[string]any{"dispatched": 1, "task_id": "t-000000000001"}, nil
}

func (b *testBackend) SchedulerTick() (map[string]any, error) {
	return map[string]any{"status": "skipped"}, nil
}
func testServer(t *testing.T, backend Backend) (*httptest.Server, string) {
	t.Helper()
	root := t.TempDir()
	assets := filepath.Join(root, "assets")
	if err := os.MkdirAll(assets, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"index.html": "index", "app.js": "script", "i18n.js": "i18n", "styles.css": "style", "secret.txt": "secret"} {
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
	for _, asset := range []string{"/app.js", "/i18n.js", "/styles.css"} {
		status, _, _ = client.request(t, http.MethodGet, asset, nil, "")
		if status != http.StatusOK {
			t.Fatalf("%s status=%d", asset, status)
		}
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
	runners, ok := result["runners"].(map[string]any)
	if !ok || runners["codex"] == nil || runners["dsh"] == nil {
		t.Fatalf("state runners=%v", result["runners"])
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
	auth, generated, err := loadPassword(dataDir, passwordFile)
	if err != nil || !auth.verify("correct horse") || auth.verify("wrong") {
		t.Fatalf("legacy password hash was not accepted: err=%v", err)
	}
	if generated != "" {
		t.Fatalf("existing password was reported as generated: %q", generated)
	}
}

func TestGeneratedPasswordIsAnnouncedOnceThenReused(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	passwordFile := filepath.Join(root, "secrets", "admin-password")
	assets := filepath.Join(root, "assets")
	if err := os.MkdirAll(assets, 0o700); err != nil {
		t.Fatal(err)
	}
	var notices []struct{ password, path string }
	notice := func(password, path string) {
		notices = append(notices, struct{ password, path string }{password, path})
	}
	options := Options{DataDir: dataDir, PasswordFile: passwordFile, AssetsDir: assets, PasswordNotice: notice}

	server, err := New(&testBackend{}, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(notices) != 1 {
		t.Fatalf("first start notices=%d, want 1", len(notices))
	}
	password := notices[0].password
	if len(password) != 64 || strings.Trim(password, "0123456789abcdef") != "" {
		t.Fatalf("announced password is not 64 hex characters: %d bytes", len(password))
	}
	if notices[0].path != passwordFile {
		t.Fatalf("notice path=%q, want %q", notices[0].path, passwordFile)
	}
	stored, err := os.ReadFile(passwordFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(stored)) != password {
		t.Fatal("announced password does not match the stored file")
	}
	info, err := os.Stat(passwordFile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("password file mode=%v, want 0600", info.Mode().Perm())
	}
	if !server.auth.verify(password) {
		t.Fatal("announced password does not authenticate")
	}

	restarted, err := New(&testBackend{}, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(notices) != 1 {
		t.Fatalf("second start notices=%d, want 1", len(notices))
	}
	if !restarted.auth.verify(password) {
		t.Fatal("second start does not accept the stored password")
	}

	// An operator-provided password file is authoritative and never announced.
	provided := filepath.Join(root, "provided", "admin-password")
	if err := os.MkdirAll(filepath.Dir(provided), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(provided, []byte("correct horse\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(&testBackend{}, Options{DataDir: filepath.Join(root, "other-data"), PasswordFile: provided, AssetsDir: assets, PasswordNotice: notice}); err != nil {
		t.Fatal(err)
	}
	if len(notices) != 1 {
		t.Fatalf("provided password notices=%d, want 1", len(notices))
	}

	// Deleting the password file regenerates the credential. The cached
	// verifier for the old password must not shadow the announced one.
	if err := os.Remove(passwordFile); err != nil {
		t.Fatal(err)
	}
	regenerated, err := New(&testBackend{}, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(notices) != 2 {
		t.Fatalf("reset start notices=%d, want 2", len(notices))
	}
	if !regenerated.auth.verify(notices[1].password) {
		t.Fatal("regenerated password does not authenticate")
	}
	if regenerated.auth.verify(password) {
		t.Fatal("replaced password still authenticates")
	}
}

func TestResponseRedactsSecretsInsideText(t *testing.T) {
	if got := redactText("token=abc secret:xyz https://user:pass@example.invalid bearer qwerty"); strings.Contains(got, "abc") || strings.Contains(got, "xyz") || strings.Contains(got, "user:pass") || strings.Contains(got, "qwerty") {
		t.Fatalf("text secret was not redacted: %q", got)
	}
}

func waitJobForTest(t *testing.T, client *testClient, jobID string) map[string]any {
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

func TestMaintenanceRunnerSelectionAndValidation(t *testing.T) {
	backend := &testBackend{}
	server, _ := testServer(t, backend)
	defer server.Close()
	client := &testClient{server: server}
	client.login(t, "correct horse")
	origin := server.URL

	for _, testCase := range []struct {
		name    string
		payload map[string]any
		code    string
	}{
		{"unknown runner", map[string]any{"mode": "agent", "runner": "other"}, "invalid_runner"},
		{"non-string runner", map[string]any{"mode": "agent", "runner": 7}, "invalid_runner"},
		{"null runner", map[string]any{"mode": "agent", "runner": nil}, "invalid_runner"},
		{"invalid model", map[string]any{"mode": "agent", "runner": "dsh", "model": "bad model"}, "invalid_model"},
	} {
		status, _, result := client.request(t, http.MethodPost, "/api/maintenance", testCase.payload, origin)
		if status != http.StatusBadRequest || result["error"] != testCase.code {
			t.Fatalf("%s status=%d result=%v", testCase.name, status, result)
		}
	}

	status, _, accepted := client.request(t, http.MethodPost, "/api/maintenance", map[string]any{"mode": "agent", "runner": "dsh", "model": "example-large"}, origin)
	if status != http.StatusAccepted || accepted["runner"] != "dsh" {
		t.Fatalf("dsh accepted status=%d result=%v", status, accepted)
	}
	job := waitJobForTest(t, client, accepted["job_id"].(string))
	if job["name"] != "maintenance:dsh:agent" {
		t.Fatalf("dsh job name=%v", job["name"])
	}

	status, _, defaulted := client.request(t, http.MethodPost, "/api/maintenance", map[string]any{"mode": "report", "runner": ""}, origin)
	if status != http.StatusAccepted || defaulted["runner"] != "codex" {
		t.Fatalf("default runner status=%d result=%v", status, defaulted)
	}
	waitJobForTest(t, client, defaulted["job_id"].(string))

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.maintenance) != 2 || backend.maintenance[0].runner != "dsh" || backend.maintenance[0].model != "example-large" || backend.maintenance[1].runner != "codex" {
		t.Fatalf("maintenance calls=%v", backend.maintenance)
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
	waitJobForTest(t, client, report["job_id"].(string))
	waitJobForTest(t, client, agent["job_id"].(string))

	backend.mu.Lock()
	calls := make(map[string]struct{ model, runner string }, len(backend.maintenance))
	for _, call := range backend.maintenance {
		if _, exists := calls[call.mode]; exists {
			t.Fatalf("duplicate maintenance mode calls=%v", backend.maintenance)
		}
		calls[call.mode] = struct{ model, runner string }{call.model, call.runner}
	}
	if len(calls) != 2 || calls["report"].model != "" || calls["agent"].model != "gpt-test" || calls["report"].runner != "codex" || calls["agent"].runner != "codex" {
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

func TestLegacyManualCommitRoutesAreGone(t *testing.T) {
	backend := &testBackend{}
	server, _ := testServer(t, backend)
	defer server.Close()
	client := &testClient{server: server}
	client.login(t, "correct horse")
	origin := server.URL
	for _, path := range []string{"/api/proposals", "/api/proposals/preview", "/api/proposals/apply", "/api/apply"} {
		method := http.MethodPost
		if path == "/api/proposals" {
			method = http.MethodGet
		}
		status, _, result := client.request(t, method, path, map[string]any{}, origin)
		if status != http.StatusNotFound || result["error"] != "not_found" {
			t.Fatalf("%s status=%d result=%v", path, status, result)
		}
	}
}

func TestAutomationEndpointsExposeAuditAndAcceptRun(t *testing.T) {
	backend := &testBackend{}
	server, password := testServer(t, backend)
	defer server.Close()
	client := &testClient{server: server}

	status, _, _ := client.request(t, http.MethodGet, "/api/automation", nil, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated automation status=%d", status)
	}
	client.login(t, password)

	status, _, listing := client.request(t, http.MethodGet, "/api/automation", nil, "")
	if status != http.StatusOK || listing["count"] != float64(1) {
		t.Fatalf("automation listing status=%d result=%v", status, listing)
	}
	latest, ok := listing["latest"].(map[string]any)
	if !ok || latest["status"] != "pushed" {
		t.Fatalf("automation latest=%v", listing["latest"])
	}
	models, ok := listing["models"].(map[string]any)
	if !ok {
		t.Fatalf("automation models=%v", listing["models"])
	}
	codexModels, _ := models["codex"].([]any)
	dshModels, _ := models["dsh"].([]any)
	if len(codexModels) != 1 || len(dshModels) != 1 {
		t.Fatalf("automation models=%v", models)
	}
	runID := "20261020T000000.000000000Z-abcdef12"
	status, _, detail := client.request(t, http.MethodGet, "/api/automation/runs/"+runID, nil, "")
	if status != http.StatusOK || detail["run_id"] != runID {
		t.Fatalf("automation detail status=%d result=%v", status, detail)
	}
	status, _, _ = client.request(t, http.MethodGet, "/api/automation/runs/bad/run", nil, "")
	if status != http.StatusNotFound {
		t.Fatalf("invalid run id status=%d", status)
	}

	origin := server.URL
	status, _, accepted := client.request(t, http.MethodPost, "/api/automation/run", map[string]any{"runner": "codex", "model": "gpt-test"}, origin)
	if status != http.StatusAccepted {
		t.Fatalf("automation run status=%d result=%v", status, accepted)
	}
	job := waitJobForTest(t, client, accepted["job_id"].(string))
	if job["name"] != "automation:codex" {
		t.Fatalf("automation job name=%v", job["name"])
	}
	if len(backend.automation) != 1 || backend.automation[0].trigger != "manual" || backend.automation[0].runner != "codex" || backend.automation[0].model != "gpt-test" {
		t.Fatalf("automation calls=%v", backend.automation)
	}

	status, _, accepted = client.request(t, http.MethodPost, "/api/automation/run", map[string]any{"runner": "dsh", "model": "example-large"}, origin)
	if status != http.StatusAccepted {
		t.Fatalf("dsh model status=%d result=%v", status, accepted)
	}
	waitJobForTest(t, client, accepted["job_id"].(string))
	if len(backend.automation) != 2 || backend.automation[1].runner != "dsh" || backend.automation[1].model != "example-large" {
		t.Fatalf("dsh automation calls=%v", backend.automation)
	}
	status, _, result := client.request(t, http.MethodPost, "/api/automation/run", map[string]any{"runner": "other"}, origin)
	if status != http.StatusBadRequest || result["error"] != "invalid_runner" {
		t.Fatalf("bad runner status=%d result=%v", status, result)
	}
}

func TestChannelEndpointsMaskDeviceKeyAndRequireSession(t *testing.T) {
	backend := &testBackend{}
	server, password := testServer(t, backend)
	defer server.Close()
	client := &testClient{server: server}

	status, _, _ := client.request(t, http.MethodGet, "/api/channels", nil, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated channels status=%d", status)
	}
	client.login(t, password)

	status, _, state := client.request(t, http.MethodGet, "/api/channels", nil, "")
	if status != http.StatusOK {
		t.Fatalf("channels status=%d result=%v", status, state)
	}
	bark, ok := state["bark"].(map[string]any)
	if !ok || bark["configured"] != false {
		t.Fatalf("channel state=%v", state)
	}
	if _, ok := bark["device_key"]; ok {
		t.Fatalf("channel state exposes a device key: %v", bark)
	}

	status, _, result := client.request(t, http.MethodPost, "/api/channels/bark", map[string]any{"enabled": true}, server.URL)
	if status != http.StatusBadRequest || result["error"] != "device_key_required" {
		t.Fatalf("missing key status=%d result=%v", status, result)
	}

	status, _, saved := client.request(t, http.MethodPost, "/api/channels/bark", map[string]any{
		"enabled": true, "server": "https://api.day.app", "device_key": "device-key-alpha", "level": "active",
	}, server.URL)
	if status != http.StatusOK {
		t.Fatalf("save status=%d result=%v", status, saved)
	}
	raw, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "device-key-alpha") {
		t.Fatalf("saved response leaked the device key: %s", raw)
	}
	if len(backend.channelSaves) != 1 {
		t.Fatalf("channel saves=%v", backend.channelSaves)
	}

	status, _, tested := client.request(t, http.MethodPost, "/api/channels/bark/test", map[string]any{}, server.URL)
	if status != http.StatusOK {
		t.Fatalf("test status=%d result=%v", status, tested)
	}
	receipt, ok := tested["receipt"].(map[string]any)
	if !ok || receipt["status"] != "delivered" {
		t.Fatalf("receipt=%v", tested["receipt"])
	}
}

func TestHookEndpointsAcceptSignedEventsWithoutOrigin(t *testing.T) {
	backend := &testBackend{}
	server, password := testServer(t, backend)
	defer server.Close()
	client := &testClient{server: server}

	status, _, _ := client.request(t, http.MethodGet, "/api/hooks", nil, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated hooks status=%d", status)
	}
	client.login(t, password)

	status, _, listing := client.request(t, http.MethodGet, "/api/hooks", nil, "")
	if status != http.StatusOK {
		t.Fatalf("hooks status=%d result=%v", status, listing)
	}
	status, _, created := client.request(t, http.MethodPost, "/api/hooks", map[string]any{"name": "Watch context"}, server.URL)
	if status != http.StatusOK || created["secret"] != "test-secret" {
		t.Fatalf("create hook status=%d result=%v", status, created)
	}

	// Inbound callers are scripts: no cookie, no CSRF token, no Origin header.
	post := func(signature string) (int, map[string]any) {
		request, err := http.NewRequest(http.MethodPost, server.URL+"/api/hooks/watch", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		request.Host = strings.TrimPrefix(server.URL, "http://")
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Folio-Timestamp", "1761000000")
		request.Header.Set("X-Folio-Event-Id", "event-1")
		request.Header.Set("X-Folio-Signature", signature)
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var result map[string]any
		_ = json.NewDecoder(response.Body).Decode(&result)
		return response.StatusCode, result
	}

	status, result := post("bad")
	if status != http.StatusUnauthorized || result["error"] != "hook_invalid_signature" {
		t.Fatalf("bad signature status=%d result=%v", status, result)
	}

	status, accepted := post("good")
	if status != http.StatusAccepted {
		t.Fatalf("accepted status=%d result=%v", status, accepted)
	}
	// The deferred task is durable but not due, so no run is started here.
	if accepted["task_id"] != "t-000000000001" || accepted["dispatched"] != false {
		t.Fatalf("accepted result=%v", accepted)
	}
	if accepted["priority"] != "P2" || accepted["created"] != true {
		t.Fatalf("accepted result=%v", accepted)
	}
	if len(backend.taskCreates) != 1 || backend.taskCreates[0] != "maintenance|hook:watch|P2" {
		t.Fatalf("task creates=%v", backend.taskCreates)
	}
	if backend.dispatches != 0 {
		t.Fatalf("dispatches=%d", backend.dispatches)
	}
}

func TestCredentialModeIsNotRedacted(t *testing.T) {
	value := safeMap(map[string]any{"git": map[string]any{"mode": "token"}})
	git, _ := value["git"].(map[string]any)
	if git["mode"] != "token" {
		t.Fatalf("credential mode was redacted: %v", value)
	}
}

func TestSafeValueKeepsNumericCountsAndRedactsCredentials(t *testing.T) {
	value := map[string]any{
		"total_tokens":  float64(1234),
		"cached_tokens": int64(7),
		"access_token":  "secret-value",
		"nested":        map[string]any{"input_tokens": float64(10)},
	}
	safe, ok := safeValue(value, "", 0).(map[string]any)
	if !ok {
		t.Fatal("safeValue did not return a map")
	}
	if safe["total_tokens"] != float64(1234) || safe["cached_tokens"] != int64(7) {
		t.Fatalf("numeric counts were redacted: %v", safe)
	}
	if safe["access_token"] != "[redacted]" {
		t.Fatalf("credential string was not redacted: %v", safe)
	}
	if nested, _ := safe["nested"].(map[string]any); nested["input_tokens"] != float64(10) {
		t.Fatalf("nested count was redacted: %v", safe)
	}
}

func TestTaskKindsAreSessionProtected(t *testing.T) {
	backend := &testBackend{}
	server, password := testServer(t, backend)
	defer server.Close()
	client := &testClient{server: server}

	status, _, _ := client.request(t, http.MethodGet, "/api/task-kinds", nil, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", status)
	}
	client.login(t, password)
	status, _, listing := client.request(t, http.MethodGet, "/api/task-kinds", nil, "")
	if status != http.StatusOK {
		t.Fatalf("status=%d", status)
	}
	kinds := listing["kinds"].([]any)
	if len(kinds) != 2 || kinds[1].(map[string]any)["id"] != "triage" {
		t.Fatalf("listing=%v", listing)
	}
	if kinds[1].(map[string]any)["label"] != "Issue triage" {
		t.Fatalf("label=%v", kinds[1])
	}
}

func TestTaskEndpointsRequireSessionAndControlTheQueue(t *testing.T) {
	backend := &testBackend{}
	server, password := testServer(t, backend)
	defer server.Close()
	client := &testClient{server: server}

	status, _, _ := client.request(t, http.MethodGet, "/api/tasks", nil, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", status)
	}
	client.login(t, password)

	status, _, listing := client.request(t, http.MethodGet, "/api/tasks", nil, "")
	if status != http.StatusOK {
		t.Fatalf("list status=%d", status)
	}
	tasks := listing["tasks"].([]any)
	if len(tasks) != 1 || tasks[0].(map[string]any)["priority"] != "P2" {
		t.Fatalf("listing=%v", listing)
	}

	status, _, paused := client.request(t, http.MethodPost, "/api/tasks/pause", map[string]any{"paused": true}, server.URL)
	if status != http.StatusOK || paused["paused"] != true {
		t.Fatalf("pause status=%d result=%v", status, paused)
	}
	status, _, _ = client.request(t, http.MethodPost, "/api/tasks/t-000000000001/run", map[string]any{}, server.URL)
	if status != http.StatusOK {
		t.Fatalf("run now status=%d", status)
	}
	status, _, _ = client.request(t, http.MethodPost, "/api/tasks/t-000000000001/dispatch", map[string]any{}, server.URL)
	if status != http.StatusNotFound {
		t.Fatalf("unknown verb status=%d", status)
	}
}

func TestQuotaEndpointIsSessionProtectedAndReturnsCards(t *testing.T) {
	backend := &testBackend{quotaValue: quota.Snapshot{
		Configured: true,
		Source:     "magpie",
		FetchedAt:  "2026-10-29T08:00:00Z",
		Cards: []quota.Card{{
			Provider:      "codex",
			ProviderLabel: "Codex",
			Account:       "a@example.com",
			Alias:         "personal",
			Kind:          "subscription",
			Plan:          "Pro",
			Windows: []quota.Window{{
				Name: "5h", Used: 42, Remaining: 58,
				ResetsAt: "2026-10-29T10:00:00Z", Display: "42% used",
			}},
			ReadAt: "2026-10-29T07:59:00Z",
		}},
	}}
	server, password := testServer(t, backend)
	defer server.Close()
	client := &testClient{server: server}

	status, _, _ := client.request(t, http.MethodGet, "/api/quotas", nil, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", status)
	}
	status, _, _ = client.request(t, http.MethodPost, "/api/quotas/refresh", map[string]any{}, server.URL)
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated refresh status=%d", status)
	}

	client.login(t, password)
	status, _, result := client.request(t, http.MethodGet, "/api/quotas", nil, "")
	if status != http.StatusOK {
		t.Fatalf("status=%d result=%v", status, result)
	}
	if result["configured"] != true || result["source"] != "magpie" || result["error"] != "" {
		t.Fatalf("result=%v", result)
	}
	cards, ok := result["cards"].([]any)
	if !ok || len(cards) != 1 {
		t.Fatalf("cards=%v", result["cards"])
	}
	card := cards[0].(map[string]any)
	if card["provider"] != "codex" || card["alias"] != "personal" || card["plan"] != "Pro" {
		t.Fatalf("card=%v", card)
	}
	windows := card["windows"].([]any)
	window := windows[0].(map[string]any)
	if window["used"] != float64(42) || window["resets_at"] != "2026-10-29T10:00:00Z" {
		t.Fatalf("window=%v", window)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "quota-key") || strings.Contains(string(encoded), "Authorization") {
		t.Fatalf("quota response leaked a credential: %s", encoded)
	}
	backend.mu.Lock()
	calls := append([]bool(nil), backend.quotaCalls...)
	backend.mu.Unlock()
	if len(calls) != 1 || calls[0] {
		t.Fatalf("quota calls=%v", calls)
	}
}

func TestQuotaRefreshMapsThrottleAndSuccess(t *testing.T) {
	backend := &testBackend{quotaValue: quota.Snapshot{
		Configured:        true,
		Error:             quota.ErrRefreshLimited,
		RetryAfterSeconds: 30,
		Cards:             []quota.Card{},
	}}
	server, password := testServer(t, backend)
	defer server.Close()
	client := &testClient{server: server}
	client.login(t, password)

	status, headers, result := client.request(t, http.MethodPost, "/api/quotas/refresh", map[string]any{}, server.URL)
	if status != http.StatusTooManyRequests || result["error"] != quota.ErrRefreshLimited {
		t.Fatalf("status=%d result=%v", status, result)
	}
	if headers.Get("Retry-After") != "30" || result["retry_after_seconds"] != float64(30) {
		t.Fatalf("retry headers=%v result=%v", headers, result)
	}

	backend.mu.Lock()
	backend.quotaValue = quota.Snapshot{
		Configured: true,
		Source:     "magpie",
		Cards:      []quota.Card{{Provider: "codex", ProviderLabel: "Codex", Kind: "subscription", Windows: []quota.Window{}}},
	}
	backend.mu.Unlock()
	status, _, result = client.request(t, http.MethodPost, "/api/quotas/refresh", map[string]any{}, server.URL)
	if status != http.StatusOK || result["configured"] != true {
		t.Fatalf("status=%d result=%v", status, result)
	}
	backend.mu.Lock()
	calls := append([]bool(nil), backend.quotaCalls...)
	backend.mu.Unlock()
	if len(calls) != 2 || !calls[0] || !calls[1] {
		t.Fatalf("quota calls=%v", calls)
	}
}
