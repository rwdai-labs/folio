package web

import (
	"net/http"
	"testing"
)

type reviewSnapshotBackend struct {
	testBackend
	reviewRun any
}

func (b *reviewSnapshotBackend) ListAutomation() (map[string]any, error) {
	return map[string]any{
		"latest":     map[string]any{"run_id": "20261020T020000.000000000Z-00000002", "status": "reported"},
		"review_run": b.reviewRun,
	}, nil
}

func TestAutomationReviewSnapshotPreservesIdentityAndRedaction(t *testing.T) {
	const reviewedID = "20261020T010000.000000000Z-00000001"
	backend := &reviewSnapshotBackend{reviewRun: map[string]any{
		"run_id": reviewedID, "status": "pushed",
		"repository":   map[string]any{"plugin": "github", "full_name": "owner/context", "branch": "main"},
		"needs_review": []any{map[string]any{"source": "inbox/example.md", "reason": "candidate_requires_owner_review"}},
		"password":     "must-not-return",
	}}
	server, password := testServer(t, backend)
	defer server.Close()
	client := &testClient{server: server}
	status, _, _ := client.request(t, http.MethodGet, "/api/automation", nil, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated review snapshot status=%d", status)
	}
	client.login(t, password)
	status, _, listing := client.request(t, http.MethodGet, "/api/automation", nil, "")
	if status != http.StatusOK {
		t.Fatalf("automation status=%d result=%v", status, listing)
	}
	latest, _ := listing["latest"].(map[string]any)
	review, _ := listing["review_run"].(map[string]any)
	repository, _ := review["repository"].(map[string]any)
	findings, _ := review["needs_review"].([]any)
	if latest["status"] != "reported" || review["run_id"] != reviewedID || repository["plugin"] != "github" || repository["full_name"] != "owner/context" || repository["branch"] != "main" || len(findings) != 1 {
		t.Fatalf("review snapshot was dropped or replaced: %v", listing)
	}
	if review["password"] != "[redacted]" {
		t.Fatalf("review snapshot bypassed redaction: %v", review)
	}
}

func TestAutomationUnknownReviewSnapshotRemainsNull(t *testing.T) {
	server, password := testServer(t, &reviewSnapshotBackend{})
	defer server.Close()
	client := &testClient{server: server}
	client.login(t, password)
	status, _, listing := client.request(t, http.MethodGet, "/api/automation", nil, "")
	value, exists := listing["review_run"]
	if status != http.StatusOK || !exists || value != nil {
		t.Fatalf("unknown review snapshot did not remain null: status=%d result=%v", status, listing)
	}
}
