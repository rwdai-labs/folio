package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"folio/internal/plugin"
	"folio/internal/storage"
)

func TestPrepareTokenWritesPrivateCredential(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GIT_AUTH_TOKEN", strings.Repeat("a", 40))
	item, err := New(root)
	if err != nil {
		t.Fatalf("new plugin: %v", err)
	}
	raw, readErr := os.ReadFile(filepath.Join(root, "secrets", "git-credentials"))
	if readErr != nil {
		t.Fatalf("credential file: %v", readErr)
	}
	if !strings.Contains(string(raw), "x-access-token:"+strings.Repeat("a", 40)+"@github.com") {
		t.Fatalf("credential content=%q", raw)
	}
	info, statErr := os.Stat(filepath.Join(root, "secrets", "git-credentials"))
	if statErr != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode=%v err=%v", info.Mode().Perm(), statErr)
	}
	args := strings.Join(item.credentialArgs(testExecutor{}), " ")
	if !strings.Contains(args, "credential.helper=store --file=") {
		t.Fatalf("credential args=%q", args)
	}
	if strings.Contains(args, strings.Repeat("a", 40)) {
		t.Fatal("the token must not appear in git arguments")
	}
}

func TestPrepareTokenRejectsMalformedValue(t *testing.T) {
	t.Setenv("GIT_AUTH_TOKEN", "short")
	if _, err := New(t.TempDir()); err == nil {
		t.Fatal("malformed token was accepted")
	}
}

func TestResolveRejectsInvalidNames(t *testing.T) {
	t.Setenv("GIT_AUTH_TOKEN", "")
	item, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"", "owner", "owner/", "/repo", "owner/repo/extra", "owner/../repo", "owner/rep o"} {
		if _, err := item.Resolve(input, false); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
	resolved, err := item.Resolve("owner/repo", false)
	if err != nil || resolved.Name != "owner/repo" || resolved.Remote != "https://github.com/owner/repo.git" {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}
	if err := item.Validate(resolved.Remote, false); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if err := item.Validate("https://gitlab.com/owner/repo.git", false); err == nil {
		t.Fatal("foreign remote accepted")
	}
}

func TestSyncClonesThenFastForwardsAndPublishes(t *testing.T) {
	t.Setenv("GIT_AUTH_TOKEN", "")
	plugin, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")
	gitForTest(t, root, "init", "--bare", "--initial-branch=main", remote)
	gitForTest(t, root, "init", "--initial-branch=main", seed)
	writeFile(t, filepath.Join(seed, "core", "profile.md"), "old\n")
	gitForTest(t, seed, "add", ".")
	gitForTest(t, seed, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-m", "initial")
	gitForTest(t, seed, "remote", "add", "origin", remote)
	gitForTest(t, seed, "push", "origin", "HEAD:refs/heads/main")

	checkout := filepath.Join(root, "checkout")
	exec := testExecutor{}
	synced, err := plugin.Sync(context.Background(), exec, storage.Options{Dir: checkout, Remote: remote, TestMode: true})
	if err != nil || synced.Status != "cloned" || synced.Head == "" {
		t.Fatalf("clone: %+v err=%v", synced, err)
	}

	writeFile(t, filepath.Join(seed, "core", "profile.md"), "new\n")
	gitForTest(t, seed, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-am", "second")
	gitForTest(t, seed, "push", "origin", "HEAD:refs/heads/main")
	synced, err = plugin.Sync(context.Background(), exec, storage.Options{Dir: checkout, Remote: remote, Branch: "main", TestMode: true})
	if err != nil || synced.Status != "synced" {
		t.Fatalf("fast-forward: %+v err=%v", synced, err)
	}
	if raw, _ := os.ReadFile(filepath.Join(checkout, "core", "profile.md")); string(raw) != "new\n" {
		t.Fatalf("checkout content=%q", raw)
	}
	if again, err := plugin.Sync(context.Background(), exec, storage.Options{Dir: checkout, Remote: remote, Branch: "main", TestMode: true}); err != nil || again.Status != "up_to_date" {
		t.Fatalf("repeat sync: %+v err=%v", again, err)
	}

	writeFile(t, filepath.Join(checkout, "core", "notes.md"), "local\n")
	gitForTest(t, checkout, "add", ".")
	gitForTest(t, checkout, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-m", "local")
	pushed, err := plugin.Publish(context.Background(), exec, storage.Options{Dir: checkout, Remote: remote, Branch: "main", TestMode: true})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if head := gitForTest(t, checkout, "rev-parse", "HEAD"); head != pushed {
		t.Fatalf("pushed=%s local=%s", pushed, head)
	}
	if head, err := plugin.Head(context.Background(), exec, storage.Options{Dir: checkout, Remote: remote, Branch: "main", TestMode: true}); err != nil || head != pushed {
		t.Fatalf("remote head=%s err=%v", head, err)
	}
}

func TestDeviceFlowStoresTokenAndListsRepositories(t *testing.T) {
	t.Setenv("GIT_AUTH_TOKEN", "")
	deviceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/login/device/code":
			_, _ = io.WriteString(w, `{"device_code":"dev-code-123","user_code":"ABCD-1234","verification_uri":"https://github.com/login/device","expires_in":900,"interval":1}`)
		case "/login/oauth/access_token":
			_, _ = io.WriteString(w, `{"access_token":"gho_`+strings.Repeat("b", 36)+`","token_type":"bearer","scope":"repo"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer deviceServer.Close()
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[{"full_name":"owner/context","private":true,"archived":false,"default_branch":"main"}]`)
	}))
	defer apiServer.Close()

	original := []string{deviceURL, deviceTokenURL, apiURL}
	t.Cleanup(func() { deviceURL, deviceTokenURL, apiURL = original[0], original[1], original[2] })
	deviceURL = deviceServer.URL + "/login/device/code"
	deviceTokenURL = deviceServer.URL + "/login/oauth/access_token"
	apiURL = apiServer.URL

	item, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	exec := &fakeGH{}
	started, err := item.startAuth(context.Background(), plugin.Request{Exec: exec})
	if err != nil || started["status"] != "pending" || started["user_code"] != "ABCD-1234" {
		t.Fatalf("start=%v err=%v", started, err)
	}
	time.Sleep(1100 * time.Millisecond)
	authorized, err := item.pollAuth(context.Background(), plugin.Request{Exec: exec})
	if err != nil || authorized["status"] != "authenticated" || authorized["mode"] != "gh" {
		t.Fatalf("poll=%v err=%v", authorized, err)
	}
	if !exec.signedIn {
		t.Fatal("token was not stored through gh")
	}
	list, err := item.repositories(context.Background(), plugin.Request{Exec: exec})
	if err != nil || list["count"] != 1 {
		t.Fatalf("repositories=%v err=%v", list, err)
	}
	items, _ := list["repositories"].([]map[string]any)
	if len(items) != 1 || items[0]["full_name"] != "owner/context" {
		t.Fatalf("items=%v", list["repositories"])
	}
}

/* ------------------------------------------------------------------ helpers */

type testExecutor struct{}

func (testExecutor) Git(ctx context.Context, dir string, credential []string, timeout time.Duration, args []string) (storage.Output, error) {
	command := []string{"git", "-c", "protocol.file.allow=always", "-c", "credential.helper="}
	for _, item := range credential {
		command = append(command, "-c", item)
	}
	command = append(command, args...)
	return runTest(ctx, command, dir, "")
}

func (testExecutor) Exec(ctx context.Context, argv []string, dir string, stdin string, timeout time.Duration) (storage.Output, error) {
	return runTest(ctx, argv, dir, stdin)
}

func (testExecutor) LookPath(name string) (string, error) { return exec.LookPath(name) }

func runTest(ctx context.Context, command []string, dir, stdin string) (storage.Output, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0", "GIT_ALLOW_PROTOCOL=https:file")
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errOut strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()
	result := storage.Output{Stdout: out.String(), Stderr: errOut.String()}
	if err == nil {
		return result, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		result.Code = exitErr.ExitCode()
		return result, nil
	}
	return result, err
}

// fakeGH stands in for the gh binary: status fails until login stores a token.
type fakeGH struct {
	mu       sync.Mutex
	signedIn bool
	token    string
}

func (f *fakeGH) LookPath(name string) (string, error) { return "/usr/bin/" + name, nil }

func (f *fakeGH) Exec(ctx context.Context, argv []string, dir string, stdin string, timeout time.Duration) (storage.Output, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(argv) < 3 {
		return storage.Output{Code: 64}, nil
	}
	switch {
	case argv[1] == "auth" && argv[2] == "status":
		if f.signedIn {
			return storage.Output{}, nil
		}
		return storage.Output{Code: 1}, nil
	case argv[1] == "auth" && argv[2] == "login":
		f.token = strings.TrimSpace(stdin)
		f.signedIn = true
		return storage.Output{}, nil
	case argv[1] == "auth" && argv[2] == "token":
		if !f.signedIn {
			return storage.Output{Code: 1}, nil
		}
		return storage.Output{Stdout: f.token + "\n"}, nil
	}
	return storage.Output{Code: 64}, nil
}

func (f *fakeGH) Git(ctx context.Context, dir string, credential []string, timeout time.Duration, args []string) (storage.Output, error) {
	return testExecutor{}.Git(ctx, dir, credential, timeout, args)
}

func gitForTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0", "GIT_ALLOW_PROTOCOL=https:file")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v (%s)", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

var _ = json.Marshal
