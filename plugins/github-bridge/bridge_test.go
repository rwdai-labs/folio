package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testSecret = "0123456789abcdef0123456789abcdef"

func writeTemp(t *testing.T, name, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadSecretRejectsLoosePermissions(t *testing.T) {
	path := writeTemp(t, "secret", testSecret, 0o644)
	if _, err := readSecret(path); err == nil {
		t.Fatal("group-readable secret was accepted")
	}
	path = writeTemp(t, "secret", testSecret, 0o600)
	got, err := readSecret(path)
	if err != nil || string(got) != testSecret {
		t.Fatalf("secret=%q err=%v", got, err)
	}
	t.Setenv("FOLIO_HOOK_SECRET", "env-secret-0123456789")
	got, err = readSecret("")
	if err != nil || string(got) != "env-secret-0123456789" {
		t.Fatalf("env secret=%q err=%v", got, err)
	}
}

func TestValidateConfigDefaultsAndRejectsBadInput(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_"+strings.Repeat("a", 36))
	value := config{
		Folio:   folioConfig{URL: "http://127.0.0.1:8080", HookID: "issues", SecretFile: "/tmp/secret"},
		Sources: []sourceConfig{{Type: "github", Name: "issues", Repos: []string{"owner/repo"}}},
	}
	if err := value.validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if value.IntervalSeconds != 300 || value.Folio.TimeoutSeconds != 15 {
		t.Fatalf("defaults not applied: %+v", value)
	}
	bad := value
	bad.Folio.URL = "file:///etc/passwd"
	if err := bad.validate(); err == nil {
		t.Fatal("non-http folio url accepted")
	}
	bad = value
	bad.Sources[0].Repos = []string{"owner/../escape"}
	if err := bad.validate(); err == nil {
		t.Fatal("path traversal repository accepted")
	}
	bad = value
	bad.Folio.HookID = "Bad ID"
	if err := bad.validate(); err == nil {
		t.Fatal("invalid hook id accepted")
	}
}

func TestHookSendSignsTheExactBody(t *testing.T) {
	var received atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		timestamp := r.Header.Get("X-Folio-Timestamp")
		mac := hmac.New(sha256.New, []byte(testSecret))
		mac.Write([]byte(timestamp + "." + string(body)))
		if r.Header.Get("X-Folio-Signature") != hex.EncodeToString(mac.Sum(nil)) {
			t.Errorf("signature mismatch for body %s", body)
		}
		if r.Header.Get("X-Folio-Event-Id") != "gh-00000000-1-1" {
			t.Errorf("event id=%q", r.Header.Get("X-Folio-Event-Id"))
		}
		received.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	client, err := newHookClient(config{Folio: folioConfig{
		URL: server.URL, HookID: "issues", SecretFile: writeTemp(t, "secret", testSecret, 0o600), TimeoutSeconds: 5,
	}})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	if err := client.send(context.Background(), "gh-00000000-1-1", map[string]any{"repo": "owner/repo", "issue": 1}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if received.Load() != 1 {
		t.Fatalf("received=%d", received.Load())
	}
}

func TestHookSendClassifiesRejections(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	client, err := newHookClient(config{Folio: folioConfig{
		URL: server.URL, HookID: "issues", SecretFile: writeTemp(t, "secret", testSecret, 0o600), TimeoutSeconds: 5,
	}})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	err = client.send(context.Background(), "e-1", map[string]any{})
	if err == nil || !isPermanent(err) {
		t.Fatalf("401 should be permanent: %v", err)
	}
}

func TestListIssuesHandlesNotModifiedAndRateLimit(t *testing.T) {
	var mode atomic.Value
	mode.Store("ok")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode.Load().(string) {
		case "not-modified":
			w.Header().Set("ETag", `"abc"`)
			w.WriteHeader(http.StatusNotModified)
		case "limited":
			w.Header().Set("X-RateLimit-Reset", fmt.Sprint(time.Now().Add(10*time.Minute).Unix()))
			w.WriteHeader(http.StatusForbidden)
		default:
			w.Header().Set("ETag", `"abc"`)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[{"number":12,"title":"Crash on save","html_url":"https://github.com/o/r/issues/12","state":"open","updated_at":"2026-10-24T04:00:00Z","labels":[{"name":"bug"}]}]`)
		}
	}))
	defer server.Close()
	client := newGitHubClient("ghp_" + strings.Repeat("a", 36))
	client.base = server.URL
	client.client = server.Client()

	issues, etag, notModified, _, err := client.listIssues(context.Background(), "owner/repo", "")
	if err != nil || len(issues) != 1 || etag != `"abc"` || notModified {
		t.Fatalf("issues=%v etag=%q notModified=%v err=%v", issues, etag, notModified, err)
	}
	if !strings.Contains(issues[0].HTMLURL, "/issues/12") || issues[0].Labels[0].Name != "bug" {
		t.Fatalf("issue=%+v", issues[0])
	}

	mode.Store("not-modified")
	if _, _, notModified, _, err := client.listIssues(context.Background(), "owner/repo", `"abc"`); err != nil || !notModified {
		t.Fatalf("304 not reported: err=%v", err)
	}

	mode.Store("limited")
	if _, _, _, hold, err := client.listIssues(context.Background(), "owner/repo", ""); err == nil || hold.IsZero() {
		t.Fatalf("rate limit not reported: hold=%v err=%v", hold, err)
	}
}

func TestSyncOnceBaselinesThenQueuesChanges(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_"+strings.Repeat("a", 36))
	var issues atomic.Value
	issues.Store(`[{"number":12,"title":"Crash on save","html_url":"https://github.com/o/r/issues/12","state":"open","updated_at":"2026-10-24T04:00:00Z","labels":[{"name":"bug"}]}]`)
	githubServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, issues.Load().(string))
	}))
	defer githubServer.Close()
	t.Setenv("GITHUB_API_URL", githubServer.URL)

	posted := make(chan map[string]any, 4)
	hookServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		posted <- payload
		w.WriteHeader(http.StatusAccepted)
	}))
	defer hookServer.Close()

	hook, err := newHookClient(config{Folio: folioConfig{
		URL: hookServer.URL, HookID: "issues", SecretFile: writeTemp(t, "secret", testSecret, 0o600), TimeoutSeconds: 5,
	}})
	if err != nil {
		t.Fatalf("hook: %v", err)
	}
	statePath := filepath.Join(t.TempDir(), "state.json")
	bridge := &bridge{
		config: config{
			Folio:     folioConfig{URL: hookServer.URL, HookID: "issues", SecretFile: "unused", TimeoutSeconds: 5},
			StateFile: statePath,
			Sources:   []sourceConfig{{Type: "github", Name: "issues", Repos: []string{"owner/repo"}}},
		},
		state: &bridgeState{Version: 1, Repos: map[string]*repoState{}},
		hook:  hook,
		log:   log.New(io.Discard, "", 0),
	}

	bridge.syncOnce(context.Background())
	select {
	case payload := <-posted:
		t.Fatalf("baseline posted an event: %v", payload)
	default:
	}

	issues.Store(`[{"number":12,"title":"Crash on save","html_url":"https://github.com/o/r/issues/12","state":"open","updated_at":"2026-10-24T05:00:00Z","labels":[{"name":"bug"}]}]`)
	bridge.syncOnce(context.Background())
	select {
	case payload := <-posted:
		if payload["repo"] != "owner/repo" || payload["issue"] != float64(12) || payload["event"] != "updated" {
			t.Fatalf("payload=%v", payload)
		}
		if !strings.HasPrefix(payload["title"].(string), "issue #12 ") {
			t.Fatalf("title=%v", payload["title"])
		}
	case <-time.After(time.Second):
		t.Fatal("change was not posted")
	}

	bridge.syncOnce(context.Background())
	select {
	case payload := <-posted:
		t.Fatalf("unchanged issue was posted again: %v", payload)
	default:
	}

	stored, err := loadState(statePath)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if !stored.Repos["owner/repo"].Baseline {
		t.Fatal("baseline was not recorded")
	}
}

func TestSyncOnceDryRunDoesNotPost(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_"+strings.Repeat("a", 36))
	var issues atomic.Value
	issues.Store("[]")
	githubServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, issues.Load().(string))
	}))
	defer githubServer.Close()
	t.Setenv("GITHUB_API_URL", githubServer.URL)
	hookServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("dry run posted an event")
	}))
	defer hookServer.Close()
	hook, err := newHookClient(config{Folio: folioConfig{
		URL: hookServer.URL, HookID: "issues", SecretFile: writeTemp(t, "secret", testSecret, 0o600), TimeoutSeconds: 5,
	}})
	if err != nil {
		t.Fatalf("hook: %v", err)
	}
	bridge := &bridge{
		config: config{
			StateFile: filepath.Join(t.TempDir(), "state.json"),
			Sources:   []sourceConfig{{Type: "github", Name: "issues", Repos: []string{"owner/repo"}}},
		},
		state:  &bridgeState{Version: 1, Repos: map[string]*repoState{}},
		hook:   hook,
		dryRun: true,
		log:    log.New(io.Discard, "", 0),
	}
	bridge.syncOnce(context.Background())
	issues.Store(`[{"number":1,"title":"first","html_url":"https://github.com/o/r/issues/1","state":"open","updated_at":"2026-10-24T04:00:00Z"}]`)
	bridge.syncOnce(context.Background())
	bridge.syncOnce(context.Background())
}
