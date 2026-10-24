package main

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// TestDockerBuildContextCoversTheSource guards the failure that local `go test`
// cannot see: the container build has its own, smaller view of the tree. When
// the Dockerfile kept a hand-written list of directories, adding a package
// (internal/plugin, internal/storage, internal/notify, plugins/github) broke
// every CI run while the local suite stayed green.
func TestDockerBuildContextCoversTheSource(t *testing.T) {
	root := repositoryRoot(t)
	dockerfile := readFile(t, filepath.Join(root, "deploy", "Dockerfile"))
	ignore := readFile(t, filepath.Join(root, ".dockerignore"))

	sourceDirs := make([]string, 0, 4)
	for _, name := range []string{"cmd", "internal", "plugins"} {
		if hasGoSource(t, filepath.Join(root, name)) {
			sourceDirs = append(sourceDirs, name)
		}
	}
	if len(sourceDirs) == 0 {
		t.Fatal("no Go source directories found")
	}
	for _, dir := range sourceDirs {
		if !allowlisted(ignore, dir) {
			t.Fatalf(".dockerignore does not allow %s/, so the container build cannot see it", dir)
		}
	}

	if !strings.Contains(dockerfile, "COPY . .") {
		for _, dir := range sourceDirs {
			pattern := regexp.MustCompile(`(?m)^COPY\s+` + regexp.QuoteMeta(dir) + `/\s`)
			if !pattern.MatchString(dockerfile) {
				t.Fatalf("deploy/Dockerfile neither copies the whole context nor %s/", dir)
			}
		}
	}
	// The runtime stage reads these directly from the build context.
	for _, want := range []string{"web/", "deploy/entrypoint"} {
		if !strings.Contains(dockerfile, " "+want+" ") {
			t.Fatalf("deploy/Dockerfile does not copy %s in the runtime stage", want)
		}
	}
}

// allowlisted reports whether the last matching .dockerignore rule un-ignores
// dir. The file starts with "**" and then re-includes specific paths.
func allowlisted(ignore, dir string) bool {
	allowed := false
	for _, line := range strings.Split(ignore, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		negated := strings.HasPrefix(line, "!")
		pattern := strings.TrimPrefix(line, "!")
		if pattern == "**" {
			allowed = negated
			continue
		}
		pattern = strings.TrimSuffix(pattern, "**")
		pattern = strings.TrimSuffix(pattern, "/")
		if pattern == dir || pattern == dir+"/" {
			allowed = negated
		}
	}
	return allowed
}

func hasGoSource(t *testing.T, dir string) bool {
	t.Helper()
	found := false
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return found
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the repository root")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}
