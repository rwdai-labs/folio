package quota

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const fixtureCards = `{
  "object": "list",
  "data": [
    {
      "provider": "codex",
      "name": "Codex",
      "kind": "subscription",
      "plan": "Pro",
      "user": "a@example.com",
      "alias": "personal",
      "windows": [
        {"name":"5h","used":42.5,"remaining":57.5,"resetsAt":"2026-10-29T10:00:00Z","display":"42% used"},
        {"name":"credits","text":true,"display":"3 left"},
        {"name":"weekly","unlimited":true,"display":"unlimited"}
      ],
      "readAt": "2026-10-29T07:59:00Z"
    },
    {
      "provider": "claude",
      "name": "Claude",
      "kind": "plan",
      "windows": [],
      "balance": "$12.00",
      "readAt": null
    },
    {"provider": "", "name": "", "kind": "subscription"}
  ]
}`

func writeQuotaConfig(t *testing.T, dir, rawURL, keyFile string, timeout, maxAge int) {
	t.Helper()
	value := map[string]any{
		"version": 1,
		"source": map[string]any{
			"kind":            "magpie",
			"url":             rawURL,
			"key_file":        keyFile,
			"timeout_seconds": timeout,
			"max_age_seconds": maxAge,
		},
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, configName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeQuotaKey(t *testing.T, dir, value string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "secrets"), 0o700); err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(dir, "secrets", "magpie-quota-key")
	if err := os.WriteFile(keyFile, []byte(value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return "secrets/magpie-quota-key"
}

func newQuotaService(t *testing.T, handler http.HandlerFunc) (*Service, *httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	keyFile := writeQuotaKey(t, dir, "test-key")
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	writeQuotaConfig(t, dir, server.URL, keyFile, 2, 5)
	return New(dir), server, dir
}

func TestSnapshotNormalizesCards(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	service, _, _ := newQuotaService(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		if r.Method != http.MethodGet || r.URL.Path != "/v1/magpie/quotas" || r.URL.Query().Get("kept") != "1" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("authorization=%q", got)
		}
		_, _ = w.Write([]byte(fixtureCards))
	})

	snapshot := service.Snapshot(context.Background(), false)
	if !snapshot.Configured || snapshot.Source != "magpie" || snapshot.Error != "" {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	if len(snapshot.Cards) != 2 {
		t.Fatalf("cards=%d", len(snapshot.Cards))
	}
	codex := snapshot.Cards[0]
	if codex.ProviderLabel != "Codex" || codex.Account != "a@example.com" || codex.Alias != "personal" || codex.Kind != "subscription" || codex.Plan != "Pro" {
		t.Fatalf("codex card=%+v", codex)
	}
	if len(codex.Windows) != 3 {
		t.Fatalf("codex windows=%d", len(codex.Windows))
	}
	if got := codex.Windows[0]; got.Name != "5h" || got.Used != 42.5 || got.Remaining != 57.5 || got.ResetsAt != "2026-10-29T10:00:00Z" {
		t.Fatalf("window=%+v", got)
	}
	if !codex.Windows[1].Text || codex.Windows[1].Display != "3 left" {
		t.Fatalf("text window=%+v", codex.Windows[1])
	}
	if !codex.Windows[2].Unlimited {
		t.Fatalf("unlimited window=%+v", codex.Windows[2])
	}
	claude := snapshot.Cards[1]
	if claude.Kind != "plan" || claude.Balance != "$12.00" || !claude.Stale || claude.ReadAt != "" {
		t.Fatalf("claude card=%+v", claude)
	}

	// A fresh cache must not ask the source again.
	service.cachedAt = time.Now()
	mu.Lock()
	before := calls
	mu.Unlock()
	_ = service.Snapshot(context.Background(), false)
	mu.Lock()
	after := calls
	mu.Unlock()
	if after != before {
		t.Fatalf("fresh cache made %d calls, want 0", after-before)
	}
}

func TestSnapshotUnconfigured(t *testing.T) {
	service := New(t.TempDir())
	snapshot := service.Snapshot(context.Background(), false)
	if snapshot.Configured || snapshot.Error != "" || len(snapshot.Cards) != 0 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestInvalidConfig(t *testing.T) {
	cases := []struct {
		name  string
		value map[string]any
	}{
		{"version", map[string]any{"version": 2, "source": map[string]any{"kind": "magpie", "url": "http://magpie:3425"}}},
		{"kind", map[string]any{"version": 1, "source": map[string]any{"kind": "other", "url": "http://magpie:3425"}}},
		{"url userinfo", map[string]any{"version": 1, "source": map[string]any{"kind": "magpie", "url": "http://user:pass@magpie:3425"}}},
		{"url query", map[string]any{"version": 1, "source": map[string]any{"kind": "magpie", "url": "http://magpie:3425?x=1"}}},
		{"url path", map[string]any{"version": 1, "source": map[string]any{"kind": "magpie", "url": "http://magpie:3425/api"}}},
		{"key missing", map[string]any{"version": 1, "source": map[string]any{"kind": "magpie", "url": "http://magpie:3425"}}},
		{"key absolute", map[string]any{"version": 1, "source": map[string]any{"kind": "magpie", "url": "http://magpie:3425", "key_file": "/etc/passwd"}}},
		{"key parent", map[string]any{"version": 1, "source": map[string]any{"kind": "magpie", "url": "http://magpie:3425", "key_file": "../key"}}},
		{"timeout", map[string]any{"version": 1, "source": map[string]any{"kind": "magpie", "url": "http://magpie:3425", "timeout_seconds": 31}}},
		{"max age", map[string]any{"version": 1, "source": map[string]any{"kind": "magpie", "url": "http://magpie:3425", "max_age_seconds": 4}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			raw, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, configName), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			snapshot := New(dir).Snapshot(context.Background(), false)
			if !snapshot.Configured || snapshot.Error != ErrInvalidConfig {
				t.Fatalf("snapshot=%+v", snapshot)
			}
		})
	}
}

func TestMissingKey(t *testing.T) {
	dir := t.TempDir()
	writeQuotaConfig(t, dir, "http://magpie:3425", "secrets/missing", 2, 5)
	snapshot := New(dir).Snapshot(context.Background(), false)
	if !snapshot.Configured || snapshot.Error != ErrKeyUnavailable || len(snapshot.Cards) != 0 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestUpstreamErrors(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		body      string
		retry     string
		wantCode  string
		wantRetry int
	}{
		{"unauthorized", http.StatusUnauthorized, `{}`, "", ErrUnauthorized, 0},
		{"forbidden", http.StatusForbidden, `{}`, "", ErrUnauthorized, 0},
		{"unavailable", http.StatusInternalServerError, `{}`, "", ErrUnavailable, 0},
		{"rate limited", http.StatusTooManyRequests, `{}`, "7", ErrRateLimited, 7},
		{"invalid json", http.StatusOK, `{`, "", ErrInvalidResponse, 0},
		{"empty", http.StatusOK, ``, "", ErrInvalidResponse, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service, _, _ := newQuotaService(t, func(w http.ResponseWriter, _ *http.Request) {
				if tc.retry != "" {
					w.Header().Set("Retry-After", tc.retry)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			snapshot := service.Snapshot(context.Background(), false)
			if snapshot.Error != tc.wantCode || snapshot.RetryAfterSeconds != tc.wantRetry {
				t.Fatalf("snapshot=%+v", snapshot)
			}
		})
	}
}

func TestTimeout(t *testing.T) {
	service, _, _ := newQuotaService(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write([]byte(fixtureCards))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	snapshot := service.Snapshot(ctx, false)
	if snapshot.Error != ErrUnavailable {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestOversizedResponse(t *testing.T) {
	service, _, _ := newQuotaService(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", maxResponseBytes+1)))
	})
	snapshot := service.Snapshot(context.Background(), false)
	if snapshot.Error != ErrInvalidResponse {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestStaleCacheOnUpstreamError(t *testing.T) {
	var mu sync.Mutex
	fail := false
	service, _, _ := newQuotaService(t, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		down := fail
		mu.Unlock()
		if down {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(fixtureCards))
	})

	first := service.Snapshot(context.Background(), false)
	if first.Error != "" || len(first.Cards) != 2 {
		t.Fatalf("first=%+v", first)
	}
	service.cachedAt = time.Now().Add(-time.Hour)
	mu.Lock()
	fail = true
	mu.Unlock()

	second := service.Snapshot(context.Background(), false)
	if second.Error != ErrUnavailable || !second.Stale || len(second.Cards) != 2 || second.FetchedAt != first.FetchedAt {
		t.Fatalf("second=%+v", second)
	}
}

func TestRefreshAndLocalLimit(t *testing.T) {
	var mu sync.Mutex
	refreshCalls, keptCalls := 0, 0
	service, _, _ := newQuotaService(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/magpie/quotas/refresh":
			refreshCalls++
			_, _ = w.Write([]byte(fixtureCards))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/magpie/quotas":
			keptCalls++
			_, _ = w.Write([]byte(fixtureCards))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	first := service.Snapshot(context.Background(), true)
	if first.Error != "" || len(first.Cards) != 2 {
		t.Fatalf("first=%+v", first)
	}
	mu.Lock()
	if refreshCalls != 1 || keptCalls != 0 {
		t.Fatalf("refresh=%d kept=%d", refreshCalls, keptCalls)
	}
	mu.Unlock()

	second := service.Snapshot(context.Background(), true)
	if second.Error != ErrRefreshLimited || second.RetryAfterSeconds == 0 {
		t.Fatalf("second=%+v", second)
	}
	mu.Lock()
	if refreshCalls != 1 {
		t.Fatalf("refresh calls after local limit=%d", refreshCalls)
	}
	mu.Unlock()

	// A magpie refresh with no body must fall back to the kept snapshot.
	emptyService, _, _ := newQuotaService(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = w.Write([]byte(fixtureCards))
	})
	third := emptyService.Snapshot(context.Background(), true)
	if third.Error != "" || len(third.Cards) != 2 {
		t.Fatalf("third=%+v", third)
	}
}

func TestConfigChangeClearsCache(t *testing.T) {
	dir := t.TempDir()
	keyFile := writeQuotaKey(t, dir, "test-key")
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(fixtureCards))
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"provider":"new","name":"New","kind":"plan","windows":[]}]}`))
	}))
	defer second.Close()

	writeQuotaConfig(t, dir, first.URL, keyFile, 2, 5)
	service := New(dir)
	if got := service.Snapshot(context.Background(), false); len(got.Cards) != 2 {
		t.Fatalf("first snapshot cards=%d", len(got.Cards))
	}
	writeQuotaConfig(t, dir, second.URL, keyFile, 2, 5)
	got := service.Snapshot(context.Background(), false)
	if len(got.Cards) != 1 || got.Cards[0].Provider != "new" {
		t.Fatalf("second snapshot=%+v", got)
	}
}

func TestParseCardsBounds(t *testing.T) {
	var cards []map[string]any
	for i := 0; i < maxCards+10; i++ {
		windows := make([]map[string]any, 0, maxWindows+2)
		for j := 0; j < maxWindows+2; j++ {
			windows = append(windows, map[string]any{"name": strings.Repeat("w", 10), "used": 10})
		}
		cards = append(cards, map[string]any{
			"provider": strings.Repeat("p", 100),
			"name":     strings.Repeat("n", 300),
			"kind":     "subscription",
			"windows":  windows,
		})
	}
	raw, err := json.Marshal(map[string]any{"data": cards})
	if err != nil {
		t.Fatal(err)
	}
	parsed, code := parseCards(raw)
	if code != "" {
		t.Fatalf("code=%q", code)
	}
	if len(parsed) != maxCards {
		t.Fatalf("cards=%d", len(parsed))
	}
	if len(parsed[0].Windows) != maxWindows {
		t.Fatalf("windows=%d", len(parsed[0].Windows))
	}
	if len(parsed[0].Provider) > maxProviderRunes || len(parsed[0].ProviderLabel) > maxNameRunes {
		t.Fatalf("unbounded card=%+v", parsed[0])
	}
}

func TestParseCardsEmptyAndArray(t *testing.T) {
	empty, code := parseCards([]byte(`{"object":"list","data":[]}`))
	if code != "" || empty == nil || len(empty) != 0 {
		t.Fatalf("empty=%v code=%q", empty, code)
	}
	array, code := parseCards([]byte(`[{"provider":"x","name":"X","kind":"plan","windows":[]}]`))
	if code != "" || len(array) != 1 {
		t.Fatalf("array=%v code=%q", array, code)
	}
	if _, code := parseCards([]byte(`{"object":"list"}`)); code != ErrInvalidResponse {
		t.Fatalf("missing data code=%q", code)
	}
}

func TestReadKeyRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	writeQuotaConfig(t, dir, "http://magpie:3425", "secrets/link", 2, 5)
	if err := os.MkdirAll(filepath.Join(dir, "secrets"), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "secrets", "link")); err != nil {
		t.Fatal(err)
	}
	snapshot := New(dir).Snapshot(context.Background(), false)
	if snapshot.Error != ErrKeyUnavailable {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestLoadConfigMissing(t *testing.T) {
	_, code := loadConfig(t.TempDir())
	if code != unconfiguredCode {
		t.Fatalf("code=%q", code)
	}
}

func TestSnapshotConfigErrorDoesNotLeakKeyPath(t *testing.T) {
	dir := t.TempDir()
	writeQuotaConfig(t, dir, "http://magpie:3425", "secrets/missing", 2, 5)
	snapshot := New(dir).Snapshot(context.Background(), false)
	if strings.Contains(snapshot.Error, "secrets") || strings.Contains(snapshot.Error, "missing") {
		t.Fatalf("error leaks path: %q", snapshot.Error)
	}
}
