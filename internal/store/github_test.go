package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareGitCredentialWritesPrivateStoreFile(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GIT_AUTH_TOKEN", strings.Repeat("a", 40))
	tools := filepath.Join(root, "tools")
	if err := os.MkdirAll(tools, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := New(filepath.Join(root, "data"), tools)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if got := store.gitCredentialMode(); got != "token" {
		t.Fatalf("credential mode=%q", got)
	}
	raw, readErr := os.ReadFile(filepath.Join(store.dataDir, "secrets", "git-credentials"))
	if readErr != nil {
		t.Fatalf("credential file: %v", readErr)
	}
	if !strings.Contains(string(raw), "x-access-token:"+strings.Repeat("a", 40)+"@github.com") {
		t.Fatalf("credential content=%q", raw)
	}
	info, statErr := os.Stat(filepath.Join(store.dataDir, "secrets", "git-credentials"))
	if statErr != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode=%v err=%v", info.Mode().Perm(), statErr)
	}
	args := strings.Join(store.gitCredentialArgs(), " ")
	if !strings.Contains(args, "credential.helper=store --file=") {
		t.Fatalf("credential args=%q", args)
	}
	if strings.Contains(args, strings.Repeat("a", 40)) {
		t.Fatal("the token must not appear in git arguments")
	}
}

func TestPrepareGitCredentialRejectsMalformedToken(t *testing.T) {
	t.Setenv("GIT_AUTH_TOKEN", "short")
	if _, err := New(t.TempDir(), t.TempDir()); err == nil {
		t.Fatal("malformed token was accepted")
	}
}
