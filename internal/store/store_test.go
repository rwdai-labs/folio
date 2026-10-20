package store

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestStateUsesLegacyPathsAndRedactsUnknownState(t *testing.T) {
	store := newTestStore(t)
	if err := store.saveState(map[string]any{
		"github":     map[string]any{"status": "authenticated", "token": "must-not-return"},
		"repository": map[string]any{"full_name": "owner/name", "branch": "main", "head": strings.Repeat("a", 40)},
		"secret":     "must-not-return",
	}); err != nil {
		t.Fatal(err)
	}
	state, err := store.State()
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(state)
	if strings.Contains(string(encoded), "must-not-return") {
		t.Fatal("State returned an unknown credential-bearing field")
	}
	if got := filepath.Base(store.authPendingPath); got != "github-device.json" {
		t.Fatalf("legacy device path changed: %s", got)
	}
	if got := filepath.Base(store.reviewsDir); got != "reviews" {
		t.Fatalf("legacy review path changed: %s", got)
	}
}

func TestTargetValidationRejectsEscapeSymlinkAndReservedPaths(t *testing.T) {
	store := newTestStore(t)
	for _, target := range []string{"../outside.md", "/tmp/outside.md", "tools/context.py", ".git/config", "config.toml"} {
		if _, err := store.validateTarget(target, true); err == nil {
			t.Errorf("target %q was accepted", target)
		}
	}
	outside := filepath.Join(filepath.Dir(store.repositoryDir), "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(store.repositoryDir, "core"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(store.repositoryDir, "core", "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.validateTarget("core/escape/file.md", true); err == nil {
		t.Fatal("symlink target escaped the checkout")
	}
}

func TestPreviewApplyIsOneUseAndPushesOnlyReviewedFile(t *testing.T) {
	store := newGitStore(t)
	writeReport(t, store, "review-1")
	preview, err := store.PreviewChange("review-1", 0, "core/profile.md", "new\n")
	if err != nil {
		t.Fatal(err)
	}
	reviewID, ok := preview["review_id"].(string)
	if !ok || !reviewIDRE.MatchString(reviewID) {
		t.Fatalf("invalid review id: %#v", preview["review_id"])
	}
	if !strings.Contains(preview["diff"].(string), "-old") || !strings.Contains(preview["diff"].(string), "+new") {
		t.Fatalf("preview did not contain the exact file change: %s", preview["diff"])
	}
	// Pending reviews written by the previous implementation do not carry a
	// remote_url field. The apply path must bind the verified current URL.
	var pending map[string]any
	pendingPath := filepath.Join(store.pendingDir, reviewID+".json")
	rawPending, err := os.ReadFile(pendingPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rawPending, &pending); err != nil {
		t.Fatal(err)
	}
	delete(pending, "remote_url")
	writeJSON(t, pendingPath, pending)
	result, err := store.ApplyChange(reviewID, "review profile")
	if err != nil {
		t.Fatal(err)
	}
	if result["status"] != "applied" {
		t.Fatalf("unexpected apply result: %#v", result)
	}
	if got, _ := os.ReadFile(filepath.Join(store.repositoryDir, "core", "profile.md")); string(got) != "new\n" {
		t.Fatalf("local target was not updated: %q", got)
	}
	if _, err := store.ApplyChange(reviewID, "replay"); err == nil {
		t.Fatal("review capability was replayable")
	}
	if _, err := os.Stat(filepath.Join(store.pendingDir, reviewID+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("consumed review remains: %v", err)
	}
	remoteHead := gitOutputForTest(t, filepath.Join(filepath.Dir(store.dataDir), "remote.git"), "rev-parse", "refs/heads/main")
	if result["commit"] != remoteHead {
		t.Fatalf("remote was not updated to the reviewed commit: result=%v remote=%s", result["commit"], remoteHead)
	}
}

func TestApplyRejectsRemoteMovedAfterPreview(t *testing.T) {
	store := newGitStore(t)
	writeReport(t, store, "review-remote")
	preview, err := store.PreviewChange("review-remote", 0, "core/profile.md", "new\n")
	if err != nil {
		t.Fatal(err)
	}
	reviewID := preview["review_id"].(string)
	remote := filepath.Join(filepath.Dir(store.dataDir), "remote.git")
	mover := filepath.Join(filepath.Dir(store.dataDir), "mover")
	runGitForTest(t, filepath.Dir(remote), "clone", remote, mover)
	if err := os.WriteFile(filepath.Join(mover, "core", "profile.md"), []byte("remote moved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitForTest(t, mover, "add", "--", "core/profile.md")
	runGitForTest(t, mover, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "move")
	runGitForTest(t, mover, "push", "origin", "HEAD:refs/heads/main")
	if _, err := store.ApplyChange(reviewID, "review profile"); err == nil {
		t.Fatal("apply succeeded after remote moved")
	}
	if _, err := os.Stat(filepath.Join(store.pendingDir, reviewID+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remote-moved review was not consumed: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(store.repositoryDir, "core", "profile.md")); string(got) != "old\n" {
		t.Fatalf("remote move changed local checkout: %q", got)
	}
}

func TestPreviewRejectsPushURLMismatch(t *testing.T) {
	store := newGitStore(t)
	writeReport(t, store, "review-push-url")
	wrong := filepath.Join(filepath.Dir(store.dataDir), "wrong.git")
	runGitForTest(t, store.repositoryDir, "remote", "set-url", "--push", "origin", wrong)
	if _, err := store.PreviewChange("review-push-url", 0, "core/profile.md", "new\n"); err == nil {
		t.Fatal("preview accepted a checkout with a mismatched push URL")
	}
}

func TestSensitiveReportsAndContentAreFiltered(t *testing.T) {
	store := newGitStore(t)
	if err := os.WriteFile(filepath.Join(store.repositoryDir, "core", "secret.md"), []byte("---\nsensitivity: sensitive\n---\nprivate\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(store.reviewsDir, "sensitive.json"), map[string]any{
		"status": "pending-review", "summary": "summary", "proposals": []any{
			map[string]any{"source": "core/secret.md", "target": "core/profile.md", "claim_type": "fact", "proposal": "private", "reason": "private"},
		},
	})
	listed, err := store.ListProposals()
	if err != nil {
		t.Fatal(err)
	}
	if listed["count"] != 1 {
		t.Fatal("invalid sensitive report count")
	}
	reports := listed["reports"].([]any)
	if len(reports) != 1 || len(reports[0].(map[string]any)["proposals"].([]any)) != 0 {
		t.Fatal("sensitive proposal was returned")
	}
	writeReport(t, store, "review-sensitive-content")
	if _, err := store.PreviewChange("review-sensitive-content", 0, "core/profile.md", "---\nsensitivity: sensitive\n---\nprivate\n"); err == nil {
		t.Fatal("sensitive edited content was reviewable")
	}
}

func TestEnvironmentNeverCarriesGitHubToken(t *testing.T) {
	store := newTestStore(t)
	t.Setenv("GH_TOKEN", "fixture-token")
	t.Setenv("GITHUB_TOKEN", "fixture-token")
	values := envMap(store.baseEnv())
	if _, ok := values["GH_TOKEN"]; ok {
		t.Fatal("GH_TOKEN entered the child environment")
	}
	if _, ok := values["GITHUB_TOKEN"]; ok {
		t.Fatal("GITHUB_TOKEN entered the child environment")
	}
}

func TestMaintenanceUsesOnlyInstalledFolioBinary(t *testing.T) {
	store := newTestStore(t)
	if err := os.WriteFile(filepath.Join(store.toolRoot, "context"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.toolRoot, "context.py"), []byte("print('legacy')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.installedFolioCommand(); err == nil {
		t.Fatal("legacy checkout scripts were accepted as the maintenance tool")
	}
	folio := filepath.Join(store.toolRoot, "folio")
	if err := os.WriteFile(folio, []byte("binary fixture\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	command, err := store.installedFolioCommand()
	if err != nil || len(command) != 1 || command[0] != folio {
		t.Fatalf("installed folio command=%v err=%v", command, err)
	}
}

func TestMaintenanceRejectsInvalidRunnerAndModel(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.RunMaintenance("agent", "", "other"); err == nil || err.Error() != "runner must be codex or dsh" {
		t.Fatalf("invalid runner err=%v", err)
	}
	if _, err := store.RunMaintenance("agent", "bad model", "dsh"); err == nil || err.Error() != "model name is invalid" {
		t.Fatalf("invalid model err=%v", err)
	}
	for _, runner := range []string{"", "codex", "dsh"} {
		if normalized, err := normalizeRunner(runner); err != nil || (runner == "" && normalized != "codex") || (runner != "" && normalized != runner) {
			t.Fatalf("normalizeRunner(%q)=%q err=%v", runner, normalized, err)
		}
	}
}

func TestStateExposesRunnerAvailabilityAndPrivateDSHDir(t *testing.T) {
	store := newTestStore(t)
	info, err := os.Stat(store.dshDir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("dsh dir: %v %v", info, err)
	}
	if filepath.Base(store.dshDir) != "dsh" {
		t.Fatalf("unexpected dsh dir: %s", store.dshDir)
	}
	for _, testCase := range []struct {
		name      string
		binaries  []string
		codexWant bool
		dshWant   bool
	}{
		{"none", nil, false, false},
		{"dsh only", []string{"dsh"}, false, true},
		{"codex only", []string{"codex"}, true, false},
		{"both", []string{"codex", "dsh"}, true, true},
	} {
		bin := t.TempDir()
		for _, name := range testCase.binaries {
			if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		t.Setenv("PATH", bin)
		state, err := store.State()
		if err != nil {
			t.Fatal(err)
		}
		runners, ok := state["runners"].(map[string]any)
		if !ok {
			t.Fatalf("%s: runners=%v", testCase.name, state["runners"])
		}
		codex, _ := runners["codex"].(map[string]any)
		dsh, _ := runners["dsh"].(map[string]any)
		if codex["available"] != testCase.codexWant || dsh["available"] != testCase.dshWant {
			t.Fatalf("%s: runners=%v", testCase.name, runners)
		}
		model, _ := state["model"].(map[string]any)
		if model["available"] != testCase.codexWant {
			t.Fatalf("%s: model availability diverged from codex: %v", testCase.name, model)
		}
	}
}

func TestMaintenancePassesRunnerAndDSHHomeToSubprocess(t *testing.T) {
	store := newGitStore(t)
	bin := t.TempDir()
	capture := filepath.Join(t.TempDir(), "folio-capture.txt")
	folio := filepath.Join(store.toolRoot, "folio")
	script := "#!/bin/sh\n" +
		"printf 'args:%s\\n' \"$*\" > \"" + capture + "\"\n" +
		"printf 'dsh_home:%s\\n' \"${DSH_HOME-}\" >> \"" + capture + "\"\n" +
		"printf 'codex_home:%s\\n' \"${CODEX_HOME-}\" >> \"" + capture + "\"\n"
	if err := os.WriteFile(folio, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "dsh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	result, err := store.RunMaintenance("agent", "", "dsh")
	if err != nil {
		t.Fatal(err)
	}
	if result["status"] != "completed" || result["runner"] != "dsh" || result["model_available"] != true {
		t.Fatalf("maintenance result=%v", result)
	}
	captured, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	got := string(captured)
	if !strings.Contains(got, "args:agent --device Docker --runtime-dir "+store.runtimeDir+" --runner dsh") {
		t.Fatalf("runner flag missing: %s", got)
	}
	if !strings.Contains(got, "dsh_home:"+store.dshDir) || !strings.Contains(got, "codex_home:"+store.codexDir) {
		t.Fatalf("runner homes missing: %s", got)
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	root := t.TempDir()
	data := filepath.Join(root, "data")
	tools := filepath.Join(root, "tools")
	if err := os.MkdirAll(tools, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := New(data, tools)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func newGitStore(t *testing.T) *Store {
	t.Helper()
	store := newTestStore(t)
	root := filepath.Dir(store.dataDir)
	remote := filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")
	runGitForTest(t, root, "init", "--bare", "--initial-branch=main", remote)
	if err := os.MkdirAll(filepath.Join(seed, "core"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(seed, "inbox"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "core", "profile.md"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "inbox", "candidate.md"), []byte("candidate\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitForTest(t, seed, "init", "--initial-branch=main")
	runGitForTest(t, seed, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "add", ".")
	runGitForTest(t, seed, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "initial")
	runGitForTest(t, seed, "remote", "add", "origin", remote)
	runGitForTest(t, seed, "-c", "protocol.file.allow=always", "push", "origin", "HEAD:refs/heads/main")
	runGitForTest(t, root, "-c", "protocol.file.allow=always", "clone", remote, store.repositoryDir)
	store.allowFileRemoteForTests = true
	return store
}

func writeReport(t *testing.T, store *Store, reportID string) {
	t.Helper()
	writeJSON(t, filepath.Join(store.reviewsDir, reportID+".json"), map[string]any{
		"status": "pending-review", "created": "2026-10-14T00:00:00+00:00", "device": "fixture", "summary": "review",
		"proposals": []any{map[string]any{
			"source": "inbox/candidate.md", "target": "core/profile.md", "claim_type": "inference", "proposal": "update", "reason": "fixture",
		}},
	})
}

func writeJSON(t *testing.T, file string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func runGitForTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v", args, err)
	} else if len(output) > 16*1024 {
		t.Fatalf("git %v produced unexpected output", args)
	}
}

func gitOutputForTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v failed: %v", args, err)
	}
	return strings.TrimSpace(string(output))
}
