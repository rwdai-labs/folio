package store

import (
	"os"
	"path/filepath"
	"testing"
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
  "summary": "merge the candidate into the profile",
  "device": "fixture",
  "created": "2026-10-20T00:00:00Z",
  "skipped": 0,
  "edits": [
    {"source": "inbox/candidate.md", "target": "core/profile.md", "claim_type": "fact", "reason": "the candidate confirms the profile", "content": "new\n"},
    {"source": "inbox/candidate.md", "target": "inbox/auto.md", "claim_type": "inference", "reason": "keep the review note", "content": "---\ntype: note\n---\nfresh\n"}
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
	if len(edits) != 2 {
		t.Fatalf("edits=%v", result["edits"])
	}
	if got, _ := os.ReadFile(filepath.Join(store.repositoryDir, "core", "profile.md")); string(got) != "new\n" {
		t.Fatalf("profile was not updated: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(store.repositoryDir, "inbox", "auto.md")); string(got) != "---\ntype: note\n---\nfresh\n" {
		t.Fatalf("new document was not written: %q", got)
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
	if detail["status"] != "pushed" || detail["summary"] != "merge the candidate into the profile" {
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
    {"source": "inbox/candidate.md", "target": "core/profile.md", "claim_type": "fact", "reason": "latest remote content", "content": "old\nnew from model\n"}
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
	installAutoFixture(t, store, `{
  "version": 1,
  "status": "pending-apply",
  "summary": "nothing to change",
  "edits": [
    {"source": "inbox/candidate.md", "target": "core/profile.md", "claim_type": "fact", "reason": "already current", "content": "old\n"},
    {"source": "inbox/candidate.md", "target": "inbox/new.md", "claim_type": "fact", "reason": "new note", "content": "---\ntype: note\n---\nfresh\n"}
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
    {"source": "inbox/candidate.md", "target": "core/profile.md", "claim_type": "fact", "reason": "already current", "content": "old\n"}
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
