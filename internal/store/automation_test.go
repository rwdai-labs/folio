package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contextrepo "folio/internal/context"
)

func installAutoFixture(t *testing.T, store *Store, report string) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	script := "#!/bin/sh\n" +
		"while [ \"$#\" -gt 0 ]; do\n" +
		"  case \"$1\" in\n" +
		"    --auto-report) shift; report=\"$1\" ;;\n" +
		"  esac\n" +
		"  shift\n" +
		"done\n" +
		"cat > \"$report\" <<'JSON'\n" + report + "\nJSON\n"
	if err := os.WriteFile(filepath.Join(store.toolRoot, "folio"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestRunAutomationPushesValidatedEditsAndRecordsRun(t *testing.T) {
	store := newGitStore(t)
	installAutoFixture(t, store, `{
  "version": 1,
  "status": "pending-apply",
  "summary": "organize the profile",
  "device": "fixture",
  "created": "2026-10-20T00:00:00Z",
  "skipped": 0,
  "edits": [
    {"source": "core/profile.md", "target": "core/profile.md", "claim_type": "fact", "reason": "group the existing text", "content": "## Summary\nold\n"}
  ]
}`)
	result, err := store.RunAutomation("manual", "codex", "")
	if err != nil {
		t.Fatal(err)
	}
	if result["status"] != "pushed" || result["stage"] != "done" {
		t.Fatalf("unexpected automation result: %v", result)
	}
	runID, _ := result["run_id"].(string)
	if !autoRunIDRE.MatchString(runID) {
		t.Fatalf("invalid run id: %q", runID)
	}
	edits, _ := result["edits"].([]any)
	if len(edits) != 1 {
		t.Fatalf("edits=%v", result["edits"])
	}
	if got, _ := os.ReadFile(filepath.Join(store.repositoryDir, "core", "profile.md")); string(got) != "## Summary\nold\n" {
		t.Fatalf("profile was not updated: %q", got)
	}
	remoteHead := gitOutputForTest(t, filepath.Join(filepath.Dir(store.dataDir), "remote.git"), "rev-parse", "refs/heads/main")
	if result["commit"] != remoteHead {
		t.Fatalf("remote head=%s result=%v", remoteHead, result["commit"])
	}
	if got, _ := os.ReadFile(filepath.Join(store.runsDir, runID+".json")); len(got) == 0 {
		t.Fatal("automation run was not recorded")
	}
	listed, err := store.ListAutomation()
	if err != nil {
		t.Fatal(err)
	}
	if listed["count"] != 1 {
		t.Fatalf("automation list=%v", listed)
	}
	detail, err := store.AutomationRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if detail["status"] != "pushed" || detail["summary"] != "organize the profile" {
		t.Fatalf("automation detail=%v", detail)
	}
}

func TestRunAutomationRejectsCredentialMaterialAndRollsBack(t *testing.T) {
	store := newGitStore(t)
	installAutoFixture(t, store, `{
  "version": 1,
  "status": "pending-apply",
  "summary": "unsafe",
  "edits": [
    {"source": "inbox/candidate.md", "target": "core/profile.md", "claim_type": "fact", "reason": "unsafe", "content": "DEEPSEEK_API_KEY=not-a-real-key\n"}
  ]
}`)
	result, err := store.RunAutomation("manual", "codex", "")
	if err != nil {
		t.Fatal(err)
	}
	if result["status"] != "failed" || result["reason"] != "content_rejected" {
		t.Fatalf("unsafe automation result: %v", result)
	}
	if got, _ := os.ReadFile(filepath.Join(store.repositoryDir, "core", "profile.md")); string(got) != "old\n" {
		t.Fatalf("unsafe content changed the checkout: %q", got)
	}
	if got := gitOutputForTest(t, filepath.Join(filepath.Dir(store.dataDir), "remote.git"), "show", "main:core/profile.md"); got != "old" {
		t.Fatalf("unsafe content was pushed: %q", got)
	}
	// The next run still starts from a clean checkout.
	result, err = store.RunAutomation("manual", "codex", "")
	if err != nil {
		t.Fatal(err)
	}
	if result["status"] != "failed" {
		t.Fatalf("second unsafe run should also fail: %v", result)
	}
}

func TestRunAutomationSkipsWithoutRepository(t *testing.T) {
	store := newTestStore(t)
	result, err := store.RunAutomation("scheduler", "codex", "")
	if err != nil {
		t.Fatal(err)
	}
	if result["status"] != "skipped" || result["reason"] != "repository_not_connected" {
		t.Fatalf("unexpected no-repository result: %v", result)
	}
	listed, err := store.ListAutomation()
	if err != nil || listed["count"] != 1 {
		t.Fatalf("no-repository run was not recorded: %v %v", listed, err)
	}
}

func TestRunAutomationPullsBeforeApplying(t *testing.T) {
	store := newGitStore(t)
	installAutoFixture(t, store, `{
  "version": 1,
  "status": "pending-apply",
  "summary": "update after pull",
  "edits": [
    {"source": "core/profile.md", "target": "core/profile.md", "claim_type": "fact", "reason": "latest remote content", "content": "## Summary\nold\nremote update\n"}
  ]
}`)
	remote := filepath.Join(filepath.Dir(store.dataDir), "remote.git")
	mover := filepath.Join(filepath.Dir(store.dataDir), "mover")
	runGitForTest(t, filepath.Dir(remote), "clone", remote, mover)
	if err := os.WriteFile(filepath.Join(mover, "core", "profile.md"), []byte("old\nremote update\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitForTest(t, mover, "add", "--", "core/profile.md")
	runGitForTest(t, mover, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "remote update")
	runGitForTest(t, mover, "push", "origin", "HEAD:refs/heads/main")
	result, err := store.RunAutomation("scheduler", "codex", "")
	if err != nil {
		t.Fatal(err)
	}
	if result["status"] != "pushed" {
		t.Fatalf("automation did not pull and push: %v", result)
	}
	pull, _ := result["pull"].(map[string]any)
	if pull["status"] != "synced" {
		t.Fatalf("automation pull=%v", result["pull"])
	}
}

func TestRunAutomationSkipsNoopEdits(t *testing.T) {
	store := newGitStore(t)
	if err := os.WriteFile(filepath.Join(store.repositoryDir, "core", "other.md"), []byte("---\ntype: note\n---\nfresh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitForTest(t, store.repositoryDir, "add", "--", "core/other.md")
	runGitForTest(t, store.repositoryDir, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "another document")
	runGitForTest(t, store.repositoryDir, "push", "origin", "HEAD:refs/heads/main")
	installAutoFixture(t, store, `{
  "version": 1,
  "status": "pending-apply",
  "summary": "nothing to change",
  "edits": [
    {"source": "core/profile.md", "target": "core/profile.md", "claim_type": "fact", "reason": "already current", "content": "old\n"},
    {"source": "core/other.md", "target": "core/other.md", "claim_type": "fact", "reason": "group the note", "content": "---\ntype: note\n---\n## Details\nfresh\n"}
  ]
}`)
	result, err := store.RunAutomation("manual", "codex", "")
	if err != nil {
		t.Fatal(err)
	}
	if result["status"] != "pushed" {
		t.Fatalf("no-op mixed with a real edit failed: %v", result)
	}
	if got, _ := os.ReadFile(filepath.Join(store.repositoryDir, "core", "profile.md")); string(got) != "old\n" {
		t.Fatalf("no-op edit rewrote a file: %q", got)
	}
}

func TestRunAutomationReturnsNoChangesWhenAllEditsAreNoops(t *testing.T) {
	store := newGitStore(t)
	head := gitOutputForTest(t, store.repositoryDir, "rev-parse", "HEAD")
	installAutoFixture(t, store, `{
  "version": 1,
  "status": "pending-apply",
  "summary": "nothing to change",
  "edits": [
    {"source": "core/profile.md", "target": "core/profile.md", "claim_type": "fact", "reason": "already current", "content": "old\n"}
  ]
}`)
	result, err := store.RunAutomation("manual", "codex", "")
	if err != nil {
		t.Fatal(err)
	}
	if result["status"] != "no_changes" {
		t.Fatalf("no-op run should not push: %v", result)
	}
	if got := gitOutputForTest(t, store.repositoryDir, "rev-parse", "HEAD"); got != head {
		t.Fatalf("no-op run created a commit: %s != %s", got, head)
	}
}

func TestRunAutomationRejectsFrontmatterRemoval(t *testing.T) {
	store := newGitStore(t)
	profile := filepath.Join(store.repositoryDir, "core", "profile.md")
	if err := os.WriteFile(profile, []byte("---\ntype: profile\n---\nold\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitForTest(t, store.repositoryDir, "add", "--", "core/profile.md")
	runGitForTest(t, store.repositoryDir, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "frontmatter fixture")
	runGitForTest(t, store.repositoryDir, "push", "origin", "HEAD:refs/heads/main")
	installAutoFixture(t, store, `{
  "version": 1,
  "status": "pending-apply",
  "summary": "drop metadata",
  "edits": [
    {"source": "inbox/candidate.md", "target": "core/profile.md", "claim_type": "fact", "reason": "lost metadata", "content": "new\n"}
  ]
}`)
	result, err := store.RunAutomation("manual", "codex", "")
	if err != nil {
		t.Fatal(err)
	}
	if result["status"] != "failed" || result["reason"] != "content_rejected" {
		t.Fatalf("frontmatter removal was accepted: %v", result)
	}
	if got, _ := os.ReadFile(profile); string(got) != "---\ntype: profile\n---\nold\n" {
		t.Fatalf("frontmatter removal changed the checkout: %q", got)
	}
}

func TestHasYAMLFrontmatter(t *testing.T) {
	for _, testCase := range []struct {
		text string
		want bool
	}{
		{"---\ntype: profile\n---\nbody\n", true},
		{"\ufeff---\r\ntype: profile\r\n---\r\nbody\r\n", true},
		{"---\ntype: profile\n---", false},
		{"---\n---\nbody\n", false},
		{"body only\n", false},
		{"", false},
	} {
		if got := hasYAMLFrontmatter(testCase.text); got != testCase.want {
			t.Fatalf("hasYAMLFrontmatter(%q)=%v want %v", testCase.text, got, testCase.want)
		}
	}
}

func TestAutomationReadsDoNotWaitForRepositoryLock(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.RunAutomation("manual", "codex", ""); err != nil {
		t.Fatal(err)
	}
	listing, err := store.ListAutomation()
	if err != nil {
		t.Fatal(err)
	}
	latest, ok := listing["latest"].(map[string]any)
	if !ok {
		t.Fatalf("latest=%v", listing["latest"])
	}
	runID, _ := latest["run_id"].(string)

	store.threadMu.Lock()
	defer store.threadMu.Unlock()
	done := make(chan error, 1)
	go func() {
		if _, err := store.ListAutomation(); err != nil {
			done <- err
			return
		}
		if _, err := store.AutomationRun(runID); err != nil {
			done <- err
			return
		}
		done <- nil
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("automation reads blocked on the repository lock")
	}
}

func TestRunAutomationPublishesRunningAudit(t *testing.T) {
	store := newGitStore(t)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	script := "#!/bin/sh\n" +
		"while [ \"$#\" -gt 0 ]; do\n" +
		"  case \"$1\" in\n" +
		"    --auto-report) shift; report=\"$1\" ;;\n" +
		"  esac\n" +
		"  shift\n" +
		"done\n" +
		"sleep 1\n" +
		"cat > \"$report\" <<'JSON'\n" +
		`{"version":1,"status":"pending-apply","summary":"running audit","edits":[{"source":"core/profile.md","target":"core/profile.md","claim_type":"fact","reason":"test","content":"## Summary\nold\n"}]}` + "\n" +
		"JSON\n"
	if err := os.WriteFile(filepath.Join(store.toolRoot, "folio"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		result, err := store.RunAutomation("manual", "codex", "")
		if err == nil && result["status"] != "pushed" {
			err = os.ErrInvalid
		}
		done <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	seenRunning := false
	for time.Now().Before(deadline) {
		listing, err := store.ListAutomation()
		if err != nil {
			t.Fatal(err)
		}
		if latest, ok := listing["latest"].(map[string]any); ok && latest["status"] == "running" {
			seenRunning = true
			if latest["stage"] == "repository" || latest["stage"] == "pull" {
				time.Sleep(20 * time.Millisecond)
				continue
			}
			if latest["stage"] != "organize" && latest["stage"] != "validate" {
				t.Fatalf("unexpected running stage: %v", latest["stage"])
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !seenRunning {
		t.Fatal("running audit was never visible")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRunAutomationPassesDSHModelToWorker(t *testing.T) {
	store := newGitStore(t)
	bin := t.TempDir()
	capture := filepath.Join(t.TempDir(), "folio-args.txt")
	providerCapture := filepath.Join(t.TempDir(), "folio-provider.txt")
	configCapture := filepath.Join(t.TempDir(), "folio-config.txt")
	configPath := filepath.Join(t.TempDir(), "cordis.patch.yml")
	if err := os.WriteFile(configPath, []byte(`- id: agent-default-model
  config:
    provider: example-route
    model: example-large
- id: llm-pi-ai
  config:
    providers:
      example-route:
        models:
          - id: example-large
            name: Example Large
`), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"printf '%s' \"$*\" > \"" + capture + "\"\n" +
		"printf '%s' \"$DSH_MODEL_PROVIDER\" > \"" + providerCapture + "\"\n" +
		"printf '%s' \"$DSH_CONFIG_PATCH\" > \"" + configCapture + "\"\n" +
		"while [ \"$#\" -gt 0 ]; do\n" +
		"  case \"$1\" in\n" +
		"    --auto-report) shift; report=\"$1\" ;;\n" +
		"  esac\n" +
		"  shift\n" +
		"done\n" +
		"cat > \"$report\" <<'JSON'\n" +
		`{"version":1,"status":"pending-apply","summary":"","edits":[]}` + "\n" +
		"JSON\n"
	if err := os.WriteFile(filepath.Join(bin, "dsh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.toolRoot, "folio"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Setenv("DSH_MODEL_PROVIDER", "custom-dsh-provider")
	t.Setenv("DSH_CONFIG_PATCH", configPath)

	result, err := store.RunAutomation("manual", "dsh", "example-large")
	if err != nil {
		t.Fatal(err)
	}
	if result["status"] != "no_changes" || result["runner"] != "dsh" || result["model"] != "example-large" {
		t.Fatalf("automation result=%v", result)
	}
	if summary := automationSummary(result); summary["runner"] != "dsh" || summary["model"] != "example-large" {
		t.Fatalf("automation summary=%v", summary)
	}
	raw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	args := string(raw)
	if !strings.Contains(args, "--runner dsh") || !strings.Contains(args, "--model example-large") || !strings.Contains(args, "--provider custom-dsh-provider") {
		t.Fatalf("worker args=%s", args)
	}
	provider, err := os.ReadFile(providerCapture)
	if err != nil {
		t.Fatal(err)
	}
	if string(provider) != "custom-dsh-provider" {
		t.Fatalf("worker provider=%q", provider)
	}
	configValue, err := os.ReadFile(configCapture)
	if err != nil {
		t.Fatal(err)
	}
	if string(configValue) != configPath {
		t.Fatalf("worker config=%q", configValue)
	}
}

func TestAutomationFailureReasonUsesOnlyRunnerClass(t *testing.T) {
	for _, testCase := range []struct{ message, want string }{
		{"automation agent failed (network)", "runner_network"},
		{"automation agent failed (authentication)", "runner_authentication"},
		{"automation agent failed (runner_exit)", "runner_runner_exit"},
		{"provider response must not be exposed", "maintenance_operation_failed"},
	} {
		if got := automationFailureReason(errStore(testCase.message)); got != testCase.want {
			t.Errorf("automationFailureReason(%q)=%q, want %q", testCase.message, got, testCase.want)
		}
	}
}

// TestAutomationWorkerProcess lets the store launch the real context CLI in a
// separate process while all model execution remains a synthetic shell fixture.
func TestAutomationWorkerProcess(t *testing.T) {
	for i, arg := range os.Args {
		if arg == "--" && i+1 < len(os.Args) && os.Args[i+1] == "agent" {
			os.Exit(contextrepo.Run(os.Args[i+1:], os.Stdout, os.Stderr))
		}
	}
}

func TestRunAutomationPreservesSanitizedWorkerFailureClass(t *testing.T) {
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct{ diagnostic, want string }{
		{"HTTP/1.1 429", "runner_rate_limited"},
		{"HTTP status 401", "runner_authentication"},
		{"HTTP 403", "runner_authentication"},
		{"dial tcp: connection refused", "runner_network"},
	} {
		t.Run(testCase.want+"/"+testCase.diagnostic, func(t *testing.T) {
			store := newGitStore(t)
			bin := t.TempDir()
			t.Setenv("PATH", bin+":/usr/bin:/bin")
			codex := "#!/bin/sh\n/bin/cat >/dev/null\nprintf '%s\\n' '" + testCase.diagnostic + "' 'synthetic-private-provider-detail Authorization: Bearer synthetic-provider-token' >&2\nexit 1\n"
			if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(codex), 0o700); err != nil {
				t.Fatal(err)
			}
			launcher := "#!/bin/sh\nexec '" + strings.ReplaceAll(testBinary, "'", "'\\''") + "' -test.run='^TestAutomationWorkerProcess$' -- \"$@\"\n"
			if err := os.WriteFile(filepath.Join(store.toolRoot, "folio"), []byte(launcher), 0o700); err != nil {
				t.Fatal(err)
			}
			result, err := store.RunAutomation("manual", "codex", "")
			if err != nil {
				t.Fatal(err)
			}
			if result["status"] != "failed" || result["reason"] != testCase.want {
				t.Fatalf("worker diagnostic did not survive the process boundary: %v", result)
			}
			raw, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			runID, _ := result["run_id"].(string)
			audit, err := os.ReadFile(filepath.Join(store.runsDir, runID+".json"))
			if err != nil {
				t.Fatal(err)
			}
			for _, data := range [][]byte{raw, audit} {
				if strings.Contains(string(data), "synthetic-private-provider-detail") || strings.Contains(string(data), "synthetic-provider-token") || strings.Contains(string(data), "Authorization:") {
					t.Fatal("raw runner output or credentials escaped into the automation result or audit")
				}
			}
		})
	}
}
