package store

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestFetchGitHubRepositoriesFiltersAndPaginates(t *testing.T) {
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("http_proxy", "")
	t.Setenv("https_proxy", "")
	store := newTestStore(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token-1234567890" {
			t.Errorf("unexpected authorization header: %q", got)
		}
		if r.URL.Path != "/user/repos" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("per_page") != "100" {
			t.Errorf("missing per_page: %s", r.URL.RawQuery)
		}
		switch r.URL.Query().Get("page") {
		case "1":
			items := make([]map[string]any, 0, 100)
			for index := 0; index < 99; index++ {
				items = append(items, map[string]any{
					"full_name":      fmt.Sprintf("owner/repo-%03d", index),
					"private":        index%2 == 0,
					"archived":       index == 7,
					"default_branch": "main",
					"description":    "must not be returned",
				})
			}
			items = append(items, map[string]any{"full_name": "../escape"})
			raw, err := json.Marshal(items)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write(raw)
		case "2":
			raw, err := json.Marshal([]map[string]any{
				{"full_name": "owner/repo-099", "private": true},
				{"full_name": "owner/repo-100", "private": false},
				{"full_name": "owner/repo-100", "private": false},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write(raw)
		default:
			t.Errorf("unexpected page: %s", r.URL.RawQuery)
		}
	}))
	defer server.Close()

	store.githubAPIBase = server.URL
	repositories, truncated, err := store.fetchGitHubRepositories("test-token-1234567890")
	if err != nil {
		t.Fatal(err)
	}
	if truncated {
		t.Fatal("a short second page must not be marked truncated")
	}
	if len(repositories) != 101 {
		t.Fatalf("unexpected repository count: %d", len(repositories))
	}
	if repositories[0]["full_name"] != "owner/repo-000" || repositories[0]["private"] != true {
		t.Fatalf("unexpected first repository: %#v", repositories[0])
	}
	if repositories[7]["archived"] != true {
		t.Fatalf("archived flag was not preserved: %#v", repositories[7])
	}
	if _, ok := repositories[0]["description"]; ok {
		t.Fatal("repository description was returned")
	}
	raw, _ := json.Marshal(repositories)
	if strings.Contains(string(raw), "escape") {
		t.Fatalf("invalid repository name was returned: %s", raw)
	}
}

func TestFetchGitHubRepositoriesCapsAndReportsTruncation(t *testing.T) {
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("http_proxy", "")
	t.Setenv("https_proxy", "")
	store := newTestStore(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		items := make([]map[string]any, 0, 100)
		for index := 0; index < 100; index++ {
			items = append(items, map[string]any{"full_name": fmt.Sprintf("owner/page-%s-%03d", page, index)})
		}
		raw, _ := json.Marshal(items)
		_, _ = w.Write(raw)
	}))
	defer server.Close()

	store.githubAPIBase = server.URL
	repositories, truncated, err := store.fetchGitHubRepositories("test-token-1234567890")
	if err != nil {
		t.Fatal(err)
	}
	if len(repositories) != maxGitHubRepositories {
		t.Fatalf("unexpected repository count: %d", len(repositories))
	}
	if !truncated {
		t.Fatal("a full second page must report truncation")
	}
}

func TestFetchGitHubRepositoriesRejectsBadResponse(t *testing.T) {
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("http_proxy", "")
	t.Setenv("https_proxy", "")
	store := newTestStore(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusForbidden)
	}))
	defer server.Close()

	store.githubAPIBase = server.URL
	if _, _, err := store.fetchGitHubRepositories("test-token-1234567890"); err == nil {
		t.Fatal("non-200 response was accepted")
	}
}

func TestGitHubRepositoriesUsesStoredToken(t *testing.T) {
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("http_proxy", "")
	t.Setenv("https_proxy", "")
	fakeDir := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = auth ] && [ \"$2\" = token ]; then\n  printf '%s\\n' 'test-token-1234567890'\n  exit 0\nfi\nexit 1\n"
	if err := os.WriteFile(fakeDir+"/gh", []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token-1234567890" {
			t.Errorf("unexpected authorization header: %q", got)
		}
		raw, _ := json.Marshal([]map[string]any{{"full_name": "owner/context", "private": true}})
		_, _ = w.Write(raw)
	}))
	defer server.Close()

	store := newTestStore(t)
	store.githubAPIBase = server.URL
	result, err := store.GitHubRepositories()
	if err != nil {
		t.Fatal(err)
	}
	if result["count"] != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "test-token") {
		t.Fatalf("repository result leaked the token: %s", raw)
	}
}

func TestStateAdoptsExistingGitHubLogin(t *testing.T) {
	fakeDir := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = auth ] && [ \"$2\" = status ]; then\n  exit 0\nfi\nexit 1\n"
	if err := os.WriteFile(fakeDir+"/gh", []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))
	store := newTestStore(t)
	if err := store.setGitHubStatus("signed_out"); err != nil {
		t.Fatal(err)
	}
	state, err := store.State()
	if err != nil {
		t.Fatal(err)
	}
	githubState, _ := state["github"].(map[string]any)
	if githubState["authenticated"] != true {
		t.Fatalf("existing gh login was not adopted: %#v", githubState)
	}
}
