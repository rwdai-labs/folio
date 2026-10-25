package contextrepo

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaintenanceKeepsCompleteInputsAndSkipsOversizedWithoutStarving(t *testing.T) {
	r := testRouter(t)
	body := strings.Repeat("完整上下文。", 600) + "\nFinal fact must survive."
	writeDoc(t, r.Root, "core/profile.md", "layer: L1\nsource: owner\nupdated: 2026-10-01", body)
	writeDoc(t, r.Root, "core/a-large.md", "layer: L1", strings.Repeat("x", reviewBatchLimit))
	writeDoc(t, r.Root, "core/secret.md", "sensitivity: sensitive", "never disclose this")
	data, err := r.MaintenanceInput(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Canonical) != 1 || data.Canonical[0].Body != body || !strings.Contains(data.Canonical[0].Content, "Final fact must survive.") {
		t.Fatalf("input was truncated: %#v", data.Files)
	}
	if data.Skipped != 2 || data.Remaining != 0 {
		t.Fatalf("skips/backlog: %#v", data)
	}
	raw, _ := json.Marshal(data)
	if len(raw) > reviewBatchLimit || strings.Contains(string(raw), "never disclose") {
		t.Fatal("packet bounds or sensitivity failed")
	}
	if len(data.Issues) != 1 || data.Issues[0].Reason != "document_too_large" {
		t.Fatalf("missing oversize finding: %v", data.Issues)
	}
}

func TestMaintenanceIncrementalBatchesCoverEveryFileAndIncludeAncestors(t *testing.T) {
	r := testRouter(t)
	writeDoc(t, r.Root, "core/parent.md", "layer: L0", "Broad summary")
	for i := 0; i < 20; i++ {
		writeDoc(t, r.Root, fmt.Sprintf("domains/child-%02d.md", i), "layer: L1\nparent: core/parent.md", fmt.Sprintf("Fact %d", i))
	}
	state := map[string]string{}
	for batch := 0; batch < 10; batch++ {
		data, err := r.MaintenanceInput(state)
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(data)
		if len(encoded) > reviewBatchLimit {
			t.Fatal("batch too large")
		}
		if len(data.Files) == 0 {
			break
		}
		if data.Files["core/parent.md"] == "" {
			t.Fatal("parent missing")
		}
		for path, hash := range data.Files {
			state[path] = hash
		}
	}
	if len(state) != 21 {
		t.Fatalf("batch starvation: %d files", len(state))
	}
	data, err := r.MaintenanceInput(state)
	if err != nil || len(data.Files) != 0 || data.Remaining != 0 {
		t.Fatalf("unchanged input not skipped: %v %v", data.Files, err)
	}
	writeDoc(t, r.Root, "domains/child-19.md", "layer: L1\nparent: core/parent.md", "Updated fact")
	data, err = r.MaintenanceInput(state)
	if err != nil || len(data.Files) != 2 || data.Files["domains/child-19.md"] == "" {
		t.Fatalf("incremental scope: %v %v", data.Files, err)
	}
}

func TestMaintenanceRejectsFactLossMetadataChangesAndCandidatePromotion(t *testing.T) {
	r := testRouter(t)
	writeDoc(t, r.Root, "core/profile.md", "claim_type: fact\nsource: owner\nreview_after: 2026-10-01", "# Profile\n\nKeep this fact.\nKeep this fact.\nAnother fact.")
	writeDoc(t, r.Root, "inbox/new.md", "type: candidate\nstatus: pending-review\nclaim_type: inference", "Maybe a preference")
	data, err := r.MaintenanceInput(nil)
	if err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile(filepath.Join(r.Root, "core/profile.md"))
	if err != nil {
		t.Fatal(err)
	}
	good := strings.Replace(string(old), "Keep this fact.\nKeep this fact.", "## Summary\nKeep this fact.", 1)
	if err := r.ValidateMaintenanceEdit(data, "core/profile.md", "core/profile.md", "fact", good); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"fact loss":        strings.Replace(good, "Another fact.", "", 1),
		"invented fact":    good + "\nA new owner preference.",
		"renew stale date": strings.Replace(good, "2026-10-01", "2026-10-25", 1),
		"change source":    strings.Replace(good, "source: owner", "source: model", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if r.ValidateMaintenanceEdit(data, "core/profile.md", "core/profile.md", "fact", content) == nil {
				t.Fatal("unsafe edit accepted")
			}
		})
	}
	if r.ValidateMaintenanceEdit(data, "inbox/new.md", "core/profile.md", "fact", good) == nil {
		t.Fatal("candidate promoted")
	}
	if r.ValidateMaintenanceEdit(data, "core/profile.md", "core/new.md", "fact", good) == nil {
		t.Fatal("unseen file created")
	}
	if len(data.Issues) != 2 {
		t.Fatalf("expected stale + candidate findings: %v", data.Issues)
	}
}

func TestMaintenanceExtractiveSummaryAndLinkRepair(t *testing.T) {
	r := testRouter(t)
	writeDoc(t, r.Root, "domains/index.md", "layer: L0\nclaim_type: fact", "# Topic\n\n[Detail](missing.md)")
	writeDoc(t, r.Root, "domains/detail.md", "layer: L1\nclaim_type: fact\nparent: domains/index.md", "# Detail\n\nVerified detail.")
	writeDoc(t, r.Root, "core/unrelated.md", "layer: L0", "Unrelated fact.")
	data, err := r.MaintenanceInput(nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(r.Root, "domains/index.md"))
	content := strings.Replace(string(raw), "missing.md", "detail.md", 1) + "\n## Summary\n\nVerified detail.\n"
	if err := r.ValidateMaintenanceEdit(data, "domains/index.md", "domains/index.md", "fact", content); err != nil {
		t.Fatal(err)
	}
	if r.ValidateMaintenanceEdit(data, "domains/index.md", "domains/index.md", "fact", content+"\nUnrelated fact.") == nil {
		t.Fatal("unrelated claim copied into summary")
	}
	writeDoc(t, r.Root, "domains/detail.md", "layer: L1\nparent: domains/index.md", "Changed after inference")
	if _, err := r.SnapshotMaintenanceInput(data.Files); err == nil {
		t.Fatal("stale snapshot accepted")
	}
}

func TestMaintenanceCRLFAndSecrets(t *testing.T) {
	r := testRouter(t)
	raw := "\ufeff---\r\nclaim_type: fact\r\nsource: owner\r\n---\r\nSame fact.\r\nSame fact.\r\n"
	if err := os.WriteFile(filepath.Join(r.Root, "core/profile.md"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	writeDoc(t, r.Root, "core/leak.md", "layer: L1", "OPENAI_API_KEY=synthetic-not-a-real-secret")
	if err := os.WriteFile(filepath.Join(r.Root, "core/bom-credential.md"), []byte("\ufeffpassword: synthetic-owner-secret\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := r.MaintenanceInput(nil)
	if err != nil {
		t.Fatal(err)
	}
	good := strings.Replace(raw, "Same fact.\r\nSame fact.", "Same fact.", 1)
	if err := r.ValidateMaintenanceEdit(data, "core/profile.md", "core/profile.md", "fact", good); err != nil {
		t.Fatal(err)
	}
	if r.ValidateMaintenanceEdit(data, "core/profile.md", "core/profile.md", "fact", strings.Replace(good, "source: owner", "source: model", 1)) == nil {
		t.Fatal("CRLF metadata bypass")
	}
	encoded, _ := json.Marshal(data)
	if strings.Contains(string(encoded), "synthetic-not-a-real-secret") || strings.Contains(string(encoded), "synthetic-owner-secret") {
		t.Fatal("secret reached model input")
	}
}

func TestMaintenanceChildSummaryPreservesClaimCategory(t *testing.T) {
	for _, category := range []string{"inference", "hypothesis", ""} {
		t.Run("child_"+category, func(t *testing.T) {
			r := testRouter(t)
			writeDoc(t, r.Root, "core/parent.md", "claim_type: fact", "# Facts")
			writeDoc(t, r.Root, "domains/child.md", "parent: core/parent.md\nclaim_type: "+category, "Owner prefers option A.")
			data, err := r.MaintenanceInput(nil)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := os.ReadFile(filepath.Join(r.Root, "core/parent.md"))
			if r.ValidateMaintenanceEdit(data, "core/parent.md", "core/parent.md", "fact", string(raw)+"\n## Summary\nOwner prefers option A.\n") == nil {
				t.Fatal("child claim promoted to fact")
			}
		})
	}
}

func TestMaintenanceBOMCRLFRoutingAndSnapshot(t *testing.T) {
	r := testRouter(t)
	for path, content := range map[string]string{
		"core/archived.md":   "---\nstatus: archived\n---\nArchived fact.",
		"inbox/candidate.md": "---\ntype: candidate\nstatus: pending-review\n---\nPending observation.",
		"domains/child.md":   "---\nparent: core/parent.md\nclaim_type: fact\n---\nChild fact.",
	} {
		if err := os.WriteFile(filepath.Join(r.Root, path), []byte("\ufeff"+strings.ReplaceAll(content, "\n", "\r\n")), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeDoc(t, r.Root, "core/parent.md", "claim_type: fact", "Parent fact.")
	data, err := r.MaintenanceInput(nil)
	if err != nil {
		t.Fatal(err)
	}
	if data.Files["inbox/candidate.md"] == "" || data.Files["core/archived.md"] != "" {
		t.Fatalf("CRLF routing lost metadata: %v", data.Files)
	}
	snapshot, err := r.SnapshotMaintenanceInput(data.Files)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(r.Root, "core/parent.md"))
	if err := r.ValidateMaintenanceEdit(snapshot, "core/parent.md", "core/parent.md", "fact", string(raw)+"\n## Summary\nChild fact.\n"); err != nil {
		t.Fatal("snapshot lost CRLF parent/claim metadata:", err)
	}
}

func TestMaintenancePacketBudgetIncludesLongFingerprintPaths(t *testing.T) {
	r := testRouter(t)
	path := "core/" + strings.Repeat(strings.Repeat("b", 250)+"/", 14) + "long.md"
	writeDoc(t, r.Root, "core/a.md", "claim_type: fact", strings.Repeat("a", 9000))
	writeDoc(t, r.Root, path, "claim_type: fact", strings.Repeat("x", 20030))
	data, err := r.MaintenanceInput(nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(data)
	if err != nil || len(encoded) > reviewBatchLimit {
		t.Fatalf("packet budget: bytes=%d err=%v", len(encoded), err)
	}
	if len(data.Files) == 0 {
		t.Fatal("valid small document was starved")
	}
}
