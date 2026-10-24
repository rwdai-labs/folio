package main

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestAppScriptReferencesExistingElements guards the failure that shipped in the
// plugin rewrite: app.js still wired "repository-form" after the manual form was
// renamed to "repo-manual". The resulting TypeError ran during boot, so nothing
// after that line was wired and no plugin was ever loaded — the owner saw an old
// manual form and no GitHub sign-in. Syntax checks pass on that file; only a
// cross-check against index.html catches it.
func TestAppScriptReferencesExistingElements(t *testing.T) {
	root := repositoryRoot(t)
	markup := readFile(t, filepath.Join(root, "web", "index.html"))
	script := readFile(t, filepath.Join(root, "web", "app.js"))

	present := map[string]bool{}
	for _, match := range regexp.MustCompile(`\bid="([^"]+)"`).FindAllStringSubmatch(markup, -1) {
		present[match[1]] = true
	}
	if len(present) == 0 {
		t.Fatal("web/index.html declares no element ids")
	}

	patterns := []*regexp.Regexp{
		regexp.MustCompile(`\$\("([a-z0-9-]+)"\)`),
		regexp.MustCompile(`\bon\("([a-z0-9-]+)",`),
	}
	referenced := map[string]bool{}
	for _, pattern := range patterns {
		for _, match := range pattern.FindAllStringSubmatch(script, -1) {
			referenced[match[1]] = true
		}
	}
	if len(referenced) == 0 {
		t.Fatal("web/app.js references no element ids")
	}

	missing := make([]string, 0, 4)
	for id := range referenced {
		if !present[id] {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("web/app.js references elements that web/index.html does not define: %s", strings.Join(missing, ", "))
	}

	// Direct wiring bypasses the missing-element report and can abort boot.
	if regexp.MustCompile(`\$\("[a-z0-9-]+"\)\.addEventListener\(`).MatchString(script) {
		t.Fatal("web/app.js wires an element with $(\"id\").addEventListener; use on(\"id\", ...)")
	}
}

// TestAppScriptLoadsPluginsAfterLogin keeps the discovery call on both session
// paths. Boot alone is not enough: an owner who logs in on the page would get
// the shell without any plugin card until a reload.
func TestAppScriptLoadsPluginsAfterLogin(t *testing.T) {
	root := repositoryRoot(t)
	script := readFile(t, filepath.Join(root, "web", "app.js"))
	if count := strings.Count(script, "await enterSession();"); count < 2 {
		t.Fatalf("web/app.js calls enterSession %d times; boot and login must both load plugins", count)
	}
	if !strings.Contains(script, "function enterSession()") || !strings.Contains(script, "await loadPlugins();") {
		t.Fatal("web/app.js lost enterSession/loadPlugins")
	}
	if !strings.Contains(script, `new CustomEvent("folio:session", { detail: { authenticated: false } })`) {
		t.Fatal("web/app.js never tells plugins that the session ended")
	}
}
