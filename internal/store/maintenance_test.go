package store

import (
	"fmt"
	contextrepo "folio/internal/context"
	"os"
	"path/filepath"
	"testing"
)

func commitMaintenanceFixture(t *testing.T, s *Store) {
	t.Helper()
	runGitForTest(t, s.repositoryDir, "add", "--", "core")
	runGitForTest(t, s.repositoryDir, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "maintenance input")
	runGitForTest(t, s.repositoryDir, "push", "origin", "HEAD:refs/heads/main")
}

func TestMaintenanceAcknowledgesOwnCommitAcrossRestartWithoutCallingModel(t *testing.T) {
	s := newGitStore(t)
	installAutoFixture(t, s, `{"version":1,"status":"pending-apply","summary":"organize","edits":[{"source":"core/profile.md","target":"core/profile.md","claim_type":"fact","reason":"group text","content":"## Summary\nold\n"}],"needs_review":[{"source":"core/profile.md","reason":"Confirm this fixture conflict."}]}`)
	first, err := s.RunAutomation("manual", "codex", "")
	if err != nil || first["status"] != "pushed" {
		t.Fatalf("first: %v %v", first, err)
	}
	inputs := first["input_files"].(map[string]string)
	if inputs["core/profile.md"] != sha256Hex([]byte("old\n")) {
		t.Fatal("audit input fingerprint was overwritten by output")
	}
	info, err := os.Stat(s.maintenanceStatePath())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("checkpoint permissions: %v %v", info, err)
	}
	// Removing the worker makes an accidental second invocation fail.
	if err := os.Remove(filepath.Join(s.toolRoot, "folio")); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(s.dataDir, s.toolRoot, s.remote)
	if err != nil {
		t.Fatal(err)
	}
	restarted.allowFileRemoteForTests = true
	second, err := restarted.RunAutomation("hook", "codex", "")
	if err != nil || second["status"] != "no_changes" {
		t.Fatalf("second: %v %v", second, err)
	}
	if _, ok := second["usage"]; ok {
		t.Fatal("unchanged run acquired usage")
	}
	if len(second["input_files"].(map[string]string)) != 0 {
		t.Fatal("own commit retriggered model")
	}
	findings := second["needs_review"]
	if len(findings.([]contextrepo.MaintenanceIssue)) != 1 {
		t.Fatalf("review finding lost on restart: %v", findings)
	}
}

func TestMaintenanceValidationAndPushFailuresDoNotAdvanceCheckpoint(t *testing.T) {
	for _, mode := range []string{"candidate", "push"} {
		t.Run(mode, func(t *testing.T) {
			s := newGitStore(t)
			base := gitOutputForTest(t, s.repositoryDir, "rev-parse", "HEAD")
			source := "core/profile.md"
			if mode == "candidate" {
				source = "inbox/candidate.md"
			} else {
				hook := filepath.Join(filepath.Dir(s.dataDir), "remote.git", "hooks", "pre-receive")
				if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			installAutoFixture(t, s, fmt.Sprintf(`{"version":1,"status":"pending-apply","summary":"organize","edits":[{"source":%q,"target":"core/profile.md","claim_type":"fact","reason":"group text","content":"## Summary\nold\n"}]}`, source))
			result, err := s.RunAutomation("manual", "codex", "")
			if err != nil || result["status"] != "failed" {
				t.Fatalf("failure: %v %v", result, err)
			}
			if _, err := os.Stat(s.maintenanceStatePath()); !os.IsNotExist(err) {
				t.Fatal("failed run acknowledged input")
			}
			if got := gitOutputForTest(t, s.repositoryDir, "rev-parse", "HEAD"); got != base {
				t.Fatal("failed run left a local commit")
			}
			if got, _ := os.ReadFile(filepath.Join(s.repositoryDir, "core/profile.md")); string(got) != "old\n" {
				t.Fatal("failed run left changes")
			}
		})
	}
}

func TestMaintenanceContinuationRespectsPauseAndDrainsBacklog(t *testing.T) {
	s := newGitStore(t)
	for i := 0; i < 20; i++ {
		if err := os.WriteFile(filepath.Join(s.repositoryDir, "core", fmt.Sprintf("file-%02d.md", i)), []byte(fmt.Sprintf("Fact %d\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	commitMaintenanceFixture(t, s)
	if err := s.savePrivateJSON(s.policyPath, map[string]any{"defer": map[string]bool{"P2": false}}); err != nil {
		t.Fatal(err)
	}
	installAutoFixture(t, s, `{"version":1,"status":"pending-apply","summary":"already organized","edits":[]}`)
	result, err := s.RunAutomation("manual", "codex", "")
	if err != nil || result["status"] != "no_changes" || result["continuation_queued"] != true {
		t.Fatalf("first batch: %v %v", result, err)
	}
	if _, err := s.SetTasksPaused(true); err != nil {
		t.Fatal(err)
	}
	if result, err := s.DispatchDue(); err != nil || result["reason"] != "paused" {
		t.Fatalf("pause: %v %v", result, err)
	}
	if _, err := s.SetTasksPaused(false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		result, err := s.DispatchDue()
		if err != nil {
			t.Fatal(err)
		}
		if result["dispatched"] == 0 {
			break
		}
	}
	_, data, err := s.maintenanceInput()
	if err != nil || len(data.Files) != 0 || data.Remaining != 0 {
		t.Fatalf("backlog stalled: %v %v", data.Files, err)
	}
}

func TestMaintenanceEventDuringRunningTaskLeavesOneQueuedFollowup(t *testing.T) {
	s := newTestStore(t)
	first, err := s.CreateTask("maintenance", "hook:context", "Changed", "P1", nil)
	if err != nil {
		t.Fatal(err)
	}
	id := first["task"].(map[string]any)["id"]
	s.taskMu.Lock()
	file, err := s.loadTasksLocked()
	if err != nil {
		s.taskMu.Unlock()
		t.Fatal(err)
	}
	file.Tasks[0].Status = taskStatusRunning
	if err := s.saveTasksLocked(file); err != nil {
		s.taskMu.Unlock()
		t.Fatal(err)
	}
	s.taskMu.Unlock()
	followup, err := s.CreateTask("maintenance", "hook:context", "Changed again", "P1", nil)
	if err != nil || followup["created"] != true || followup["task"].(map[string]any)["id"] == id {
		t.Fatalf("event lost: %v %v", followup, err)
	}
	duplicate, err := s.CreateTask("maintenance", "hook:context", "Changed once more", "P1", nil)
	if err != nil || duplicate["created"] != false || duplicate["task"].(map[string]any)["id"] != followup["task"].(map[string]any)["id"] {
		t.Fatalf("events did not coalesce: %v %v", duplicate, err)
	}
}

func TestMaintenanceRestartDoesNotAcknowledgeUnpublishedCommit(t *testing.T) {
	s := newGitStore(t)
	remoteHead := gitOutputForTest(t, s.repositoryDir, "rev-parse", "HEAD")
	// Synthetic interruption after local commit, before push and Go rollback.
	if err := os.WriteFile(filepath.Join(s.repositoryDir, "core/profile.md"), []byte("## Summary\nold\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitForTest(t, s.repositoryDir, "add", "--", "core/profile.md")
	runGitForTest(t, s.repositoryDir, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "folio: interrupted maintenance")
	localHead := gitOutputForTest(t, s.repositoryDir, "rev-parse", "HEAD")
	restarted, err := New(s.dataDir, s.toolRoot, s.remote)
	if err != nil {
		t.Fatal(err)
	}
	restarted.allowFileRemoteForTests = true
	installAutoFixture(t, restarted, `{"version":1,"status":"pending-apply","summary":"already organized","edits":[]}`)
	result, err := restarted.RunAutomation("manual", "codex", "")
	if err != nil || result["status"] != "failed" || result["reason"] != "checkout_remote_mismatch" {
		t.Fatalf("unpublished commit acknowledged: %v %v", result, err)
	}
	if _, err := os.Stat(restarted.maintenanceStatePath()); !os.IsNotExist(err) {
		t.Fatal("unpublished bytes checkpointed")
	}
	if got := gitOutputForTest(t, s.repositoryDir, "rev-parse", "HEAD"); got != localHead {
		t.Fatal("unpublished owner-review commit was changed")
	}
	if got := gitOutputForTest(t, filepath.Join(filepath.Dir(s.dataDir), "remote.git"), "rev-parse", "refs/heads/main"); got != remoteHead {
		t.Fatal("unreviewed local commit was pushed")
	}
}
